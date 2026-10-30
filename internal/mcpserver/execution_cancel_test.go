package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

type cancelProjectionService struct {
	BridgeService
	state    agentconversation.State
	receipts []executioncancel.Receipt
}

func (s cancelProjectionService) CancelExecutionForSession(context.Context, executioncancel.Input) (executioncancel.Receipt, error) {
	return executioncancel.Receipt{}, nil
}
func (s cancelProjectionService) ListExecutionCancellationsForSession(context.Context, string, string) ([]executioncancel.Receipt, error) {
	return s.receipts, nil
}
func (s cancelProjectionService) FeatureAreaAgentOperationState(context.Context, string, string) (agentconversation.State, error) {
	return s.state, nil
}
func (s cancelProjectionService) RecoverFeatureAreaAgentOperation(context.Context, string, agentconversation.RecoverInput) (agentconversation.RecoveryResult, error) {
	return agentconversation.RecoveryResult{}, nil
}

func TestExecutionCancellationProjectionRequiresExactActiveClaim(t *testing.T) {
	for _, purpose := range []run.Purpose{run.PurposeImplementation, run.PurposeVerification} {
		t.Run(string(purpose), func(t *testing.T) {
			execution := agentexecution.Projection{ExecutionID: "execution-a", Kind: agentexecution.KindDevelopment, ProjectID: "project-a", SemanticOwnerKind: "EXECUTION", SemanticOwnerID: "execution-a", Status: "EXECUTING", RunID: "run-a"}
			current := run.Run{ID: "run-a", ProjectID: "project-a", FeatureAreaID: "area-a", OperationKind: run.OperationExecution, OperationID: "execution-a", ExecutionProposalID: "execution-a", AgentExecutionID: "execution-a", RuntimeEpoch: "epoch-a", ThreadID: "thread-a", TurnID: "turn-a", Purpose: purpose, Status: run.StatusRunning}
			service := cancelProjectionService{state: agentconversation.State{Status: "BUSY", Claim: &agentconversation.Claim{FeatureAreaID: "area-a", Kind: agentconversation.Execution, OperationID: "execution-a", RunID: "run-a", RuntimeEpoch: "epoch-a"}}}
			got, err := projectExecutionCancellation(context.Background(), service, "session-a", &execution, []run.Run{current})
			if err != nil || !got.Available || got.ActiveRunID != "run-a" || len(got.AvailableActions) != 1 || got.AvailableActions[0] != "operation_cancel" {
				t.Fatalf("projection=%+v err=%v", got, err)
			}
			for _, fixture := range []struct {
				name   string
				change func(*run.Run, *agentconversation.Claim, *agentexecution.Projection)
			}{
				{"stale run", func(r *run.Run, _ *agentconversation.Claim, _ *agentexecution.Projection) { r.ID = "stale" }},
				{"other execution", func(r *run.Run, _ *agentconversation.Claim, _ *agentexecution.Projection) {
					r.AgentExecutionID = "other"
				}},
				{"other project", func(r *run.Run, _ *agentconversation.Claim, _ *agentexecution.Projection) { r.ProjectID = "other" }},
				{"review", func(r *run.Run, _ *agentconversation.Claim, _ *agentexecution.Projection) {
					r.OperationKind = run.OperationReview
					r.Purpose = run.PurposeScopeReview
				}},
				{"foreign claim", func(_ *run.Run, c *agentconversation.Claim, _ *agentexecution.Projection) {
					c.OperationID = "successor"
				}},
				{"old epoch", func(_ *run.Run, c *agentconversation.Claim, _ *agentexecution.Projection) { c.RuntimeEpoch = "old" }},
				{"no turn", func(r *run.Run, _ *agentconversation.Claim, _ *agentexecution.Projection) { r.TurnID = "" }},
				{"terminal run", func(r *run.Run, _ *agentconversation.Claim, _ *agentexecution.Projection) {
					r.Status = run.StatusSucceeded
				}},
				{"pending execution", func(_ *run.Run, _ *agentconversation.Claim, e *agentexecution.Projection) {
					e.Status = "PENDING"
				}},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					r, c, e := current, *service.state.Claim, execution
					fixture.change(&r, &c, &e)
					s := service
					s.state.Claim = &c
					got, err := projectExecutionCancellation(context.Background(), s, "session-a", &e, []run.Run{r})
					if err != nil || got.Available || len(got.AvailableActions) != 0 {
						t.Fatalf("projection=%+v err=%v", got, err)
					}
				})
			}
			coreUnknown := execution
			coreUnknown.Status = "UNKNOWN"
			coreUnknown.RecoveryRequired = true
			unknownProjection, err := projectExecutionCancellation(context.Background(), service, "session-a", &coreUnknown, []run.Run{current})
			if err != nil || unknownProjection.Available || !unknownProjection.RecoveryRequired || unknownProjection.State != "OUTCOME_UNKNOWN" {
				t.Fatalf("Core recovery disposition lost: %+v %v", unknownProjection, err)
			}
			service.receipts = []executioncancel.Receipt{{State: "CANCEL_REQUESTED", BackendInterruptState: "ACKNOWLEDGED", QuiescenceState: "UNKNOWN"}}
			got, err = projectExecutionCancellation(context.Background(), service, "session-a", &execution, []run.Run{current})
			if err != nil || got.Available || got.State != "CANCEL_REQUESTED" {
				t.Fatalf("pending=%+v err=%v", got, err)
			}
			service.receipts[0].RecoveryRequired = true
			got, err = projectExecutionCancellation(context.Background(), service, "session-a", &execution, []run.Run{current})
			if err != nil || got.Available || got.State != "OUTCOME_UNKNOWN" || !got.RecoveryRequired {
				t.Fatalf("unknown=%+v err=%v", got, err)
			}
			now := time.Now()
			service.receipts = []executioncancel.Receipt{{State: "CANCELED", ResolvedAt: &now}}
			execution.Status = "CANCELED"
			current.Status = run.StatusCanceled
			got, err = projectExecutionCancellation(context.Background(), service, "fresh", &execution, []run.Run{current})
			if err != nil || got.Available || got.State != "CANCELED" || len(got.Receipts) != 1 {
				t.Fatalf("canceled=%+v err=%v", got, err)
			}
			execution.Status = "COMPLETED"
			current.Status = run.StatusSucceeded
			service.receipts[0].State = "CANCEL_NOT_APPLIED"
			got, err = projectExecutionCancellation(context.Background(), service, "fresh", &execution, []run.Run{current})
			if err != nil || got.Available || got.State != "ALREADY_TERMINAL" {
				t.Fatalf("terminal=%+v err=%v", got, err)
			}
			service.receipts = []executioncancel.Receipt{{State: "OUTCOME_UNKNOWN", QuiescenceState: "UNKNOWN", ResolvedAt: &now}}
			execution.Status = "FAILED"
			current.Status = run.StatusInfrastructureFailed
			service.state = agentconversation.State{Status: "READY"}
			got, err = projectExecutionCancellation(context.Background(), service, "fresh", &execution, []run.Run{current})
			if err != nil || got.Available || got.State != "OUTCOME_UNKNOWN" || got.RecoveryRequired || len(got.AvailableActions) != 0 {
				t.Fatalf("recovered unknown=%+v err=%v", got, err)
			}
		})
	}
}
