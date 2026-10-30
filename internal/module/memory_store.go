package module

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]Module
}

func (s *MemoryStore) ReplaceContextMap(_ context.Context, projectID string, values []Module, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, value := range s.data {
		if value.ProjectID == projectID {
			value.ProductionFacts = nil
			s.data[id] = value
		}
	}
	for _, value := range values {
		for id, existing := range s.data {
			if existing.ProjectID == projectID && existing.Key == value.Key {
				value.ID, value.CreatedAt, value.ParentID, value.SystemOwned = existing.ID, existing.CreatedAt, existing.ParentID, existing.SystemOwned
				delete(s.data, id)
				break
			}
		}
		s.data[value.ID] = clone(value)
	}
	return nil
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{data: make(map[string]Module)} }

func (s *MemoryStore) Create(_ context.Context, value Module) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[value.ID]; exists {
		return ErrConflict
	}
	for _, existing := range s.data {
		if existing.ProjectID == value.ProjectID && existing.Key == value.Key {
			return ErrConflict
		}
	}
	s.data[value.ID] = clone(value)
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Module, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.data[id]
	if !exists {
		return Module{}, ErrNotFound
	}
	return clone(value), nil
}

func (s *MemoryStore) ListByProject(_ context.Context, projectID string) ([]Module, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Module, 0)
	for _, value := range s.data {
		if value.ProjectID == projectID {
			values = append(values, clone(value))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Key < values[j].Key })
	return values, nil
}
