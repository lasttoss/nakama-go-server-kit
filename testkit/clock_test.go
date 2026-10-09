package testkit_test

import (
	"sync"
	"testing"
	"time"

	"github.com/lasttoss/nakama-go-server-kit/testkit"
)

const interval = 10 * time.Millisecond

// A test that fails should print a date and a number that mean the same thing on every run, so the
// fake clock does not start at the wall clock.
func TestTheFakeClockDoesNotStartAtTheWallClock(t *testing.T) {
	clk := testkit.NewFakeClock()

	at := clk.Now()
	if at.IsZero() || at.Year() != 2026 {
		t.Fatalf("clock started at %v; a fixed instant keeps a failing test reproducible", at)
	}
	if clk.Start(time.Date(2030, 5, 4, 3, 2, 1, 0, time.UTC)).Now().Year() != 2030 {
		t.Fatal("Start did not move the clock")
	}
}

// The behaviour a real time.Ticker has, and the reason a loop counts what it missed: a tick nobody
// takes is dropped, not queued for later.
func TestATickNobodyTakesIsDroppedAndCounted(t *testing.T) {
	clk := testkit.NewFakeClock()
	ticker := clk.NewTicker(interval)

	clk.Advance(3 * interval)

	fake, ok := ticker.(*testkit.FakeTicker)
	if !ok {
		t.Fatalf("NewTicker returned %T, want a FakeTicker", ticker)
	}
	if got := len(ticker.C()); got != 1 {
		t.Fatalf("buffered ticks = %d, want 1: the third tick has nowhere to go", got)
	}
	if got := fake.Dropped(); got != 2 {
		t.Fatalf("dropped = %d, want 2", got)
	}
}

// The other half of the same idea: a test that wants three ticks gets three, because AdvanceByTicks
// waits for each one to be taken before firing the next.
func TestAdvanceByTicksDeliversEveryTick(t *testing.T) {
	clk := testkit.NewFakeClock()
	ticker := clk.NewTicker(interval)

	var mu sync.Mutex
	var times []time.Time
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case at := <-ticker.C():
				mu.Lock()
				times = append(times, at)
				mu.Unlock()
			case <-stop:
				return
			}
		}
	}()

	clk.AdvanceByTicks(5)
	if !testkit.WaitFor(15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(times) == 5
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("ticks = %d, want 5", len(times))
	}
	close(stop)

	mu.Lock()
	defer mu.Unlock()
	var last time.Time
	for i, at := range times {
		if !at.After(last) {
			t.Errorf("tick %d arrived at %v, after the previous one at %v", i+1, at, last)
		}
		last = at
	}
}

// Jump moves time without moving the schedule. It is how a test makes one handler look slow.
func TestJumpMovesTheClockWithoutFiringATick(t *testing.T) {
	clk := testkit.NewFakeClock()
	ticker := clk.NewTicker(interval)
	start := clk.Now()

	clk.Jump(3 * time.Millisecond)

	if got := clk.Now().Sub(start); got != 3*time.Millisecond {
		t.Fatalf("clock moved by %v, want 3ms", got)
	}
	if got := len(ticker.C()); got != 0 {
		t.Fatalf("Jump fired %d ticks, want none", got)
	}

	// and the schedule is where it was: the first tick is still one interval after the start
	clk.Advance(interval - 3*time.Millisecond)
	if got := len(ticker.C()); got != 1 {
		t.Fatalf("buffered ticks = %d, want 1 after one interval", got)
	}
}

func TestAStoppedTickerStops(t *testing.T) {
	clk := testkit.NewFakeClock()
	ticker := clk.NewTicker(interval)

	ticker.Stop()
	clk.Advance(3 * interval)

	if got := len(ticker.C()); got != 0 {
		t.Fatalf("a stopped ticker delivered %d ticks", got)
	}
	if tickers := clk.Tickers(); len(tickers) != 1 || !tickers[0].Stopped() {
		t.Fatal("Tickers did not report the ticker that was stopped")
	}
}

// WaitFor is the alternative to a sleep that is either too short (flaky) or too long (slow).
func TestWaitForGivesUpAndSaysSo(t *testing.T) {
	if testkit.WaitFor(50*time.Millisecond, func() bool { return false }) {
		t.Fatal("WaitFor reported success for a condition that never held")
	}
	started := time.Now()
	if !testkit.WaitFor(15*time.Second, func() bool { return true }) {
		t.Fatal("WaitFor reported failure for a condition that held")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("WaitFor took %v to see a condition that was already true", elapsed)
	}
}
