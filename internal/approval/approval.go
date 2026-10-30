package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/observability"
)

type Kind string

const (
	KindCommandExecution Kind = "COMMAND_EXECUTION"
	KindFileChange       Kind = "FILE_CHANGE"
	KindPermissions      Kind = "PERMISSIONS"
	KindLegacyCommand    Kind = "LEGACY_COMMAND"
	KindLegacyFileChange Kind = "LEGACY_FILE_CHANGE"
)

type Status string

const (
	StatusPending   Status = "PENDING"
	StatusDecided   Status = "DECIDED"
	StatusDelivered Status = "DELIVERED"
	StatusCanceled  Status = "CANCELED"
	StatusExpired   Status = "EXPIRED"
)

type DecisionType string

const (
	DecisionApproveOnce               DecisionType = "APPROVE_ONCE"
	DecisionApproveForSession         DecisionType = "APPROVE_FOR_SESSION"
	DecisionDecline                   DecisionType = "DECLINE"
	DecisionCancel                    DecisionType = "CANCEL"
	DecisionGrantRequestedPermissions DecisionType = "GRANT_REQUESTED_PERMISSIONS"
)

type Decision struct {
	Type        DecisionType    `json:"type"`
	Permissions json.RawMessage `json:"permissions,omitempty"`
}

type Approval struct {
	ID               string
	RunID            string
	ThreadID         string
	TurnID           string
	ItemID           string
	Kind             Kind
	Status           Status
	RequestPayload   json.RawMessage
	AllowedDecisions []DecisionType
	Decision         *Decision
	CreatedAt        time.Time
	DecidedAt        *time.Time
	DeliveredAt      *time.Time
	// WorkflowOwnedRun is derived from the immutable Run owner by the store.
	// CANCEL declines this action only; whole Execution cancellation is owned by
	// execution_cancel and its exact terminal/finalization protocol.
	WorkflowOwnedRun bool `json:"-"`
}

var (
	ErrNotFound                     = errors.New("approval not found")
	ErrInvalidDecision              = errors.New("approval decision is not legal")
	ErrAlreadyResolved              = errors.New("approval is already resolved")
	ErrSessionNotLive               = errors.New("approval Codex session is not live")
	ErrRunAlreadyManaged            = errors.New("run already has a live approval session")
	ErrWorkflowFinalizationRequired = errors.New("workflow Approval failure requires coordinator finalization")
)

type DeliveryError struct {
	Code string
	Err  error
}

func (e *DeliveryError) Error() string { return "approval decision delivery failed: " + e.Err.Error() }
func (e *DeliveryError) Unwrap() error { return e.Err }

type Store interface {
	CreatePending(context.Context, Approval, time.Time) (Approval, error)
	Get(context.Context, string) (Approval, error)
	ListByRun(context.Context, string) ([]Approval, error)
	Decide(context.Context, string, Decision, time.Time) (Approval, error)
	MarkDelivered(context.Context, string, time.Time) (Approval, error)
	// FailRun is a standalone compatibility writer. Implementations must reject
	// workflow-owned Runs, whose terminal authority belongs to the Coordinator.
	FailRun(context.Context, string, string, string, time.Time) error
}

type Resolver interface {
	ResolveApproval(context.Context, string, codex.ApprovalDecision) error
}

type liveSession struct {
	resolver        Resolver
	cancel          context.CancelFunc
	mu              sync.Mutex
	inFlight        int
	idle            chan struct{}
	closing         bool
	failure         *DeliveryError
	deliveryContext context.Context
	stopDelivery    context.CancelFunc
}

type Service struct {
	store Store
	now   func() time.Time

	mu       sync.RWMutex
	sessions map[string]*liveSession
}

func NewService(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now, sessions: make(map[string]*liveSession)}
}

