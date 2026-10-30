package bridgehttp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func TestConsoleAggregatesExecutionPhasesAndKeepsFrozenDefinitionHistory(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	ctx := context.Background()
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Go packages", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"all tests pass"}, CodexInstruction: "Run the confirmed tests."}
	current := createConsoleTask(t, f, f.moduleID, verification, "Original prepared intent")
	implementation := startTestRun(t, f, current, false)
	if _, err := f.plane.Runs.AttachEndSnapshot(ctx, implementation.ID, strings.Repeat("2", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.Runs.Transition(ctx, implementation.ID, run.StatusCollecting); err != nil {
		t.Fatal(err)
	}
	zero := run.TokenCounts{}
	window := int64(200000)
	if err := f.plane.AgentConversations.RecordFinalization(ctx, implementation.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: implementation.TurnID, Quiesced: true}); err != nil {
		t.Fatal(err)
	}
	implementation, err := f.plane.Runs.SucceedWithUsage(ctx, implementation.ID, run.Result{}, &run.TokenUsageObservation{StartCumulative: zero, EndCumulative: zero, ModelContextWindow: &window, Reliable: true})
	if err != nil {
		t.Fatal(err)
	}
	proposals := sqlite.NewExecutionProposalStore(f.plane.DB)
	owner, verifyRun, err := proposals.ReserveVerificationRun(ctx, implementation.ExecutionProposalID, "verification-console-owner", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	control, err := controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := consoleapi.New(f.plane, control)
	if err != nil {
		t.Fatal(err)
	}
	view, err := reader.GetProductProjectWorkspace(f.operatorContext(t), f.projectID)
	if err != nil || len(view.Executions) != 2 || len(view.Tasks) != 1 {
		t.Fatalf("one Execution with two phases: %+v err=%v", view, err)
	}
	got := view.Executions[0]
	if got.ID != owner.ID || got.State != "VERIFYING" || got.TaskDefinitionRevisionID != owner.TaskDefinitionRevisionID || view.Tasks[0].LatestExecution == nil || view.Tasks[0].LatestExecution.State != got.State || view.Tasks[0].Status != task.StatusActive || view.Tasks[0].Phase != task.PhaseVerifying {
		t.Fatalf("phase/ref mismatch: execution=%+v task=%+v", got, view.Tasks[0])
	}
	revision, err := f.plane.Tasks.GetDefinitionRevision(ctx, owner.TaskDefinitionRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskDefinitionVersion != revision.Ordinal || got.TaskDefinitionSHA256 != revision.PayloadSHA256 || view.Tasks[0].CurrentDefinitionVersion == 0 || view.Tasks[0].CurrentDefinitionSHA256 == "" {
		t.Fatalf("immutable Task definition provenance missing: revision=%+v execution=%+v task=%+v", revision, got, view.Tasks[0])
	}
	// The synthetic Console fixture must establish the same authoritative
	// cancellation facts as runtime; generic Fail(CANCELED) is not authority.
	if _, err := f.plane.Runs.Transition(ctx, verifyRun.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.AgentConversations.BeginRun(ctx, verifyRun.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.Runs.AttachThread(ctx, verifyRun.ID, implementation.ThreadID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.Runs.Transition(ctx, verifyRun.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	turnID := "turn-" + verifyRun.ID
	if _, err := f.plane.Runs.AttachTurn(ctx, verifyRun.ID, turnID); err != nil {
		t.Fatal(err)
	}
	if err := f.plane.AgentConversations.MarkTurnPending(ctx, verifyRun.ID); err != nil {
		t.Fatal(err)
	}
	cancellations := sqlite.NewExecutionCancellationStore(f.plane.DB)
	cancelInput := executioncancel.Input{AISessionID: f.sessionID, ProjectID: f.projectID, ExecutionID: owner.ID, RunID: verifyRun.ID, RequestID: "console-verification-cancel"}
	if _, dispatch, err := cancellations.Begin(ctx, cancelInput, true); err != nil || !dispatch {
		t.Fatalf("cancellation admission: dispatch=%t err=%v", dispatch, err)
	}
	if _, dispatch, err := cancellations.ClaimInterrupt(ctx, f.projectID, cancelInput.RequestID); err != nil || !dispatch {
		t.Fatalf("interrupt authority: dispatch=%t err=%v", dispatch, err)
	}
	if err := f.plane.AgentConversations.RecordFinalization(ctx, verifyRun.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: turnID, Quiesced: true, Canceled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.Runs.Fail(ctx, verifyRun.ID, run.Failure{Kind: run.FailureCanceled, Code: "EXECUTION_CANCELED", Message: "Verification canceled"}); err != nil {
		t.Fatal(err)
	}
	if _, err := proposals.Complete(ctx, owner.ID, executionproposal.OutcomeCanceled, "CANCELED", "Verification canceled", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	view, err = reader.GetProductProjectWorkspace(f.operatorContext(t), f.projectID)
	if err != nil || len(view.Executions) != 2 || len(view.Tasks) != 1 {
		t.Fatalf("history=%+v %v", view, err)
	}
	if view.Executions[0].TaskTitle != "Original prepared intent" || view.Tasks[0].Status != task.StatusActive {
		t.Fatalf("cancellation rewrote history: %+v", view)
	}
}
