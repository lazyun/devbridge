package executionproposal

import (
	"context"

	"github.com/lazyun/devbridge/internal/observability"
)

type ApprovedExecution struct{ ExecutionID string }
type approvedExecutionStore interface {
	PendingApprovedExecutions(context.Context, string) ([]ApprovedExecution, error)
}

// ApprovedContinuation runs on the existing write-side reconciliation loop.
// Only the original explicit continuation effect may enqueue work. The worker
// never borrows a creation Session; it uses the same durable Run dispatcher.
func (s *Service) ApprovedContinuation() func(context.Context) {
	after := ""
	readObserved := false
	return func(ctx context.Context) {
		store, ok := s.store.(approvedExecutionStore)
		if !ok {
			return
		}
		pending, err := store.PendingApprovedExecutions(ctx, after)
		if err != nil {
			observability.Error(ctx, "execution", "execution.continuation.read_failed", "continuation queue read failed", map[string]any{"errorCode": "CONTINUATION_READ_FAILED"})
			return
		}
		if !readObserved {
			observability.Info(ctx, "execution", "execution.continuation.read_succeeded", "continuation queue read succeeded", map[string]any{"count": len(pending)})
			readObserved = true
		}
		reservations, ok := s.store.(reservedExecutionStore)
		if !ok {
			return
		}
		for _, v := range pending {
			if ctx.Err() != nil {
				return
			}
			proposal, reserved, reserveErr := reservations.ReserveExecution(ctx, v.ExecutionID, true, s.now().UTC())
			result := StartResult{Proposal: proposal, Run: reserved}
			if reserveErr == nil {
				proposal, reserved, reserveErr = s.prepareAdmittedExecution(ctx, proposal, reserved)
				result = StartResult{Proposal: proposal, Run: reserved}
			}
			if reserveErr == nil {
				result, reserveErr = s.startAuthorizedTurn(ctx, proposal, reserved)
			}
			if reserveErr != nil {
				code, stage := result.Handoff.Code, "RUNTIME_HANDOFF"
				if code == "" {
					code, stage = "CONTINUATION_RESERVATION_FAILED", "RESERVATION"
				}
				observability.Error(observability.WithContext(ctx, observability.Correlation{ExecutionID: v.ExecutionID, RunID: result.Run.ID, TaskID: proposal.TaskID, ProjectID: proposal.ProjectID}), "execution", "execution.continuation.handoff_failed", "continuation handoff was not accepted", map[string]any{"attempt": result.Run.Attempt, "stage": stage, "errorCode": code, "handoffState": result.Handoff.State})
			}
			after = v.ExecutionID
		}
		if len(pending) < 16 {
			after = ""
		}
	}
}
