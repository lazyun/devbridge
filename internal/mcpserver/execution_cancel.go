package mcpserver

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

const ExecutionCancelDescription = "Cancel an exact universal REVIEW, INSPECTION or DEVELOPMENT Execution through its canonical semantic owner cancellation protocol. Supply the observed runId for running work; omit it only for a pending or confirmed Execution with no Run. Reuse the same requestId and payload after response loss. ACK is not terminal. UNKNOWN_ORPHANED permits canceled Task/owner and released claim after permanent authority revocation and workspace/context quarantine, without asserting physical backend death. No Decision Card is required."
const ExecutionCancelInputSchema = `{"type":"object","properties":{"expectedUpperVersion":` + UpperVersionSchema + `,"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"executionId":{"type":"string","minLength":1,"maxLength":128},"runId":{"type":"string","maxLength":128},"requestId":{"type":"string","minLength":1,"maxLength":128},"reason":{"type":"string","maxLength":2048}},"required":["aiSessionId","executionId","requestId"],"additionalProperties":false}`
const ExecutionCancelReceiptSchema = `{"type":"object","properties":{"expectedUpperVersion":` + UpperVersionSchema + `,"ownerType":{"type":"string"},"ownerId":{"type":"string"},"taskId":{"type":"string"},"backendDisposition":{"type":"string"},"requestId":{"type":"string"},"projectId":{"type":"string"},"executionId":{"type":"string"},"runId":{"type":"string"},"requestedByAiSessionId":{"type":"string"},"requestedByOperatorId":{"type":"string","minLength":1,"maxLength":128},"requestedAt":{"type":"string"},"reason":{"type":"string"},"state":{"type":"string"},"backendInterruptState":{"type":"string"},"quiescenceState":{"type":"string"},"runStatus":{"type":"string"},"executionStatus":{"type":"string"},"recoveryRequired":{"type":"boolean"},"resolvedAt":{"type":"string"}},"required":["requestId","projectId","executionId","runId","requestedByAiSessionId","requestedAt","state","backendInterruptState","quiescenceState","runStatus","executionStatus","recoveryRequired"],"additionalProperties":false}`
const ExecutionCancellationProjectionSchema = `{"type":"object","properties":{"available":{"type":"boolean"},"activeRunId":{"type":"string"},"state":{"type":"string","enum":["UNAVAILABLE","AVAILABLE","CANCEL_REQUESTED","CANCELED","OUTCOME_UNKNOWN","ALREADY_TERMINAL"]},"recoveryRequired":{"type":"boolean"},"availableActions":{"type":"array","items":{"type":"string","enum":["operation_cancel"]}},"receipts":{"type":"array","items":` + ExecutionCancelReceiptSchema + `}},"required":["available","state","recoveryRequired","availableActions","receipts"],"additionalProperties":false}`

type executionCancellationService interface {
	CancelExecutionForSession(context.Context, executioncancel.Input) (executioncancel.Receipt, error)
	ListExecutionCancellationsForSession(context.Context, string, string) ([]executioncancel.Receipt, error)
}

var errExecutionCancellationUnavailable = errors.New("Execution cancellation unavailable")

func (s *StaticService) CancelExecutionForSession(ctx context.Context, input executioncancel.Input) (executioncancel.Receipt, error) {
	if s.control == nil {
		return executioncancel.Receipt{}, errExecutionCancellationUnavailable
	}
	return s.control.CancelExecutionForSession(ctx, input)
}
func (s *StaticService) ListExecutionCancellationsForSession(ctx context.Context, sessionID, executionID string) ([]executioncancel.Receipt, error) {
	if s.control == nil {
		return []executioncancel.Receipt{}, nil
	}
	return s.control.ListExecutionCancellationsForSession(ctx, sessionID, executionID)
}

// ExecutionCancelCore is shared by both Upper AI adapters. The durable runtime
// state machine stays behind the session-scoped application operation.
func ExecutionCancelCore(ctx context.Context, service BridgeService, input executioncancel.Input) (executioncancel.Receipt, error) {
	provider, ok := service.(executionCancellationService)
	if !ok {
		return executioncancel.Receipt{}, publicToolError(errExecutionCancellationUnavailable)
	}
	receipt, err := provider.CancelExecutionForSession(ctx, input)
	if err != nil {
		return receipt, publicToolError(controlapi.MapError(err))
	}
	return receipt, nil
}

type executionCancellationProjection struct {
	Available        bool                      `json:"available"`
	ActiveRunID      string                    `json:"activeRunId,omitempty"`
	State            string                    `json:"state"`
	RecoveryRequired bool                      `json:"recoveryRequired"`
	AvailableActions []string                  `json:"availableActions"`
	Receipts         []executioncancel.Receipt `json:"receipts"`
}

func unavailableExecutionCancellation() executionCancellationProjection {
	return executionCancellationProjection{State: "UNAVAILABLE", AvailableActions: []string{}, Receipts: []executioncancel.Receipt{}}
}

