package controlapi_test

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"slices"
	"strings"
	"testing"
)

func TestCanceledT2WithoutT3GuidesRevision(t *testing.T) {
	f, id, ref := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	interruptFormalExecutionContext(t, f, id, ref, "formal_execution_receipts")
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	prefix, err := store.ReadApproveAndStartProgress(f.ctx, id)
	if err != nil || prefix.T2 == nil {
		t.Fatalf("T2=%+v err=%v", prefix, err)
	}
	cancel, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "EXECUTION", OwnerID: prefix.T2.ExecutionID, ExecutionID: prefix.T2.ExecutionID, RequestID: "cancel-unstarted-t2", Reason: "Stop admitted execution before final preparation receipt"})
	if err != nil || cancel.State != "CANCELED" {
		t.Fatalf("cancel=%+v err=%v", cancel, err)
	}
	guidance, err := f.facade.WorkflowGuidanceForResource(f.ctx, wp.ResourceScope{AISessionID: f.session, ProjectID: f.project, TaskID: f.task})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("postcancel guidance: state=%s next=%s actions=%v", guidance.CurrentState, guidance.NextAction.Code, guidance.AvailableActions)
	if guidance.NextAction.Code != "STOP_AND_REVISE" || slices.Contains(guidance.AvailableActions, wp.NoRunSuccessorAction) {
		t.Fatalf("invalid successor still offered: %+v", guidance)
	}
	node, nodeErr := sqlite.NewConfirmationStore(f.plane.DB).ReadTaskActionNode(f.ctx, f.project, f.task, f.session)
	if nodeErr != nil || node.Kind != "NO_RUN_RECOVERY" || !node.Blocking {
		t.Fatalf("original recovery node: %+v %v", node, nodeErr)
	}
	view, viewErr := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
	if viewErr != nil {
		t.Fatal(viewErr)
	}
	allowed := false
	for _, a := range view.TaskView.Actions {
		if a.Available {
			if a.Action != "STOP_AND_REVISE" {
				t.Fatalf("independent branch %s", a.Action)
			}
			allowed = true
		}
	}
	if !allowed {
		t.Fatal("exact stop-and-revise exit hidden")
	}
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	in := wp.PrepareSuccessorExecution{RequestID: "followup-successor", ProjectID: f.project, TaskID: f.task, PredecessorExecutionID: prefix.T2.ExecutionID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: prefix.T2.ExecutionID, ExpectedDefinitionRevisionID: prefix.Archive.RevisionID, ActorAuthority: f.session}
	in.PayloadSHA256 = c.ValueHash(in)
	result, err := f.facade.PrepareSuccessorExecutionForSession(f.ctx, f.session, in)
	t.Logf("offered successor result=%+v err=%v", result, err)
	if err == nil || !strings.Contains(err.Error(), "RUNTIME_SCOPE_SOURCE_STALE") {
		t.Fatalf("T2 lacking T3 did not reject unavailable source: %v", err)
	}
	if _, err = store.CommitApproveAndStart(f.ctx, id, ref); err == nil {
		t.Fatal("canceled T2 could still acquire T3")
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "unavailable successor and canceled T3 reject without writes")
	revised, reviseErr := f.facade.StopAndReviseNoRunTaskForSession(f.ctx, f.session, wp.StopAndReviseNoRunTask{ProjectID: f.project, TaskID: f.task, PredecessorExecutionID: prefix.T2.ExecutionID, ExpectedDefinitionRevisionID: prefix.Archive.RevisionID, ActorAuthority: f.session})
	t.Logf("alternate StopAndRevise phase=%s err=%v", revised.Phase, reviseErr)
	if reviseErr != nil || revised.Phase != task.PhaseReviewRequired {
		t.Fatalf("revision exit unavailable: %+v %v", revised, reviseErr)
	}
	next, e := sqlite.NewConfirmationStore(f.plane.DB).ReadTaskActionNode(f.ctx, f.project, f.task, f.session)
	if e != nil || next.Blocking || next.Generation <= node.Generation {
		t.Fatalf("original release not advanced: %+v %v", next, e)
	}
	var release string
	if e = f.plane.DB.SQL().QueryRow(`SELECT reason FROM task_action_releases WHERE task_id=?`, f.task).Scan(&release); e != nil || release != "CANCELED_NO_RUN_REVISED" {
		t.Fatalf("release receipt=%s %v", release, e)
	}

}

func TestCanceledNoRunOperatorUsesSharedTaskNode(t *testing.T) {
	f, id, ref := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	interruptFormalExecutionContext(t, f, id, ref, "formal_execution_receipts")
	prefix, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).ReadApproveAndStartProgress(f.ctx, id)
	if err != nil || prefix.T2 == nil {
		t.Fatal(err)
	}
	if _, err = f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "EXECUTION", OwnerID: prefix.T2.ExecutionID, ExecutionID: prefix.T2.ExecutionID, RequestID: "operator-cancel-t2", Reason: "Stop before dispatch"}); err != nil {
		t.Fatal(err)
	}
	ctx := operatorActionContext(t, f.project, f.task, "shared-norun-operator", "STOP_AND_REVISE")
	reader, err := consoleapi.New(f.plane, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.GetProductTask(ctx, f.project, f.task)
	if err != nil {
		t.Fatal(err)
	}
	var target *workflowpresentation.ActionTarget
	for _, a := range page.OperatorActions {
		if a.Action == "STOP_AND_REVISE" && a.DomainAvailable && a.ChannelAvailable {
			target = a.Target
		}
	}
	if target == nil || target.TaskActionGeneration == 0 {
		t.Fatal("shared operator recovery target missing")
	}
	input := consoleapi.TaskActionInput{Action: "STOP_AND_REVISE", Target: target, RequestID: "operator-stop-t2"}
	for range 2 {
		out, e := reader.ResolveTaskAction(ctx, f.project, f.task, input)
		if e != nil {
			t.Fatal(e)
		}
		if out.(task.Task).Phase != task.PhaseReviewRequired {
			t.Fatal("operator did not advance original Task")
		}
	}
	var starts int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=? AND purpose='IMPLEMENTATION'`, f.task).Scan(&starts); err != nil || starts != 0 {
		t.Fatal("recovery started implementation", err)
	}
}
