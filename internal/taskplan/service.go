package taskplan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/task"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 100
	MaxOffset        = 1_000_000
	MaxIDBytes       = 128
)

var (
	ErrNotFound        = errors.New("task plan not found")
	ErrConflict        = errors.New("task plan conflict")
	ErrInvalidArgument = errors.New("invalid task plan argument")
	validIdentifier    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

type TransitionError struct{ From, To Status }

func (e *TransitionError) Error() string {
	return fmt.Sprintf("task plan cannot transition from %s to %s", e.From, e.To)
}

type Store interface {
	Create(context.Context, Plan) error
	Get(context.Context, string) (Plan, error)
	ListByProject(context.Context, string) ([]Plan, error)
	Confirm(context.Context, string, time.Time) (Plan, error)
	Reject(context.Context, string, time.Time) (Plan, error)
	Apply(context.Context, string, time.Time) (Plan, task.Task, error)
}

type SessionLookup interface {
	Get(context.Context, string) (aisession.Session, error)
}
type TaskLookup interface {
	Get(context.Context, string) (task.Task, error)
}
type RepositoryLookup interface {
	Get(context.Context, string) (repository.Repository, error)
}

type CreateInput struct {
	AISessionID      string
	TaskID           string
	Title            string
	WorkSpec         task.WorkSpec
	VerificationSpec task.VerificationSpec
}

type ListOptions struct{ Limit, Offset int }
type ApplyResult struct {
	TaskPlan Plan
	Task     task.Task
}

type Service struct {
	store        Store
	sessions     SessionLookup
	tasks        TaskLookup
	repositories RepositoryLookup
	newID        func() (string, error)
	now          func() time.Time
}

func NewService(store Store, sessions SessionLookup, tasks TaskLookup, repositories RepositoryLookup, newID func() (string, error), now func() time.Time) (*Service, error) {
	if store == nil || sessions == nil || tasks == nil || repositories == nil {
		return nil, errors.New("task plan dependencies are incomplete")
	}
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, sessions: sessions, tasks: tasks, repositories: repositories, newID: newID, now: now}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Plan, error) {
	if !validID(input.AISessionID) || !validID(input.TaskID) || len(input.Title) > 256 || task.ValidateWorkSpec(input.WorkSpec) != nil || task.ValidateVerificationSpec(input.VerificationSpec) != nil {
		return Plan{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, input.AISessionID)
	if err != nil {
		return Plan{}, mapNotFound(err)
	}
	taskValue, err := s.tasks.Get(ctx, input.TaskID)
	if err != nil || taskValue.ProjectID != session.ProjectID {
		return Plan{}, ErrNotFound
	}
	if !task.CanAuthorPlan(taskValue.Status) || taskValue.LegacyReadOnly {
		return Plan{}, ErrConflict
	}
	repositoryValue, err := s.repositories.Get(ctx, taskValue.RepositoryID)
	if err != nil || repositoryValue.ProjectID != session.ProjectID {
		return Plan{}, ErrNotFound
	}
	work := task.CloneWorkSpec(input.WorkSpec)
	// Preserve authored scope metadata even if repository files have moved.
	// Repository ownership and runtime containment are enforced independently.
	id, err := s.newID()
	if err != nil {
		return Plan{}, fmt.Errorf("allocate task plan id: %w", err)
	}
	now := s.now().UTC()
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = work.Goal
	}
	value := Plan{ID: id, AISessionID: session.ID, ProjectID: session.ProjectID, TaskID: taskValue.ID, Title: title, WorkSpec: work, VerificationSpec: task.CloneVerificationSpec(input.VerificationSpec), Status: StatusPendingConfirmation, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Create(ctx, value); err != nil {
		return Plan{}, err
	}
	logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: input.AISessionID, TaskID: value.TaskID})
	observability.Info(logCtx, "task", "task.confirmation.presented", "Task confirmation card created", map[string]any{"taskPlanId": value.ID, "status": value.Status, "workInstruction": observability.Fingerprint(value.WorkSpec.CodexInstruction)})
	return Clone(value), nil
}

