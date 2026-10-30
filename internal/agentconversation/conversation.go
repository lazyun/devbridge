// Package agentconversation owns the linear Coding Agent context of a FeatureArea.
// Backend interaction IDs are internal provenance, never caller-selected context.
package agentconversation

import (
	"context"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
)

const HostToolContractVersion = 1

type RotationReason string

const (
	ExplicitReset            RotationReason = "EXPLICIT_RESET"
	BackendThreadUnavailable RotationReason = "BACKEND_THREAD_UNAVAILABLE"
	HostToolContractUpgrade  RotationReason = "HOST_TOOL_CONTRACT_UPGRADE"
	UnsafeContextBoundary    RotationReason = "UNSAFE_CONTEXT_BOUNDARY"
)

type Advance string

const (
	NoAdvance    Advance = "NO_ADVANCE"
	KnownAdvance Advance = "KNOWN_ADVANCE"
	Unknown      Advance = "UNKNOWN"
)

type OperationKind string

const (
	Review     OperationKind = "REVIEW"
	Execution  OperationKind = "EXECUTION"
	Inspection OperationKind = "INSPECTION"
)

// OwnerKind distinguishes the established FeatureArea stream from the
// project-scoped Inspection stream.  It is durable provenance, never input
// selected by an API caller.
type OwnerKind string

const (
	OwnerFeatureArea       OwnerKind = "FEATURE_AREA"
	OwnerProjectInspection OwnerKind = "PROJECT_INSPECTION"
)

var (
	ErrNotFound        = errors.New("agent conversation not found")
	ErrBusy            = errors.New("FEATURE_AREA_BUSY")
	ErrConflict        = errors.New("agent conversation ownership conflict")
	ErrUnsafe          = errors.New("AGENT_CONTEXT_UNSAFE")
	ErrUnavailable     = errors.New("Feature Area agent context unavailable")
	ErrInvalidArgument = errors.New("invalid agent conversation argument")
)

type Conversation struct {
	ID, ProjectID, FeatureAreaID, OwnerID                                                   string
	OwnerKind                                                                               OwnerKind
	Backend, BackendThreadID                                                                string
	InitialModel, InitialModelSourceRunID, InitialModelSourceThreadID, InitialModelEvidence string
	BackendSelection                                                                        *codexconfig.BackendSelection
	BackendSelectionSourceRunID, BackendSelectionSourceThreadID, BackendSelectionEvidence   string
	HostToolContractVersion                                                                 int
	// CurrentBoundaryTurnID is a completed, safely reusable canonical context
	// point. It is never an optimistic turn/start identity or partial head.
	CurrentBoundaryTurnID string
	ContinuationSafe      bool
	PredecessorID         string
	RotationReason        RotationReason
	CreatedAt             time.Time
	EndedAt               *time.Time
}

type BackendContinuityState string

const (
	BackendContinuityThreadless          BackendContinuityState = "THREADLESS"
	BackendContinuityBoundAvailable      BackendContinuityState = "BOUND_AVAILABLE"
	BackendContinuityBoundUnavailable    BackendContinuityState = "BOUND_UNAVAILABLE"
	BackendContinuityHistoricalUnknown   BackendContinuityState = "HISTORICAL_UNKNOWN"
	BackendContinuityReadUnavailable     BackendContinuityState = "READ_UNAVAILABLE"
	BackendContinuityAssociationConflict BackendContinuityState = "CONFLICT"
)

// BackendContinuity is pure-read evidence about the canonical FeatureArea
// Thread. It never grants a claim, Run, dispatch, or configuration mutation.
type BackendContinuity struct {
	State               BackendContinuityState        `json:"state"`
	ConversationID      string                        `json:"conversationId,omitempty"`
	ThreadID            string                        `json:"threadId,omitempty"`
	SourceRunID         string                        `json:"sourceRunId,omitempty"`
	SourceOperationKind string                        `json:"sourceOperationKind,omitempty"`
	SourceOperationID   string                        `json:"sourceOperationId,omitempty"`
	Evidence            string                        `json:"evidence,omitempty"`
	BoundBackend        *codexconfig.BackendSelection `json:"boundBackend,omitempty"`
	Reason              string                        `json:"reason,omitempty"`
}

// ModelDefault is a pure-read default source. It is neither execution
// authority nor a restriction on an explicit currently valid model choice.
type ModelDefault struct {
	ConversationID string `json:"conversationId,omitempty"`
	ThreadID       string `json:"threadId,omitempty"`
	State          string `json:"state"` // THREADLESS, KNOWN, HISTORICAL_UNKNOWN, READ_UNAVAILABLE
	InitialModel   string `json:"initialModel,omitempty"`
	SourceRunID    string `json:"sourceRunId,omitempty"`
	Evidence       string `json:"evidence,omitempty"`
}

type Claim struct {
	FeatureAreaID      string
	Kind               OperationKind
	OperationID, RunID string
	RuntimeEpoch       string `json:"-"`
	CreatedAt          time.Time
}

// Boundary is immutable entry provenance plus a once-set exit observation.
// Unknown has no exit, and leaves the conversation's safe boundary unchanged.
type Boundary struct {
	RunID, AgentConversationID              string
	EntryBoundaryTurnID, ExitBoundaryTurnID string
	Advance                                 Advance
	Quiesced                                bool
	ThreadStartPending                      bool
}

// Finalization records observed backend facts before the Run terminal write.
// Its exact owner is resolved and frozen by the durable runtime store.
type Finalization struct {
	// Canceled is an exact observed interrupted Execution Turn, not an ACK.
	Canceled           bool
	Advance            Advance
	ExitBoundaryTurnID string
	Quiesced           bool
}

type State struct {
	Status              string
	FeatureAreaID       string
	Claim               *Claim
	AgentConversationID string
	Message             string
}

type RecoverInput struct {
	OperatorPrincipalID                                  string        `json:"-"`
	ProjectID                                            string        `json:"-"`
	ExpectedUpperVersion                                 *UpperVersion `json:"expectedUpperVersion,omitempty"`
	FeatureAreaID, RunID, AgentConversationID, RequestID string
	OperationKind                                        OperationKind
	OperationID                                          string
}

type RecoveryResult struct {
	OperatorPrincipalID                  string        `json:"operatorPrincipalId,omitempty"`
	ExpectedUpperVersion                 *UpperVersion `json:"expectedUpperVersion,omitempty"`
	AuthorizedUpperVersion               *UpperVersion `json:"authorizedUpperVersion,omitempty"`
	GrantState                           string        `json:"grantState,omitempty"`
	BackendDisposition                   string
	FeatureAreaID, RunID, OperationID    string
	OperationKind                        OperationKind
	OldConversationID, NewConversationID string
	RequestID                            string
	Boundary                             Advance
	ContextAbandoned, FreshWorkAllowed   bool
}

// Runtime is the shared seam used by every FeatureArea Run. Reservation of
// the durable claim happens atomically with Run allocation in the Run store.
type Runtime interface {
	BeginRun(context.Context, string) (Conversation, error)
	BindThread(context.Context, string, string) error
	BindThreadWithRuntimeProvenance(context.Context, string, string, string, string, codexconfig.BackendSelection) error
	MarkThreadStartPending(context.Context, string) error
	MarkTurnPending(context.Context, string) error
	FinishRun(context.Context, string, Advance, string, bool) error
	AuthorizeRun(context.Context, string) (Claim, error)
}
