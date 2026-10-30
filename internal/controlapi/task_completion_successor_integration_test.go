package controlapi_test

import (
	"os"
	"path/filepath"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestCompletionMergeSuccessorUsesFormalConfirmationAndKeepsAcceptance(t *testing.T) {
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		owner := awaitAcceptanceRecoveryFixture(t, f, executionID)
		prepare := func(in c.PrepareInput) c.Intent {
			t.Helper()
			in.AISessionID = f.session
			p, err := f.facade.PrepareConfirmation(f.ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
		commit := func(p c.Intent, request string) c.Intent {
			t.Helper()
			out, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: request, ContentHash: p.ContentHash, VariantID: "confirm"})
			if err != nil || out.State != "COMPLETED" {
				t.Fatalf("confirmation %s: %+v %v", p.Action, out, err)
			}
			return out
		}
		initial := prepare(c.PrepareInput{RequestID: "complete-prepare", Purpose: c.Execution, Action: "COMPLETE_TASK", Execution: &c.ExecutionBody{Action: "COMPLETE_TASK", TaskID: f.task, FeatureAreaID: f.area, ExecutionID: executionID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID)}, Variants: []c.Variant{{ID: "confirm", Summary: "Complete and integrate verified work"}}})
		frozen := initial.Content.Execution.CompletionPlan
		if frozen == nil || len(frozen.Members) != 2 {
			t.Fatalf("missing multi-member plan: %+v", frozen)
		}
		// Let the first member merge, then advance the second target before its
		// dispatch. A successor must preserve the completed first member's receipt.
		target := frozen.Members[1]
		advanced := false
		f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, repository string) error {
			if stage == "AREA_MERGE_BEFORE_GIT" && repository == target.RepositoryID && !advanced {
				advanced = true
				if err := os.WriteFile(filepath.Join(target.TargetPath, "concurrent.txt"), []byte("independent development change\n"), 0600); err != nil {
					t.Fatal(err)
				}
				compositionGit(t, target.TargetPath, "add", "concurrent.txt")
				compositionGit(t, target.TargetPath, "commit", "-m", "concurrent development change")
			}
			return nil
		})
		commit(initial, "complete-confirm")
		f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
		current, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil || current.Status != task.StatusActive {
			t.Fatalf("completed before all members: %+v %v", current, err)
		}
		state, err := f.plane.WorkflowProgression.ReadTaskCompletion(f.ctx, owner.ProjectID, f.task)
		if err != nil || state == nil {
			t.Fatalf("missing completion: %+v %v", state, err)
		}
		oldRequest := state.MergeRequestID
		var firstReceipt string
		if err = f.plane.DB.SQL().QueryRow(`SELECT state||resulting_dev_head||completed_at FROM feature_area_commit_members WHERE request_id=? AND repository_id=?`, oldRequest, frozen.Members[0].RepositoryID).Scan(&firstReceipt); err != nil {
			t.Fatal(err)
		}
		candidates, err := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
		if err != nil || len(candidates) != 1 || !candidates[0].CanRelease {
			t.Fatalf("release: %+v %v", candidates, err)
		}
		candidate := candidates[0]
		if _, err = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: oldRequest, RepositoryID: candidate.RepositoryID, PredecessorOperationID: candidate.OperationID, RequestID: "release-completion", ReleasePlan: true}); err != nil {
			t.Fatal(err)
		}
		view, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
		if err != nil || view.TaskView.VerificationDecisions.Completion.State != "REPLAN_REQUIRED" {
			t.Fatalf("missing replan presentation: %+v %v", view.TaskView, err)
		}
		canPrepare := false
		for _, cap := range view.Capabilities {
			if cap.Action == "COMMIT_FEATURE_AREA" && cap.Available {
				canPrepare = true
			}
		}
		if !canPrepare {
			t.Fatal("no formal merge preparation capability")
		}
		_, wrongErr := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "wrong-completion-task", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area, CompletionTaskID: "missing-task"}, Variants: []c.Variant{{ID: "confirm", Summary: "must retain exact Task"}}})
		if wrongErr == nil {
			t.Fatal("substituted another completion Task")
		}
		successor := prepare(c.PrepareInput{RequestID: "successor-prepare", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area, CompletionTaskID: f.task}, Variants: []c.Variant{{ID: "confirm", Summary: "Integrate original accepted work into the updated development targets"}}})
		p := successor.Content.FeatureArea.DeliveryPlan
		if p == nil || p.CompletionTaskID != f.task || p.PredecessorMergeRequestID != oldRequest {
			t.Fatalf("lost original Task binding: %+v", p)
		}
		current, err = f.facade.GetTask(f.ctx, f.task)
		if err != nil || current.Status != task.StatusActive {
			t.Fatal("prepare dispatched merge")
		}
		commit(successor, "successor-confirm")
		current, err = f.facade.GetTask(f.ctx, f.task)
		if err != nil || current.Status != task.StatusSucceeded {
			t.Fatalf("not completed after successor: %+v %v", current, err)
		}
		commit(successor, "successor-confirm")
		var count int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM decision_resolution_receipts WHERE decision_id=?`, wp.TaskAcceptanceDecisionID(executionID)).Scan(&count); err != nil || count != 1 {
			t.Fatal("repeated acceptance", err)
		}
		var oldAfter string
		if err = f.plane.DB.SQL().QueryRow(`SELECT state||resulting_dev_head||completed_at FROM feature_area_commit_members WHERE request_id=? AND repository_id=?`, oldRequest, frozen.Members[0].RepositoryID).Scan(&oldAfter); err != nil || oldAfter != firstReceipt {
			t.Fatal("rewrote completed original member", err)
		}
		return false
	})
}
