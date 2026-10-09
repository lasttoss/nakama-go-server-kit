package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrNoChange tells Update that the change decided nothing needed to be written. It is not a failure:
// the caller gets the object as it was, with no write and no new version. It exists so that "already
// done" can be answered without pretending to write.
var ErrNoChange = errors.New("storage: no change")

// Once grants something exactly once per request id.
//
// Retrying is normal: a client loses its connection and asks again, a queue delivers a message twice,
// a player taps twice on a laggy phone. Granting a reward twice is not. The record of ids that have
// been applied lives in the same object as the state it changed, so the check and the effect land in
// one compare-and-swap - which an id checked in one table and applied in another does not give you.
type Once struct {
	store Store
	key   string
	keep  int
	retry []Option
}

// OnceOption changes an Once.
type OnceOption func(*Once)

// Keep is how many applied request ids are remembered. It defaults to 128, which covers the retries of
// a session and no more: the list is bounded on purpose, because keeping every id a game has ever
// granted is a leak that only shows up in production a year later. An id that has been forgotten can
// be granted again, which is why it is worth thinking about what "long enough" means for the reward.
func Keep(n int) OnceOption { return func(o *Once) { o.keep = n } }

// NewOnce returns a grant ledger over an object. The key is the object the state lives in, so a player
// has one ledger per thing that can be granted twice.
func NewOnce(store Store, key string, opts ...OnceOption) *Once {
	o := &Once{store: store, key: key, keep: 128}
	for _, opt := range opts {
		opt(o)
	}
	if o.keep < 1 {
		o.keep = 1
	}
	return o
}

// Retry changes how the underlying write retries a version conflict.
func (o *Once) Retry(opts ...Option) *Once {
	o.retry = opts
	return o
}

// Grant is the outcome of a request.
type Grant struct {
	// Applied is false when this request id had already been granted, in which case the effect was not
	// run again and nothing was written.
	Applied bool
	// State is what the object holds after the request: the effect's result when it was applied now,
	// and the stored state when it had been applied before. A retry gets the same answer as the first
	// attempt, rather than nothing at all.
	State []byte
	// Version identifies the object this answer came from.
	Version string
}

// Do applies effect at most once for requestID and reports whether it applied now.
//
// effect receives the state the object held - the state, not the ledger around it - and returns the
// state to store. An error from effect is returned unchanged and nothing is recorded, so a reward that
// could not be written stays retryable.
func (o *Once) Do(ctx context.Context, requestID string, effect Apply) (Grant, error) {
	if requestID == "" {
		return Grant{}, errors.New("storage: a grant needs a request id, or a second tap cannot be told from a retry")
	}

	var grant Grant
	object, err := Update(ctx, o.store, o.key, func(current []byte, exists bool) ([]byte, error) {
		grant = Grant{}

		ledger, err := decodeLedger(current, exists)
		if err != nil {
			return nil, err
		}
		if ledger.applied(requestID) {
			grant.State = ledger.State
			return nil, ErrNoChange
		}

		state, err := effect(ledger.State, exists)
		if err != nil {
			return nil, err
		}
		ledger.record(requestID, time.Now(), o.keep)
		ledger.State = state
		grant.Applied, grant.State = true, state

		return json.Marshal(ledger)
	}, o.retry...)
	if err != nil {
		return Grant{}, err
	}

	grant.Version = object.Version
	return grant, nil
}

// Applied is what the ledger holds: the state, and when each request id was applied.
type ledger struct {
	IDs   map[string]int64 `json:"ids"`
	State []byte           `json:"state,omitempty"`
}

func decodeLedger(current []byte, exists bool) (ledger, error) {
	l := ledger{IDs: map[string]int64{}}
	if !exists || len(current) == 0 {
		return l, nil
	}
	if err := json.Unmarshal(current, &l); err != nil {
		return ledger{}, fmt.Errorf("storage: the grant ledger is not readable: %w", err)
	}
	if l.IDs == nil {
		l.IDs = map[string]int64{}
	}
	return l, nil
}

func (l ledger) applied(requestID string) bool {
	_, done := l.IDs[requestID]
	return done
}

// record remembers a request id and forgets the oldest ones past the limit.
func (l *ledger) record(requestID string, at time.Time, keep int) {
	l.IDs[requestID] = at.UnixNano()
	if len(l.IDs) <= keep {
		return
	}

	ids := make([]string, 0, len(l.IDs))
	for id := range l.IDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if l.IDs[ids[i]] != l.IDs[ids[j]] {
			return l.IDs[ids[i]] < l.IDs[ids[j]]
		}
		return ids[i] < ids[j] // the same instant twice still has to sort the same way every time
	})
	for _, id := range ids[:len(ids)-keep] {
		delete(l.IDs, id)
	}
}
