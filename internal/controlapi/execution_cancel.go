package controlapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/agentexecution"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
)

// CancelExecutionForSession binds the immutable AI session Project before the
// existing runtime supervisor performs exact Run/claim/epoch authorization.
func (f *Facade) CancelExecutionForSession(ctx context.Context, input executioncancel.Input) (executioncancel.Receipt, error) {
	projectID, err := f.sessionProject(ctx, input.AISessionID)
	if err != nil {
		return executioncancel.Receipt{}, err
	}
	if input.ProjectID != "" && input.ProjectID != projectID {
		return executioncancel.Receipt{}, MapError(executioncancel.ErrProjectMismatch)
	}
	input.ProjectID = projectID
	universalID := input.ExecutionID
	input, err = f.resolveUniversalCancellation(ctx, input)
	if err != nil {
		return executioncancel.Receipt{}, err
	}
	if err := input.Validate(); err != nil {
		return executioncancel.Receipt{}, MapError(err)
	}
	if recovery, ok := f.deps.RecoveryStatus.(interface {
		CancelRecovery(context.Context, executioncancel.Input) (executioncancel.Receipt, bool, error)
	}); ok {
		receipt, handled, recoveryErr := recovery.CancelRecovery(ctx, input)
		if handled {
			if universalID != "" {
				receipt.ExecutionID = universalID
			}
			if recoveryErr != nil {
				return receipt, MapError(recoveryErr)
			}
			return receipt, nil
		}
	}
	var cancellationRunner any = f.deps.Runner
	if input.OwnerType == "INSPECTION" {
		cancellationRunner = f.deps.ContextProbeRunner
	}
	runner, ok := cancellationRunner.(interface {
		CancelExecution(context.Context, executioncancel.Input) (executioncancel.Receipt, error)
	})
	if !ok {
		return executioncancel.Receipt{}, invalid("Execution cancellation unavailable")
	}
	receipt, err := runner.CancelExecution(ctx, input)
	if err != nil {
		return receipt, MapError(err)
	}
	// Cancellation has already committed. Its Task projection is a separate,
	// retryable transaction; a failure must never be reported as an undone
	// cancellation or cause another backend interrupt.
	if receipt.OwnerType == "EXECUTION" && receipt.RunID == "" && (receipt.State == "CANCELED" || receipt.State == "ALREADY_TERMINAL") && receipt.TaskID != "" && f.isCanceledAdmittedNoRun(ctx, input.AISessionID, projectID, receipt.OwnerID) {
		projector, ok := f.deps.ExecutionCancellations.(interface {
			ProjectCanceledAdmittedExecutionToSuccessorRequired(context.Context, string, string) (task.Phase, error)
		})
		if !ok {
			return receipt, MapError(executioncancel.ErrLifecycleProjectionPending)
		}
		if _, projectionErr := projector.ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, receipt.TaskID, receipt.OwnerID); projectionErr != nil {
			return receipt, MapError(fmt.Errorf("%w: %v", executioncancel.ErrLifecycleProjectionPending, projectionErr))
		}
	}
	if universalID == "" && input.RunID != "" && f.deps.AgentExecutions != nil {
		if r, readErr := f.deps.Runs.Get(ctx, input.RunID); readErr == nil && r.ProjectID == projectID && string(r.OperationKind) == input.OwnerType && r.OperationID == input.OwnerID {
			universalID = r.AgentExecutionID
		}
	}
	if universalID != "" {
		receipt.ExecutionID = universalID
	}
	return receipt, nil
}

func (f *Facade) isCanceledAdmittedNoRun(ctx context.Context, sessionID, projectID, ownerID string) bool {
	if f.deps.ExecutionProposals == nil {
		return false
	}
	value, err := f.deps.ExecutionProposals.Get(ctx, sessionID, ownerID)
	return err == nil && value.ProjectID == projectID && value.Status == executionproposal.StatusAdmitted && value.Outcome == executionproposal.OutcomeCanceled && value.RunID == ""
}

// ExecutionCancellationState adds receipt facts to an already authorized Agent
// operation projection. Its machine state and claim remain owned by the runtime.
// Trusted Console reads and session-scoped MCP reads use this same projection.
func (f *Facade) ExecutionCancellationState(ctx context.Context, projectID string, state agentconversation.State) (agentconversation.State, error) {
	if f.deps.ExecutionCancellations == nil || state.Claim == nil {
		return state, nil
	}
	receipts, err := f.deps.ExecutionCancellations.List(ctx, projectID, state.Claim.OperationID)
	if err != nil {
		return state, MapError(err)
	}
	for _, receipt := range receipts {
		if receipt.RunID != state.Claim.RunID || (receipt.OwnerType != "" && receipt.OwnerType != string(state.Claim.Kind)) {
			continue
		}
		if receipt.RecoveryRequired {
			state.Message = "Operation cancellation outcome is unknown. The exact claim remains held until permanent authority revocation and workspace/context quarantine finish."
			return state, nil
		}
		if receipt.ResolvedAt == nil {
			state.Message = "Operation cancellation requested. The exact claim remains held until known terminal finalization or permanently fenced UNKNOWN orphan convergence."
		}
	}
	return state, nil
}

