package featurearea

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type LifecycleAction string

const (
	ActionActivate  LifecycleAction = "ACTIVATE"
	ActionDecompose LifecycleAction = "DECOMPOSE"
	ActionRetire    LifecycleAction = "RETIRE"
)

type ChildDefinition struct {
	Title            string `json:"title"`
	ProblemStatement string `json:"problemStatement"`
}
type DecomposeInput struct {
	AISessionID   string
	FeatureAreaID string
	RequestID     string
	Children      []ChildDefinition
}
type LifecycleResult struct {
	Parent    FeatureArea   `json:"parent"`
	Children  []FeatureArea `json:"children"`
	Relations []Relation    `json:"relations"`
}

// LifecycleMutation carries validated structured intent. Storage rechecks all
// ownership, lifecycle and claim facts under its write lock.
type LifecycleMutation struct {
	ProjectID, FeatureAreaID, RequestID, PayloadSHA256, AISessionID string
	Action                                                          LifecycleAction
	Children                                                        []FeatureArea
	At                                                              time.Time
}

func (s *Service) Activate(ctx context.Context, session, id, request string) (FeatureArea, error) {
	result, err := s.lifecycle(ctx, session, id, request, ActionActivate, nil)
	return result.Parent, err
}
func (s *Service) Retire(ctx context.Context, session, id, request string) (FeatureArea, error) {
	result, err := s.lifecycle(ctx, session, id, request, ActionRetire, nil)
	return result.Parent, err
}
func (s *Service) Decompose(ctx context.Context, input DecomposeInput) (LifecycleResult, error) {
	return s.lifecycle(ctx, input.AISessionID, input.FeatureAreaID, input.RequestID, ActionDecompose, input.Children)
}
func (s *Service) lifecycle(ctx context.Context, sessionID, id, requestID string, action LifecycleAction, children []ChildDefinition) (LifecycleResult, error) {
	if !validID(sessionID) || !validID(id) || !validID(requestID) || action == ActionDecompose && (len(children) == 0 || len(children) > 32) {
		return LifecycleResult{}, ErrInvalidArgument
	}
	for _, child := range children {
		if !validText(child.Title, MaxTitleBytes) || !validText(child.ProblemStatement, MaxProblemStatementBytes) {
			return LifecycleResult{}, ErrInvalidArgument
		}
	}
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return LifecycleResult{}, mapNotFound(err)
	}
	payload, err := json.Marshal(struct {
		ProjectID     string
		FeatureAreaID string
		Action        LifecycleAction
		Children      []ChildDefinition
	}{session.ProjectID, id, action, children})
	if err != nil {
		return LifecycleResult{}, err
	}
	sum := sha256.Sum256(payload)
	mutation := LifecycleMutation{AISessionID: sessionID, ProjectID: session.ProjectID, FeatureAreaID: id, RequestID: requestID, Action: action, PayloadSHA256: hex.EncodeToString(sum[:]), At: s.now().UTC()}
	for _, definition := range children {
		childID, err := s.newID()
		if err != nil {
			return LifecycleResult{}, err
		}
		mutation.Children = append(mutation.Children, FeatureArea{ID: childID, ProjectID: session.ProjectID, CreatedByAISessionID: sessionID, Title: definition.Title, ProblemStatement: definition.ProblemStatement, Status: StatusDiscovering, CreatedAt: mutation.At, UpdatedAt: mutation.At})
	}
	result, err := s.store.ApplyLifecycle(ctx, mutation)
	if err == nil && action == ActionRetire && s.onRetire != nil {
		s.onRetire()
	}
	return result, err
}

func LifecycleTarget(current FeatureArea, action LifecycleAction) (Status, error) {
	if current.LegacyReadOnly {
		return "", ErrUnavailable
	}
	switch {
	case action == ActionActivate && current.Status == StatusDiscovering:
		return StatusActive, nil
	case action == ActionDecompose && current.Status == StatusDiscovering:
		return StatusDecomposed, nil
	case action == ActionRetire && (current.Status == StatusActive || current.Status == StatusDiscovering):
		return StatusRetiring, nil
	default:
		return "", ErrConflict
	}
}

func CanRetire(value FeatureArea) bool {
	return !value.LegacyReadOnly && (value.Status == StatusDiscovering || value.Status == StatusActive)
}
