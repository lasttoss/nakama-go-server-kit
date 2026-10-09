package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/lasttoss/nakama-go-server-kit/storage"
	"github.com/lasttoss/nakama-go-server-kit/testkit"
)

// The whole point: a client that retries a claim gets the same answer as the first call, and the
// player was paid once.
func TestAGrantAppliedTwiceIsOnlyPaidOnce(t *testing.T) {
	ctx := context.Background()
	store := testkit.NewStore()
	once := storage.NewOnce(store, "players/p1/rewards")

	first, err := once.Do(ctx, "req-1", addGold(100))
	if err != nil {
		t.Fatalf("first grant: %v", err)
	}
	if !first.Applied {
		t.Error("the first call did not apply the grant")
	}
	writes := store.Writes()

	second, err := once.Do(ctx, "req-1", addGold(100))
	if err != nil {
		t.Fatalf("second grant: %v", err)
	}
	if second.Applied {
		t.Error("the same request id was applied twice")
	}
	if string(second.State) != string(first.State) {
		t.Errorf("the retry answered with state %q, want the state the first call left: %q", second.State, first.State)
	}
	if store.Writes() != writes {
		t.Errorf("the retry wrote to the store: %d writes became %d", writes, store.Writes())
	}

	if state := ledgerState(t, store, "players/p1/rewards"); state != "100" {
		t.Fatalf("the ledger holds %q, want the 100 that was granted once", state)
	}
}

func TestADifferentRequestIsGranted(t *testing.T) {
	ctx := context.Background()
	store := testkit.NewStore()
	once := storage.NewOnce(store, "players/p1/rewards")

	if _, err := once.Do(ctx, "req-1", addGold(100)); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	second, err := once.Do(ctx, "req-2", addGold(100))
	if err != nil {
		t.Fatalf("second grant: %v", err)
	}
	if !second.Applied {
		t.Error("a new request id was treated as already applied")
	}
	if string(second.State) != "200" {
		t.Errorf("state = %q, want 200", second.State)
	}
}

// A retry must not run the effect again: an effect can be a mail sent, a purchase charged, a
// leaderboard moved, and "the state ended up the same" is not the same as "it did not happen twice".
func TestTheEffectDoesNotRunForARequestThatAlreadyRan(t *testing.T) {
	ctx := context.Background()
	once := storage.NewOnce(testkit.NewStore(), "players/p1/rewards")
	runs := 0
	effect := func(current []byte, exists bool) ([]byte, error) {
		runs++
		return addGold(100)(current, exists)
	}

	if _, err := once.Do(ctx, "req-1", effect); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	if _, err := once.Do(ctx, "req-1", effect); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if runs != 1 {
		t.Errorf("the effect ran %d times, want 1", runs)
	}
}

// Two identical requests arriving at once, which is what a double tap actually looks like.
func TestTwoIdenticalRequestsAtOnceArePaidOnce(t *testing.T) {
	ctx := context.Background()
	store := testkit.NewStore()
	once := storage.NewOnce(store, "players/p1/rewards").Retry(noSleep, storage.Attempts(20))

	results := make([]storage.Grant, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = once.Do(ctx, "req-1", addGold(100))
		}(i)
	}
	wg.Wait()

	applied := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if results[i].Applied {
			applied++
		}
	}
	if applied != 1 {
		t.Errorf("%d of the two identical requests applied the grant, want 1", applied)
	}
	if state := ledgerState(t, store, "players/p1/rewards"); state != "100" {
		t.Errorf("the ledger holds %q, want the 100 granted by one of the two requests", state)
	}
}

// A reward that could not be written has to stay retryable, or a failed grant is a lost reward.
func TestAGrantThatFailedIsNotRecorded(t *testing.T) {
	ctx := context.Background()
	store := testkit.NewStore()
	once := storage.NewOnce(store, "players/p1/rewards")
	refused := errors.New("the mail service is down")

	if _, err := once.Do(ctx, "req-1", func([]byte, bool) ([]byte, error) {
		return nil, refused
	}); !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the effect's error", err)
	}
	if store.Writes() != 0 {
		t.Errorf("something was written for a grant that failed")
	}

	granted, err := once.Do(ctx, "req-1", addGold(100))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !granted.Applied {
		t.Error("the request id was remembered even though nothing was granted")
	}
}

func TestAGrantNeedsARequestID(t *testing.T) {
	store := testkit.NewStore()
	once := storage.NewOnce(store, "players/p1/rewards")

	_, err := once.Do(context.Background(), "", addGold(100))
	if err == nil {
		t.Fatal("a grant with no request id was accepted")
	}
	if store.Writes() != 0 {
		t.Error("something was written for a request with no id")
	}
}

// The bound, and its consequence, stated by a test rather than discovered in production: an id that
// has been forgotten can be granted again.
func TestOnlyTheMostRecentRequestIDsAreRemembered(t *testing.T) {
	ctx := context.Background()
	once := storage.NewOnce(testkit.NewStore(), "players/p1/rewards", storage.Keep(2))
	for _, id := range []string{"req-1", "req-2", "req-3"} {
		if _, err := once.Do(ctx, id, addGold(1)); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}

	if again, err := once.Do(ctx, "req-3", addGold(1)); err != nil || again.Applied {
		t.Errorf("the most recent id was forgotten: applied = %v, err = %v", again.Applied, err)
	}
	forgotten, err := once.Do(ctx, "req-1", addGold(1))
	if err != nil {
		t.Fatalf("req-1: %v", err)
	}
	if !forgotten.Applied {
		t.Error("a forgotten id was refused; the limit is meant to forget the oldest ids, not to refuse them")
	}
}

func TestALedgerThatCannotBeReadIsReportedRatherThanOverwritten(t *testing.T) {
	ctx := context.Background()
	store := testkit.NewStore()
	if _, err := store.Write(ctx, "players/p1/rewards", []byte("not a ledger"), ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := storage.NewOnce(store, "players/p1/rewards").Do(ctx, "req-1", addGold(100))
	if err == nil {
		t.Fatal("a corrupt ledger was overwritten instead of reported")
	}
	if gold, _ := store.Get("players/p1/rewards"); string(gold) != "not a ledger" {
		t.Errorf("the object became %q", gold)
	}
}

// ledgerState reads the state out of the ledger object the way the caller would have to: the ids
// and the state they changed are one object, so a test that asserts on the raw value is asserting on
// the envelope rather than on the reward.
func ledgerState(t *testing.T, store *testkit.MemStore, key string) string {
	t.Helper()
	raw, ok := store.Get(key)
	if !ok {
		t.Fatalf("nothing is stored under %s", key)
	}
	var envelope struct {
		State []byte `json:"state"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("the ledger is not readable: %v (%s)", err, raw)
	}
	return string(envelope.State)
}

func addGold(amount int) storage.Apply {
	return func(current []byte, exists bool) ([]byte, error) {
		gold := 0
		if exists && len(current) > 0 {
			parsed, err := strconv.Atoi(string(current))
			if err != nil {
				return nil, err
			}
			gold = parsed
		}
		return []byte(strconv.Itoa(gold + amount)), nil
	}
}
