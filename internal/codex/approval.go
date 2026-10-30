package codex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

const (
	approvalQueueDepth  = 32
	maxRetiredApprovals = 64
)

type ApprovalMode string

const (
	ApprovalModeDecline     ApprovalMode = "DECLINE"
	ApprovalModeInteractive ApprovalMode = "INTERACTIVE"
)

type ApprovalKind string

const (
	ApprovalKindCommandExecution ApprovalKind = "COMMAND_EXECUTION"
	ApprovalKindFileChange       ApprovalKind = "FILE_CHANGE"
	ApprovalKindPermissions      ApprovalKind = "PERMISSIONS"
	ApprovalKindLegacyCommand    ApprovalKind = "LEGACY_COMMAND"
	ApprovalKindLegacyFileChange ApprovalKind = "LEGACY_FILE_CHANGE"
)

type DecisionType string

const (
	DecisionApproveOnce               DecisionType = "APPROVE_ONCE"
	DecisionApproveForSession         DecisionType = "APPROVE_FOR_SESSION"
	DecisionDecline                   DecisionType = "DECLINE"
	DecisionCancel                    DecisionType = "CANCEL"
	DecisionGrantRequestedPermissions DecisionType = "GRANT_REQUESTED_PERMISSIONS"
)

type ApprovalRequest struct {
	ID               string
	ThreadID         string
	TurnID           string
	ItemID           string
	Kind             ApprovalKind
	RequestPayload   json.RawMessage
	AllowedDecisions []DecisionType
}

type ApprovalDecision struct {
	Type        DecisionType
	Permissions json.RawMessage
}

var (
	ErrApprovalModeDisabled    = errors.New("interactive approval mode is disabled")
	ErrApprovalNotFound        = errors.New("approval request is unknown")
	ErrApprovalAlreadySolved   = errors.New("approval request was already resolved")
	ErrApprovalInvalidDecision = errors.New("approval decision is not legal")
	ErrApprovalQueueFull       = errors.New("approval request queue is full")
	ErrAuthorizationCeiling    = errors.New("operation authorization ceiling exceeded")
)

// AuthorizationError is returned at the runtime protocol boundary when an
// approval request would exceed the operation's immutable authority ceiling.
// It is deliberately distinct from a user's decline.
type AuthorizationError struct {
	Ceiling AuthorizationCeiling
	Kind    ApprovalKind
}

func (e *AuthorizationError) Error() string {
	return fmt.Sprintf("AUTHORIZATION_CEILING_EXCEEDED: %s operation cannot authorize %s", e.Ceiling, e.Kind)
}

func (e *AuthorizationError) Unwrap() error { return ErrAuthorizationCeiling }

type approvalResolution struct {
	result    any
	delivered chan error
}

type interactiveApproval struct {
	request  ApprovalRequest
	decision chan approvalResolution
}

type approvalBroker struct {
	ceiling  AuthorizationCeiling
	mu       sync.Mutex
	pending  map[string]*interactiveApproval
	retired  map[string]uint64
	serial   uint64
	queue    chan ApprovalRequest
	closed   bool
	closeCh  chan struct{}
	closeOne sync.Once
}

type serverRequestResolution struct {
	beforeWrite func() error
	result      any
	delivered   chan error
}

func newApprovalBroker(ceilings ...AuthorizationCeiling) *approvalBroker {
	ceiling := AuthorizationWorkspaceWrite
	if len(ceilings) != 0 && ceilings[0] != "" {
		ceiling = ceilings[0]
	}
	return &approvalBroker{
		ceiling: ceiling,
		pending: make(map[string]*interactiveApproval),
		retired: make(map[string]uint64),
		queue:   make(chan ApprovalRequest, approvalQueueDepth),
		closeCh: make(chan struct{}),
	}
}

func (b *approvalBroker) close() {
	b.closeOne.Do(func() {
		b.mu.Lock()
		b.closed = true
		close(b.closeCh)
		b.mu.Unlock()
	})
}

func (b *approvalBroker) next(ctx context.Context) (ApprovalRequest, error) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return ApprovalRequest{}, ErrClientClosed
	}
	select {
	case request := <-b.queue:
		b.mu.Lock()
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return ApprovalRequest{}, ErrClientClosed
		}
		return request, nil
	case <-b.closeCh:
		return ApprovalRequest{}, ErrClientClosed
	case <-ctx.Done():
		return ApprovalRequest{}, ctx.Err()
	}
}