func (s *Service) HasPendingForRun(ctx context.Context, runID string) (bool, error) {
	if store, ok := s.store.(interface {
		HasPendingByRun(context.Context, string) (bool, error)
	}); ok {
		return store.HasPendingByRun(ctx, runID)
	}
	values, err := s.store.ListByRun(ctx, runID)
	if err != nil {
		return false, err
	}
	for _, value := range values {
		if value.Status == StatusPending {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) Register(runID string, resolver Resolver, cancel context.CancelFunc) (func(), error) {
	if s == nil || s.store == nil || resolver == nil || runID == "" {
		return nil, errors.New("approval session registration is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[runID]; exists {
		return nil, ErrRunAlreadyManaged
	}
	idle := make(chan struct{})
	close(idle)
	deliveryContext, stopDelivery := context.WithCancel(context.Background())
	session := &liveSession{resolver: resolver, cancel: cancel, idle: idle, deliveryContext: deliveryContext, stopDelivery: stopDelivery}
	s.sessions[runID] = session
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			session.mu.Lock()
			session.closing = true
			session.mu.Unlock()
			session.stopDelivery()
			delete(s.sessions, runID)
			s.mu.Unlock()
		})
	}, nil
}

// Wait seals delivery admission and joins all admitted decisions, returning the
// first workflow delivery failure to the owning Coordinator. It writes no Run
// terminal state. The Coordinator must drain before terminal finalization.
func (s *Service) Wait(ctx context.Context, runID string) error {
	s.mu.RLock()
	session := s.sessions[runID]
	s.mu.RUnlock()
	if session == nil {
		return ErrSessionNotLive
	}
	session.mu.Lock()
	session.closing = true
	idle := session.idle
	session.mu.Unlock()
	select {
	case <-idle:
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.failure != nil {
			return session.failure
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Request(ctx context.Context, runID string, request codex.ApprovalRequest) (Approval, error) {
	if s == nil || s.store == nil {
		return Approval{}, errors.New("approval store is required")
	}
	value := Approval{
		ID:               request.ID,
		RunID:            runID,
		ThreadID:         request.ThreadID,
		TurnID:           request.TurnID,
		ItemID:           request.ItemID,
		Kind:             kindFromCodex(request.Kind),
		Status:           StatusPending,
		RequestPayload:   cloneRaw(request.RequestPayload),
		AllowedDecisions: decisionsFromCodex(request.AllowedDecisions),
	}
	created, err := s.store.CreatePending(ctx, value, s.now().UTC())
	if err == nil {
		logCtx := observability.WithContext(ctx, observability.Correlation{RunID: created.RunID, ApprovalID: created.ID})
		observability.Info(logCtx, "approval", "approval.created", "durable Approval created", map[string]any{"kind": created.Kind, "action": observability.Fingerprint(string(created.RequestPayload)), "allowedDecisionCount": len(created.AllowedDecisions)})
		observability.Info(logCtx, "run", "run.waiting_approval", "Run is waiting on a durable Approval", map[string]any{"newState": "WAITING_APPROVAL", "approvalKind": created.Kind})
	}
	return created, err
}

func (s *Service) Get(ctx context.Context, id string) (Approval, error) {
	if s == nil || s.store == nil {
		return Approval{}, errors.New("approval store is required")
	}
	return s.store.Get(ctx, id)
}

func (s *Service) ListByRun(ctx context.Context, runID string) ([]Approval, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("approval store is required")
	}
	return s.store.ListByRun(ctx, runID)
}

// Decide durably records the human decision, delivers it to the live Codex
// session, and only then records protocol delivery. No store transaction spans
// the external delivery call.
func (s *Service) Decide(ctx context.Context, id string, decision Decision) (Approval, error) {
	if s == nil || s.store == nil {
		return Approval{}, errors.New("approval store is required")
	}
	pending, err := s.store.Get(ctx, id)
	if err != nil {
		return Approval{}, err
	}
	decision, err = NormalizeDecision(pending, decision)
	if err != nil {
		return Approval{}, err
	}
	openBefore := s.openApprovalSnapshot(ctx, pending.RunID, id)
	s.mu.RLock()
	session := s.sessions[pending.RunID]
	if session != nil && !session.begin() {
		s.mu.RUnlock()
		return Approval{}, ErrSessionNotLive
	}
	s.mu.RUnlock()
	if session == nil {
		decided, decideErr := s.store.Decide(ctx, id, decision, s.now().UTC())
		if decideErr != nil {
			return Approval{}, decideErr
		}
		logCtx := observability.WithContext(ctx, observability.Correlation{RunID: decided.RunID, ApprovalID: decided.ID})
		observability.Info(logCtx, "approval", "approval.decided", "Approval decision persisted", map[string]any{"kind": decided.Kind, "decision": decision.Type})
		if !decided.WorkflowOwnedRun {
			if failErr := s.store.FailRun(context.WithoutCancel(ctx), decided.RunID, "approval_delivery_failed", ErrSessionNotLive.Error(), s.now().UTC()); failErr == nil {
				observability.Error(logCtx, "run", "run.infrastructure_failed", "Run failed after Approval delivery became impossible", map[string]any{"previousState": "WAITING_APPROVAL", "newState": "INFRASTRUCTURE_FAILED", "errorCode": "APPROVAL_SESSION_NOT_LIVE"})
				s.logSupersededApprovals(ctx, decided.RunID, openBefore, "run_failed")
			}
		}
		observability.Error(logCtx, "approval", "approval.delivery_failed", "Approval delivery failed", map[string]any{"kind": decided.Kind, "decision": decision.Type, "errorCode": "APPROVAL_SESSION_NOT_LIVE"})
		return decided, &DeliveryError{Code: "approval_delivery_failed", Err: ErrSessionNotLive}
	}
	defer session.end()
	decided, err := s.store.Decide(ctx, id, decision, s.now().UTC())
	if err != nil {
		return Approval{}, err
	}
	logCtx := observability.WithContext(ctx, observability.Correlation{RunID: decided.RunID, ApprovalID: decided.ID})
	observability.Info(logCtx, "approval", "approval.decided", "Approval decision persisted", map[string]any{"kind": decided.Kind, "decision": decision.Type, "decisionLatencyMillis": decisionDuration(decided)})
	cancelRun := decision.Type == DecisionCancel && !decided.WorkflowOwnedRun
	if cancelRun {
		s.logSupersededApprovals(ctx, decided.RunID, openBefore, "run_canceled")
	}
	protocolDecision := codex.ApprovalDecision{Type: codex.DecisionType(decision.Type), Permissions: cloneRaw(decision.Permissions)}
	if decision.Type == DecisionCancel && decided.WorkflowOwnedRun {
		// Backend CANCEL can abort a Turn. An Approval action has no durable
		// cancellation intent or exact interrupt authority, so reject only this
		// action while retaining the caller's durable human decision.
		protocolDecision.Type = codex.DecisionDecline
	}
	// A failed delivery stops sibling resolver waits on this session. Their
	// durable decisions still join the Coordinator's drain before finalization.
	deliveryContext, cancelDelivery := context.WithCancel(ctx)
	stop := context.AfterFunc(session.deliveryContext, cancelDelivery)
	defer stop()
	defer cancelDelivery()
	if err := session.resolver.ResolveApproval(deliveryContext, id, protocolDecision); err != nil {
		delivery := &DeliveryError{Code: "approval_delivery_failed", Err: err}
		if decided.WorkflowOwnedRun {
			session.recordFailure(delivery)
		} else {
			if failErr := s.store.FailRun(context.WithoutCancel(ctx), decided.RunID, "approval_delivery_failed", err.Error(), s.now().UTC()); failErr == nil {
				observability.Error(logCtx, "run", "run.infrastructure_failed", "Run failed after Approval protocol delivery failed", map[string]any{"previousState": "WAITING_APPROVAL", "newState": "INFRASTRUCTURE_FAILED", "errorCode": "APPROVAL_PROTOCOL_DELIVERY_FAILED"})
				s.logSupersededApprovals(ctx, decided.RunID, openBefore, "run_failed")
			}
		}
		if session.cancel != nil {
			session.cancel()
		}
		observability.Error(logCtx, "approval", "approval.delivery_failed", "Approval delivery failed", map[string]any{"kind": decided.Kind, "decision": decision.Type, "errorCode": "APPROVAL_PROTOCOL_DELIVERY_FAILED", "errorSummary": err.Error()})
		return decided, delivery
	}
	delivered, err := s.store.MarkDelivered(ctx, id, s.now().UTC())
	if err != nil {
		delivery := &DeliveryError{Code: "approval_delivery_persistence_failed", Err: fmt.Errorf("record approval delivery: %w", err)}
		if decided.WorkflowOwnedRun {
			session.recordFailure(delivery)
		} else {
			if failErr := s.store.FailRun(context.WithoutCancel(ctx), decided.RunID, "approval_delivery_persistence_failed", err.Error(), s.now().UTC()); failErr == nil {
				observability.Error(logCtx, "run", "run.infrastructure_failed", "Run failed after Approval delivery persistence failed", map[string]any{"previousState": "WAITING_APPROVAL", "newState": "INFRASTRUCTURE_FAILED", "errorCode": "APPROVAL_DELIVERY_PERSIST_FAILED"})
				s.logSupersededApprovals(ctx, decided.RunID, openBefore, "run_failed")
			}
		}
		if session.cancel != nil {
			session.cancel()
		}
		observability.Error(logCtx, "approval", "approval.delivery_failed", "Approval delivery persistence failed", map[string]any{"kind": decided.Kind, "decision": decision.Type, "errorCode": "APPROVAL_DELIVERY_PERSIST_FAILED", "errorSummary": err.Error()})
		return decided, delivery
	}
	observability.Info(logCtx, "approval", "approval.delivered", "Approval decision accepted by Codex runtime", map[string]any{"kind": delivered.Kind, "decision": decision.Type, "deliveryLatencyMillis": deliveryDuration(delivered)})
	if cancelRun {
		observability.Info(logCtx, "run", "run.canceled", "Run canceled by delivered Approval decision", map[string]any{"previousState": "WAITING_APPROVAL", "newState": "CANCELED", "errorCode": "HUMAN_APPROVAL_CANCELED", "approvalKind": delivered.Kind})
		if session.cancel != nil {
			session.cancel()
		}
	} else if !s.hasOpenApprovals(ctx, delivered.RunID) {
		observability.Info(logCtx, "run", "run.running", "Run resumed after all Approval decisions were delivered", map[string]any{"previousState": "WAITING_APPROVAL", "newState": "RUNNING"})
	}
	return delivered, nil
}

func (s *Service) openApprovalSnapshot(ctx context.Context, runID, exceptID string) map[string]Approval {
	values, err := s.store.ListByRun(context.WithoutCancel(ctx), runID)
	if err != nil {
		return nil
	}
	result := make(map[string]Approval)
	for _, value := range values {
		if value.ID != exceptID && (value.Status == StatusPending || value.Status == StatusDecided) {
			result[value.ID] = value
		}
	}
	return result
}

func (s *Service) logSupersededApprovals(ctx context.Context, runID string, before map[string]Approval, reason string) {
	if len(before) == 0 {
		return
	}
	values, err := s.store.ListByRun(context.WithoutCancel(ctx), runID)
	if err != nil {
		return
	}
	for _, value := range values {
		prior, tracked := before[value.ID]
		if !tracked || value.Status != StatusCanceled {
			continue
		}
		logCtx := observability.WithContext(ctx, observability.Correlation{RunID: runID, ApprovalID: value.ID})
		data := map[string]any{"kind": value.Kind, "previousState": prior.Status, "newState": value.Status, "reasonCode": reason}
		observability.Info(logCtx, "approval", "approval.superseded", "Approval superseded by a durable Run decision", data)
		observability.Info(logCtx, "approval", "approval.canceled", "Approval durably canceled", data)
	}
}

func (s *Service) hasOpenApprovals(ctx context.Context, runID string) bool {
	values, err := s.store.ListByRun(context.WithoutCancel(ctx), runID)
	if err != nil {
		return true
	}
	for _, value := range values {
		if value.Status == StatusPending || value.Status == StatusDecided {
			return true
		}
	}
	return false
}

func decisionDuration(value Approval) int64 {
	if value.DecidedAt == nil || value.DecidedAt.Before(value.CreatedAt) {
		return 0
	}
	return value.DecidedAt.Sub(value.CreatedAt).Milliseconds()
}

func deliveryDuration(value Approval) int64 {
	if value.DeliveredAt == nil {
		return 0
	}
	start := value.CreatedAt
	if value.DecidedAt != nil {
		start = *value.DecidedAt
	}
	if value.DeliveredAt.Before(start) {
		return 0
	}
	return value.DeliveredAt.Sub(start).Milliseconds()
}

func (s *liveSession) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	if s.inFlight == 0 {
		s.idle = make(chan struct{})
	}
	s.inFlight++
	return true
}

func (s *liveSession) recordFailure(failure *DeliveryError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		s.failure = failure
	}
	s.closing = true
	s.stopDelivery()
}

