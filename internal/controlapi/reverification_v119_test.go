package controlapi_test

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"testing"
	"time"
)

func TestReverificationV119AcceptsAndDelivers(t *testing.T) {
	t.Setenv("DEVBRIDGE_TEST_UNIQUE_TURNS", "1")
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		current := awaitAcceptanceRecoveryFixture(t, f, executionID)
		_, input := prepareAcceptanceRecoveryFixture(t, f, current, executionID, "REQUEST_REVERIFICATION")
		intent, err := f.facade.CommitConfirmation(f.ctx, input)
		if err != nil || intent.State != "COMPLETED" {
			t.Fatalf("reverification admission: %+v %v", intent, err)
		}
		current, err = f.facade.GetTask(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		successor := current.CurrentExecutionID
		proposal, err := f.facade.GetExecutionProposal(f.ctx, f.session, successor)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("successor=%s phase=%s implementation=%q approvedWorkset=%s", successor, current.Phase, proposal.ImplementationRunID, proposal.ApprovedWorksetHash)
		started, err := f.facade.StartTaskExecutionExpectedCommand(f.ctx, f.session, f.task, "START_VERIFICATION", "audit-reverification-start", wp.TaskExecutionExpectation{FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: successor, Phase: task.PhaseVerificationReady, PrimaryMemberID: proposal.PrimaryMemberID, ApprovedWorksetHash: proposal.ApprovedWorksetHash})
		if err != nil {
			t.Fatalf("legal reverify start failed: proposal=%+v run=%s err=%v", started.Proposal, started.Run.ID, err)
		}
		if session, ok := f.plane.Supervisor.GetSession(started.Run.ID); ok {
			select {
			case <-session.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("reverification session timeout")
			}
		}
		current, err = f.facade.GetTask(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		// A new verification Run needs its own explicit GPT analysis before acceptance.
		current = awaitAcceptanceRecoveryFixture(t, f, successor)
		vr, readErr := f.facade.GetRunForSession(f.ctx, f.session, started.Run.ID)
		t.Logf("reverification Run=%s failure=%+v phase=%s readErr=%v", vr.Status, vr.Failure, current.Phase, readErr)
		if vr.Status != run.StatusSucceeded {
			t.Fatalf("public reverification cannot complete: status=%s failure=%+v", vr.Status, vr.Failure)
		}
		proposal, err = f.facade.GetExecutionProposal(f.ctx, f.session, successor)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := f.facade.GetRunForSession(f.ctx, f.session, proposal.VerificationRunID)
		if err != nil {
			t.Fatal(err)
		}
		if verified.VerificationReport == nil || verified.VerificationReport.Availability != run.VerificationReportAvailable {
			t.Fatalf("reverification didn't record complete report: %+v", verified)
		}
		t.Logf("reverification complete impl=%s verify=%s status=%s", proposal.ImplementationRunID, proposal.VerificationRunID, verified.Status)
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "audit-accept-reverification", Purpose: c.Execution, Action: "ACCEPT", Execution: &c.ExecutionBody{Action: "ACCEPT", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: successor, DecisionID: wp.TaskAcceptanceDecisionID(successor)}, Variants: []c.Variant{{ID: "accept", Summary: "Accept complete reverification"}}})
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "audit-accept-reverification-commit", ContentHash: prepared.ContentHash, VariantID: "accept"})
		if err != nil || accepted.State != "COMPLETED" {
			t.Errorf("complete reverification cannot be accepted: state=%s err=%v", accepted.State, err)
		}
		var completed int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_commit_receipts WHERE task_id=? AND completed_at IS NOT NULL AND terminal_outcome='APPLIED'`, f.task).Scan(&completed); err != nil || completed != 1 {
			t.Fatalf("accepted result failed delivery: %d %v", completed, err)
		}
		merged, input := prepareFollowupMerge(t, f)
		if _, err = f.facade.CommitConfirmation(f.ctx, input); err != nil {
			t.Fatalf("accepted reverification cannot merge: %v (plan=%+v)", err, merged.Content.FeatureArea.DeliveryPlan)
		}
		return false
	})
}

func TestOriginalVerificationV119Acceptance(t *testing.T) {
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		current := awaitAcceptanceRecoveryFixture(t, f, executionID)
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "audit-accept-original", Purpose: c.Execution, Action: "ACCEPT", Execution: &c.ExecutionBody{Action: "ACCEPT", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: executionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID)}, Variants: []c.Variant{{ID: "accept", Summary: "Accept original complete verification"}}})
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "audit-accept-original-commit", ContentHash: prepared.ContentHash, VariantID: "accept"})
		if err != nil || accepted.State != "COMPLETED" {
			t.Fatalf("original complete verification acceptance: state=%s err=%v", accepted.State, err)
		}
		t.Logf("control original multi-repository verification ACCEPT=%s", accepted.State)
		return false
	})
}
