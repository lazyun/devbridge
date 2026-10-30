package taskproposal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/task"
)

const (
	DefaultListLimit     = 50
	MaxListLimit         = 100
	MaxOffset            = 1_000_000
	MaxIDBytes           = 128
	MaxGoalBytes         = 16 << 10
	MaxConstraintCount   = 32
	MaxConstraintBytes   = 2 << 10
	MaxBaseRevisionBytes = 256
)

var (
	ErrRetired         = errors.New("legacy Task Proposal authoring is retired; use exact Review Approval")
	ErrNotFound        = errors.New("task proposal not found")
	ErrConflict        = errors.New("task proposal conflict")
	ErrInvalidArgument = errors.New("invalid task proposal argument")
)

type TransitionError struct {
	From Status
	To   Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("task proposal cannot transition from %s to %s", e.From, e.To)
}

type Store interface {
	Create(context.Context, Proposal) error
	Get(context.Context, string) (Proposal, error)
	ListBySession(context.Context, string) ([]Proposal, error)
	Confirm(context.Context, string, time.Time) (Proposal, error)
	Reject(context.Context, string, time.Time) (Proposal, error)
	ConsumeCreate(context.Context, string, task.Task, time.Time) (Proposal, error)
	ConsumeContinuation(context.Context, string, time.Time) (Proposal, error)
}

type SessionLookup interface {
	Get(context.Context, string) (aisession.Session, error)
}

type ModuleLookup interface {
	Get(context.Context, string) (module.Module, error)
}

type RepositoryLookup interface {
	Get(context.Context, string) (repository.Repository, error)
}

type CodexConfigLookup interface {
	Get(context.Context, string) (codexconfig.Config, error)
}

type TaskLookup interface {
	Get(context.Context, string) (task.Task, error)
}

type BaseResolver interface {
	ResolveBase(context.Context, string, string) (string, error)
}

type CreateInput struct {
	AISessionID      string
	Kind             Kind
	ModuleID         string
	RepositoryID     string
	TargetTaskID     string
	CodexConfigID    string
	Goal             string
	Constraints      []string
	WorkSpec         task.WorkSpec
	VerificationSpec task.VerificationSpec
	BaseRevision     string
}

type ListOptions struct {
	Limit  int
	Offset int
}

type ApplyResult struct {
	Proposal Proposal
	Task     task.Task
}

type Service struct {
	store    Store
	sessions SessionLookup
}

// NewService retains the legacy constructor for historical readers. The
// authoring dependencies are no longer used by this retired workflow.
func NewService(store Store, sessions SessionLookup, _ ModuleLookup, _ RepositoryLookup, _ CodexConfigLookup, _ TaskLookup, _ BaseResolver, _, _ func() (string, error), _ func() time.Time) (*Service, error) {
	if store == nil || sessions == nil {
		return nil, errors.New("task proposal history dependencies are incomplete")
	}
	return &Service{store: store, sessions: sessions}, nil
}

func (s *Service) Create(context.Context, CreateInput) (Proposal, error) {
	return Proposal{}, ErrRetired
}

func (s *Service) Get(ctx context.Context, aiSessionID, proposalID string) (Proposal, error) {
	if !validID(aiSessionID) || !validID(proposalID) {
		return Proposal{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return Proposal{}, mapNotFound(err)
	}
	value, err := s.store.Get(ctx, proposalID)
	if err != nil {
		return Proposal{}, mapNotFound(err)
	}
	if value.AISessionID != aiSessionID || value.ProjectID != session.ProjectID {
		return Proposal{}, ErrNotFound
	}
	return clone(value), nil
}

func (s *Service) List(ctx context.Context, aiSessionID string, options ListOptions) ([]Proposal, error) {
	if !validID(aiSessionID) {
		return nil, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	values, err := s.store.ListBySession(ctx, aiSessionID)
	if err != nil {
		return nil, err
	}
	filtered := make([]Proposal, 0, len(values))
	for _, value := range values {
		if value.AISessionID == aiSessionID && value.ProjectID == session.ProjectID {
			filtered = append(filtered, clone(value))
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})
	return page(filtered, offset, limit), nil
}

func (s *Service) Confirm(context.Context, string, string) (Proposal, error) {
	return Proposal{}, ErrRetired
}
func (s *Service) Reject(context.Context, string, string) (Proposal, error) {
	return Proposal{}, ErrRetired
}
func (s *Service) Apply(context.Context, string, string) (ApplyResult, error) {
	return ApplyResult{}, ErrRetired
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

func validID(value string) bool { return value != "" && len(value) <= MaxIDBytes }

func page[T any](values []T, offset, limit int) []T {
	if offset >= len(values) {
		return []T{}
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	result := make([]T, end-offset)
	copy(result, values[offset:end])
	return result
}

func mapNotFound(err error) error {
	if errors.Is(err, aisession.ErrNotFound) || errors.Is(err, module.ErrNotFound) || errors.Is(err, repository.ErrNotFound) || errors.Is(err, codexconfig.ErrNotFound) || errors.Is(err, task.ErrNotFound) || errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return err
}
