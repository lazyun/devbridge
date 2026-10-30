package workspace

import (
	"context"
	"sort"
	"sync"
)

type MemoryStore struct {
	mu      sync.RWMutex
	items   map[string]Workspace
	byArea  map[string]string
	byOwner map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]Workspace), byArea: make(map[string]string), byOwner: make(map[string]string)}
}

func (s *MemoryStore) Create(_ context.Context, value Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.OwnerKind == "" {
		value.OwnerKind, value.OwnerID = OwnerFeatureArea, value.FeatureAreaID
	}
	if _, ok := s.items[value.ID]; ok || value.ProjectID == "" || value.OwnerID == "" || s.byOwner[string(value.OwnerKind)+"\x00"+value.OwnerID] != "" || (value.OwnerKind == OwnerFeatureArea && value.FeatureAreaID == "") || (value.OwnerKind == OwnerProjectInspection && (value.FeatureAreaID != "" || value.InspectionTaskID == "" || value.InspectionID != value.OwnerID)) || (value.OwnerKind == OwnerRecoveryStatus && (value.RepositoryID != "" || value.BaseCommit != "" || value.InspectionID != "" || value.InspectionTaskID != "")) || (value.OwnerKind != OwnerFeatureArea && value.OwnerKind != OwnerProjectInspection && value.OwnerKind != OwnerRecoveryStatus) || (len(value.Members) == 0 && (value.RepositoryID == "" || value.BaseCommit == "")) {
		return ErrConflict
	}
	if len(value.Members) == 0 {
		value.Members = []Member{LegacyMember(value)}
	}
	if err := ValidateMembers(value); err != nil {
		return err
	}
	s.items[value.ID] = Clone(value)
	if value.OwnerKind == OwnerFeatureArea {
		s.byArea[value.FeatureAreaID] = value.ID
	}
	s.byOwner[string(value.OwnerKind)+"\x00"+value.OwnerID] = value.ID
	return nil
}

func (s *MemoryStore) GetByOwner(_ context.Context, kind OwnerKind, ownerID string) (Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.items[s.byOwner[string(kind)+"\x00"+ownerID]]
	if !ok {
		return Workspace{}, ErrNotFound
	}
	return Clone(value), nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.items[id]
	if !ok {
		return Workspace{}, ErrNotFound
	}
	return Clone(value), nil
}

func (s *MemoryStore) GetByFeatureArea(_ context.Context, areaID string) (Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := s.byArea[areaID]
	value, ok := s.items[id]
	if !ok {
		return Workspace{}, ErrNotFound
	}
	return Clone(value), nil
}

func (s *MemoryStore) List(_ context.Context) ([]Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Workspace, 0, len(s.items))
	for _, value := range s.items {
		values = append(values, Clone(value))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

func (s *MemoryStore) Update(_ context.Context, id string, update func(*Workspace) error) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.items[id]
	if !ok {
		return Workspace{}, ErrNotFound
	}
	previous := Clone(value)
	value = Clone(value)
	if err := update(&value); err != nil {
		return Workspace{}, err
	}
	if value.ID != previous.ID || value.ProjectID != previous.ProjectID || value.RepositoryID != previous.RepositoryID || value.FeatureAreaID != previous.FeatureAreaID || value.OwnerKind != previous.OwnerKind || value.OwnerID != previous.OwnerID || value.InspectionTaskID != previous.InspectionTaskID || value.InspectionID != previous.InspectionID || value.BaseCommit != previous.BaseCommit || value.LegacyThreadID != previous.LegacyThreadID || value.RuntimeQuarantined != previous.RuntimeQuarantined || !value.CreatedAt.Equal(previous.CreatedAt) {
		return Workspace{}, ErrConflict
	}
	if err := SyncLegacyWorktreeUpdate(previous, &value); err != nil {
		return Workspace{}, err
	}
	if err := ValidateMemberUpdate(previous, value); err != nil {
		return Workspace{}, err
	}
	s.items[id] = Clone(value)
	return Clone(value), nil
}
