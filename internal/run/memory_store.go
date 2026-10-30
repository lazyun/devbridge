package run

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("run not found")
var ErrOwnerImmutable = errors.New("Run operation ownership is immutable")

type MemoryStore struct {
	mu                sync.RWMutex
	data              map[string]Run
	attempts          map[string]int
	executionEvidence map[string]json.RawMessage
}

func NewMemoryStore(initial ...Run) *MemoryStore {
	s := &MemoryStore{executionEvidence: map[string]json.RawMessage{}, data: make(map[string]Run), attempts: make(map[string]int)}
	for _, value := range initial {
		s.data[value.ID] = clone(value)
		key := value.TaskID
		if value.ReviewID != "" {
			key = "review:" + value.ReviewID
		}
		if value.Attempt > s.attempts[key] {
			s.attempts[key] = value.Attempt
		}
	}
	return s
}

func (s *MemoryStore) CreateAttempt(_ context.Context, taskID, runID string, now time.Time) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[runID]; exists {
		return Run{}, errors.New("run id already exists")
	}
	s.attempts[taskID]++
	value := Run{ID: runID, TaskID: taskID, Purpose: PurposeImplementation, Attempt: s.attempts[taskID], Status: StatusCreated, CreatedAt: now, UpdatedAt: now, TokenUsage: TokenUsageAccounting{Status: TokenUsagePending}}
	s.data[runID] = value
	return clone(value), nil
}

func (s *MemoryStore) CreateExecutionAttempt(_ context.Context, taskID, runID string, purpose Purpose, executionProposalID string, now time.Time) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[runID]; exists {
		return Run{}, errors.New("run id already exists")
	}
	s.attempts[taskID]++
	value := Run{ID: runID, TaskID: taskID, Purpose: purpose, AgentExecutionID: executionProposalID, ExecutionProposalID: executionProposalID, OperationKind: OperationExecution, OperationID: executionProposalID, Attempt: s.attempts[taskID], Status: StatusCreated, CreatedAt: now, UpdatedAt: now, TokenUsage: TokenUsageAccounting{Status: TokenUsagePending}}
	s.data[runID] = value
	return clone(value), nil
}

func (s *MemoryStore) CreateScopeReviewAttempt(_ context.Context, taskID, runID, instruction string, scope ReviewScope, now time.Time) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[runID]; exists {
		return Run{}, errors.New("run id already exists")
	}
	s.attempts[taskID]++
	value := Run{ID: runID, TaskID: taskID, Purpose: PurposeScopeReview, CodexInstruction: instruction, ReviewScope: ReviewScope{Kind: scope.Kind, Paths: append([]string(nil), scope.Paths...)}, Attempt: s.attempts[taskID], Status: StatusCreated, CreatedAt: now, UpdatedAt: now, TokenUsage: TokenUsageAccounting{Status: TokenUsagePending}}
	s.data[runID] = value
	return clone(value), nil
}

func (s *MemoryStore) CreateFocusedScopeReviewAttempt(_ context.Context, taskID, runID, instruction string, scope ReviewScope, plan ReviewPlan, now time.Time) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[runID]; exists {
		return Run{}, errors.New("run id already exists")
	}
	s.attempts[taskID]++
	value := Run{ID: runID, TaskID: taskID, Purpose: PurposeScopeReview, CodexInstruction: instruction, ReviewScope: ReviewScope{Kind: scope.Kind, Paths: append([]string(nil), scope.Paths...)}, ReviewPlan: ReviewPlan{Mode: plan.Mode, InitialScope: ReviewScope{Kind: plan.InitialScope.Kind, Paths: append([]string(nil), plan.InitialScope.Paths...)}, SelectedModules: append([]ReviewModuleSelection(nil), plan.SelectedModules...)}, Attempt: s.attempts[taskID], Status: StatusCreated, CreatedAt: now, UpdatedAt: now, TokenUsage: TokenUsageAccounting{Status: TokenUsagePending}}
	s.data[runID] = value
	return clone(value), nil
}

// Product Reviews require atomic durable Task admission; this standalone fake
// has no Task ownership authority and must not manufacture a Task identity.
func (s *MemoryStore) CreateProductScopeReviewAttempt(_ context.Context, reviewID, runID, instruction string, scope ReviewScope, plan ReviewPlan, now time.Time) (Run, error) {
	return Run{}, errors.New("product Review requires durable Task-owned admission")
}

func (s *MemoryStore) Get(_ context.Context, id string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.data[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	return clone(value), nil
}

func (s *MemoryStore) List(_ context.Context) ([]Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Run, 0, len(s.data))
	for _, value := range s.data {
		values = append(values, clone(value))
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].TaskID == values[j].TaskID {
			return values[i].Attempt < values[j].Attempt
		}
		return values[i].TaskID < values[j].TaskID
	})
	return values, nil
}

func (s *MemoryStore) LatestByTaskPurpose(_ context.Context, taskID string, purpose Purpose, executionProposalID string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest Run
	for _, value := range s.data {
		if value.TaskID != taskID || value.Purpose != purpose || (executionProposalID != "" && value.ExecutionProposalID != executionProposalID) || (latest.ID != "" && (value.CreatedAt.Before(latest.CreatedAt) || value.CreatedAt.Equal(latest.CreatedAt) && value.ID >= latest.ID)) {
			continue
		}
		latest = value
	}
	if latest.ID == "" {
		return Run{}, ErrNotFound
	}
	return clone(latest), nil
}

func (s *MemoryStore) Update(_ context.Context, id string, update func(*Run) error) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	previous := value
	if err := update(&value); err != nil {
		return Run{}, err
	}
	if !value.SameOwner(previous) {
		return Run{}, ErrOwnerImmutable
	}
	s.data[id] = clone(value)
	return clone(value), nil
}

func (s *MemoryStore) GetByInteraction(_ context.Context, threadID, turnID string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found Run
	for _, v := range s.data {
		if v.ThreadID == threadID && v.TurnID == turnID {
			if found.ID != "" {
				return Run{}, errors.New("ambiguous Run interaction")
			}
			found = v
		}
	}
	if found.ID == "" {
		return Run{}, ErrNotFound
	}
	return clone(found), nil
}

func (s *MemoryStore) PrepareNaturalResult(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[id]
	if !ok {
		return ErrNotFound
	}
	if v.ResultProtocol == NaturalResultProtocol {
		return nil
	}
	if v.TurnID != "" || (v.Status != StatusPreparing && v.Status != StatusRunning) {
		return errors.New("execution contract already dispatched")
	}
	v.ResultProtocol = NaturalResultProtocol
	s.data[id] = v
	return nil
}
func (s *MemoryStore) RecordExecutionCommands(_ context.Context, id string, raw json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[id]
	if !ok {
		return ErrNotFound
	}
	if v.ResultProtocol != NaturalResultProtocol || !json.Valid(raw) {
		return errors.New("execution evidence contract missing")
	}
	if old, ok := s.executionEvidence[id]; ok && string(old) != string(raw) {
		return errors.New("execution evidence immutable")
	}
	s.executionEvidence[id] = append([]byte(nil), raw...)
	return nil
}
