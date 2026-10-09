package match

import "errors"

var (
	// ErrTickRateUnset is returned when a loop is started without a tick rate.
	ErrTickRateUnset = errors.New("match: TickRate must be set")
	// ErrTickRateTooHigh is returned for a tick rate above 1000, which no server keeps up with and no
	// player can tell apart from a lower one.
	ErrTickRateTooHigh = errors.New("match: TickRate above 1000 is not a rate a server can keep")
	// ErrNoOnTick is returned when a loop is started without anything to run.
	ErrNoOnTick = errors.New("match: OnTick must be set")
)