func (s *liveSession) end() {
	s.mu.Lock()
	s.inFlight--
	if s.inFlight == 0 {
		close(s.idle)
	}
	s.mu.Unlock()
}

func NormalizeDecision(value Approval, decision Decision) (Decision, error) {
	if value.Status != StatusPending {
		return Decision{}, ErrAlreadyResolved
	}
	allowed := false
	for _, candidate := range value.AllowedDecisions {
		if candidate == decision.Type {
			allowed = true
			break
		}
	}
	if !allowed {
		return Decision{}, fmt.Errorf("%w: %s is not allowed for %s", ErrInvalidDecision, decision.Type, value.Kind)
	}
	if decision.Type != DecisionGrantRequestedPermissions {
		if len(decision.Permissions) != 0 {
			return Decision{}, fmt.Errorf("%w: permissions are only valid for %s", ErrInvalidDecision, DecisionGrantRequestedPermissions)
		}
		return Decision{Type: decision.Type}, nil
	}
	requested, err := requestedPermissions(value.RequestPayload)
	if err != nil {
		return Decision{}, fmt.Errorf("%w: invalid requested permissions: %v", ErrInvalidDecision, err)
	}
	if len(decision.Permissions) != 0 && !jsonEqual(decision.Permissions, requested) {
		return Decision{}, fmt.Errorf("%w: granted permissions must exactly match the requested set", ErrInvalidDecision)
	}
	return Decision{Type: decision.Type, Permissions: requested}, nil
}

