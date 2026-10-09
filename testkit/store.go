package testkit

import (
	"context"
	"fmt"
	"sync"

	"github.com/lasttoss/nakama-go-server-kit/storage"
)

// MemStore is an in-memory storage.Store for tests. It behaves like a real one about the rule that
// matters - a write that quotes a version somebody else has already replaced is refused - and it can
// be told to refuse the next few writes so that a retry loop is tested rather than trusted.
type MemStore struct {
	mu        sync.Mutex
	objects   map[string]storage.Object
	version   int
	conflicts int
	writes    int
}

// NewStore returns an empty store.
func NewStore() *MemStore {
	return &MemStore{objects: map[string]storage.Object{}}
}

// ConflictOnWrite refuses the next n writes with storage.ErrVersionConflict, as if another request
// had written the object first.
func (s *MemStore) ConflictOnWrite(n int) *MemStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conflicts = n
	return s
}

// Writes is how many writes were accepted, which is how a test says that a retry loop did not write
// more than it had to.
func (s *MemStore) Writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// Get returns the value stored under key without the version, or ok false when there is none.
func (s *MemStore) Get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, ok := s.objects[key]
	return append([]byte(nil), object.Value...), ok
}

// Read implements storage.Store.
func (s *MemStore) Read(_ context.Context, key string) (storage.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, ok := s.objects[key]
	if !ok {
		return storage.Object{}, storage.ErrNotFound
	}
	// The caller gets its own copy: a store that hands out its own slice is how a test suite starts
	// lying about what was persisted.
	return storage.Object{Key: object.Key, Version: object.Version, Value: append([]byte(nil), object.Value...)}, nil
}

// Write implements storage.Store.
func (s *MemStore) Write(_ context.Context, key string, value []byte, ifVersion string) (storage.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, exists := s.objects[key]
	if s.conflicts > 0 {
		s.conflicts--
		return storage.Object{}, storage.ErrVersionConflict
	}
	if ifVersion == "" && exists {
		return storage.Object{}, storage.ErrVersionConflict
	}
	if ifVersion != "" && (!exists || current.Version != ifVersion) {
		return storage.Object{}, storage.ErrVersionConflict
	}

	s.version++
	object := storage.Object{
		Key:     key,
		Version: fmt.Sprintf("v%d", s.version),
		Value:   append([]byte(nil), value...),
	}
	s.objects[key] = object
	s.writes++

	return object, nil
}
