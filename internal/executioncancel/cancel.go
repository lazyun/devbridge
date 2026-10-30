// Package executioncancel defines the exact Execution cancellation contract.
// Runtime dispatch and finalization remain owned by the existing supervisor.
package executioncancel

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/run"
)

var (
	ErrInvalidArgument            = errors.New("INVALID_ARGUMENT")
	ErrExecutionNotRunning        = errors.New("EXECUTION_NOT_RUNNING")
	ErrRunNotActive               = errors.New("RUN_NOT_ACTIVE")
	ErrRunExecutionMismatch       = errors.New("RUN_EXECUTION_MISMATCH")
	ErrClaimMismatch              = errors.New("FEATURE_AREA_CLAIM_MISMATCH")
	ErrRecoveryRequired           = errors.New("RECOVERY_REQUIRED")
	ErrRequestConflict            = errors.New("REQUEST_CONFLICT")
	ErrProjectMismatch            = errors.New("PROJECT_MISMATCH")
	ErrOutcomeUnknown             = errors.New("EXECUTION_CANCEL_OUTCOME_UNKNOWN")
	ErrLifecycleProjectionPending = errors.New("CANCELLATION_LIFECYCLE_PROJECTION_PENDING")
)

type Input struct {
	OperatorPrincipalID  string                          `json:"-"`
	ExpectedUpperVersion *agentconversation.UpperVersion `json:"expectedUpperVersion,omitempty"`
	OwnerType            string                          `json:"ownerType,omitempty"`
	OwnerID              string                          `json:"ownerId,omitempty"`
	ProjectID            string                          `json:"-"`
	AISessionID          string                          `json:"aiSessionId"`
	ExecutionID          string                          `json:"executionId"`
	RunID                string                          `json:"runId"`
	RequestID            string                          `json:"requestId"`
	Reason               string                          `json:"reason,omitempty"`
}

func (in Input) Validate() error {
	in = in.Normalized()
	if in.OwnerType != "EXECUTION" && in.OwnerType != "REVIEW" && in.OwnerType != "TASK" && in.OwnerType != "INSPECTION" {
		return ErrInvalidArgument
	}
	if in.OwnerType == "EXECUTION" && in.ExecutionID != in.OwnerID {
		return ErrInvalidArgument
	}
	if in.OwnerType != "EXECUTION" && in.ExecutionID != "" {
		return ErrInvalidArgument
	}
	if len(in.RunID) > 128 {
		return ErrInvalidArgument
	}
	for _, id := range []string{in.ProjectID, in.OwnerID, in.RequestID} {
		if strings.TrimSpace(id) == "" || len(id) > 128 {
			return ErrInvalidArgument
		}
	}
	if (in.AISessionID == "") == (in.OperatorPrincipalID == "") || len(in.AISessionID) > 128 || len(in.OperatorPrincipalID) > 128 || (in.AISessionID != "" && strings.TrimSpace(in.AISessionID) == "") || (in.OperatorPrincipalID != "" && strings.TrimSpace(in.OperatorPrincipalID) == "") {
		return ErrInvalidArgument
	}
	if len(in.Reason) > 2048 {
		return ErrInvalidArgument
	}
	return nil
}

func (in Input) Normalized() Input {
	if in.OwnerType == "" {
		in.OwnerType = "EXECUTION"
	}
	if in.OwnerID == "" && in.OwnerType == "EXECUTION" {
		in.OwnerID = in.ExecutionID
	}
	if in.ExecutionID == "" && in.OwnerType == "EXECUTION" {
		in.ExecutionID = in.OwnerID
	}
	return in
}

// Receipt contains request provenance and references authoritative Run and
// Execution facts. It does not capture a second workflow snapshot.
type Receipt struct {
	RequestedByOperatorID  string                          `json:"requestedByOperatorId,omitempty"`
	ExpectedUpperVersion   *agentconversation.UpperVersion `json:"expectedUpperVersion,omitempty"`
	OwnerType              string                          `json:"ownerType,omitempty"`
	OwnerID                string                          `json:"ownerId,omitempty"`
	TaskID                 string                          `json:"taskId,omitempty"`
	BackendDisposition     string                          `json:"backendDisposition,omitempty"`
	RequestID              string                          `json:"requestId"`
	ProjectID              string                          `json:"projectId"`
	ExecutionID            string                          `json:"executionId"`
	RunID                  string                          `json:"runId"`
	RequestedByAISessionID string                          `json:"requestedByAiSessionId"`
	RequestedAt            time.Time                       `json:"requestedAt"`
	Reason                 string                          `json:"reason,omitempty"`
	State                  string                          `json:"state"`
	BackendInterruptState  string                          `json:"backendInterruptState"`
	QuiescenceState        string                          `json:"quiescenceState"`
	RunStatus              run.Status                      `json:"runStatus"`
	ExecutionStatus        string                          `json:"executionStatus"`
	RecoveryRequired       bool                            `json:"recoveryRequired"`
	ResolvedAt             *time.Time                      `json:"resolvedAt,omitempty"`
}

type Target struct{ RunID, RuntimeEpoch, ThreadID, TurnID string }

type Store interface {
	Begin(context.Context, Input, bool) (Receipt, bool, error)
	Get(context.Context, string, string) (Receipt, error)
	List(context.Context, string, string) ([]Receipt, error)
	ClaimInterrupt(context.Context, string, string) (Target, bool, error)
	RecordInterrupt(context.Context, string, string, string) error
	Requested(context.Context, string) (bool, error)
}
