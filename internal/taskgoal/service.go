package taskgoal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/task"
)

type Status string

const (
	StatusPending   Status = "PENDING"
	StatusRunning   Status = "RUNNING"
	StatusCompleted Status = "COMPLETED"
	StatusFailed    Status = "FAILED"
	StatusSkipped   Status = "SKIPPED"
)

// ValidStatus reports whether value is one of the closed goal progress
// statuses.
func ValidStatus(value Status) bool {
	switch value {
	case StatusRunning, StatusCompleted, StatusFailed, StatusSkipped:
		return true
	default:
		return false
	}
}

// Progress is the per-Execution projection of one Task Goal.
type Progress struct {
	ExecutionID string
	GoalID      string
	ProjectID   string
	Position    int
	Title       string
	Status      Status
	UpdatedAt   time.Time
}

// Store persists the per-Execution goal progress projection.
type Store interface {
	InitializeGoalProgress(ctx context.Context, executionProposalID, taskID, projectID string, now time.Time) error
	GoalProgress(ctx context.Context, executionProposalID string) ([]Progress, error)
	UpdateGoalProgress(ctx context.Context, executionProposalID, goalID string, status Status, now time.Time) error
}

// TaskGoalLookup loads Task Goal definitions for progress initialization.
type TaskGoalLookup interface {
	Goals(ctx context.Context, taskID string) ([]task.Goal, error)
}

// ExecutionLookup resolves an Execution (execution proposal) to its Task and
// project for progress initialization.
type ExecutionLookup interface {
	Get(ctx context.Context, id string) (Execution, error)
}

var ErrNotFound = errors.New("execution goal progress not found")

// ErrInvalidTransition signals a goal progress status transition the closed
// lifecycle does not allow.
type ErrInvalidTransition struct {
	From, To Status
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid goal progress transition %s -> %s", e.From, e.To)
}

// Execution is the minimal Execution view the goal service needs.
type Execution struct {
	RunID, VerificationRunID, Outcome string
	TaskDefinitionRevisionID          string
	PrimaryMemberID                   string
	ApprovedWorksetHash               string
	RuntimeAdapterIdentity            string
	RuntimeProtocolIdentity           string
	ID                                string
	TaskID                            string
	ProjectID                         string
	Status                            string
	RuntimeConfig                     codexruntime.NextRunRuntimeConfig
	BackendSelection                  *codexconfig.BackendSelection
}

// ProposalLookup loads an execution proposal by ID without session scoping;
// the adapter is used inside trusted runtime paths only.
type ProposalLookup interface {
	GetByID(ctx context.Context, id string) (Execution, error)
}

// StoreWithProposals composes the progress store with the proposal lookup the
// service needs to initialize progress.
type StoreWithProposals interface {
	Store
	ProposalLookup
}

// ExecutionAdapter adapts a proposal store to ExecutionLookup.
type ExecutionAdapter struct {
	Proposals ProposalLookup
}

func (a *ExecutionAdapter) Get(ctx context.Context, id string) (Execution, error) {
	if a == nil || a.Proposals == nil {
		return Execution{}, errors.New("execution proposal lookup is not configured")
	}
	return a.Proposals.GetByID(ctx, id)
}

// Service owns observable Goal progress for one Execution.
type Service struct {
	store      Store
	executions ExecutionLookup
	tasks      TaskGoalLookup
	now        func() time.Time
}

func NewService(store Store, executions ExecutionLookup, tasks TaskGoalLookup, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, executions: executions, tasks: tasks, now: now}
}

func (s *Service) ready() error {
	if s.store == nil || s.executions == nil || s.tasks == nil {
		return errors.New("task goal service requires a store, execution lookup and task goal lookup")
	}
	return nil
}

// Initialize creates PENDING progress rows for every Goal of the Execution's
// Task. It is idempotent and returns false when the Execution already has
// progress rows.
func (s *Service) Initialize(ctx context.Context, executionID string) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	execution, err := s.executions.Get(ctx, executionID)
	if err != nil {
		return false, err
	}
	goals, err := s.tasks.Goals(ctx, execution.TaskID)
	if err != nil {
		return false, err
	}
	if len(goals) == 0 {
		return false, nil
	}
	existing, err := s.store.GoalProgress(ctx, executionID)
	if err != nil {
		return false, err
	}
	if len(existing) > 0 {
		return false, nil
	}
	if err := s.store.InitializeGoalProgress(ctx, executionID, execution.TaskID, execution.ProjectID, s.now().UTC()); err != nil {
		return false, err
	}
	return true, nil
}

// Report applies one goal status observation for the Execution.
func (s *Service) Report(ctx context.Context, executionID, goalID string, status Status) (Progress, error) {
	if err := s.ready(); err != nil {
		return Progress{}, err
	}
	if !ValidStatus(status) {
		return Progress{}, fmt.Errorf("goal status must be one of RUNNING, COMPLETED, FAILED, SKIPPED")
	}
	current, err := s.progress(ctx, executionID, goalID)
	if err != nil {
		return Progress{}, err
	}
	if current.Status == status {
		// Idempotent observations still require the store's current operation
		// authorization. Preserve the timestamp while checking it atomically.
		if err := s.store.UpdateGoalProgress(ctx, executionID, goalID, status, current.UpdatedAt); err != nil {
			return Progress{}, err
		}
		return current, nil
	}
	if !validTransition(current.Status, status) {
		return Progress{}, &ErrInvalidTransition{From: current.Status, To: status}
	}
	if err := s.store.UpdateGoalProgress(ctx, executionID, goalID, status, s.now().UTC()); err != nil {
		return Progress{}, err
	}
	current.Status = status
	current.UpdatedAt = s.now().UTC()
	return current, nil
}

// ListForExecution returns the Execution's goal progress ordered by goal
// position. An Execution without goals returns an empty slice.
func (s *Service) ListForExecution(ctx context.Context, executionID string) ([]Progress, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.store.GoalProgress(ctx, executionID)
}

func (s *Service) progress(ctx context.Context, executionID, goalID string) (Progress, error) {
	values, err := s.store.GoalProgress(ctx, executionID)
	if err != nil {
		return Progress{}, err
	}
	for _, value := range values {
		if value.GoalID == goalID {
			return value, nil
		}
	}
	return Progress{}, ErrNotFound
}

func validTransition(from, to Status) bool {
	switch from {
	case StatusPending:
		return to == StatusRunning || to == StatusCompleted || to == StatusFailed || to == StatusSkipped
	case StatusRunning:
		return to == StatusCompleted || to == StatusFailed || to == StatusSkipped
	case StatusCompleted, StatusFailed, StatusSkipped:
		return false
	default:
		return false
	}
}
