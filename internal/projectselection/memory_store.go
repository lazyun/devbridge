package projectselection

import (
	"context"
	"sync"
)

type MemoryStore struct {
	mu    sync.Mutex
	value Selection
	set   bool
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }
func (s *MemoryStore) Get(context.Context) (Selection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set {
		return Selection{}, ErrNotSelected
	}
	return s.value, nil
}
func (s *MemoryStore) Set(_ context.Context, value Selection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value, s.set = value, true
	return nil
}