func projectExecutionCancellation(ctx context.Context, service BridgeService, sessionID string, execution *agentexecution.Projection, runs []run.Run) (executionCancellationProjection, error) {
	out := unavailableExecutionCancellation()
	if execution == nil || execution.ReadOnly {
		return out, nil
	}
	if execution.RecoveryRequired {
		out.RecoveryRequired = true
		out.State = "OUTCOME_UNKNOWN"
	}
	if status := execution.Status; status == "COMPLETED" || status == "FAILED" {
		out.State = "ALREADY_TERMINAL"
	} else if status == "CANCELED" {
		out.State = "CANCELED"
	}
	provider, ok := service.(executionCancellationService)
	if !ok {
		return out, nil
	}
	receipts, err := provider.ListExecutionCancellationsForSession(ctx, sessionID, execution.ExecutionID)
	if err != nil {
		return out, err
	}
	out.Receipts = append(out.Receipts, receipts...)
	pending := false
	for _, receipt := range receipts {
		if receipt.State == "OUTCOME_UNKNOWN" {
			out.State = "OUTCOME_UNKNOWN"
		}
		if receipt.ResolvedAt != nil && out.State == "UNAVAILABLE" && (receipt.State == "ALREADY_TERMINAL" || receipt.State == "CANCEL_NOT_APPLIED") {
			out.State = "ALREADY_TERMINAL"
		}
		if receipt.RecoveryRequired {
			out.RecoveryRequired = true
			out.State = "OUTCOME_UNKNOWN"
		}
		if receipt.ResolvedAt == nil {
			pending = true
			if !out.RecoveryRequired {
				out.State = "CANCEL_REQUESTED"
			}
		}
	}
	if !pending && !out.RecoveryRequired && execution.Kind == agentexecution.KindDevelopment && execution.Status == "PENDING" && execution.RunID == "" && len(execution.RunIDs) == 0 {
		out.Available = true
		out.State = "AVAILABLE"
		out.AvailableActions = []string{"operation_cancel"}
		return out, nil
	}
	if pending || out.RecoveryRequired || execution.Status == "PENDING" || execution.RunID == "" || execution.Status == "COMPLETED" || execution.Status == "FAILED" || execution.Status == "CANCELED" || execution.BackendDisposition == "UNKNOWN_ORPHANED" {
		return out, nil
	}
	if execution.Kind == agentexecution.KindInspection {
		for _, r := range runs {
			if r.ID == execution.RunID && r.Status == run.StatusRunning && r.OperationKind == run.OperationInspection && r.OperationID == execution.SemanticOwnerID {
				out.Available = true
				out.ActiveRunID = r.ID
				out.State = "AVAILABLE"
				out.AvailableActions = []string{"operation_cancel"}
				return out, nil
			}
		}
		return out, nil
	}
	recovery, ok := service.(agentOperationRecoveryService)
	if !ok {
		return out, nil
	}
	activeID := execution.RunID
	for _, candidate := range runs {
		if candidate.ID != activeID || candidate.AgentExecutionID != execution.ExecutionID || string(candidate.OperationKind) != execution.SemanticOwnerKind || candidate.OperationID != execution.SemanticOwnerID || candidate.ProjectID != execution.ProjectID || candidate.FeatureAreaID == "" || candidate.RuntimeEpoch == "" || candidate.ThreadID == "" || candidate.TurnID == "" {
			continue
		}
		if candidate.Status != run.StatusRunning && candidate.Status != run.StatusWaitingApproval {
			continue
		}
		state, err := recovery.FeatureAreaAgentOperationState(ctx, sessionID, candidate.FeatureAreaID)
		if err != nil {
			return out, err
		}
		if state.Status == "RECOVERY_REQUIRED" {
			out.State = "OUTCOME_UNKNOWN"
			out.RecoveryRequired = true
			return out, nil
		}
		claim := state.Claim
		if state.Status != "BUSY" || claim == nil || claim.FeatureAreaID != candidate.FeatureAreaID || string(claim.Kind) != execution.SemanticOwnerKind || claim.OperationID != execution.SemanticOwnerID || claim.RunID != candidate.ID || claim.RuntimeEpoch != candidate.RuntimeEpoch {
			continue
		}
		out.Available = true
		out.ActiveRunID = candidate.ID
		out.State = "AVAILABLE"
		out.AvailableActions = []string{"operation_cancel"}
		return out, nil
	}
	return out, nil
}

func (s projectOpenProjectionService) FeatureAreaAgentOperationState(ctx context.Context, sessionID, featureAreaID string) (agentconversation.State, error) {
	if provider, ok := s.BridgeService.(agentOperationRecoveryService); ok {
		return provider.FeatureAreaAgentOperationState(ctx, sessionID, featureAreaID)
	}
	return agentconversation.State{}, errors.New("agent operation state unavailable")
}
func (s projectOpenProjectionService) RecoverFeatureAreaAgentOperation(ctx context.Context, sessionID string, input agentconversation.RecoverInput) (agentconversation.RecoveryResult, error) {
	if provider, ok := s.BridgeService.(agentOperationRecoveryService); ok {
		return provider.RecoverFeatureAreaAgentOperation(ctx, sessionID, input)
	}
	return agentconversation.RecoveryResult{}, errors.New("agent operation recovery unavailable")
}

func (s projectOpenProjectionService) CancelExecutionForSession(ctx context.Context, input executioncancel.Input) (executioncancel.Receipt, error) {
	if provider, ok := s.BridgeService.(executionCancellationService); ok {
		return provider.CancelExecutionForSession(ctx, input)
	}
	return executioncancel.Receipt{}, errExecutionCancellationUnavailable
}
func (s projectOpenProjectionService) ListExecutionCancellationsForSession(ctx context.Context, sessionID, executionID string) ([]executioncancel.Receipt, error) {
	if provider, ok := s.BridgeService.(executionCancellationService); ok {
		return provider.ListExecutionCancellationsForSession(ctx, sessionID, executionID)
	}
	return []executioncancel.Receipt{}, nil
}
