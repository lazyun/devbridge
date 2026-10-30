package workflowprogression

import (
	"context"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/task"
)

// LocalApproveAndStart is the durable, local-operator parent command.  It is
// intentionally separate from confirmation.Intent: a browser operator does
// not acquire an AI session or a GPT confirmation identity.
type LocalApproveAndStart struct {
	ProjectID             string                            `json:"projectId"`
	TaskID                string                            `json:"taskId"`
	FeatureAreaID         string                            `json:"featureAreaId"`
	ReviewID              string                            `json:"reviewId"`
	DecisionID            string                            `json:"decisionId"`
	ActorID               string                            `json:"actorId"`
	RawRequestID          string                            `json:"rawRequestId"`
	RequestID             string                            `json:"requestId"`
	PayloadSHA256         string                            `json:"payloadSha256"`
	TargetJSON            string                            `json:"targetJson"`
	ApprovalRequestID     string                            `json:"approvalRequestId"`
	StartRequestID        string                            `json:"startRequestId"`
	ExecutionInstructions *task.ExecutionInstructions       `json:"executionInstructions,omitempty"`
	RuntimeConfig         codexruntime.NextRunRuntimeConfig `json:"runtimeConfig"`
	BackendSelection      *codexconfig.BackendSelection     `json:"backendSelection,omitempty"`
	RevisionID            string                            `json:"revisionId,omitempty"`
	ExecutionID           string                            `json:"executionId,omitempty"`
	RunID                 string                            `json:"runId,omitempty"`
	HandoffState          string                            `json:"handoffState,omitempty"`
	ErrorCode             string                            `json:"errorCode,omitempty"`
	State                 string                            `json:"state"`
	CreatedAt             time.Time                         `json:"createdAt"`
	UpdatedAt             time.Time                         `json:"updatedAt"`
}

// LocalApproveAndStartStore is implemented by the same canonical progression
// store that owns Review approval and exact Execution reservation.
type LocalApproveAndStartStore interface {
	ReadLocalApproveAndStart(context.Context, string, string, string) (LocalApproveAndStart, bool, error)
	BeginLocalApproveAndStart(context.Context, LocalApproveAndStart) (LocalApproveAndStart, bool, error)
	RecordLocalApproveAndStartApproval(context.Context, string, string, string, string) (LocalApproveAndStart, error)
	RecordLocalApproveAndStartApprovalFailure(context.Context, string, string, string, bool) (LocalApproveAndStart, error)
	RecordLocalApproveAndStartReservation(context.Context, string, string, string, string) (LocalApproveAndStart, error)
	// ReadLocalApproveAndStartRun resolves only the immutable reservation
	// receipt belonging to this parent. It is a read-only fallback for legacy
	// parents whose run_id was not durably copied into the parent row.
	ReadLocalApproveAndStartRun(context.Context, string, string, string) (string, bool, error)
	// runID and handoffAccepted are exact immutable Run/Supervisor receipt
	// evidence, rather than a derived interpretation of a Run's present status.
	RecordLocalApproveAndStartOutcome(context.Context, string, string, string, string, string, bool) (LocalApproveAndStart, error)
}