func (b *approvalBroker) process(ctx context.Context, request ServerRequest) (any, error) {
	value, err := decodeApprovalRequest(request)
	if err != nil {
		if errors.Is(err, ErrApprovalModeDisabled) {
			return builtInServerRequestPolicy(ctx, request)
		}
		return nil, err
	}
	if !b.ceiling.allowsApproval(value.Kind) {
		return nil, &AuthorizationError{Ceiling: b.ceiling, Kind: value.Kind}
	}
	if b.ceiling == AuthorizationReadOnly && (value.Kind == ApprovalKindCommandExecution || value.Kind == ApprovalKindLegacyCommand) {
		// Commands that fit the read-only sandbox execute without raising the
		// ceiling. An approval request means the backend is asking to escape
		// that sandbox, so the host may only decline or cancel it.
		value.AllowedDecisions = []DecisionType{DecisionDecline, DecisionCancel}
	}
	id, err := newApprovalID()
	if err != nil {
		return safeApprovalResponse(value.Kind), nil
	}
	value.ID = id
	entry := &interactiveApproval{request: value, decision: make(chan approvalResolution, 1)}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return safeApprovalResponse(value.Kind), nil
	}
	if len(b.pending) >= approvalQueueDepth {
		b.mu.Unlock()
		return safeApprovalResponse(value.Kind), nil
	}
	b.pending[id] = entry
	queued := false
	select {
	case b.queue <- value:
		queued = true
	default:
	}
	if !queued {
		delete(b.pending, id)
		b.retireLocked(id)
	}
	b.mu.Unlock()
	if !queued {
		return safeApprovalResponse(value.Kind), nil
	}
	select {
	case resolution := <-entry.decision:
		return serverRequestResolution{result: resolution.result, delivered: resolution.delivered}, nil
	case <-ctx.Done():
		b.removePending(id)
		return safeApprovalResponse(value.Kind), nil
	case <-b.closeCh:
		b.removePending(id)
		return safeApprovalResponse(value.Kind), nil
	}
}

func (b *approvalBroker) resolve(ctx context.Context, id string, decision ApprovalDecision) error {
	b.mu.Lock()
	entry := b.pending[id]
	if entry == nil {
		_, duplicate := b.retired[id]
		b.mu.Unlock()
		if duplicate {
			return ErrApprovalAlreadySolved
		}
		return ErrApprovalNotFound
	}
	if !b.ceiling.allowsApproval(entry.request.Kind) {
		b.mu.Unlock()
		return &AuthorizationError{Ceiling: b.ceiling, Kind: entry.request.Kind}
	}
	if b.ceiling == AuthorizationReadOnly && (decision.Type == DecisionApproveOnce || decision.Type == DecisionApproveForSession || decision.Type == DecisionGrantRequestedPermissions) {
		kind := entry.request.Kind
		b.mu.Unlock()
		return &AuthorizationError{Ceiling: b.ceiling, Kind: kind}
	}
	result, err := approvalResponse(entry.request, decision)
	if err != nil {
		b.mu.Unlock()
		return err
	}
	delete(b.pending, id)
	b.retireLocked(id)
	b.mu.Unlock()
	delivered := make(chan error, 1)
	entry.decision <- approvalResolution{result: result, delivered: delivered}
	select {
	case err := <-delivered:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-b.closeCh:
		return ErrClientClosed
	}
}

func (c AuthorizationCeiling) allowsApproval(kind ApprovalKind) bool {
	if c != AuthorizationReadOnly {
		return true
	}
	return kind == ApprovalKindCommandExecution || kind == ApprovalKindLegacyCommand
}

func (b *approvalBroker) removePending(id string) {
	b.mu.Lock()
	if _, exists := b.pending[id]; exists {
		delete(b.pending, id)
		b.retireLocked(id)
	}
	b.mu.Unlock()
}

func (b *approvalBroker) retireLocked(id string) {
	b.serial++
	b.retired[id] = b.serial
	for len(b.retired) > maxRetiredApprovals {
		var oldestID string
		var oldest uint64
		for candidate, serial := range b.retired {
			if oldestID == "" || serial < oldest {
				oldestID, oldest = candidate, serial
			}
		}
		delete(b.retired, oldestID)
	}
}