func (s *Service) Get(ctx context.Context, aiSessionID, planID string) (Plan, error) {
	if !validID(aiSessionID) || !validID(planID) {
		return Plan{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return Plan{}, mapNotFound(err)
	}
	value, err := s.store.Get(ctx, planID)
	if err != nil {
		return Plan{}, mapNotFound(err)
	}
	// Task plans are Project-owned: any session bound to the Project can read
	// and act on them. AISessionID is immutable creation provenance only.
	if value.ProjectID != session.ProjectID {
		return Plan{}, ErrNotFound
	}
	return Clone(value), nil
}

func (s *Service) List(ctx context.Context, aiSessionID string, options ListOptions) ([]Plan, error) {
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	values, err := s.store.ListByProject(ctx, session.ProjectID)
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	if offset >= len(values) {
		return []Plan{}, nil
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	result := make([]Plan, 0, end-offset)
	for _, value := range values[offset:end] {
		result = append(result, Clone(value))
	}
	return result, nil
}

func (s *Service) ListByProject(ctx context.Context, projectID string) ([]Plan, error) {
	if !validID(projectID) {
		return nil, ErrInvalidArgument
	}
	values, err := s.store.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result := make([]Plan, 0, len(values))
	for _, value := range values {
		result = append(result, Clone(value))
	}
	return result, nil
}

func (s *Service) LatestForTask(ctx context.Context, aiSessionID, taskID string) (Plan, error) {
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return Plan{}, mapNotFound(err)
	}
	if store, ok := s.store.(interface {
		LatestByTask(context.Context, string) (Plan, error)
	}); ok {
		value, getErr := store.LatestByTask(ctx, taskID)
		if getErr != nil {
			return Plan{}, getErr
		}
		if value.ProjectID != session.ProjectID {
			return Plan{}, ErrNotFound
		}
		return Clone(value), nil
	}
	values, err := s.store.ListByProject(ctx, session.ProjectID)
	if err != nil {
		return Plan{}, err
	}
	var latest Plan
	for _, value := range values {
		if value.TaskID != taskID || (latest.ID != "" && (value.CreatedAt.Before(latest.CreatedAt) || value.CreatedAt.Equal(latest.CreatedAt) && value.ID >= latest.ID)) {
			continue
		}
		latest = value
	}
	if latest.ID == "" {
		return Plan{}, ErrNotFound
	}
	return Clone(latest), nil
}

func (s *Service) Confirm(ctx context.Context, aiSessionID, planID string) (Plan, error) {
	if _, err := s.Get(ctx, aiSessionID, planID); err != nil {
		return Plan{}, err
	}
	value, err := s.store.Confirm(ctx, planID, s.now().UTC())
	if err == nil {
		observability.Info(observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: aiSessionID, TaskID: value.TaskID}), "task", "task.confirmed", "Task plan confirmation persisted", map[string]any{"taskPlanId": value.ID, "status": value.Status})
	}
	return Clone(value), err
}

func (s *Service) Reject(ctx context.Context, aiSessionID, planID string) (Plan, error) {
	if _, err := s.Get(ctx, aiSessionID, planID); err != nil {
		return Plan{}, err
	}
	value, err := s.store.Reject(ctx, planID, s.now().UTC())
	if err == nil {
		observability.Info(observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: aiSessionID, TaskID: value.TaskID}), "task", "task.rejected", "Task plan rejected", map[string]any{"taskPlanId": value.ID, "status": value.Status})
	}
	return Clone(value), err
}

func (s *Service) Apply(ctx context.Context, aiSessionID, planID string) (ApplyResult, error) {
	value, err := s.Get(ctx, aiSessionID, planID)
	if err != nil {
		return ApplyResult{}, err
	}
	if value.Status != StatusConfirmed {
		return ApplyResult{TaskPlan: value}, &TransitionError{From: value.Status, To: StatusApplied}
	}
	planValue, taskValue, err := s.store.Apply(ctx, planID, s.now().UTC())
	return ApplyResult{TaskPlan: Clone(planValue), Task: taskValue}, err
}

func boundedList(options ListOptions) (int, int, error) {
	limit := options.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit || options.Offset < 0 || options.Offset > MaxOffset {
		return 0, 0, ErrInvalidArgument
	}
	return limit, options.Offset, nil
}

func validID(value string) bool {
	return value != "" && len(value) <= MaxIDBytes && validIdentifier.MatchString(value)
}

func mapNotFound(err error) error {
	if errors.Is(err, aisession.ErrNotFound) || errors.Is(err, task.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "task_plan_" + hex.EncodeToString(raw[:]), nil
}
