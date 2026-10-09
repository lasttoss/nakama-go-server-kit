package match

import "time"

// Clock is the passage of time, as far as the loop is concerned. A tick loop is the one place in a
// game server where the wall clock is the whole point, which is also what makes it untestable: a
// test that sleeps for a second is a slow test, and a test that expects sixty ticks in a second is
// a test that fails on a loaded machine. Handing the loop a clock lets a test step time by hand and
// assert on ticks that never happened.
type Clock interface {
	// Now is the current time. The loop uses it to measure how long a tick took.
	Now() time.Time
	// NewTicker fires every interval until Stop is called.
	NewTicker(interval time.Duration) Ticker
}

// Ticker is what Clock.NewTicker hands back.
type Ticker interface {
	// C is the channel the ticks arrive on.
	C() <-chan time.Time
	// Stop releases the ticker.
	Stop()
}

// RealClock is the clock a server runs on.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) NewTicker(interval time.Duration) Ticker {
	return realTicker{t: time.NewTicker(interval)}
}

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }
