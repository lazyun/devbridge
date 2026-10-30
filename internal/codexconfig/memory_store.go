package codexconfig

import (
	"context"
	"sort"
	"sync"
)

type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]Config
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{data: make(map[string]Config)} }

func (s *MemoryStore) Create(_ context.Context, value Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[value.ID]; exists {
		return ErrConflict
	}
	s.data[value.ID] = value
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.data[id]
	if !exists {
		return Config{}, ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) List(_ context.Context) ([]Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Config, 0, len(s.data))
	for _, value := range s.data {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}
