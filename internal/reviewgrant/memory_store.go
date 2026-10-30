package reviewgrant

import (
	"context"
	"sync"
)

type MemoryStore struct {
	mu   sync.Mutex
	data map[string]Grant
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{data: map[string]Grant{}} }

func (s *MemoryStore) Create(_ context.Context, value Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[value.ReviewID]; exists {
		return ErrConflict
	}
	s.data[value.ReviewID] = clone(value)
	return nil
}

func (s *MemoryStore) Get(_ context.Context, reviewID string) (Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.data[reviewID]
	if !exists {
		return Grant{}, ErrNotFound
	}
	return clone(value), nil
}