func decodeApprovalRequest(request ServerRequest) (ApprovalRequest, error) {
	value := ApprovalRequest{RequestPayload: append(json.RawMessage(nil), request.Params...)}
	switch request.Method {
	case "item/commandExecution/requestApproval":
		value.Kind = ApprovalKindCommandExecution
		value.AllowedDecisions = []DecisionType{DecisionApproveOnce, DecisionApproveForSession, DecisionDecline, DecisionCancel}
	case "item/fileChange/requestApproval":
		value.Kind = ApprovalKindFileChange
		value.AllowedDecisions = []DecisionType{DecisionApproveOnce, DecisionApproveForSession, DecisionDecline, DecisionCancel}
	case "item/permissions/requestApproval":
		value.Kind = ApprovalKindPermissions
		value.AllowedDecisions = []DecisionType{DecisionGrantRequestedPermissions, DecisionDecline, DecisionCancel}
	case "execCommandApproval":
		value.Kind = ApprovalKindLegacyCommand
		value.AllowedDecisions = []DecisionType{DecisionApproveOnce, DecisionApproveForSession, DecisionDecline, DecisionCancel}
	case "applyPatchApproval":
		value.Kind = ApprovalKindLegacyFileChange
		value.AllowedDecisions = []DecisionType{DecisionApproveOnce, DecisionApproveForSession, DecisionDecline, DecisionCancel}
	default:
		return ApprovalRequest{}, ErrApprovalModeDisabled
	}
	var correlation struct {
		ThreadID       string `json:"threadId"`
		TurnID         string `json:"turnId"`
		ItemID         string `json:"itemId"`
		ConversationID string `json:"conversationId"`
		CallID         string `json:"callId"`
	}
	if err := json.Unmarshal(request.Params, &correlation); err != nil {
		return ApprovalRequest{}, &ProtocolError{Message: "malformed approval request: " + err.Error()}
	}
	value.ThreadID = correlation.ThreadID
	if value.ThreadID == "" {
		value.ThreadID = correlation.ConversationID
	}
	value.TurnID = correlation.TurnID
	value.ItemID = correlation.ItemID
	if value.ItemID == "" {
		value.ItemID = correlation.CallID
	}
	if value.ThreadID == "" || value.ItemID == "" {
		return ApprovalRequest{}, &ProtocolError{Message: "approval request omitted required correlation identifiers"}
	}
	if value.Kind != ApprovalKindLegacyCommand && value.Kind != ApprovalKindLegacyFileChange && value.TurnID == "" {
		return ApprovalRequest{}, &ProtocolError{Message: "approval request omitted turn id"}
	}
	return value, nil
}

func approvalResponse(request ApprovalRequest, decision ApprovalDecision) (any, error) {
	if !decisionAllowed(request.AllowedDecisions, decision.Type) {
		return nil, fmt.Errorf("%w: %s is not allowed for %s", ErrApprovalInvalidDecision, decision.Type, request.Kind)
	}
	if decision.Type != DecisionGrantRequestedPermissions && len(decision.Permissions) != 0 {
		return nil, errors.New("permissions may only accompany GRANT_REQUESTED_PERMISSIONS")
	}
	switch request.Kind {
	case ApprovalKindCommandExecution, ApprovalKindFileChange:
		values := map[DecisionType]string{DecisionApproveOnce: "accept", DecisionApproveForSession: "acceptForSession", DecisionDecline: "decline", DecisionCancel: "cancel"}
		return map[string]any{"decision": values[decision.Type]}, nil
	case ApprovalKindPermissions:
		if decision.Type == DecisionGrantRequestedPermissions {
			requested, err := permissionPayload(request.RequestPayload)
			if err != nil {
				return nil, err
			}
			if len(decision.Permissions) != 0 && !rawJSONEqual(decision.Permissions, requested) {
				return nil, errors.New("granted permissions must exactly match requested permissions")
			}
			return map[string]any{"permissions": requested}, nil
		}
		return map[string]any{"permissions": map[string]any{}}, nil
	case ApprovalKindLegacyCommand, ApprovalKindLegacyFileChange:
		var value any
		switch decision.Type {
		case DecisionApproveOnce:
			value = "approved"
		case DecisionApproveForSession:
			value = "approved_for_session"
		case DecisionDecline:
			value = map[string]any{"denied": map[string]string{"rejection": "Declined by user"}}
		case DecisionCancel:
			value = "abort"
		}
		return map[string]any{"decision": value}, nil
	default:
		return nil, errors.New("unknown approval kind")
	}
}

func safeApprovalResponse(kind ApprovalKind) any {
	request := ApprovalRequest{Kind: kind, AllowedDecisions: []DecisionType{DecisionDecline}}
	result, _ := approvalResponse(request, ApprovalDecision{Type: DecisionDecline})
	return result
}

func permissionPayload(payload json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Permissions json.RawMessage `json:"permissions"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, &ProtocolError{Message: "malformed permission approval: " + err.Error()}
	}
	if len(request.Permissions) == 0 {
		return nil, &ProtocolError{Message: "permission approval omitted permissions"}
	}
	return append(json.RawMessage(nil), request.Permissions...), nil
}

func rawJSONEqual(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func decisionAllowed(allowed []DecisionType, value DecisionType) bool {
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

func newApprovalID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "approval_" + hex.EncodeToString(raw[:]), nil
}
