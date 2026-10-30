package featurearea

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
)

type MemoryStore struct {
	mu        sync.RWMutex
	data      map[string]FeatureArea
	relations []Relation
	taskCount SplitTaskCount
	receipts  map[string]memoryLifecycleReceipt
	creations map[string]CreationReceipt
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]FeatureArea), receipts: make(map[string]memoryLifecycleReceipt), creations: make(map[string]CreationReceipt)}
}

func (s *MemoryStore) Create(_ context.Context, value FeatureArea) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[value.ID]; exists {
		return ErrConflict
	}
	s.data[value.ID] = value
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (FeatureArea, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.data[id]
	if !exists {
		return FeatureArea{}, ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) ListByProject(_ context.Context, projectID string) ([]FeatureArea, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]FeatureArea, 0)
	for _, value := range s.data {
		if value.ProjectID == projectID {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

func (s *MemoryStore) Update(_ context.Context, id string, update func(*FeatureArea) error) (FeatureArea, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.data[id]
	if !exists {
		return FeatureArea{}, ErrNotFound
	}
	previous := value
	if err := update(&value); err != nil {
		return FeatureArea{}, err
	}
	if value.ID != previous.ID || value.ProjectID != previous.ProjectID || value.CreatedByAISessionID != previous.CreatedByAISessionID || value.Title != previous.Title || value.ProblemStatement != previous.ProblemStatement || !value.CreatedAt.Equal(previous.CreatedAt) {
		return FeatureArea{}, ErrConflict
	}
	s.data[id] = value
	return value, nil
}

func (s *MemoryStore) RecordRelation(_ context.Context, relation Relation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.relations {
		if existing.SourceFeatureAreaID == relation.SourceFeatureAreaID && existing.TargetFeatureAreaID == relation.TargetFeatureAreaID {
			return ErrConflict
		}
	}
	s.relations = append(s.relations, relation)
	return nil
}

func (s *MemoryStore) ListRelationsBySource(_ context.Context, sourceID string) ([]Relation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Relation, 0)
	for _, relation := range s.relations {
		if relation.SourceFeatureAreaID == sourceID {
			values = append(values, relation)
		}
	}
	return values, nil
}

func (s *MemoryStore) ListRelationsByTarget(_ context.Context, targetID string) ([]Relation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Relation, 0)
	for _, relation := range s.relations {
		if relation.TargetFeatureAreaID == targetID {
			values = append(values, relation)
		}
	}
	return values, nil
}

type memoryLifecycleReceipt struct {
	Hash   string
	Result []byte
}

func creationKey(sessionID, projectID, command, requestID string) string {
	return sessionID + "\x00" + projectID + "\x00" + command + "\x00" + requestID
}

func (s *MemoryStore) ReplayCreation(_ context.Context, sessionID, projectID, command, requestID, payloadSHA256 string) (FeatureArea, CreationReceipt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	receipt, ok := s.creations[creationKey(sessionID, projectID, command, requestID)]
	if !ok {
		return FeatureArea{}, CreationReceipt{}, ErrNotFound
	}
	if receipt.PayloadSHA256 != payloadSHA256 {
		return FeatureArea{}, CreationReceipt{}, ErrConflict
	}
	value, ok := s.data[receipt.FeatureAreaID]
	if !ok {
		return FeatureArea{}, CreationReceipt{}, ErrNotFound
	}
	return value, cloneCreationReceipt(receipt), nil
}

func (s *MemoryStore) CreateReceipted(_ context.Context, value FeatureArea, receipt CreationReceipt) (FeatureArea, CreationReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := creationKey(receipt.AISessionID, receipt.ProjectID, receipt.Command, receipt.RequestID)
	if existing, ok := s.creations[key]; ok {
		if existing.PayloadSHA256 != receipt.PayloadSHA256 {
			return FeatureArea{}, CreationReceipt{}, ErrConflict
		}
		area, ok := s.data[existing.FeatureAreaID]
		if !ok {
			return FeatureArea{}, CreationReceipt{}, ErrNotFound
		}
		return area, cloneCreationReceipt(existing), nil
	}
	if _, exists := s.data[value.ID]; exists || receipt.FeatureAreaID != value.ID {
		return FeatureArea{}, CreationReceipt{}, ErrConflict
	}
	s.data[value.ID] = value
	s.creations[key] = cloneCreationReceipt(receipt)
	return value, cloneCreationReceipt(receipt), nil
}

