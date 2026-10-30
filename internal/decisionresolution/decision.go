// Package decisionresolution defines the durable identity and request receipt
// contract shared by every Decision transport.
package decisionresolution

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
)

const (
	KindTaskConfirmation = "TASK_CONFIRMATION"
	KindTaskAcceptance   = "TASK_ACCEPTANCE"
	SubjectTaskPlan      = "TASK_PLAN"
	SubjectExecution     = "EXECUTION_PROPOSAL"
	ResultTaskPlan       = "TASK_PLAN"
	ResultExecution      = "EXECUTION_PROPOSAL"
	ResultTask           = "DEVELOPMENT_TASK"
)

var (
	ErrNotFound        = errors.New("decision resolution not found")
	ErrInvalidArgument = errors.New("invalid decision resolution argument")
	validIdentifier    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

type Request struct {
	// SourceReviewID distinguishes repeated gates on one Task; it is provenance, not the business subject.
	SourceReviewID string
	DecisionID     string
	Kind           string
	SubjectKind    string
	SubjectID      string
	Option         string
	RequestID      string
	// ActorKind and ActorID are written only for newly admitted sources.
	// Historical receipts remain unknown rather than being backfilled.
	ActorKind string
	ActorID   string
}

type Receipt struct {
	DecisionID  string
	ProjectID   string
	Kind        string
	SubjectKind string
	SubjectID   string
	Option      string
	RequestID   string
	ResultKind  string
	ResultID    string
	ResolvedAt  time.Time
	ActorKind   string
	ActorID     string
}

// Attempt captures how an atomic store handled one request. It is scoped to a
// single call and lets the application distinguish first commit, request
// replay, same-option convergence, and an opposite-option conflict.
type Attempt struct {
	Request         Request
	Receipt         Receipt
	Replay          bool
	AlreadyResolved bool
	Conflict        bool
	mu              sync.Mutex
}

type attemptKey struct{}

func WithAttempt(ctx context.Context, request Request) (context.Context, *Attempt) {
	attempt := &Attempt{Request: request}
	return context.WithValue(ctx, attemptKey{}, attempt), attempt
}

func FromContext(ctx context.Context) (*Attempt, bool) {
	attempt, ok := ctx.Value(attemptKey{}).(*Attempt)
	return attempt, ok && attempt != nil
}

func (a *Attempt) Record(receipt Receipt, replay, alreadyResolved, conflict bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Receipt, a.Replay, a.AlreadyResolved, a.Conflict = receipt, replay, alreadyResolved, conflict
}

func (a *Attempt) Snapshot() (Receipt, bool, bool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Receipt, a.Replay, a.AlreadyResolved, a.Conflict
}

func TaskDecisionID(taskPlanID string) string { return KindTaskConfirmation + ":" + taskPlanID }
func TaskAcceptanceDecisionID(executionID string) string {
	return KindTaskAcceptance + ":" + executionID
}

func ValidateRequest(value Request) error {
	for _, value := range []string{value.DecisionID, value.Kind, value.SubjectKind, value.SubjectID, value.Option, value.RequestID} {
		if !validIdentifier.MatchString(strings.TrimSpace(value)) || len(value) > 256 {
			return ErrInvalidArgument
		}
	}
	identity := value.Kind + ":" + value.SubjectID
	if value.Kind == "REVIEW_APPROVAL" {
		if value.SubjectKind != "TASK_REVIEW_GATE" || !validIdentifier.MatchString(value.SourceReviewID) {
			return ErrInvalidArgument
		}
		identity = value.Kind + ":" + value.SourceReviewID
	}
	if value.Kind == "INVALID_REVIEW_RESULT_RESOLUTION" {
		if value.SubjectKind != "INVALID_REVIEW_RESULT" || !validIdentifier.MatchString(value.SourceReviewID) {
			return ErrInvalidArgument
		}
		identity = value.Kind + ":" + value.SourceReviewID
	}
	if len(value.RequestID) > 128 || value.DecisionID != identity {
		return ErrInvalidArgument
	}
	if (value.ActorKind == "") != (value.ActorID == "") || len(value.ActorKind) > 64 || len(value.ActorID) > 128 {
		return ErrInvalidArgument
	}
	if value.ActorKind != "" && (!validIdentifier.MatchString(value.ActorKind) || !validIdentifier.MatchString(value.ActorID)) {
		return ErrInvalidArgument
	}
	return nil
}

type Store interface {
	GetByDecision(context.Context, string) (Receipt, error)
	GetByRequest(context.Context, string, string) (Receipt, error)
}

func (s *Service) ListTaskAcceptance(ctx context.Context, taskID string) ([]Receipt, error) {
	store, ok := s.store.(interface {
		ListTaskAcceptance(context.Context, string) ([]Receipt, error)
	})
	if !ok {
		return nil, errors.New("Task acceptance history is unavailable")
	}
	return store.ListTaskAcceptance(ctx, taskID)
}

type SessionLookup interface {
	Get(context.Context, string) (aisession.Session, error)
}

type Service struct {
	store    Store
	sessions SessionLookup
}

func NewService(store Store, sessions SessionLookup) (*Service, error) {
	if store == nil || sessions == nil {
		return nil, errors.New("decision resolution dependencies are incomplete")
	}
	return &Service{store: store, sessions: sessions}, nil
}

func (s *Service) Get(ctx context.Context, aiSessionID, decisionID string) (Receipt, error) {
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return Receipt{}, ErrNotFound
	}
	receipt, err := s.store.GetByDecision(ctx, decisionID)
	if err != nil {
		return Receipt{}, err
	}
	if receipt.ProjectID != session.ProjectID {
		return Receipt{}, ErrNotFound
	}
	return receipt, nil
}

func (s *Service) GetByRequest(ctx context.Context, aiSessionID, requestID string) (Receipt, error) {
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return Receipt{}, ErrNotFound
	}
	return s.store.GetByRequest(ctx, session.ProjectID, requestID)
}

// GetByProjectRequest is called only after the local loopback ingress has
// authenticated and bound projectID. It never turns a project ID into a
// Session or grants Session-scoped file authority.
func (s *Service) GetByProjectRequest(ctx context.Context, projectID, requestID string) (Receipt, error) {
	if projectID == "" || requestID == "" {
		return Receipt{}, ErrNotFound
	}
	return s.store.GetByRequest(ctx, projectID, requestID)
}
