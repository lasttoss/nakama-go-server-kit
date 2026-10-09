package testkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lasttoss/nakama-go-server-kit/storage"
	"github.com/lasttoss/nakama-go-server-kit/testkit"
)

// The fake store is what every test of the storage package leans on, so it gets its own test: a fake
// that is more forgiving than a real database turns a passing test suite into a false claim.
func TestTheFakeStoreRefusesAWriteThatQuotesAStaleVersion(t *testing.T) {
	store := testkit.NewStore()
	ctx := context.Background()

	if _, err := store.Read(ctx, "k"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("reading nothing: err = %v, want ErrNotFound", err)
	}

	first, err := store.Write(ctx, "k", []byte("one"), "")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := store.Write(ctx, "k", []byte("two"), ""); !errors.Is(err, storage.ErrVersionConflict) {
		t.Fatalf("creating an object that exists: err = %v, want a conflict", err)
	}
	if _, err := store.Write(ctx, "k", []byte("winner"), first.Version); err != nil {
		t.Fatalf("writing with the version that was read: %v", err)
	}
	if _, err := store.Write(ctx, "k", []byte("loser"), first.Version); !errors.Is(err, storage.ErrVersionConflict) {
		t.Fatalf("writing with a version that has been replaced: err = %v, want a conflict", err)
	}
	if got, _ := store.Get("k"); string(got) != "winner" {
		t.Errorf("stored %q, want the write that quoted the current version", got)
	}
	if store.Writes() != 2 {
		t.Errorf("accepted %d writes, want 2", store.Writes())
	}
}

func TestTheFakeStoreCanBeToldToRefuseWrites(t *testing.T) {
	store := testkit.NewStore().ConflictOnWrite(1)
	ctx := context.Background()

	if _, err := store.Write(ctx, "k", []byte("one"), ""); !errors.Is(err, storage.ErrVersionConflict) {
		t.Fatalf("the first write was accepted; err = %v", err)
	}
	if _, err := store.Write(ctx, "k", []byte("one"), ""); err != nil {
		t.Fatalf("the second write was refused too: %v", err)
	}
	if store.Writes() != 1 {
		t.Errorf("accepted %d writes, want 1", store.Writes())
	}
}

// A store that hands out its own slice lets a caller change what is stored without writing, and every
// test after that one is testing a value that was never persisted.
func TestTheFakeStoreHandsOutCopies(t *testing.T) {
	store := testkit.NewStore()
	ctx := context.Background()

	value := []byte("original")
	if _, err := store.Write(ctx, "k", value, ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	value[0] = 'X'

	fromGet, _ := store.Get("k")
	fromGet[1] = 'X'
	object, err := store.Read(ctx, "k")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	object.Value[2] = 'X'

	if got, _ := store.Get("k"); string(got) != "original" {
		t.Errorf("stored value became %q: the store is sharing its slice", got)
	}
}