func (s *MemoryStore) UpdateCreationReceipt(_ context.Context, receipt CreationReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := creationKey(receipt.AISessionID, receipt.ProjectID, receipt.Command, receipt.RequestID)
	previous, ok := s.creations[key]
	if !ok {
		return ErrNotFound
	}
	if !sameCreationRoot(previous, receipt) || !monotonicCreationReceipt(previous, receipt) {
		return ErrConflict
	}
	s.creations[key] = cloneCreationReceipt(receipt)
	return nil
}

func cloneCreationReceipt(value CreationReceipt) CreationReceipt {
	value.Prepared = append([]byte(nil), value.Prepared...)
	return value
}

func sameCreationRoot(a, b CreationReceipt) bool {
	return a.AISessionID == b.AISessionID && a.ProjectID == b.ProjectID && a.Command == b.Command && a.RequestID == b.RequestID && a.PayloadSHA256 == b.PayloadSHA256 && a.ContractVersion == b.ContractVersion && string(a.Prepared) == string(b.Prepared) && a.FeatureAreaID == b.FeatureAreaID
}

func monotonicCreationReceipt(old, next CreationReceipt) bool {
	if (old.TaskID != "" && old.TaskID != next.TaskID) || (old.ReviewID != "" && old.ReviewID != next.ReviewID) || (old.RunID != "" && old.RunID != next.RunID) || (old.ReviewAdmitted && !next.ReviewAdmitted) {
		return false
	}
	return true
}

func (s *MemoryStore) ApplyLifecycle(ctx context.Context, input LifecycleMutation) (LifecycleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	empty := LifecycleResult{}
	parent, ok := s.data[input.FeatureAreaID]
	if !ok || parent.ProjectID != input.ProjectID {
		return empty, ErrNotFound
	}
	key := input.FeatureAreaID + "\x00" + input.RequestID
	if receipt, ok := s.receipts[key]; ok {
		if receipt.Hash != input.PayloadSHA256 {
			return empty, ErrConflict
		}
		var result LifecycleResult
		err := json.Unmarshal(receipt.Result, &result)
		if err == nil && input.Action == ActionRetire {
			result.Parent = parent
		}
		return result, err
	}
	if input.Action == ActionRetire && (parent.Status == StatusRetiring || parent.Status == StatusRetired) {
		return LifecycleResult{Parent: parent, Children: []FeatureArea{}, Relations: []Relation{}}, nil
	}
	target, err := LifecycleTarget(parent, input.Action)
	if err != nil {
		return empty, err
	}
	if input.Action == ActionDecompose {
		if len(input.Children) == 0 || s.taskCount == nil {
			return empty, ErrConflict
		}
		count, err := s.taskCount(ctx, parent.ID)
		if err != nil {
			return empty, err
		}
		if count != 0 {
			return empty, ErrConflict
		}
	}
	seen := map[string]bool{}
	for _, child := range input.Children {
		if _, ok := s.data[child.ID]; ok || seen[child.ID] || child.ProjectID != parent.ProjectID {
			return empty, ErrConflict
		}
		seen[child.ID] = true
	}
	result := LifecycleResult{Parent: parent, Children: []FeatureArea{}, Relations: []Relation{}}
	result.Parent.Status, result.Parent.UpdatedAt = target, input.At
	if input.Action == ActionRetire {
		result.Parent.Retirement = &Retirement{RequestID: input.RequestID, RequestedAt: input.At, Summary: "正在完成关闭"}
	}
	for _, child := range input.Children {
		relation := Relation{SourceFeatureAreaID: parent.ID, TargetFeatureAreaID: child.ID, Kind: RelationSplitInto, CreatedAt: input.At}
		result.Children = append(result.Children, child)
		result.Relations = append(result.Relations, relation)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return empty, err
	}
	for _, child := range result.Children {
		s.data[child.ID] = child
	}
	s.relations = append(s.relations, result.Relations...)
	s.data[parent.ID] = result.Parent
	s.receipts[key] = memoryLifecycleReceipt{Hash: input.PayloadSHA256, Result: data}
	return result, nil
}
