package project

import (
	"context"
	"sort"
	"sync"
)

type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]Project
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{data: make(map[string]Project)} }

func (s *MemoryStore) Create(_ context.Context, value Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[value.ID]; ok {
		return ErrConflict
	}
	s.data[value.ID] = value
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.data[id]
	if !ok {
		return Project{}, ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) List(_ context.Context) ([]Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Project, 0, len(s.data))
	for _, value := range s.data {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}
