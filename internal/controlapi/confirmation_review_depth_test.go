package controlapi_test

import (
	"encoding/json"
	"errors"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

func reviewDepthFixture(t *testing.T) (*upperLifecycleFixture, c.PrepareInput) {
	t.Helper()
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	_, commit := prepareDefinitionFormalApproval(t, f, "depth-parent")
	if _, err := f.facade.CommitConfirmation(f.ctx, commit); err != nil {
		t.Fatal(err)
	}
	owner, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := f.plane.Reviews.Get(f.ctx, owner.CurrentReviewID)
	if err != nil || previous.ReviewDepth != task.ReviewDepthDeep {
		t.Fatal("fixture's previous Review must be DEEP")
	}
	return f, c.PrepareInput{AISessionID: f.session, RequestID: "depth-supplemental", Purpose: c.Review, Action: "REQUEST_SUPPLEMENTAL_REVIEW",
		Review:   &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: workset.CloneTargets(f.targets), ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}, SupplementalPurpose: "Revise the existing implementation contract", ExpectedTarget: &wp.TaskReviewRequestTarget{Phase: owner.Phase, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, CurrentReviewID: owner.CurrentReviewID, CurrentExecutionID: owner.CurrentExecutionID}},
		Variants: []c.Variant{{ID: "upper", Label: "GPT Review", Summary: "Revise exact contracts", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read both exact contracts and revise the implementation instruction", PrimaryMemberID: f.targets[0].MemberID}},
	}
}

func TestReviewDepthRejectedBeforeSupplementalConfirmationOrConsumption(t *testing.T) {
	f, input := reviewDepthFixture(t)
	projection, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, cap := range projection.Capabilities {
		if cap.Action == "REQUEST_TASK_REVIEW" && cap.Available && cap.RequiredReviewDepth != nil && *cap.RequiredReviewDepth == task.ReviewDepthDeep && len(cap.AllowedReviewDepths) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("supplemental depth lost while approved Task required_review_depth is NULL")
	}
	tables := []string{"tasks", "confirmation_intents", "confirmation_commit_requests", "task_supplemental_review_requests", "reviews", "runs", "feature_area_agent_claims"}
	before := definitionFormalSnapshot(t, f, tables)
	input.Review.ReviewDepth = task.ReviewDepthFast
	_, err = f.facade.PrepareConfirmation(f.ctx, input)
	var failure *c.Failure
	if !errors.As(err, &failure) || failure.Code != "REVIEW_DEPTH_TOO_SHALLOW" || failure.Stage != "CONFIRMATION_PREFLIGHT" || failure.NextAction != c.PrepareNewVersion {
		t.Fatalf("wrong shallow depth rejection: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "invalid depth prepare")
	// A pending intent prepared by an older server is rechecked before its
	// first business step, not after REQUEST_REVIEW has changed the Task.
	input.SubmittedHash = c.ValueHash(input)
	legacy, err := sqlite.NewConfirmationStore(f.plane.DB).Prepare(f.ctx, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	request := c.CommitInput{AISessionID: f.session, IntentID: legacy.IntentID, Version: legacy.Version, Purpose: legacy.Purpose, Action: legacy.Action, ContentHash: legacy.ContentHash, RequestID: "legacy-shallow-confirm", VariantID: "upper"}
	refused, err := f.facade.CommitConfirmation(f.ctx, request)
	if !errors.As(err, &failure) || failure.Code != "REVIEW_DEPTH_TOO_SHALLOW" || refused.Commit != nil || refused.State != "PENDING" || refused.Submission == nil || refused.Submission.Outcome != c.RejectedNotConsumed {
		t.Fatalf("legacy shallow consumed: %+v %v", refused, err)
	}
	var requests int
	if err = f.plane.DB.SQL().QueryRow(`SELECT COUNT(*) FROM task_supplemental_review_requests WHERE task_id=?`, f.task).Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("request committed before depth check: %d %v", requests, err)
	}
}

func TestHistoricalPartialSupplementalGetsSameTaskCorrectedStartGuidance(t *testing.T) {
	f, input := reviewDepthFixture(t)
	valid, err := f.facade.PrepareConfirmation(f.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(valid)); err != nil {
		t.Fatal(err)
	}
	old := valid.Content
	body := *old.Review
	old.Review = &body
	old.RequestID = "historical-shallow"
	old.Review.ReviewDepth = task.ReviewDepthFast
	old.SubmittedHash = c.ValueHash(old)
	store := sqlite.NewConfirmationStore(f.plane.DB)
	legacy, err := store.Prepare(f.ctx, f.project, old)
	if err != nil {
		t.Fatal(err)
	}
	commit := c.CommitInput{AISessionID: f.session, IntentID: legacy.IntentID, Version: legacy.Version, Purpose: legacy.Purpose, Action: legacy.Action, ContentHash: legacy.ContentHash, RequestID: "historical-confirm", VariantID: "upper"}
	commit.SubmittedHash = c.ValueHash(commit)
	steps := []c.Step{}
	for _, kind := range []string{"REQUEST_REVIEW", "START_REVIEW"} {
		steps = append(steps, c.Step{Kind: kind, RequestID: "confirmation_" + c.ValueHash([]string{legacy.IntentID, kind})[:32], State: "PENDING", ProjectID: f.project, FeatureAreaID: f.area, TaskID: f.task})
	}
	if _, _, err = store.BeginCommit(f.ctx, commit, steps); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.RequestTaskReview(taskaction.WithIntent(f.ctx, legacy.IntentID), wp.TaskReviewRequestInput{AISessionID: f.session, TaskID: f.task, FeatureAreaID: f.area, RequestID: steps[0].RequestID, Purpose: old.Review.SupplementalPurpose, ExpectedTarget: old.Review.ExpectedTarget}); err != nil {
		t.Fatal(err)
	}
	steps[0].State = "COMPLETED"
	steps[0].Effect = "COMMAND_COMPLETED"
	if _, err = store.RecordStep(f.ctx, f.session, legacy.IntentID, 1, steps[0], "CONFIRMING"); err != nil {
		t.Fatal(err)
	}
	_, startErr := f.facade.StartAdmittedReview(taskaction.WithIntent(f.ctx, legacy.IntentID), scopereview.AdmittedStartInput{AISessionID: f.session, RequestID: steps[1].RequestID, PayloadSHA256: c.ValueHash([]string{legacy.IntentID, "start"}), Review: review.Review{TaskID: f.task, FeatureAreaID: f.area, ProjectID: f.project, CreatedByAISessionID: f.session, ExecutorKind: review.ExecutorUpperLLM, WorkspaceID: old.Review.WorkspaceID, RepositoryTargets: old.Review.RepositoryTargets, ReviewDepth: task.ReviewDepthFast, Instruction: old.Variants[0].ReviewInstruction, EngineeringGoal: "Revise contracts", Focus: review.Focus{Kind: review.FocusRepository}, PrimaryMemberID: old.Variants[0].PrimaryMemberID}})
	var api *controlapi.Error
	if !errors.As(startErr, &api) || api.Code != "REVIEW_DEPTH_TOO_SHALLOW" {
		t.Fatalf("historical start not rejected at depth gate: %v", startErr)
	}
	steps[1].State = "UNKNOWN"
	steps[1].Effect = "UNKNOWN"
	steps[1].Failure = c.NewFailure("REVIEW_DEPTH_TOO_SHALLOW", "CORE", "BUSINESS_STEP", "REVIEW_DEPTH_TOO_SHALLOW", c.ReadOriginalReceipt, startErr)
	steps[1].Code = steps[1].Failure.Code
	steps[1].Message = steps[1].Failure.Message
	if _, err = store.RecordStep(f.ctx, f.session, legacy.IntentID, 1, steps[1], "PARTIAL"); err != nil {
		t.Fatal(err)
	}
	tables := []string{"tasks", "confirmation_intents", "task_supplemental_review_requests", "reviews", "runs"}
	before := definitionFormalSnapshot(t, f, tables)
	read, err := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: legacy.IntentID, Version: 1, Purpose: c.Review, ContentHash: legacy.ContentHash, RequestID: commit.RequestID})
	if err != nil || read.ReviewStartPreparation == nil || read.ReviewStartPreparation.Action != "START_REVIEW" || len(read.ReviewStartPreparation.AllowedReviewDepths) != 1 || read.ReviewStartPreparation.AllowedReviewDepths[0] != task.ReviewDepthDeep || read.CanResume || read.State != "PARTIAL" || read.Steps[1].State != "UNKNOWN" {
		t.Fatalf("missing read-only continuation: %+v %v", read, err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "guidance read")
	corrected := old
	nextBody := *old.Review
	corrected.Review = &nextBody
	corrected.Action = "START_REVIEW"
	corrected.RequestID = "correct-depth-start"
	corrected.Review.SupplementalPurpose = ""
	corrected.Review.ExpectedTarget = &read.ReviewStartPreparation.ExpectedTarget
	corrected.Review.ReviewDepth = task.ReviewDepthDeep
	corrected.SubmittedHash = ""
	next, err := f.facade.PrepareConfirmation(f.ctx, corrected)
	if err != nil {
		t.Fatal(err)
	}
	readBefore := definitionFormalSnapshot(t, f, tables)
	workspace, err := f.facade.DevelopmentWorkspaceForSession(f.ctx, f.session, f.area, f.task)
	if err != nil || workspace.ConfirmationFeed == nil || len(workspace.ConfirmationFeed.Items) == 0 || workspace.ConfirmationFeed.Items[0].IntentID != next.IntentID || workspace.ConfirmationFeed.Items[0].State != "PENDING" {
		t.Fatalf("current start hidden behind historical partial: %+v %v", workspace.ConfirmationFeed, err)
	}
	var partialPreserved, oldProposalRetired bool
	for _, item := range workspace.ConfirmationFeed.Items {
		if item.IntentID == legacy.IntentID && item.State == "PARTIAL" {
			partialPreserved = true
		}
		if item.IntentID == valid.IntentID && item.State == "SUPERSEDED" {
			oldProposalRetired = true
		}
	}
	if !partialPreserved || !oldProposalRetired {
		t.Fatalf("wrong historical read classification: %+v", workspace.ConfirmationFeed)
	}
	assertDefinitionFormalSnapshot(t, f, tables, readBefore, "prioritized feed read")
	started, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: next.IntentID, Version: 1, Purpose: c.Review, Action: next.Action, ContentHash: next.ContentHash, RequestID: "correct-depth-confirm", VariantID: "upper"})
	if err != nil || started.State != "COMPLETED" || len(started.Steps) != 1 || started.Steps[0].ReviewID == "" || started.Steps[0].RunID == "" || started.Steps[0].TaskID != f.task {
		t.Fatalf("corrected start stuck: %+v %v", started, err)
	}
	read, err = f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: legacy.IntentID, Version: 1, Purpose: c.Review, ContentHash: legacy.ContentHash})
	if err != nil || read.ReviewStartPreparation != nil || read.State != "PARTIAL" {
		t.Fatalf("old guidance survived new Review: %+v %v", read, err)
	}

	// Reconstruct the deployed v133 migration: the original shallow failure
	// and a new current proposal were both adopted as unresolved candidates.
	f.review, f.run = started.Steps[0].ReviewID, started.Steps[0].RunID
	f.access, err = f.facade.UpperReviewAccessForSession(f.ctx, f.session, f.review)
	if err != nil {
		t.Fatal(err)
	}
	submitLifecycleApprovalReady(t, f, "successor-report")
	pending, pendingCommit := prepareLifecycleReviewDecision(t, f, "REVISE", "successor-pending")
	var legacyRaw, pendingRaw string
	if err = f.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, legacy.IntentID).Scan(&legacyRaw); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, pending.IntentID).Scan(&pendingRaw); err != nil {
		t.Fatal(err)
	}
	rebuildTaskActionV133(t, f)
	var conflicts int
	if err = f.plane.DB.SQL().QueryRow(`SELECT conflict FROM task_action_heads WHERE task_id=?`, f.task).Scan(&conflicts); err != nil || conflicts != 1 {
		t.Fatalf("legacy conflict not reproduced: %d %v", conflicts, err)
	}
	// An arbitrary UNKNOWN is never released merely because a later Review
	// exists. The exact deterministic refusal and request link are mandatory.
	if _, err = f.plane.DB.SQL().Exec(`UPDATE confirmation_intents SET intent_json=json_set(intent_json,'$.steps[1].code','UNKNOWN_FAILURE') WHERE intent_id=?`, legacy.IntentID); err != nil {
		t.Fatal(err)
	}
	node, err := store.ReadTaskActionNode(f.ctx, f.project, f.task, f.session)
	if err != nil || node.State != "CONFLICT" || !node.Blocking {
		t.Fatalf("unproven historical effect released: %+v %v", node, err)
	}
	for _, candidate := range node.Candidates {
		if candidate.IntentID == pending.IntentID && candidate.Selectable {
			t.Fatal("pending proposal selectable over an unproven consumed sibling")
		}
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE confirmation_intents SET intent_json=? WHERE intent_id=?`, legacyRaw, legacy.IntentID); err != nil {
		t.Fatal(err)
	}
	beforeRead := definitionFormalSnapshot(t, f, []string{"confirmation_intents", "task_action_heads", "task_action_releases"})
	node, err = store.ReadTaskActionNode(f.ctx, f.project, f.task, f.session)
	if err != nil || node.State != "PENDING" || !node.Owned || node.IntentID != pending.IntentID {
		t.Fatalf("exact successor evidence did not expose unique original: %+v %v", node, err)
	}
	assertDefinitionFormalSnapshot(t, f, []string{"confirmation_intents", "task_action_heads", "task_action_releases"}, beforeRead, "historical conflict pure read")
	for range 2 {
		if err = sqlite.ApplyMigrations(f.ctx, f.plane.DB.SQL(), sqlite.DefaultMigrations()); err != nil {
			t.Fatal(err)
		}
	}
	var afterLegacy, afterPending, release string
	if err = f.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, legacy.IntentID).Scan(&afterLegacy); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, pending.IntentID).Scan(&afterPending); err != nil {
		t.Fatal(err)
	}
	if legacyRaw != afterLegacy || pendingRaw != afterPending {
		t.Fatal("repair rewrote historical PARTIAL or unconsumed frozen proposal")
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT reason FROM task_action_releases WHERE intent_id=?`, legacy.IntentID).Scan(&release); err != nil || release != "SUPPLEMENTAL_REVIEW_STARTED_BY_SUCCESSOR" {
		t.Fatalf("missing exact release proof: %q %v", release, err)
	}
	var content c.Intent
	if err = json.Unmarshal([]byte(afterPending), &content); err != nil || content.Commit != nil || content.ContentHash != pending.ContentHash {
		t.Fatal("migration consumed or replaced the pending user choice")
	}
	continued, err := f.facade.CommitConfirmation(f.ctx, pendingCommit)
	if err != nil || continued.State != "COMPLETED" {
		t.Fatalf("same original proposal cannot continue after repair: %+v %v", continued, err)
	}
}
