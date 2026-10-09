// Package storage holds the part of a game backend that has to survive two requests at once:
// reading a player's save, changing it, and writing it back without losing the other change.
//
// It is written against an interface rather than against a database or Nakama's runtime, for two
// reasons: the rule that matters - a write that quotes a version somebody else has already replaced
// is refused - can then be tested with a fake in milliseconds, and a server that keeps its saves in
// Nakama's storage engine and the one that keeps them in Postgres use the same code.
package storage

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

var (
	// ErrNotFound means the object does not exist.
	ErrNotFound = errors.New("storage: object not found")
	// ErrVersionConflict means somebody else wrote the object between the read and the write, so the
	// write was refused rather than silently overwriting their change.
	ErrVersionConflict = errors.New("storage: version conflict")
)

// Object is a stored value together with the version that identifies which revision of it this is.
// The version is opaque: a store hands one out on every write, and a write quotes the one it read.
type Object struct {
	Key     string
	Version string
	Value   []byte
}

// Store is the whole of what this package needs from a database: read an object, and write one back
// under a condition. A Nakama storage engine, a Postgres table with a version column, and the fake
// in testkit all satisfy it.
type Store interface {
	// Read returns ErrNotFound when there is no object under key.
	Read(ctx context.Context, key string) (Object, error)
	// Write stores value under key and returns the object with its new version.
	//
	// ifVersion is the version that was read: the write succeeds only if the object still has it.
	// An empty ifVersion means "this object must not exist yet", which makes creating an object
	// safe against a second request creating it at the same time.
	Write(ctx context.Context, key string, value []byte, ifVersion string) (Object, error)
}

// Apply changes an object. current is what was read and exists says whether there was anything to
// read; the returned value is written back.
type Apply func(current []byte, exists bool) ([]byte, error)

// SleepFunc pauses between two attempts. It is a function so that a test can run the retry loop a
// hundred times in no time at all.
type SleepFunc func(ctx context.Context, d time.Duration) error

// Options for Update.
type Options struct {
	attempts int
	sleep    SleepFunc
}

// Option changes how Update retries.
type Option func(*Options)

// Attempts is how many times Update may read and apply before giving up. Five is enough for the case
// this exists for - a handful of players saving the same object at the same second - and low enough
// that a loop that cannot converge fails instead of hammering the database.
func Attempts(n int) Option { return func(o *Options) { o.attempts = n } }

// Sleeper replaces the wait between attempts.
func Sleeper(f SleepFunc) Option { return func(o *Options) { o.sleep = f } }

func options(opts []Option) Options {
	o := Options{attempts: 5, sleep: sleepContext}
	for _, opt := range opts {
		opt(&o)
	}
	if o.attempts < 1 {
		o.attempts = 1
	}
	if o.sleep == nil {
		o.sleep = sleepContext
	}
	return o
}

// Update reads an object, applies change to it, and writes the result back only if nobody else
// changed the object in between, retrying when they did.
//
// This is the shape a save or a purchase has to have. Reading a player's gold, adding a reward and
// writing the total back is the bug that pays a player twice: the two requests read the same total
// and the second write overwrites the first. Quoting the version means the loser is told so and
// applies its change to the winner's value instead.
//
// An error from change stops the loop and is returned unchanged: a purchase that cannot be afforded
// is not a race to retry. Returning ErrNoChange means the change decided there was nothing to write. A refused write is retried, with a short jittered wait, because two clients
// retrying in lockstep would keep colliding.
//
// The returned object carries the value that was finally written, and the version that identifies it.
func Update(ctx context.Context, store Store, key string, change Apply, opts ...Option) (Object, error) {
	o := options(opts)

	var lastErr error
	for attempt := 1; attempt <= o.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return Object{}, err
		}

		current, err := store.Read(ctx, key)
		exists := err == nil
		switch {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			current = Object{Key: key}
		default:
			return Object{}, fmt.Errorf("storage: read %s: %w", key, err)
		}

		value, err := change(current.Value, exists)
		switch {
		case err == nil:
		case errors.Is(err, ErrNoChange):
			// Nothing to write, and saying so is not a failure: the caller gets what is stored, and
			// the object keeps its version.
			return current, nil
		default:
			return Object{}, err
		}

		written, err := store.Write(ctx, key, value, current.Version)
		switch {
		case err == nil:
			return written, nil
		case errors.Is(err, ErrVersionConflict):
			lastErr = err
		default:
			return Object{}, fmt.Errorf("storage: write %s: %w", key, err)
		}

		if attempt < o.attempts {
			if err := o.sleep(ctx, backoff(attempt)); err != nil {
				return Object{}, err
			}
		}
	}

	return Object{}, fmt.Errorf("storage: update %s: %w after %d attempts", key, lastErr, o.attempts)
}

// ReadTo is Read with the "there is nothing there yet" case spelled out, which is what most callers
// mean: a player who has never played has no save, and that is not an error.
func ReadTo(ctx context.Context, store Store, key string) (Object, bool, error) {
	object, err := store.Read(ctx, key)
	switch {
	case err == nil:
		return object, true, nil
	case errors.Is(err, ErrNotFound):
		return Object{Key: key}, false, nil
	default:
		return Object{}, false, fmt.Errorf("storage: read %s: %w", key, err)
	}
}

// backoff grows a little and is then jittered: two requests that collided once are likely to collide
// again at the same moment, so waiting a fixed time would just repeat the collision.
func backoff(attempt int) time.Duration {
	const step = 2 * time.Millisecond
	d := time.Duration(attempt) * step
	if d > 50*time.Millisecond {
		d = 50 * time.Millisecond
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
