package controlapi_test

import (
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestRuntimeUserDecisionProductionReviewStopSuccessor(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-review")
	runCodingReviewFixture(t, func(f *upperLifecycleFixture, taskID, reviewID, runID string) bool {
		t := f.t
		request, _ := waitUserRequest(t, f, taskID, "turn-review")
		if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, request, "STOP_AND_REVISE", "")); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			value, err := f.facade.GetTask(f.ctx, taskID)
			claim, e := f.plane.AgentConversations.GetClaim(f.ctx, f.area)
			if err == nil && value.Phase == task.PhaseReviewRequired && (e != nil || claim.RunID == "") {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		current, err := f.facade.GetTask(f.ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("DEVBRIDGE_CODING_V1_TURN_SUFFIX", "-successor")
		t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-review-successor")
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "review-stop-successor", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: taskID, FeatureAreaID: f.area, RepositoryID: current.RepositoryID, RepositoryTargets: workset.CloneTargets(f.targets), ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "new", Summary: "Explicit new Review after stop", ExecutorKind: string(review.ExecutorCodingAgent), ReviewInstruction: "Review the revised question within the same approved members.", PrimaryMemberID: f.targets[0].MemberID}}})
		if err != nil {
			t.Fatalf("prepare review successor phase=%s: %v", current.Phase, err)
		}
		options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
		if err != nil || len(options.BackendCandidates) == 0 {
			t.Fatalf("options %v", err)
		}
		backend := options.BackendCandidates[0]
		runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
		result, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "review-stop-successor-commit", ContentHash: prepared.ContentHash, VariantID: "new", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}})
		if err != nil {
			t.Fatal(err)
		}
		next, _ := waitUserRequest(t, f, taskID, "turn-review-successor")
		if next.ReviewID == reviewID || next.RunID == runID || len(result.Steps) != 1 || result.Steps[0].ReviewID != next.ReviewID {
			t.Fatalf("review successor not exact: %+v", next)
		}
		old, err := f.plane.Runs.Get(f.ctx, runID)
		if err != nil || old.AgentRuntime == nil || old.AgentRuntime.Requests[0].State != "CANCELED" || old.ReviewID != reviewID {
			t.Fatalf("old review overwritten: %+v %v", old, err)
		}
		if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, next, "CANCEL_OPERATION", "")); err != nil {
			t.Fatal(err)
		}
		return false
	})
}
