package repository

import (
	"context"
	"errors"
	"sort"
	"sync"
)

var (
	ErrNotFound              = errors.New("repository not found")
	ErrIDExists              = errors.New("repository id already exists")
	ErrPathConflict          = errors.New("repository path belongs to another project")
	ErrRepositoryUnlinked    = errors.New("repository is unlinked historical identity")
	ErrUnlinkedHistoryExists = errors.New("REPOSITORY_UNLINKED_HISTORY_EXISTS")
	ErrProjectMismatch       = errors.New("repository project mismatch")
	ErrInvalidPath           = errors.New("invalid repository path")
	ErrHEADUnavailable       = errors.New("repository HEAD has no available commit")
	ErrNotGit                = errors.New("path is not a usable Git repository")
)

type MemoryStore struct {
	mu     sync.RWMutex
	byID   map[string]Repository
	byPath map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: make(map[string]Repository), byPath: make(map[string]string)}
}

func (s *MemoryStore) PutIfAbsent(_ context.Context, value Repository) (Repository, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byPath[value.Path]; ok {
		if s.byID[id].ProjectID != value.ProjectID {
			return Repository{}, false, ErrPathConflict
		}
		if s.byID[id].RegistrationState == RegistrationUnlinked {
			return Repository{}, false, ErrUnlinkedHistoryExists
		}
		return clone(s.byID[id]), false, nil
	}
	if _, ok := s.byID[value.ID]; ok {
		return Repository{}, false, ErrIDExists
	}
	if value.RegistrationState == "" {
		value.RegistrationState = RegistrationActive
	}
	if value.RegistrationGeneration == 0 {
		value.RegistrationGeneration = 1
	}
	s.byID[value.ID] = clone(value)
	s.byPath[value.Path] = value.ID
	return clone(value), true, nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Repository, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.byID[id]
	if !ok {
		return Repository{}, ErrNotFound
	}
	return clone(value), nil
}

func (s *MemoryStore) List(_ context.Context) ([]Repository, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Repository, 0, len(s.byID))
	for _, value := range s.byID {
		if value.RegistrationState != RegistrationActive {
			continue
		}
		values = append(values, clone(value))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

// ListAll retains historical identities for audit projections.
func (s *MemoryStore) ListAll(_ context.Context) ([]Repository, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Repository, 0, len(s.byID))
	for _, value := range s.byID {
		values = append(values, clone(value))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}
