package contextprobe

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu   sync.Mutex
	data map[string]Probe
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{data: map[string]Probe{}} }

func (s *MemoryStore) Create(_ context.Context, value Probe) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[value.ID]; exists {
		return ErrConflict
	}
	s.data[value.ID] = clone(value)
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Probe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	if !ok {
		return Probe{}, ErrNotFound
	}
	return clone(value), nil
}

func (s *MemoryStore) List(_ context.Context) ([]Probe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]Probe, 0, len(s.data))
	for _, value := range s.data {
		values = append(values, clone(value))
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	return values, nil
}

func (s *MemoryStore) RecordPrompt(_ context.Context, id, prompt string, sentAt time.Time) (Probe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	if !ok {
		return Probe{}, ErrNotFound
	}
	if err := setPrompt(&value, prompt, sentAt); err != nil {
		return Probe{}, err
	}
	s.data[id] = clone(value)
	return clone(value), nil
}

func (s *MemoryStore) Update(_ context.Context, id string, update func(*Probe) error) (Probe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	if !ok {
		return Probe{}, ErrNotFound
	}
	previous := clone(value)
	if err := update(&value); err != nil {
		return Probe{}, err
	}
	if value.AISessionID != previous.AISessionID || value.ProjectID != previous.ProjectID || value.RepositoryID != previous.RepositoryID || value.CodexConfigID != previous.CodexConfigID || value.Scope.Kind != previous.Scope.Kind || !sameScope(value.Scope, previous.Scope) || value.CodexInstruction != previous.CodexInstruction {
		return Probe{}, ErrConflict
	}
	s.data[id] = clone(value)
	return clone(value), nil
}

func (s *MemoryStore) RecoverRunning(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, value := range s.data {
		if value.Status == StatusRunning {
			value.Status, value.FinishedAt, value.UpdatedAt, value.FailureMessage = StatusFailed, &now, now, "Bridge restarted during context probe"
			s.data[id] = value
		}
	}
	return nil
}
