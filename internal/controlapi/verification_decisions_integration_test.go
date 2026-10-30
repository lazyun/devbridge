package controlapi_test

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"testing"
)

func TestVerificationPrimaryChoicesSameTaskAndMergedCompletion(t *testing.T) {
	testVerificationPrimaryChoicesSameTaskAndMergedCompletion(t)
}

func TestRequiredCustomVerificationCanReachExplicitMergedCompletion(t *testing.T) {
	t.Setenv("DEVBRIDGE_TEST_REQUIRED_CUSTOM", "PASSED")
	testVerificationPrimaryChoicesSameTaskAndMergedCompletion(t)
}

func testVerificationPrimaryChoicesSameTaskAndMergedCompletion(t *testing.T) {
	t.Helper()
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		owner := awaitAcceptanceRecoveryFixture(t, f, executionID)
		read := func() {
			t.Helper()
			v, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
			if err != nil || v.TaskView == nil || v.TaskView.VerificationDecisions == nil || len(v.TaskView.VerificationDecisions.Choices) != 3 {
				t.Fatalf("missing three primary choices: %+v %v", v.TaskView, err)
			}
		}
		read()
		commit := func(action, note string) c.Intent {
			t.Helper()
			input := c.PrepareInput{AISessionID: f.session, RequestID: action + "-prepare", Purpose: c.Execution, Action: action, Execution: &c.ExecutionBody{Action: action, TaskID: f.task, FeatureAreaID: f.area, ExecutionID: executionID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID), Note: note}, Variants: []c.Variant{{ID: "choice", Label: action, Summary: action}}}
			prepared, err := f.facade.PrepareConfirmation(f.ctx, input)
			if err != nil {
				t.Fatalf("prepare %s: %v", action, err)
			}
			if action == "COMPLETE_TASK" && (prepared.Content.Execution.CompletionPlan == nil || !prepared.Content.Execution.CompletionPlan.Valid()) {
				t.Fatal("completion did not freeze integration targets")
			}
			if _, e := f.plane.DB.SQL().Exec(`CREATE TRIGGER task_choice_receipt_loss BEFORE UPDATE OF intent_json ON confirmation_intents WHEN NEW.intent_id='` + prepared.IntentID + `' AND json_extract(NEW.intent_json,'$.state')='COMPLETED' BEGIN SELECT RAISE(ABORT,'injected task choice receipt loss'); END`); e != nil {
				t.Fatal(e)
			}
			out, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: action, RequestID: action + "-confirm", ContentHash: prepared.ContentHash, VariantID: "choice"})
			if err == nil {
				t.Fatal("expected receipt response loss after committed choice")
			}
			if _, e := f.plane.DB.SQL().Exec(`DROP TRIGGER task_choice_receipt_loss`); e != nil {
				t.Fatal(e)
			}
			out, err = f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: action + "-confirm"})

			if err != nil || out.State != "COMPLETED" {
				t.Fatalf("commit %s: %+v %v", action, out, err)
			}
			return out
		}
		commit("DEFER_TASK", "保留本次验证结果，稍后完成验收与合入。")
		v, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
		if err != nil || v.TaskView.VerificationDecisions.Hold == nil || v.TaskView.NextStep != nil {
			t.Fatalf("hold projection: %+v %v", v.TaskView, err)
		}
		if _, err = f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "held-complete", Purpose: c.Execution, Action: "COMPLETE_TASK", Execution: &c.ExecutionBody{Action: "COMPLETE_TASK", TaskID: f.task, FeatureAreaID: f.area, ExecutionID: executionID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID)}, Variants: []c.Variant{{ID: "choice", Summary: "complete"}}}); err == nil {
			t.Fatal("held Task offered completion")
		}
		commit("CONTINUE_TASK", "")
		read()
		v, err = f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
		if err != nil || v.TaskView.VerificationDecisions.Hold != nil || !v.TaskView.VerificationDecisions.ContinuationConfirmed {
			t.Fatalf("continuation projection: %+v %v", v.TaskView, err)
		}
		commit("COMPLETE_TASK", "")
		current, err := f.facade.GetTask(f.ctx, f.task)
		completed, completionErr := f.plane.WorkflowProgression.ReadTaskCompletion(f.ctx, owner.ProjectID, f.task)
		if err != nil || current.ID != owner.ID || current.Status != task.StatusSucceeded || completionErr != nil || completed == nil || completed.State != "COMPLETED" {
			t.Fatalf("completion state=%s/%s intent=%+v read=%v/%v", current.Status, current.Phase, completed, err, completionErr)
		}
		read()
		return false
	})
}

