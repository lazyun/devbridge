package controlapi_test

import (
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestCodingAcceptanceRejectsDuplicateRepositoryVerification(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_DUPLICATE_VERIFICATION_ROOT", "1")
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		current := awaitAcceptanceRecoveryFixture(t, f, executionID)
		proposal, err := f.facade.GetExecutionProposal(f.ctx, f.session, executionID)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := f.facade.GetRunForSession(f.ctx, f.session, proposal.VerificationRunID)
		if err != nil {
			t.Fatal(err)
		}
		report := verified.VerificationReport
		if report == nil || len(report.Checks) != 3 || report.Checks[0].Observed == nil || report.Checks[0].Observed.Status != run.VerificationCheckPassed || report.Checks[1].Observed == nil || report.Checks[1].Observed.Status != run.VerificationCheckNotRun || report.Checks[1].Reported.Status != run.VerificationCheckPassed {
			t.Fatalf("duplicate repository evidence was reused: %+v", report)
		}
		_, err = f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "duplicate-root-accept", Purpose: c.Execution, Action: "ACCEPT", Execution: &c.ExecutionBody{Action: "ACCEPT", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: executionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID)}, Variants: []c.Variant{{ID: "accept", Summary: "Attempt acceptance with a missing member observation"}}})
		if err == nil {
			t.Fatal("unobserved member offered a new acceptance confirmation")
		}
		current, err = f.facade.GetTask(f.ctx, f.task)
		if err != nil || current.Status != task.StatusActive || current.Phase != task.PhaseAcceptancePending {
			t.Fatalf("rejected acceptance changed Task: %+v %v", current, err)
		}
		var resolutions, deliveries, dispatches int
		if err = f.plane.DB.SQL().QueryRow(`SELECT
 (SELECT count(*) FROM decision_resolution_receipts WHERE decision_id=?),
 (SELECT count(*) FROM task_commit_members WHERE task_id=?),
 (SELECT count(*) FROM task_delivery_dispatch_intents WHERE task_id=?)`, wp.TaskAcceptanceDecisionID(executionID), f.task, f.task).Scan(&resolutions, &deliveries, &dispatches); err != nil || resolutions != 0 || deliveries != 0 || dispatches != 0 {
			t.Fatalf("rejected acceptance created effects: receipts/members/dispatches=%d/%d/%d err=%v", resolutions, deliveries, dispatches, err)
		}
		return false
	})
}
