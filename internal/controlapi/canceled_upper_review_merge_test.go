package controlapi_test

import (
	"github.com/lazyun/devbridge/internal/agentconversation"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
)

func TestCanceledUpperReviewTaskPreservesSiblingDelivery(t *testing.T) {
	f := mixedLegacyTask(t)
	commit := func(in c.PrepareInput) c.Intent {
		t.Helper()
		in.AISessionID = f.session
		p, err := f.facade.PrepareConfirmation(f.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "confirm-" + in.RequestID, ContentHash: p.ContentHash, VariantID: in.Variants[0].ID})
		if err != nil || out.State != "COMPLETED" {
			t.Fatalf("%s: %+v %v", in.Action, out, err)
		}
		return out
	}
	created := commit(c.PrepareInput{RequestID: "withdrawn-reviewed-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: f.area, RepositoryID: f.repos["app"], Title: "Withdraw after read-only review", ProblemStatement: "Read same.txt only", Constraints: []string{"read only"}}, Variants: []c.Variant{{ID: "create", Summary: "Create"}}})
	withdrawn := created.Steps[0].TaskID
	started := commit(c.PrepareInput{RequestID: "withdrawn-upper-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: withdrawn, FeatureAreaID: f.area, RepositoryID: f.repos["app"], ReviewDepth: task.ReviewDepthFast, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "Read only", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt without editing"}}})
	reviewID := started.Steps[0].ReviewID
	access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "REVIEW", OwnerID: reviewID, RunID: access.Target.RunID, RequestID: "cancel-upper", Reason: "Withdraw read-only review", ExpectedUpperVersion: &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: access.Target.RuntimeEpoch, GrantGeneration: access.Target.GrantGeneration}})
	if err != nil || receipt.State != "CANCELED" {
		t.Fatalf("review cancel: %+v %v", receipt, err)
	}
	receipt, err = f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: withdrawn, RequestID: "cancel-reviewed-task", Reason: "Withdraw before implementation"})
	if err != nil || receipt.State != "CANCELED" {
		t.Fatalf("task cancel: %+v %v", receipt, err)
	}
	for _, cold := range []bool{false, true} {
		if cold {
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
		}
		cap, err := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
		if err != nil || !cap.CanCommit {
			t.Fatalf("cold=%t: %+v %v", cold, cap, err)
		}
	}
	prepared, input := prepareFollowupMerge(t, f)
	for _, member := range prepared.Content.FeatureArea.DeliveryPlan.Members {
		if len(member.TaskCommits) != 1 {
			t.Fatalf("unexpected delivery workset: %+v", member)
		}
	}
	out, err := f.facade.CommitConfirmation(f.ctx, input)
	if err != nil || out.State != "COMPLETED" {
		t.Fatalf("merge: %+v %v", out, err)
	}
	old, err := f.facade.GetTask(f.ctx, withdrawn)
	if err != nil || string(old.Status) != "CANCELED" || old.CommitSHA != "" {
		t.Fatalf("canceled history changed: %+v %v", old, err)
	}
}
