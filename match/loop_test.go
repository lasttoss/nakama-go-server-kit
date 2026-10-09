package match_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lasttoss/nakama-go-server-kit/match"
	"github.com/lasttoss/nakama-go-server-kit/testkit"
)

const rate = 30
const interval = time.Second / rate

// patience is how long a test waits for something that should happen in microseconds. It is a deadlock
// detector, not a performance assertion: a runner with two shared cores and four packages under -race
// is slower than a laptop, and a test that fails there is telling the wrong story. What the loop does
// is asserted by the ticks themselves, which the fake clock makes exact.
const patience = 15 * time.Second

// waitFor fails the test if the loop has not done what the test expects by the time the deadline
// runs out. It is the only place in this file that touches the wall clock.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	if !testkit.WaitFor(patience, cond) {
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestLoopTicksAtTheRateItWasGiven(t *testing.T) {
	clk := testkit.NewFakeClock()

	loop, err := match.Start(context.Background(), match.Options{
		TickRate: rate,
		Clock:    clk,
		OnTick:   func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	clk.AdvanceByTicks(3)

	waitFor(t, "three ticks", func() bool { return loop.Snapshot().Ticks >= 3 })

	stats := loop.Snapshot()
	if stats.Ticks != 3 {
		t.Fatalf("ticks = %d, want 3 in three intervals at %d Hz", stats.Ticks, rate)
	}
	if stats.SlowTicks != 0 {
		t.Fatalf("slow ticks = %d, want none: the handler returns immediately", stats.SlowTicks)
	}
}

func TestTheTickIsHandedTheGameTimeThatPassed(t *testing.T) {
	clk := testkit.NewFakeClock()
	seen := make(chan time.Duration, 8)

	loop, err := match.Start(context.Background(), match.Options{
		TickRate: rate,
		Clock:    clk,
		OnTick:   func(_ context.Context, dt time.Duration) { seen <- dt },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	clk.AdvanceByTicks(3)

	waitFor(t, "three ticks", func() bool { return loop.Snapshot().Ticks >= 3 })
	close(seen)

	for dt := range seen {
		if dt != interval {
			t.Errorf("dt = %v, want one tick interval %v: a fixed-rate loop steps by a fixed amount", dt, interval)
		}
	}
}

func TestASlowTickIsCountedAndReported(t *testing.T) {
	clk := testkit.NewFakeClock()
	type overrun struct {
		tick uint64
		took time.Duration
	}
	reported := make(chan overrun, 4)

	loop, err := match.Start(context.Background(), match.Options{
		TickRate: rate,
		Clock:    clk,
		OnTick: func(context.Context, time.Duration) {
			// A handler that took 30ms of a 33ms budget: the fake clock only moves when it is told to, so
			// the test moves it rather than sleeping - and Jump moves it without firing a tick, so that
			// a slow handler stays one handler rather than a burst of them.
			clk.Jump(30 * time.Millisecond)
		},
		OnSlowTick: func(tick uint64, took time.Duration) { reported <- overrun{tick, took} },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	clk.Advance(50 * time.Millisecond)

	waitFor(t, "the overrun to be reported", func() bool { return loop.Snapshot().SlowTicks > 0 })

	select {
	case got := <-reported:
		if got.took < 30*time.Millisecond {
			t.Errorf("reported %v, want the 30ms the handler took", got.took)
		}
	case <-time.After(patience):
		t.Fatal("OnSlowTick was never called")
	}
	stats := loop.Snapshot()
	if stats.MaxTick < 30*time.Millisecond {
		t.Errorf("MaxTick = %v, want at least the 30ms tick", stats.MaxTick)
	}
}

func TestAPanickingTickCostsATickAndNotTheProcess(t *testing.T) {
	clk := testkit.NewFakeClock()
	var calls int

	loop, err := match.Start(context.Background(), match.Options{
		TickRate: rate,
		Clock:    clk,
		OnTick: func(context.Context, time.Duration) {
			calls++
			if calls == 2 {
				panic("a message from one player that the handler did not expect")
			}
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	clk.AdvanceByTicks(4)

	waitFor(t, "the loop to carry on past the panic", func() bool { return loop.Snapshot().Ticks >= 4 })
	if calls != 4 {
		t.Fatalf("OnTick ran %d times, want 4: the panic must not stop the loop", calls)
	}
}

func TestTheLoopStopsWhenTheMatchIsOver(t *testing.T) {
	clk := testkit.NewFakeClock()
	ctx, cancel := context.WithCancel(context.Background())

	loop, err := match.Start(ctx, match.Options{
		TickRate: rate,
		Clock:    clk,
		OnTick:   func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	clk.AdvanceByTicks(2)
	waitFor(t, "two ticks", func() bool { return loop.Snapshot().Ticks == 2 })

	cancel()

	done := make(chan match.Stats, 1)
	go func() { done <- loop.Wait() }()
	select {
	case stats := <-done:
		if !errors.Is(stats.StopReason, context.Canceled) {
			t.Errorf("StopReason = %v, want context.Canceled", stats.StopReason)
		}
		if stats.Ticks != 2 {
			t.Errorf("ticks = %d, want the 2 that ran before the match ended", stats.Ticks)
		}
		if stats.StoppedAt.Before(stats.StartedAt) {
			t.Errorf("stopped before it started: %v then %v", stats.StartedAt, stats.StoppedAt)
		}
	case <-time.After(patience):
		t.Fatal("Wait never returned after the context was cancelled")
	}
}

// The percentile is nearest-rank over the recent ticks: the p95 of ten samples is the largest of
// them. An operator asking for p95 is asking which tick an unlucky player felt, so the answer should
// be a tick time that really happened rather than an average of two that did.
func TestNearestRankKeepsTheConvention(t *testing.T) {
	ten := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	cases := []struct {
		percentile float64
		want       time.Duration
	}{
		{0, ten[0]},
		{0.5, ten[5]},  // the sixth of ten: 6ms, not 5.5ms
		{0.9, ten[9]},  // the last one
		{0.95, ten[9]}, // and still the last one: ten samples do not have a 95th between them
		{1, ten[9]},
		{-1, ten[0]},  // clamped rather than panicking
		{1.5, ten[9]}, // as is a percentile above one
	}
	for _, tc := range cases {
		if got := match.NearestRank(ten, tc.percentile); got != tc.want {
			t.Errorf("NearestRank(1..10ms, %g) = %v, want %v", tc.percentile, got, tc.want)
		}
	}
	if got := match.NearestRank(nil, 0.5); got != 0 {
		t.Errorf("NearestRank of nothing = %v, want 0", got)
	}
}

// And the loop feeds it the ticks it really ran. The assertions stay loose on purpose: what is
// being tested here is that the numbers come from the ticks rather than from a placeholder, not
// that the test can predict the scheduler to the millisecond.
func TestPercentileReportsTheTicksTheLoopRan(t *testing.T) {
	clk := testkit.NewFakeClock()
	const samples = 10
	var which int

	loop, err := match.Start(context.Background(), match.Options{
		TickRate: rate,
		Clock:    clk,
		OnTick: func(context.Context, time.Duration) {
			which++
			if which <= samples {
				// 3ms, 6ms, ... 30ms, each still inside one 33ms interval
				clk.Jump(time.Duration(which*3) * time.Millisecond)
			}
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	for i := 0; i < samples; i++ {
		clk.AdvanceByTicks(1)
		want := uint64(i + 1)
		waitFor(t, "a tick", func() bool { return loop.Snapshot().Ticks >= want })
	}

	if stats := loop.Snapshot(); stats.Ticks < samples {
		t.Fatalf("ticks = %d, want at least the %d that were driven", stats.Ticks, samples)
	}
	p50, p95, p100 := loop.Percentile(0.5), loop.Percentile(0.95), loop.Percentile(1)
	if p95 < 27*time.Millisecond {
		t.Errorf("p95 = %v, want at least the 30ms tick the handler took", p95)
	}
	if p50 < 3*time.Millisecond || p50 > time.Second {
		t.Errorf("p50 = %v, want something between the fastest and the slowest tick", p50)
	}
	if p100 < p95 || p95 < p50 {
		t.Errorf("percentiles are not ordered: p50 = %v, p95 = %v, p100 = %v", p50, p95, p100)
	}
}

// The log is how an operator learns that a room is in trouble, so the lines are part of the
// interface and are asserted rather than assumed.
func TestTheLoopSaysWhenATickOverran(t *testing.T) {
	clk := testkit.NewFakeClock()
	logger, recorder := testkit.NewLogger()

	if _, err := match.Start(context.Background(), match.Options{
		TickRate: rate,
		Clock:    clk,
		Logger:   logger,
		OnTick: func(context.Context, time.Duration) {
			clk.Jump(30 * time.Millisecond) // most of a 33ms budget
		},
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	clk.AdvanceByTicks(1)

	waitFor(t, "the overrun to be logged", func() bool { return recorder.Contains("overran") })
	if !recorder.Contains("tick=1") {
		t.Errorf("the log line does not name the tick that overran: %v", recorder.Lines())
	}
}

func TestTheLoopSaysWhenATickPanicked(t *testing.T) {
	clk := testkit.NewFakeClock()
	logger, recorder := testkit.NewLogger()

	if _, err := match.Start(context.Background(), match.Options{
		TickRate: rate,
		Clock:    clk,
		Logger:   logger,
		OnTick:   func(context.Context, time.Duration) { panic("a message the handler did not expect") },
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	clk.AdvanceByTicks(1)

	waitFor(t, "the panic to be logged", func() bool { return recorder.Contains("panicked") })
}

// Run is the blocking form, and the one a server's main uses: it returns when the match ends, with
// what the loop did and a line in the log saying so.
//
// The tick count is deliberately not asserted here. Run stops the moment the context is cancelled, and
// a tick that has been fired but not yet taken is a tick the loop never counted - so a test that
// cancels immediately after moving the clock is a race, and it is a race in the test rather than in
// the loop. That the loop counts the ticks it is given is asserted by the tests above, which wait for
// the count itself.
func TestRunReturnsWhenTheMatchIsOver(t *testing.T) {
	clk := testkit.NewFakeClock()
	logger, recorder := testkit.NewLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registered := make(chan bool, 1)
	go func() {
		// Run starts the loop, which registers the ticker, so wait for it before driving the clock.
		if !testkit.WaitFor(patience, func() bool { return len(clk.Tickers()) > 0 }) {
			registered <- false
			cancel()
			return
		}
		registered <- true
		clk.AdvanceByTicks(2)
		cancel()
	}()

	stats, err := match.Run(ctx, match.Options{
		TickRate: rate,
		Clock:    clk,
		Logger:   logger,
		OnTick:   func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !errors.Is(stats.StopReason, context.Canceled) {
		t.Errorf("StopReason = %v, want context.Canceled", stats.StopReason)
	}
	if !testkit.WaitFor(patience, func() bool { return recorder.Contains("match loop stopped") }) {
		t.Errorf("the loop did not say it had stopped: %v", recorder.Lines())
	}
	if stats.StartedAt.IsZero() || stats.StoppedAt.Before(stats.StartedAt) {
		t.Errorf("the loop did not record a match: started %v, stopped %v", stats.StartedAt, stats.StoppedAt)
	}
	if !<-registered {
		t.Error("the loop never registered its ticker, so the test could not drive it")
	}
}

func TestALoopWithNothingToDoRefusesToStart(t *testing.T) {
	cases := []struct {
		name string
		opts match.Options
		want error
	}{
		{"no tick rate", match.Options{OnTick: func(context.Context, time.Duration) {}}, match.ErrTickRateUnset},
		{"negative tick rate", match.Options{TickRate: -1, OnTick: func(context.Context, time.Duration) {}}, match.ErrTickRateUnset},
		{"tick rate nobody can keep up with", match.Options{TickRate: 5000, OnTick: func(context.Context, time.Duration) {}}, match.ErrTickRateTooHigh},
		{"nothing to run", match.Options{TickRate: 30}, match.ErrNoOnTick},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := match.Start(context.Background(), tc.opts); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// Run with -race: a match reads its own stats while the loop writes them, and the operator's
// dashboard reads the same fields from another goroutine entirely.
func TestStatsAreSafeToReadWhileTheLoopRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	loop, err := match.Start(ctx, match.Options{
		TickRate: 200, // a real 5ms ticker: this test is about the lock, not the clock
		OnTick:   func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitFor(t, "the first tick", func() bool { return loop.Snapshot().Ticks > 0 })

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = loop.Snapshot()
				_ = loop.Percentile(0.95)
			}
		}()
	}
	wg.Wait()
	cancel()

	stats := loop.Wait()
	if stats.Ticks == 0 {
		t.Fatal("no ticks in 50ms at 200 Hz")
	}
}