func requestedPermissions(payload json.RawMessage) (json.RawMessage, error) {
	var fields struct {
		Permissions json.RawMessage `json:"permissions"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	if len(fields.Permissions) == 0 || bytes.Equal(bytes.TrimSpace(fields.Permissions), []byte("null")) {
		return nil, errors.New("permissions are missing")
	}
	return cloneRaw(fields.Permissions), nil
}

func jsonEqual(left, right json.RawMessage) bool {
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	canonicalA, _ := json.Marshal(a)
	canonicalB, _ := json.Marshal(b)
	return bytes.Equal(canonicalA, canonicalB)
}

func kindFromCodex(value codex.ApprovalKind) Kind { return Kind(value) }

func decisionsFromCodex(values []codex.DecisionType) []DecisionType {
	result := make([]DecisionType, len(values))
	for i, value := range values {
		result[i] = DecisionType(value)
	}
	return result
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func Clone(value Approval) Approval {
	value.RequestPayload = cloneRaw(value.RequestPayload)
	value.AllowedDecisions = append([]DecisionType(nil), value.AllowedDecisions...)
	if value.Decision != nil {
		decision := *value.Decision
		decision.Permissions = cloneRaw(decision.Permissions)
		value.Decision = &decision
	}
	if value.DecidedAt != nil {
		when := *value.DecidedAt
		value.DecidedAt = &when
	}
	if value.DeliveredAt != nil {
		when := *value.DeliveredAt
		value.DeliveredAt = &when
	}
	return value
}
