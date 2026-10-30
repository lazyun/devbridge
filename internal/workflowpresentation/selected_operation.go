package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/task"
	"time"
)

// SelectedOperation is a client-neutral operation summary. Task is its owner,
// never its identity. Selection and this read-only DTO confer no capability.
type OperationTarget struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operationId"`
	OwnerTaskID string `json:"ownerTaskId,omitempty"`
	RunID       string `json:"runId,omitempty"`
}

type SelectedOperation struct {
	Target      OperationTarget `json:"target"`
	Kind        string          `json:"kind"`
	OperationID string          `json:"operationId"`
	OwnerTaskID string          `json:"ownerTaskId,omitempty"`
	RunID       string          `json:"runId,omitempty"`
	Status      string          `json:"status"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	// ReadOnly restricts this operation; it never determines owner Task actions.
	// Reviews remain technically read-only even when their Task is approvable.
	ReadOnly           bool   `json:"readOnly,omitempty"`
	BackendDisposition string `json:"backendDisposition,omitempty"`
	RecoveryRequired   bool   `json:"recoveryRequired,omitempty"`
	FailureSummary     string `json:"failureSummary,omitempty"`
}

func SelectedOperationFromTaskView(v TaskView) *SelectedOperation {
	if v.ID == "" {
		return nil
	}
	if v.Phase == task.PhaseReviewing || v.Phase == task.PhaseReviewApprovalPending {
		r := v.CurrentReview
		if r == nil || r.ID == "" || r.TaskID != v.ID {
			return nil
		}
		out := &SelectedOperation{Kind: "REVIEW", OperationID: r.ID, OwnerTaskID: v.ID, Status: r.Status, UpdatedAt: r.UpdatedAt, ReadOnly: true, FailureSummary: r.FailureSummary}
		if r.Run != nil {
			out.RunID = r.Run.ID
		}
		if v.Recovery != nil {
			out.RecoveryRequired = v.Recovery.Required
		}
		out.Target = OperationTarget{Kind: out.Kind, OperationID: out.OperationID, OwnerTaskID: out.OwnerTaskID, RunID: out.RunID}
		return out
	}
	if v.Phase.ReviewStage() {
		return nil
	}
	e := v.CurrentExecution
	if e == nil || e.ID == "" {
		return nil
	}
	out := &SelectedOperation{Kind: "EXECUTION", OperationID: e.ID, OwnerTaskID: v.ID, Status: e.State, UpdatedAt: e.UpdatedAt, ReadOnly: v.ReadOnly}
	if e.Outcome != "" && e.Outcome != "PENDING" {
		out.Status = e.Outcome
	}
	if v.Phase == task.PhaseImplementing {
		out.RunID = e.ImplementationRunID
	} else {
		out.RunID = e.VerificationRunID
		if out.RunID == "" {
			out.RunID = e.ImplementationRunID
		}
	}
	if v.Recovery != nil {
		out.RecoveryRequired = v.Recovery.Required
	}
	out.Target = OperationTarget{Kind: out.Kind, OperationID: out.OperationID, OwnerTaskID: out.OwnerTaskID, RunID: out.RunID}
	return out
}

func SelectedOperationFromAgentExecution(v agentexecution.Projection) *SelectedOperation {
	out := &SelectedOperation{OperationID: v.SemanticOwnerID, OwnerTaskID: v.TaskID, RunID: v.RunID, Status: v.Status, UpdatedAt: v.UpdatedAt, ReadOnly: v.ReadOnly, BackendDisposition: v.BackendDisposition, RecoveryRequired: v.RecoveryRequired}
	switch v.Kind {
	case agentexecution.KindReview:
		out.Kind = "REVIEW"
		out.ReadOnly = true
	case agentexecution.KindDevelopment:
		out.Kind = "EXECUTION"
		out.OperationID = v.ExecutionProposalID
	case agentexecution.KindInspection:
		out.Kind = "INSPECTION"
		out.OwnerTaskID = ""
	default:
		return nil
	}
	if out.OperationID == "" {
		return nil
	}
	out.Target = OperationTarget{Kind: out.Kind, OperationID: out.OperationID, OwnerTaskID: out.OwnerTaskID, RunID: out.RunID}
	return out
}