// ListExecutionCancellationsForSession reads durable receipts, including from a
// fresh session in the same Project. The store scopes both Project and Execution.
func (f *Facade) ListExecutionCancellationsForSession(ctx context.Context, sessionID, executionID string) ([]executioncancel.Receipt, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := validID("executionId", executionID); err != nil {
		return nil, err
	}
	if f.deps.ExecutionCancellations == nil {
		return []executioncancel.Receipt{}, nil
	}
	universalID := ""
	if f.deps.AgentExecutions != nil {
		e, readErr := f.deps.AgentExecutions.Get(ctx, executionID)
		if readErr != nil && !errors.Is(readErr, agentexecution.ErrNotFound) {
			return nil, MapError(readErr)
		}
		if readErr == nil {
			if e.ProjectID != projectID {
				return nil, notFound()
			}
			universalID = e.ID
			if string(e.ProvenanceKind) == "LEGACY_UNKNOWN" {
				return []executioncancel.Receipt{}, nil
			}
			executionID = e.SemanticOwnerID
		}
	}
	receipt, err := f.deps.ExecutionCancellations.List(ctx, projectID, executionID)
	if err != nil {
		return nil, MapError(err)
	}
	if receipt == nil {
		receipt = []executioncancel.Receipt{}
	}
	if universalID != "" {
		for i := range receipt {
			receipt[i].ExecutionID = universalID
		}
	}
	return receipt, nil
}

// ExecutionCancellationPresentationForSession reuses the same exact eligibility
// and receipt projection as Cards. It grants no authority to the adapter.
func (f *Facade) ExecutionCancellationPresentationForSession(ctx context.Context, sessionID, executionID string) (presentation.Capability, *presentation.Cancellation, error) {
	cap := presentation.Capability{Action: "CANCEL_EXECUTION", Reason: "Exact cancellation eligibility unavailable."}
	e, err := f.deps.ExecutionProposals.Get(ctx, sessionID, executionID)
	if err != nil {
		return cap, nil, MapError(err)
	}
	t, err := f.deps.Tasks.Get(ctx, e.TaskID)
	if err != nil {
		return cap, nil, MapError(err)
	}
	if f.deps.AgentExecutions != nil && e.RunID == "" && e.ImplementationRunID == "" && e.VerificationRunID == "" && e.Outcome == "PENDING" && (e.Status == "PENDING_CONFIRMATION" || e.Status == "CONFIRMED") {
		p, err := f.GetAgentExecutionForSession(ctx, sessionID, executionID)
		if err != nil {
			return cap, nil, err
		}
		if !p.ReadOnly && p.TaskID == t.ID && p.SemanticOwnerID == e.ID && len(p.RunIDs) == 0 {
			cap.Available = true
			cap.Reason = "The exact authorization has not started a backend."
			cap.Target = &presentation.ActionTarget{FeatureAreaID: t.FeatureAreaID, ExecutionID: p.ExecutionID, OperationKind: p.SemanticOwnerKind, OperationID: p.SemanticOwnerID}
			return cap, nil, nil
		}
	}
	area, err := (historicalFeatureAreaRead{f.deps.FeatureAreas}).GetForSession(ctx, sessionID, t.FeatureAreaID)
	if err != nil {
		return cap, nil, MapError(err)
	}
	op, err := f.FeatureAreaAgentOperationState(ctx, sessionID, area.ID)
	if err != nil {
		return cap, nil, err
	}
	current := CurrentWorkflow{Topic: &area, Task: &t, Execution: &e}
	out := presentation.Projection{}
	if err = f.enrichFeatureAreaActionCapabilities(ctx, sessionID, current, op, &out); err != nil {
		return cap, nil, err
	}
	for _, v := range out.Capabilities {
		// This read is scoped to one Execution. A separately valid no-Run
		// Task cancellation remains on the Task/Card/workspace projection.
		if v.Action == "CANCEL_EXECUTION" && (v.Target == nil || v.Target.OperationKind != "TASK") {
			cap = v
		}
	}
	return cap, out.Cancellation, nil
}