func TestBlockedRequiredCustomCannotCompleteOrMerge(t *testing.T) {
	t.Setenv("DEVBRIDGE_TEST_REQUIRED_CUSTOM", "BLOCKED")
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		owner := awaitAcceptanceRecoveryFixture(t, f, executionID)
		v, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
		if err != nil || v.TaskView == nil || v.TaskView.VerificationDecisions == nil || v.TaskView.VerificationReport == nil || len(v.TaskView.VerificationReport.Checks) != 5 {
			t.Fatalf("missing custom report/choices: %+v %v", v.TaskView, err)
		}
		for _, check := range v.TaskView.VerificationReport.Checks[3:] {
			if !check.Required || check.Observed != nil || check.Reported.Status != "BLOCKED" {
				t.Fatalf("custom requirement lost or fabricated: %+v", check)
			}
		}
		for _, choice := range v.TaskView.VerificationDecisions.Choices {
			if choice.Action == "COMPLETE_TASK" && choice.Available {
				t.Fatal("blocked custom enabled completion")
			}
		}
		_, err = f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "blocked-custom-complete", Purpose: c.Execution, Action: "COMPLETE_TASK", Execution: &c.ExecutionBody{Action: "COMPLETE_TASK", TaskID: f.task, FeatureAreaID: f.area, ExecutionID: executionID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID)}, Variants: []c.Variant{{ID: "complete", Summary: "must not complete"}}})
		if err == nil {
			t.Fatal("blocked custom passed Core completion prepare")
		}
		return false
	})
}

func TestOperatorVerificationChoicesKeepTheirOwnActorAndMerge(t *testing.T) {
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		owner := awaitAcceptanceRecoveryFixture(t, f, executionID)
		hold := task.HoldInput{TaskID: f.task, FeatureAreaID: f.area, ExecutionID: executionID, RevisionID: owner.CurrentDefinitionRevisionID, Action: "DEFER_TASK", RequestID: "operator-hold", Note: "暂存验证结果，待继续。"}
		ctx := operatorActionContext(t, owner.ProjectID, f.task, "operator-decisions", "DEFER_TASK")
		if _, err := f.facade.ChangeTaskHoldForOperator(ctx, owner.ProjectID, hold); err != nil {
			t.Fatal(err)
		}
		ctx = operatorActionContext(t, owner.ProjectID, f.task, "operator-decisions", "CONTINUE_TASK")
		hold.Action = "CONTINUE_TASK"
		hold.RequestID = "operator-continue"
		if _, err := f.facade.ChangeTaskHoldForOperator(ctx, owner.ProjectID, hold); err != nil {
			t.Fatal(err)
		}
		ctx = operatorActionContext(t, owner.ProjectID, f.task, "operator-decisions", "COMPLETE_TASK")
		plan, err := f.facade.PrepareTaskCompletionForOperator(ctx, owner.ProjectID, f.task)
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.facade.CompleteTaskForOperator(ctx, owner.ProjectID, plan, "operator-complete")
		if err != nil || out.Task.Status != task.StatusSucceeded || out.CommitError != "" {
			t.Fatalf("operator completion %+v %v", out, err)
		}
		var actor string
		if err = f.plane.DB.SQL().QueryRow(`SELECT actor_kind FROM decision_resolution_receipts WHERE decision_id=?`, wp.TaskAcceptanceDecisionID(executionID)).Scan(&actor); err != nil || actor != "LOCAL_OPERATOR" {
			t.Fatalf("actor=%s %v", actor, err)
		}
		return false
	})
}
