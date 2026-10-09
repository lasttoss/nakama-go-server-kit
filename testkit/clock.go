// Package testkit is what a match looks like when the server is not running: a clock you move by
// hand and a log you can read. A game server's logic is tested here rather than against a live
// Nakama, because a test that needs a server is a test that runs in a minute instead of in a
// millisecond, and it is the milliseconds that let you test the interesting path.
package testkit

import (
	"sync"
	"time"

	"github.com/lasttoss/nakama-go-server-kit/match"
)

// FakeClock is a clock that only moves when the test says so.
//
// It implements match.Clock, so it can be handed straight to a match loop. The one thing worth
// knowing about it: Advance delivers the ticks that are due and returns, while the loop processes
// them in its own goroutine, so a test that wants to assert on tick N waits for it with WaitFor
// rather than with a sleep.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*FakeTicker
}

// NewFakeClock returns a clock started at a fixed instant, so that a failing test prints a date and
// not a difference that changes every run.
func NewFakeClock() *FakeClock {
	return &FakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Start sets the instant the clock begins at.
func (c *FakeClock) Start(at time.Time) *FakeClock {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
	return c
}

// Tickers returns the tickers registered so far, so that a test can ask how many ticks were
// dropped rather than only how many arrived.
func (c *FakeClock) Tickers() []*FakeTicker {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*FakeTicker(nil), c.tickers...)
}

// Now is the current time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTicker registers a ticker, exactly as match.Clock requires.
func (c *FakeClock) NewTicker(interval time.Duration) match.Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &FakeTicker{
		clock:    c,
		interval: interval,
		next:     c.now.Add(interval),
		ch:       make(chan time.Time, 1),
	}
	c.tickers = append(c.tickers, t)
	return t
}

// Advance moves the clock forward and delivers every tick that falls inside the interval, in time
// order.
//
// A tick the loop was not ready to receive is dropped and counted, which is what a real server does
// with a tick it could not keep up with: the tick is late, not queued forever. AdvanceByTicks is the
// one to use when the test cares that every tick arrives.
func (c *FakeClock) Advance(d time.Duration) {
	c.fire(c.Now().Add(d), false)
}

// AdvanceByTicks moves the clock forward by n intervals, waiting for the loop to take each tick
// before firing the next so that none of them is dropped, then returns. It reads better in a test
// that thinks in ticks than in milliseconds, and a test that asserts "after three ticks" wants
// three ticks.
func (c *FakeClock) AdvanceByTicks(n int) {
	c.mu.Lock()
	if len(c.tickers) == 0 {
		c.mu.Unlock()
		return
	}
	interval, ticker := c.tickers[0].interval, c.tickers[0]
	c.mu.Unlock()

	for i := 0; i < n; i++ {
		// The loop has taken the previous tick once the buffer is empty again. The wait is generous on
		// purpose: on a loaded machine the goroutine taking the tick may not be scheduled for a while,
		// and a short deadline would turn that into a dropped tick and a failed test rather than a slow
		// one. A test that drives a clock nobody is listening to gets its ticks anyway when the wait is
		// over, rather than hanging.
		for deadline := time.Now().Add(10 * time.Second); len(ticker.ch) > 0 && time.Now().Before(deadline); {
			time.Sleep(50 * time.Microsecond)
		}
		c.fire(c.Now().Add(interval), true)
	}
}

// Jump moves the clock forward without delivering any tick. A test uses it to make one tick look
// slow without waiting for one: the loop measures how long its handler took by reading the clock
// afterwards, and this is how a test decides what that reading will be.
func (c *FakeClock) Jump(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fire walks the clock to target, delivering the ticks that are due on the way. With wait set, a
// tick the loop does not take within a generous bound is a mistake in the test rather than a slow
// loop, so it is counted as dropped and the walk continues rather than blocking for ever.
func (c *FakeClock) fire(target time.Time, wait bool) {
	for {
		t := c.earliestDue(target)
		if t == nil {
			break
		}
		c.mu.Lock()
		due := t.next
		c.now = due
		t.next = due.Add(t.interval)
		stopped := t.stopped
		c.mu.Unlock()

		if stopped {
			continue
		}
		// The tick carries the instant it was due, read while the clock was held: reading it here
		// instead would be a race with whoever else is moving this clock.
		sent := due
		select {
		case t.ch <- sent:
		default:
			if !wait {
				c.mu.Lock()
				t.dropped++
				c.mu.Unlock()
				continue
			}
			select {
			case t.ch <- sent:
			case <-time.After(2 * time.Second):
				c.mu.Lock()
				t.dropped++
				c.mu.Unlock()
			}
		}
	}
	c.mu.Lock()
	c.now = target
	c.mu.Unlock()
}

func (c *FakeClock) earliestDue(limit time.Time) *FakeTicker {
	c.mu.Lock()
	defer c.mu.Unlock()

	var earliest *FakeTicker
	for _, t := range c.tickers {
		if t.stopped || t.next.After(limit) {
			continue
		}
		if earliest == nil || t.next.Before(earliest.next) {
			earliest = t
		}
	}
	return earliest
}

// FakeTicker is one ticker of a FakeClock.
type FakeTicker struct {
	clock    *FakeClock
	interval time.Duration
	next     time.Time

	ch      chan time.Time
	stopped bool
	dropped int
}

// C is the channel the ticks arrive on.
func (t *FakeTicker) C() <-chan time.Time { return t.ch }

// Stop releases the ticker, as the real one does.
func (t *FakeTicker) Stop() {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.stopped = true
}

// Stopped reports whether the ticker has been stopped.
func (t *FakeTicker) Stopped() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	return t.stopped
}

// Dropped is how many ticks this ticker fired while the loop was not ready to take them.
func (t *FakeTicker) Dropped() int {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	return t.dropped
}

// WaitFor blocks until cond is true, and fails the test if it never becomes true. It exists so that
// a test can wait for the loop to catch up without a sleep that is either too short (flaky) or too
// long (slow).
func WaitFor(deadline time.Duration, cond func() bool) bool {
	until := time.Now().Add(deadline)
	for time.Now().Before(until) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}
