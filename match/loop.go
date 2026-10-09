// Package match runs an authoritative game match: one fixed-rate tick, one place where the time
// budget is spent, and nothing else.
//
// The loop is the part of a game server that players feel. A tick that runs long is a stutter, and
// the only way to know that a handler is too slow is to measure the tick it ran in. This loop calls
// OnTick at the rate you asked for, measures how long each call took, counts the ticks that missed
// their budget, keeps those numbers available to whatever exposes metrics, and then calls OnTick
// again.
package match

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Options configure a loop. TickRate and OnTick are required.
type Options struct {
	// TickRate is how many ticks per second the loop aims for, for example 30.
	TickRate int
	// OnTick runs once per tick. dt is the game time since the previous tick, ready to be used as
	// the simulation step.
	OnTick func(ctx context.Context, dt time.Duration)
	// Clock is the passage of time; nil means the real one.
	Clock Clock
	// Logger receives a line when a tick overruns and when the loop stops. nil means no logging, which
	// is what a test wants.
	Logger *slog.Logger
	// SlowTick is the share of the tick interval that counts as an overrun, between 0 and 1. The
	// default 0.8 leaves the loop a fifth of the interval to do its own work.
	SlowTick float64
	// OnSlowTick, when set, is called with the tick number and how long it took, alongside the log
	// line. A match can use it to shed load, for example by lowering the tick rate for that room.
	OnSlowTick func(tick uint64, took time.Duration)
}

// Stats is what a loop has done so far.
type Stats struct {
	Ticks      uint64
	SlowTicks  uint64
	MaxTick    time.Duration
	LastTick   time.Duration
	StartedAt  time.Time
	StoppedAt  time.Time
	StopReason error
}

// Loop is a running match loop. Create one with Start or Run.
type Loop struct {
	opts     Options
	interval time.Duration
	clock    Clock
	ticker   Ticker
	started  time.Time

	mu     sync.Mutex
	done   sync.WaitGroup
	stats  Stats
	recent []time.Duration
}

// recentWindow is how many tick times the percentile is computed over: the last few seconds at
// 30 Hz, which is what an operator asking about p95 means.
const recentWindow = 512

func (o *Options) normalise() error {
	if o.TickRate <= 0 {
		return ErrTickRateUnset
	}
	if o.TickRate > 1000 {
		return ErrTickRateTooHigh
	}
	if o.OnTick == nil {
		return ErrNoOnTick
	}
	if o.Clock == nil {
		o.Clock = RealClock{}
	}
	if o.SlowTick <= 0 || o.SlowTick > 1 {
		o.SlowTick = 0.8
	}
	return nil
}

// Run ticks until ctx is done and returns what the loop did. It blocks, so a server normally runs
// it in its own goroutine.
func Run(ctx context.Context, opts Options) (Stats, error) {
	l, err := Start(ctx, opts)
	if err != nil {
		return Stats{}, err
	}
	return l.Wait(), nil
}

// Start begins ticking in the background. Call Wait to block until the loop has stopped.
func Start(ctx context.Context, opts Options) (*Loop, error) {
	if err := opts.normalise(); err != nil {
		return nil, err
	}
	l := &Loop{
		opts:     opts,
		interval: time.Duration(int64(time.Second) / int64(opts.TickRate)),
		clock:    opts.Clock,
	}
	l.stats.StartedAt = opts.Clock.Now()
	// The ticker is registered here rather than in the goroutine, so that the schedule starts when
	// the loop is started: a caller that starts a match and immediately looks at the clock does not
	// lose the first interval to a race with the scheduler.
	l.ticker = l.clock.NewTicker(l.interval)
	l.started = l.stats.StartedAt
	l.done.Add(1)
	go l.run(ctx)
	return l, nil
}

func (l *Loop) run(ctx context.Context) {
	defer l.done.Done()

	ticker := l.ticker
	defer ticker.Stop()

	previous := l.started
	var tick uint64
	for {
		select {
		case <-ctx.Done():
			l.mu.Lock()
			l.stats.StoppedAt = l.clock.Now()
			l.stats.StopReason = ctx.Err()
			ticks, slow, worst := l.stats.Ticks, l.stats.SlowTicks, l.stats.MaxTick
			l.mu.Unlock()
			if l.opts.Logger != nil {
				l.opts.Logger.Info("match loop stopped",
					"ticks", ticks, "slow_ticks", slow, "max_tick", worst.String())
			}
			return
		case now := <-ticker.C():
			dt := now.Sub(previous)
			previous = now

			started := l.clock.Now()
			tick++
			l.tick(ctx, dt, tick)
			took := l.clock.Now().Sub(started)
			l.record(took)
		}
	}
}

// record files one tick's cost. The lock is held for the few assignments only: a handler that
// calls Snapshot from inside OnTick must not deadlock, and logging must not happen under the lock.
func (l *Loop) record(took time.Duration) {
	l.mu.Lock()
	slow := time.Duration(float64(l.interval) * l.opts.SlowTick)
	l.stats.Ticks++
	l.stats.LastTick = took
	if took > l.stats.MaxTick {
		l.stats.MaxTick = took
	}
	l.recent = append(l.recent, took)
	if len(l.recent) > recentWindow {
		l.recent = l.recent[1:]
	}
	overran := took > slow
	if overran {
		l.stats.SlowTicks++
	}
	tick, interval := l.stats.Ticks, l.interval
	logger, onSlow := l.opts.Logger, l.opts.OnSlowTick
	l.mu.Unlock()

	if !overran {
		return
	}
	if logger != nil {
		logger.Warn("tick overran its budget", "tick", tick, "took", took.String(), "interval", interval.String())
	}
	if onSlow != nil {
		onSlow(tick, took)
	}
}

// tick runs one OnTick and survives whatever it does. A handler that panics on one message from one
// player should cost that player's room a tick, not the whole process: in Nakama a panic in the
// runtime takes the server with it, so the recovery belongs here rather than in the handler.
func (l *Loop) tick(ctx context.Context, dt time.Duration, tick uint64) {
	defer func() {
		if r := recover(); r != nil && l.opts.Logger != nil {
			l.opts.Logger.Error("the tick panicked, the loop carries on", "tick", tick, "panic", r)
		}
	}()
	l.opts.OnTick(ctx, dt)
}

// Wait blocks until the loop has stopped and returns what it did.
func (l *Loop) Wait() Stats {
	l.done.Wait()
	return l.Snapshot()
}

// Snapshot returns what the loop has done so far, including while it is still running.
func (l *Loop) Snapshot() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stats
}

// Percentile is the tick time this share of recent ticks came in under, for example 0.95 for the
// p95 an operator asks about. It reports zero before the first tick.
func (l *Loop) Percentile(p float64) time.Duration {
	l.mu.Lock()
	recent := append([]time.Duration(nil), l.recent...)
	l.mu.Unlock()

	sort.Slice(recent, func(i, j int) bool { return recent[i] < recent[j] })
	return NearestRank(recent, p)
}

// NearestRank is the convention this package uses for a percentile, as a function so that it can be
// tested and read on its own: the tick time at index floor(p*n) of the sorted tick times, clamped
// into the slice. The p95 of ten ticks is therefore the slowest of them, not an interpolation
// between the ninth and the tenth - an operator asking about p95 is asking which tick an unlucky
// player felt, and the answer should be a tick time that really happened.
//
// sorted must be sorted ascending; an empty slice has no percentile and yields zero.
func NearestRank(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	index := int(float64(len(sorted)) * p)
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	if index < 0 {
		index = 0
	}
	return sorted[index]
}
