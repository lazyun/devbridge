// Package agentruntime observes Codex activity and routes exact runtime requests.
// It has no Task, Run, Execution, cancellation, or finalization authority.
package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	MaxPayload  = 64 << 10
	MaxOutput   = 16 << 10
	MaxItems    = 128
	MaxRequests = 128
)

var ErrStale = errors.New("AGENT_REQUEST_STALE_OR_NOT_OWNED")
var ErrInvalid = errors.New("AGENT_RESPONSE_INVALID")
var ErrUnsupported = errors.New("AGENT_REQUEST_UNSUPPORTED")

type Identity struct {
	OperationKind            string `json:"operationKind,omitempty"`
	OperationID              string `json:"operationId,omitempty"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId,omitempty"`
	ReviewID                 string `json:"reviewId,omitempty"`
	ExecutionID              string `json:"executionId,omitempty"`
	AgentConversationID      string `json:"agentConversationId,omitempty"`
	RecoveryStatusID         string `json:"recoveryStatusId,omitempty"`
	RuntimeEpoch             string `json:"runtimeEpoch,omitempty"`
	AppServerInstanceID      string `json:"appServerInstanceId,omitempty"`
	AppServerInstanceEpoch   int64  `json:"appServerInstanceEpoch,omitempty"`
	ProjectID                string `json:"projectId"`
	FeatureAreaID            string `json:"featureAreaId"`
	TaskID                   string `json:"taskId"`
	RunID                    string `json:"runId"`
	ThreadID                 string `json:"threadId"`
	TurnID                   string `json:"turnId"`
	ConnectionID             string `json:"connectionId"`
}
type PlanStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}
type Plan struct {
	Steps       []PlanStep `json:"steps"`
	Explanation string     `json:"explanation,omitempty"`
	Completed   int        `json:"completed"`
	Total       int        `json:"total"`
	Revision    uint64     `json:"revision"`
}
type Item struct {
	Text        string          `json:"text,omitempty"`
	Source      string          `json:"source"`
	Revision    uint64          `json:"revision"`
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	StartedAt   *time.Time      `json:"startedAt,omitempty"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
	Metadata    json.RawMessage `json:"metadata"`
	Unsupported bool            `json:"unsupported,omitempty"`
	Truncated   bool            `json:"truncated,omitempty"`
	OutputTail  string          `json:"outputTail,omitempty"`
	OutputBytes int64           `json:"outputBytes"`
}
type Diagnostic struct {
	Method  string          `json:"method"`
	At      time.Time       `json:"at"`
	Summary string          `json:"summary"`
	Details json.RawMessage `json:"details,omitempty"`
}
type Request struct {
	Projection       *RuntimeRequestProjection  `json:"projection,omitempty"`
	DispatchAttempts []ResponseDispatchAttempt  `json:"dispatchAttempts,omitempty"`
	RecoveryEvidence []ResponseRecoveryEvidence `json:"recoveryEvidence,omitempty"`
	Identity
	OriginalIdentity      *Identity         `json:"originalIdentity,omitempty"`
	PendingReplayVerified bool              `json:"pendingReplayVerified,omitempty"`
	ID                    string            `json:"runtimeRequestId"`
	Kind                  string            `json:"kind"`
	PayloadSHA256         string            `json:"requestPayloadSHA256"`
	ResponseSchema        json.RawMessage   `json:"responseSchema,omitempty"`
	ResponseSchemaSHA256  string            `json:"responseSchemaSHA256,omitempty"`
	UpdatedAt             time.Time         `json:"updatedAt"`
	UserDecision          *UserDecision     `json:"userDecision,omitempty"`
	AvailableActions      []string          `json:"availableActions,omitempty"`
	RequestID             json.RawMessage   `json:"requestId"`
	ItemID                string            `json:"itemId,omitempty"`
	Method                string            `json:"method"`
	WaitingState          string            `json:"waitingState"`
	Payload               json.RawMessage   `json:"payload"`
	AvailableResponses    []json.RawMessage `json:"availableResponses,omitempty"`
	State                 string            `json:"state"`
	CreatedAt             time.Time         `json:"createdAt"`
	ResolvedAt            *time.Time        `json:"resolvedAt,omitempty"`
	Resolution            json.RawMessage   `json:"resolution,omitempty"`
	Delivery              string            `json:"delivery,omitempty"`
}

// ThreadStatus is a native observation, not a workflow transition or approval.
type ThreadStatus struct {
	Type        string    `json:"type"`
	ActiveFlags []string  `json:"activeFlags,omitempty"`
	ObservedAt  time.Time `json:"observedAt"`
}

type Snapshot struct {
	NativeThreadStatus *ThreadStatus       `json:"nativeThreadStatus,omitempty"`
	Observation        *RuntimeObservation `json:"runtimeObservation,omitempty"`
	CaptureCoverage    Coverage            `json:"captureCoverage"`
	Agent              Agent               `json:"agent"`
	AgentHealth        string              `json:"agentHealth"`
	Identity
	Availability    string       `json:"availability"`
	Sequence        uint64       `json:"sequence"`
	UpdatedAt       time.Time    `json:"updatedAt"`
	LastActivityAt  time.Time    `json:"lastActivityAt"`
	LatestPlan      *Plan        `json:"latestPlan,omitempty"`
	CurrentItem     *Item        `json:"currentItem,omitempty"`
	Items           []Item       `json:"items,omitempty"`
	Requests        []Request    `json:"requests,omitempty"`
	WaitingState    string       `json:"waitingState"`
	TurnStatus      string       `json:"turnStatus,omitempty"`
	ConnectionState string       `json:"connectionState"`
	LastError       *Diagnostic  `json:"lastError,omitempty"`
	LastWarning     *Diagnostic  `json:"lastWarning,omitempty"`
	Diagnostics     []Diagnostic `json:"diagnostics,omitempty"`
	TurnDiff        string       `json:"turnDiff,omitempty"`
	DiffBytes       int          `json:"diffBytes"`
	DiffSource      string       `json:"diffSource,omitempty"`
}
type Resolution struct {
	Identity
	RuntimeRequestID string          `json:"runtimeRequestId,omitempty"`
	UserDecisionID   string          `json:"userDecisionId,omitempty"`
	ExpectedState    string          `json:"expectedState,omitempty"`
	PayloadSHA256    string          `json:"payloadSHA256,omitempty"`
	RequestID        json.RawMessage `json:"requestId"`
	ItemID           string          `json:"itemId"`
	Response         json.RawMessage `json:"response"`
}

// Save atomically fences exact runtime, Run, claim, Thread, Turn and sequence.
// Resolution writes additionally reject cancellation/RETIRE/finalization.
type Store interface {
	Open(context.Context, Identity) (Identity, error)
	Save(context.Context, uint64, Snapshot, bool) error
	Read(context.Context, string) (Snapshot, error)
}
type Event struct {
	barrier    chan struct{}
	ReceivedAt time.Time
	Method     string
	Params     json.RawMessage
	RequestID  json.RawMessage
}

func OpenRequest(r Request) bool {
	return r.State == "PENDING" || r.State == "INTENDED" || r.State == "SENT" || r.State == "WAITING_FOR_USER" || r.State == "USER_DECISION_RECORDED" || r.State == "RESPONDING" || r.State == "UNKNOWN"
}
func unavailable(runID string) Snapshot {
	return Snapshot{Identity: Identity{RunID: runID}, Availability: "UNAVAILABLE", WaitingState: "RUNNING"}
}

// Summary is the compact, client-neutral Card projection. It contains no raw
// request forms, output, or extra workflow decisions.
type Summary struct {
	CurrentItemType string    `json:"currentItemType,omitempty"`
	AgentHealth     string    `json:"agentHealth"`
	RunID           string    `json:"runId"`
	ThreadID        string    `json:"threadId"`
	TurnID          string    `json:"turnId"`
	Sequence        uint64    `json:"sequence"`
	UpdatedAt       time.Time `json:"updatedAt"`
	WaitingState    string    `json:"waitingState"`
	PlanCompleted   int       `json:"planCompleted"`
	PlanTotal       int       `json:"planTotal"`
	Step            string    `json:"step,omitempty"`
	LastActivityAt  time.Time `json:"lastActivityAt"`
}

func (s Snapshot) Summary() Summary {
	v := Summary{AgentHealth: s.AgentHealth, RunID: s.RunID, ThreadID: s.ThreadID, TurnID: s.TurnID, Sequence: s.Sequence, UpdatedAt: s.UpdatedAt, WaitingState: s.WaitingState, LastActivityAt: s.LastActivityAt}
	if s.CurrentItem != nil {
		v.CurrentItemType = s.CurrentItem.Type
	}
	if s.LatestPlan != nil {
		v.PlanCompleted = s.LatestPlan.Completed
		v.PlanTotal = s.LatestPlan.Total
		for _, step := range s.LatestPlan.Steps {
			if step.Status == "inProgress" {
				v.Step = step.Step
				break
			}
		}
	}
	return v
}
