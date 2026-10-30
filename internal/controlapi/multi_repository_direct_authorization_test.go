package controlapi_test

import (
	"encoding/json"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestMultiRepositoryDirectG7OnlyConsumedExactConfirmationCanStart(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	var unrelated string
	if err := f.plane.DB.SQL().QueryRow(`SELECT intent_id FROM confirmation_intents WHERE json_extract(intent_json,'$.action')='APPROVE' AND ai_session_id=?`, f.session).Scan(&unrelated); err != nil {
		t.Fatal(err)
	}
	in := directfile.StartInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: wp.DecisionID(f.review), RequestID: "forged-start", ConfirmationIntentID: unrelated, Action: "START_DIRECT_FILE"}
	if _, err := f.plane.DirectFiles.StartTaskAuthorization(f.ctx, in); err == nil {
		t.Fatal("unrelated completed confirmation authorized write grant")
	}
	pending, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "unconsumed-start", Purpose: c.Execution, Action: "START_DIRECT_FILE", Execution: &c.ExecutionBody{Action: "START_DIRECT_FILE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID}, Variants: []c.Variant{{ID: "exact", Summary: "Explicit start not yet confirmed", ImplementationMode: task.ImplementationDirectFile, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec, RepositoryTargets: current.RepositoryTargets}}})
	if err != nil {
		t.Fatal(err)
	}
	in.ConfirmationIntentID = pending.IntentID
	if _, err = f.plane.DirectFiles.StartTaskAuthorization(f.ctx, in); err == nil {
		t.Fatal("unconsumed pending intent authorized write grant")
	}
	if _, err = f.facade.DirectFileReconcileTaskAuthorization(f.ctx, directfile.Registration{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: wp.DecisionID(f.review), ImplementationMode: "DIRECT_FILE", Confirmed: true}); err == nil {
		t.Fatal("hidden reconcile created write authority")
	}
	var count int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_task_authorizations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unconsumed side effect %d %v", count, err)
	}
	if _, err = f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(pending)); err != nil {
		t.Fatal(err)
	}
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatal(err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.DirectFileDisable(f.ctx, directfile.Scope{AISessionID: f.session, FeatureAreaID: f.area, WorkflowID: state.WorkflowID}); err == nil {
		t.Fatal("generic disable bypassed confirmed v2 cancellation")
	}
	automatic := directInput(f, state, current.RepositoryTargets[0])
	automatic.Path = "new.txt"
	automatic.RequestID = "implicit-complete"
	automatic.Content = "must not write"
	automatic.WorkflowComplete = true
	if _, err = f.facade.FileCreate(f.ctx, automatic); err == nil {
		t.Fatal("implicit workflow completion accepted")
	}
	var ops int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id='implicit-complete'`, state.WorkflowID).Scan(&ops); err != nil || ops != 0 {
		t.Fatalf("implicit completion modified durable operation state %d %v", ops, err)
	}
	if _, err = directConfirmation(t, f, current, "CANCEL_DIRECT_FILE", state); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err = f.plane.DB.SQL().QueryRow(`SELECT payload_json FROM direct_file_start_receipts WHERE workflow_id=? AND action='START_DIRECT_FILE'`, state.WorkflowID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	if replay, err := f.plane.DirectFiles.StartTaskAuthorization(f.ctx, in); err != nil || replay.State == "ACTIVE" || replay.Enabled {
		t.Fatalf("old start replay revived grant %+v %v", replay, err)
	}
	if _, err = f.facade.DirectFileReconcileTaskAuthorization(f.ctx, directfile.Registration{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: wp.DecisionID(f.review), ImplementationMode: "DIRECT_FILE", Confirmed: true}); err == nil {
		t.Fatal("closed grant hidden reconcile accepted")
	}
}
