package storage_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lasttoss/nakama-go-server-kit/storage"
	"github.com/lasttoss/nakama-go-server-kit/testkit"
)

// noSleep is what makes a retry loop testable: the loop still runs its attempts, just not the waiting.
var noSleep = storage.Sleeper(func(context.Context, time.Duration) error { return nil })

func TestUpdateCreatesAnObjectThatWasNotThere(t *testing.T) {
	store := testkit.NewStore()

	written, err := storage.Update(context.Background(), store, "players/p1/save", func(current []byte, exists bool) ([]byte, error) {
		if exists {
			t.Errorf("a player who has never played has a save: %q", current)
		}
		return []byte("level 1"), nil
	}, noSleep)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if written.Version == "" {
		t.Error("the write came back without a version, so nothing can be updated safely afterwards")
	}
	if got, ok := store.Get("players/p1/save"); !ok || string(got) != "level 1" {
		t.Errorf("stored %q, ok = %v", got, ok)
	}
}

// The reason the package exists: the second request is not allowed to overwrite the first, so it is
// told so and applies its own change on top of the value that beat it.
func TestUpdateAppliesTheChangeOnTopOfWhoeverWonTheRace(t *testing.T) {
	ctx := context.Background()
	store := testkit.NewStore()
	if _, err := store.Write(ctx, "players/p1/save", []byte("the first change"), ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// and that request keeps winning while ours retries
	store.ConflictOnWrite(2)
	before := store.Writes()
	calls := 0

	written, err := storage.Update(ctx, store, "players/p1/save", func(current []byte, exists bool) ([]byte, error) {
		calls++
		if !exists {
			t.Errorf("attempt %d was given nothing to apply the change to", calls)
			return nil, errors.New("lost the object")
		}
		return append(append([]byte(nil), current...), []byte(" and a second change")...), nil
	}, noSleep)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if calls != 3 {
		t.Errorf("change was applied %d times, want 3: twice it should have been told the object had moved on", calls)
	}
	if got := store.Writes() - before; got != 1 {
		t.Errorf("the store accepted %d writes, want 1: a refused write must not be counted as one", got)
	}
	if string(written.Value) != "the first change and a second change" {
		t.Errorf("wrote %q, want the second change applied on top of the first", written.Value)
	}
}

func TestUpdateGivesUpAndSaysWhy(t *testing.T) {
	store := testkit.NewStore().ConflictOnWrite(99)

	_, err := storage.Update(context.Background(), store, "players/p1/save", func([]byte, bool) ([]byte, error) {
		return []byte("never gets written"), nil
	}, noSleep, storage.Attempts(3))
	if !errors.Is(err, storage.ErrVersionConflict) {
		t.Fatalf("err = %v, want a version conflict", err)
	}
	if got := err.Error(); !contains(got, "3 attempts") {
		t.Errorf("the error does not say how hard it tried: %q", got)
	}
	if store.Writes() != 0 {
		t.Errorf("the store accepted %d writes, want none", store.Writes())
	}
}

// Ten increments from two goroutines are ten increments, not nine: this is the test that a save with
// a shop in it has to pass, and the one a read-modify-write without a version fails.
func TestEveryIncrementSurvivesTwoRequestsAtOnce(t *testing.T) {
	store := testkit.NewStore()
	ctx := context.Background()

	var wg sync.WaitGroup
	for player := 0; player < 2; player++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if _, err := storage.Update(ctx, store, "players/p1/gold", addOne, noSleep, storage.Attempts(20)); err != nil {
					t.Errorf("update: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	gold, ok := store.Get("players/p1/gold")
	if !ok {
		t.Fatal("the object is not there at all")
	}
	if got := string(gold); got != "10" {
		t.Fatalf("gold = %s, want 10: %s of the increments were lost", got, lost(got))
	}
}

// A purchase that cannot be afforded is a decision, not a race: retrying it would charge the player
// twice or not at all, depending on what the second attempt decided.
func TestAnErrorFromTheChangeIsNotRetried(t *testing.T) {
	store := testkit.NewStore()
	refused := errors.New("not enough gold")
	calls := 0

	_, err := storage.Update(context.Background(), store, "players/p1/gold", func([]byte, bool) ([]byte, error) {
		calls++
		return nil, refused
	}, noSleep)
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the error the change returned", err)
	}
	if calls != 1 {
		t.Errorf("the change was asked %d times, want 1", calls)
	}
	if store.Writes() != 0 {
		t.Errorf("something was written anyway")
	}
}

func TestUpdateStopsWhenTheRequestIsOver(t *testing.T) {
	store := testkit.NewStore().ConflictOnWrite(99)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := storage.Update(ctx, store, "players/p1/save", func([]byte, bool) ([]byte, error) {
		t.Error("the change was applied for a request that was already over")
		return nil, nil
	}, noSleep)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestReadToTellsNothingThereFromSomethingWrong(t *testing.T) {
	store := testkit.NewStore()

	if _, ok, err := storage.ReadTo(context.Background(), store, "players/nobody/save"); ok || err != nil {
		t.Errorf("a player with no save: ok = %v, err = %v", ok, err)
	}

	broken := &brokenStore{err: errors.New("the database is gone")}
	_, _, err := storage.ReadTo(context.Background(), broken, "players/p1/save")
	if !errors.Is(err, broken.err) {
		t.Fatalf("err = %v, want the store's error", err)
	}
	if !contains(err.Error(), "players/p1/save") {
		t.Errorf("the error does not name the object: %q", err)
	}
}

func TestUpdateTellsAReadFailureFromAConflict(t *testing.T) {
	broken := &brokenStore{err: errors.New("the database is gone")}

	_, err := storage.Update(context.Background(), broken, "players/p1/save", func([]byte, bool) ([]byte, error) {
		return nil, nil
	}, noSleep)
	if !errors.Is(err, broken.err) {
		t.Fatalf("err = %v, want the store's error rather than a retry loop", err)
	}
	if errors.Is(err, storage.ErrVersionConflict) {
		t.Error("a broken database was reported as a race, which would send the caller into a retry loop")
	}
}

func addOne(current []byte, exists bool) ([]byte, error) {
	if !exists {
		return []byte("1"), nil
	}
	n, err := strconv.Atoi(string(current))
	if err != nil {
		return nil, err
	}
	return []byte(strconv.Itoa(n + 1)), nil
}

func lost(got string) string {
	n, err := strconv.Atoi(got)
	if err != nil {
		return "all"
	}
	return strconv.Itoa(10 - n)
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// brokenStore is a store that is down, which is not the same thing as a race and must not be retried.
type brokenStore struct{ err error }

func (s *brokenStore) Read(context.Context, string) (storage.Object, error) {
	return storage.Object{}, s.err
}

func (s *brokenStore) Write(context.Context, string, []byte, string) (storage.Object, error) {
	return storage.Object{}, s.err
}
