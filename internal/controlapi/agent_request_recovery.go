package controlapi

import (
	"context"
	"strings"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
)

// AgentRequestRecoveryInput selects disposition handling, never a replacement
// answer. Identity is copied from the original request's Core projection.
type AgentRequestRecoveryInput struct {
	agentruntime.Identity
	RuntimeRequestID string `json:"runtimeRequestId"`
	CommandRequestID string `json:"commandRequestId"`
	Action           string `json:"action"`
}
type AgentRequestRecoveryResult struct {
	Runtime                       agentruntime.Snapshot    `json:"runtime"`
	Cancellation                  *executioncancel.Receipt `json:"cancellation,omitempty"`
	SuccessorRequiresConfirmation bool                     `json:"successorRequiresConfirmation"`
}

func (f *Facade) RecoverAgentRequestForSession(ctx context.Context, sessionID string, in AgentRequestRecoveryInput) (AgentRequestRecoveryResult, error) {
	value, _, err := f.scopedRun(ctx, sessionID, in.RunID)
	if err != nil {
		return AgentRequestRecoveryResult{}, err
	}
	return f.recoverAgentRequest(ctx, in, value, func(input executioncancel.Input) (executioncancel.Receipt, error) {
		input.AISessionID = sessionID
		return f.CancelExecutionForSession(ctx, input)
	})
}
func (f *Facade) RecoverAgentRequestForOperator(ctx context.Context, in AgentRequestRecoveryInput) (AgentRequestRecoveryResult, error) {
	ctx, err := localoperator.BindProject(ctx, in.ProjectID)
	if err != nil {
		return AgentRequestRecoveryResult{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, in.ProjectID)
	if err != nil {
		return AgentRequestRecoveryResult{}, err
	}
	value, _, err := reader.scopedRun(ctx, "", in.RunID)
	if err != nil {
		return AgentRequestRecoveryResult{}, err
	}
	return f.recoverAgentRequest(ctx, in, value, func(input executioncancel.Input) (executioncancel.Receipt, error) {
		return f.CancelExecutionForOperator(ctx, in.ProjectID, in.TaskID, input)
	})
}
func (f *Facade) recoverAgentRequest(ctx context.Context, in AgentRequestRecoveryInput, value run.Run, cancel func(executioncancel.Input) (executioncancel.Receipt, error)) (AgentRequestRecoveryResult, error) {
	out := AgentRequestRecoveryResult{}
	if strings.TrimSpace(in.CommandRequestID) == "" || in.RuntimeRequestID == "" {
		return out, invalid("Exact request and command identity required")
	}
	if in.Action != "CONTINUE_WAITING" && in.Action != "CANCEL_OPERATION" && in.Action != "STOP_AND_REVISE" {
		return out, invalid("Unsupported request recovery action")
	}
	if value.AgentRuntime == nil {
		return out, MapError(agentruntime.ErrStale)
	}
	out.Runtime = *value.AgentRuntime
	var exact *agentruntime.Request
	for i := range out.Runtime.Requests {
		if out.Runtime.Requests[i].ID == in.RuntimeRequestID {
			exact = &out.Runtime.Requests[i]
			break
		}
	}
	if exact == nil || exact.Identity != in.Identity || value.ProjectID != in.ProjectID || value.OperationID != in.OperationID || string(value.OperationKind) != in.OperationKind {
		return out, MapError(agentruntime.ErrStale)
	}
	// Continue waiting is always a scoped read. It creates no dispatch, decision,
	// polling job or cancellation receipt.
	if in.Action == "CONTINUE_WAITING" {
		return out, nil
	}
	projection := agentruntime.ProjectRequest(*exact, out.Runtime.Observation, out.Runtime.TurnStatus)
	if exact.State != "CANCELED" {
		allowed := false
		for _, action := range projection.AvailableActions {
			if action == in.Action {
				allowed = true
				break
			}
		}
		if !allowed {
			return out, invalid("Recovery action is not available for this exact request; read canonical recovery disposition")
		}
	}
	// A canceled request remains readable for idempotent canonical cancellation
	// receipt replay. No cancellation ever rewrites the immutable user answer.
	if agentruntime.ResponseDisposition(*exact) != "UNKNOWN" && exact.State != "CANCELED" {
		return out, invalid("Recovery action requires an unknown response disposition")
	}
	receipt, err := cancel(executioncancel.Input{ProjectID: in.ProjectID, OwnerType: in.OperationKind, OwnerID: in.OperationID, RunID: in.RunID, RequestID: in.CommandRequestID, Reason: "Runtime request " + in.RuntimeRequestID + ": " + in.Action})
	out.Cancellation = &receipt
	out.SuccessorRequiresConfirmation = in.Action == "STOP_AND_REVISE"
	if updated, readErr := f.deps.Runs.Get(ctx, in.RunID); readErr == nil && updated.AgentRuntime != nil {
		out.Runtime = *updated.AgentRuntime
	}
	return out, err
}
