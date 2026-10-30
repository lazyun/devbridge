package localmcp_test

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	"github.com/lazyun/devbridge/internal/workspace"
)

// TestExplicitRecoveryReprojectsFailedExecutionAsReady exercises the complete
// SQLite recovery path. The old failed product facts remain history; only the
// exact unsafe Agent context is abandoned and made eligible for fresh work.
func TestExplicitRecoveryReprojectsFailedExecutionAsReady(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), recoveryFixtureCallerScope)
	fixture := createUnknownRecoveryFixture(t)

	// A new exclusive writer fences the fixture's old runtime epoch and runs
	// startup classification. This is the production prerequisite for explicit
	// recovery, rather than a synthetic RECOVERY_REQUIRED projection stub.
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: fixture.root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}

	development, err := facade.DevelopmentContextForSession(ctx, fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(development.Repositories) != 1 || len(development.Repositories[0].Branches) == 0 {
		t.Fatalf("development branch fixture=%+v", development)
	}
	branch := development.Repositories[0].Branches[0]
	if _, err = facade.SelectDevelopmentBranchForSession(ctx, developmentcycle.SelectInput{
		AISessionID: fixture.sessionID, RepositoryID: development.Repositories[0].RepositoryID,
		DevelopmentRef: branch.FullRef, RequestID: "select-recovery-projection-branch",
	}); err != nil {
		t.Fatal(err)
	}

	// The original fixture intentionally had no workspace. Add one as persisted
	// test setup and bind it to the already terminal old Run so the real recovery
	// transaction must quarantine it without creating a successor Run.
	taskValue, err := plane.Tasks.Get(ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	workspaceValue, err := plane.Workspaces.EnsureForFeatureArea(ctx, taskValue.ProjectID, taskValue.RepositoryID, taskValue.FeatureAreaID, taskValue.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := plane.DB.SQL().ExecContext(ctx, `UPDATE runs SET workspace_id=? WHERE id=? AND COALESCE(workspace_id,'')=''`, workspaceValue.ID, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := bound.RowsAffected(); changed != 1 {
		t.Fatalf("bind terminal workspace rows=%d", changed)
	}

	beforeState, err := plane.AgentConversations.State(ctx, fixture.featureAreaID)
	if err != nil || beforeState.Status != "RECOVERY_REQUIRED" || beforeState.Claim == nil || beforeState.AgentConversationID == "" {
		t.Fatalf("real startup recovery state=%+v err=%v", beforeState, err)
	}
	// Startup recovery canonicalizes the durable operation identity. The test
	// must submit the exact current claim, rather than its historical synthetic
	// identifiers.
	fixture.executionID = beforeState.Claim.OperationID
	fixture.runID = beforeState.Claim.RunID
	fixture.conversationID = beforeState.AgentConversationID
	beforeProjection, err := facade.FeatureAreaPresentationForSession(ctx, fixture.sessionID, fixture.featureAreaID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeProjection.WorkflowStatus != "RECOVERY_REQUIRED" || beforeProjection.Recovery == nil || !beforeProjection.Recovery.Required || beforeProjection.Details.ExecutionID != fixture.executionID || beforeProjection.Details.TaskID != fixture.taskID || beforeProjection.Details.RunID != fixture.runID {
		t.Fatalf("pre-recovery projection=%+v", beforeProjection)
	}
	assertRecoveryExecutionHistory(t, beforeProjection.TaskStages.Stages, fixture.runID, "INFRASTRUCTURE_FAILED")

	var runCountBefore, recoveryCountBefore int
	if err = plane.DB.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs`).Scan(&runCountBefore); err != nil {
		t.Fatal(err)
	}
	if err = plane.DB.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_operation_recoveries`).Scan(&recoveryCountBefore); err != nil {
		t.Fatal(err)
	}

	input := agentconversation.RecoverInput{
		FeatureAreaID: fixture.featureAreaID, OperationKind: agentconversation.Execution,
		OperationID: fixture.executionID, RunID: fixture.runID,
		AgentConversationID: fixture.conversationID, RequestID: "recover-projection-once",
	}
	result, err := facade.RecoverFeatureAreaAgentOperation(ctx, fixture.sessionID, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Boundary != agentconversation.Unknown || !result.ContextAbandoned || !result.FreshWorkAllowed || result.OldConversationID != fixture.conversationID || result.NewConversationID == "" || result.NewConversationID == fixture.conversationID {
		t.Fatalf("explicit recovery result=%+v", result)
	}

	afterProjection, err := facade.FeatureAreaPresentationForSession(ctx, fixture.sessionID, fixture.featureAreaID)
	if err != nil {
		t.Fatal(err)
	}
	// Operation recovery rotates the unsafe Agent context but is not a formal
	// Task recovery resolution: the durable Phase remains under
	// TASK_RECOVERY_REQUIRED/RESOLVE_TASK_RECOVERY (the shared task-owned
	// projection identity, distinct from the operation-level RECOVERY_REQUIRED
	// literal) until the exact Resolve transaction exits it.
	if afterProjection.WorkflowStatus != "TASK_RECOVERY_REQUIRED" || afterProjection.NextAction.Code != "RESOLVE_TASK_RECOVERY" || (afterProjection.Recovery != nil && afterProjection.Recovery.Required) {
		t.Fatalf("post-recovery projection did not remain under Task recovery=%+v", afterProjection)
	}
	if afterProjection.CurrentOperation != nil || afterProjection.Details.ExecutionID != fixture.executionID || afterProjection.Details.TaskID != fixture.taskID || afterProjection.Details.RunID != fixture.runID {
		t.Fatalf("post-recovery identity changed or retained live ownership=%+v", afterProjection)
	}
	// Task stages retain the selected Task's terminal Run as historical
	// identity. They must not grant cancellation of that old operation. The
	// existing Core may still allow cancellation of the active semantic Task
	// after recovery, with no Run or Execution target.
	for _, capability := range afterProjection.Capabilities {
		if capability.Action == "CANCEL_EXECUTION" && capability.Available {
			target := capability.Target
			if target == nil || target.OperationKind != "TASK" || target.OperationID != fixture.taskID || target.TaskID != fixture.taskID || target.RunID != "" || target.ExecutionID != "" {
				t.Fatalf("recovered history exposed live operation cancellation: %+v", capability)
			}
			eligible, e := sqlite.NewExecutionCancellationStore(plane.DB).PreWorkTaskCancellationEligible(ctx, taskValue.ProjectID, fixture.taskID)
			if e != nil || !eligible {
				t.Fatalf("Task capability diverged from canonical cancellation guards: %v %v", eligible, e)
			}
		}
	}
	historicalProjection, e := facade.TaskPresentationForSession(ctx, fixture.sessionID, fixture.featureAreaID, fixture.taskID)
	if e != nil {
		t.Fatal(e)
	}
	assertRecoveryExecutionHistory(t, historicalProjection.TaskStages.Stages, fixture.runID, "INFRASTRUCTURE_FAILED")

	oldRun, err := plane.Runs.Get(ctx, fixture.runID)
	if err != nil || oldRun.Status != run.StatusInfrastructureFailed || oldRun.ExecutionProposalID != fixture.executionID || oldRun.TaskID != fixture.taskID || oldRun.WorkspaceID != workspaceValue.ID {
		t.Fatalf("old Run history=%+v err=%v", oldRun, err)
	}
	executionValue, err := sqlite.NewExecutionProposalStore(plane.DB).Get(ctx, fixture.executionID)
	if err != nil || executionValue.Outcome != executionproposal.OutcomeFailed || executionValue.ImplementationRunID != fixture.runID || executionValue.VerificationRunID != "" {
		t.Fatalf("old Execution history=%+v err=%v", executionValue, err)
	}
	taskValue, err = plane.Tasks.Get(ctx, fixture.taskID)
	if err != nil || taskValue.Status != task.StatusActive || taskValue.Phase != task.PhaseRecoveryRequired || taskValue.Work.BackendDisposition != "UNKNOWN_ORPHANED" {
		t.Fatalf("old Task history=%+v err=%v", taskValue, err)
	}
	quarantined, err := sqlite.NewWorkspaceStore(plane.DB).Get(ctx, workspaceValue.ID)
	if err != nil || !quarantined.RuntimeQuarantined || quarantined.Status != workspace.StatusContinuityUnavailable {
		t.Fatalf("old workspace was not quarantined=%+v err=%v", quarantined, err)
	}

	currentConversation, err := plane.AgentConversations.Current(ctx, fixture.featureAreaID)
	if err != nil || currentConversation.ID != result.NewConversationID || currentConversation.PredecessorID != fixture.conversationID || currentConversation.BackendThreadID != "" || currentConversation.CurrentBoundaryTurnID != "" {
		t.Fatalf("rotated conversation=%+v err=%v", currentConversation, err)
	}
	receipts, err := plane.AgentConversations.ListRecoveryReceipts(ctx, fixture.featureAreaID)
	if err != nil || len(receipts) != recoveryCountBefore+1 {
		t.Fatalf("recovery receipts=%+v err=%v", receipts, err)
	}
	receipt := receipts[len(receipts)-1]
	if receipt.RequestID != input.RequestID || receipt.RunID != fixture.runID || receipt.OperationID != fixture.executionID || receipt.OldConversationID != fixture.conversationID || receipt.NewConversationID != result.NewConversationID {
		t.Fatalf("exact recovery receipt=%+v", receipt)
	}

	var runCountAfter int
	if err = plane.DB.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs`).Scan(&runCountAfter); err != nil {
		t.Fatal(err)
	}
	if runCountAfter != runCountBefore {
		t.Fatalf("recovery created successor Run: before=%d after=%d", runCountBefore, runCountAfter)
	}
	var advance string
	if err = plane.DB.SQL().QueryRowContext(ctx, `SELECT advance_state FROM agent_operation_boundaries WHERE run_id=?`, fixture.runID).Scan(&advance); err != nil || advance != string(agentconversation.Unknown) {
		t.Fatalf("old UNKNOWN boundary changed: advance=%q err=%v", advance, err)
	}
}

func assertRecoveryExecutionHistory(t *testing.T, progress []presentation.PhaseProgress, runID, status string) {
	t.Helper()
	for _, item := range progress {
		if item.Phase == "IMPLEMENTATION" {
			if item.SourceID != runID || item.Status != status {
				t.Fatalf("historical implementation progress=%+v", item)
			}
			return
		}
	}
	t.Fatalf("implementation progress missing: %+v", progress)
}
