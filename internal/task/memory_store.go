package task

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
)

var ErrNotFound = errors.New("task not found")

var (
	ErrCodexConfigImmutable = errors.New("task codex config is immutable")
	ErrBaseCommitImmutable  = errors.New("task base commit is immutable")
	ErrLegacyReadOnly       = errors.New("legacy task is read-only")
	ErrTopicImmutable       = errors.New("task featureArea is immutable")
	ErrConflict             = errors.New("task semantic mutation conflict")
)

type MemoryStore struct {
	mu        sync.RWMutex
	data      map[string]Task
	revisions map[string]DefinitionRevision
	intents   map[string]createIntent
}

type createIntent struct {
	taskID string
	hash   string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]Task), revisions: make(map[string]DefinitionRevision), intents: make(map[string]createIntent)}
}

func (s *MemoryStore) Create(_ context.Context, value Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[value.ID]; exists {
		return errors.New("task id already exists")
	}
	if err := s.snapshot(&value); err != nil {
		return err
	}
	s.data[value.ID] = clone(value)
	return nil
}

func (s *MemoryStore) CreateIntent(_ context.Context, value Task, _ string, request, hash string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := value.ProjectID + "\x00" + request
	if existing, ok := s.intents[key]; ok {
		if existing.hash != hash {
			return Task{}, errors.New("task create intent payload conflicts")
		}
		return clone(s.data[existing.taskID]), nil
	}
	if _, exists := s.data[value.ID]; exists {
		return Task{}, errors.New("task id already exists")
	}
	if err := s.snapshot(&value); err != nil {
		return Task{}, err
	}
	s.data[value.ID] = clone(value)
	s.intents[key] = createIntent{taskID: value.ID, hash: hash}
	return clone(value), nil
}

func (s *MemoryStore) GetIntent(_ context.Context, project, request, hash string) (Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	intent, ok := s.intents[project+"\x00"+request]
	if !ok {
		return Task{}, ErrNotFound
	}
	if intent.hash != hash {
		return Task{}, errors.New("task create intent payload conflicts")
	}
	return clone(s.data[intent.taskID]), nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.data[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return clone(value), nil
}

func (s *MemoryStore) List(_ context.Context) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Task, 0, len(s.data))
	for _, value := range s.data {
		values = append(values, clone(value))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

func (s *MemoryStore) LatestByFeatureArea(_ context.Context, featureAreaID string) (Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest Task
	for _, value := range s.data {
		if value.FeatureAreaID != featureAreaID || (value.Type != "" && value.Type != TypeDevelopment) || (latest.ID != "" && (value.CreatedAt.Before(latest.CreatedAt) || value.CreatedAt.Equal(latest.CreatedAt) && value.ID >= latest.ID)) {
			continue
		}
		latest = value
	}
	if latest.ID == "" {
		return Task{}, ErrNotFound
	}
	return clone(latest), nil
}

func (s *MemoryStore) ActiveFeatureAreaIDsByProject(_ context.Context, projectID string, limit int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, value := range s.data {
		if value.ProjectID == projectID && value.FeatureAreaID != "" && isActive(value.Status) {
			seen[value.FeatureAreaID] = struct{}{}
		}
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}

func (s *MemoryStore) Update(_ context.Context, id string, update func(*Task) error) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	if value.LegacyReadOnly || value.Type != TypeDevelopment {
		return Task{}, ErrLegacyReadOnly
	}
	value = clone(value)
	original := clone(value)
	if err := update(&value); err != nil {
		return Task{}, err
	}
	if !reflect.DeepEqual(value.RepositoryTargets, original.RepositoryTargets) || value.Status != original.Status || value.Phase != original.Phase || !reflect.DeepEqual(value.RequiredReviewDepth, original.RequiredReviewDepth) || value.Title != original.Title || value.ProjectID != original.ProjectID || value.RepositoryID != original.RepositoryID || value.ProblemStatement != original.ProblemStatement || value.Goal != original.Goal || !reflect.DeepEqual(value.Constraints, original.Constraints) || !reflect.DeepEqual(value.WorkSpec, original.WorkSpec) || !reflect.DeepEqual(value.VerificationSpec, original.VerificationSpec) || value.CurrentReviewID != original.CurrentReviewID || value.CurrentExecutionID != original.CurrentExecutionID || value.CurrentDefinitionRevisionID != original.CurrentDefinitionRevisionID || value.ReviewApprovalID != original.ReviewApprovalID {
		return Task{}, ErrConflict
	}
	if original.ReviewApprovalID != "" && (value.Model != original.Model || value.ReasoningEffort != original.ReasoningEffort) {
		return Task{}, ErrConflict
	}
	if value.CodexConfigID != original.CodexConfigID {
		return Task{}, ErrCodexConfigImmutable
	}
	if original.BaseCommit != "" && value.BaseCommit != original.BaseCommit {
		return Task{}, ErrBaseCommitImmutable
	}
	if value.FeatureAreaID != original.FeatureAreaID {
		return Task{}, ErrTopicImmutable
	}
	if err := s.snapshot(&value); err != nil {
		return Task{}, err
	}
	s.data[id] = clone(value)
	return clone(value), nil
}

func (s *MemoryStore) snapshot(value *Task) error {
	if value.Phase.ReviewStage() && value.ReviewApprovalID == "" {
		return nil
	}
	if value.LegacyReadOnly {
		return ErrLegacyReadOnly
	}
	definition := DefinitionFromTask(*value, nil)
	_, hash, err := EncodeDefinition(definition)
	if err != nil {
		return err
	}
	current := s.revisions[value.CurrentDefinitionRevisionID]
	if current.ID != "" && current.PayloadSHA256 == hash {
		return nil
	}
	ordinal := current.Ordinal + 1
	revision := DefinitionRevision{ID: fmt.Sprintf("task_definition_%s_%d_%s", value.ID, ordinal, hash[:16]), TaskID: value.ID, Ordinal: ordinal, Definition: definition, PayloadSHA256: hash, CreatedAt: value.UpdatedAt}
	s.revisions[revision.ID] = revision
	value.CurrentDefinitionRevisionID = revision.ID
	return nil
}
func (s *MemoryStore) GetDefinitionRevision(_ context.Context, id string) (DefinitionRevision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.revisions[id]
	if !ok {
		return DefinitionRevision{}, ErrNotFound
	}
	raw, hash, err := EncodeDefinition(value.Definition)
	if err != nil {
		return value, err
	}
	value.Definition, err = DecodeDefinition(raw, hash)
	return value, err
}
