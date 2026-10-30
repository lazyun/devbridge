package controlapi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestUpperV1FacadeReviewApprovalNoStartPreservesReportAndWorktree(t *testing.T) {
	testUpperV1ApprovalNoStart(t, false)
}

func TestUpperV1ConfirmationApprovalAcceptsExplicitCodingSelection(t *testing.T) {
	testUpperV1ApprovalNoStart(t, true)
}

func TestUpperV1ConfirmationApprovesAndStartsExactlyOnce(t *testing.T) {
	testUpperV1ApprovalNoStart(t, true, true)
}

func testUpperV1ApprovalNoStart(t *testing.T, viaConfirmation bool, start ...bool) {
	t.Helper()
	f := newUpperLifecycleFixture(t)
	autoStart := len(start) > 0 && start[0]
	f.plane.Retirement.Close()
	f.plane.Coordinator.Codex = preparationOnlyCodexFactory{}
	commit := func(in c.PrepareInput) c.Intent {
		t.Helper()
		in.AISessionID = f.session
		p, err := f.facade.PrepareConfirmation(f.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "v1-commit-" + in.RequestID, ContentHash: p.ContentHash, VariantID: in.Variants[0].ID})
		if err != nil || out.State != "COMPLETED" {
			t.Fatalf("commit %s: %+v %v", in.Action, out, err)
		}
		return out
	}
	area := commit(c.PrepareInput{RequestID: "v1-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Legacy compatibility", ProblemStatement: "Preserve the single-repository Upper Review protocol"}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	areaID := area.Steps[0].FeatureAreaID
	created := commit(c.PrepareInput{RequestID: "v1-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: areaID, RepositoryID: f.repos["app"], Title: "Legacy app review", ProblemStatement: "Review app source", Constraints: []string{"read only"}}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	taskID := created.Steps[0].TaskID
	started := commit(c.PrepareInput{RequestID: "v1-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: taskID, FeatureAreaID: areaID, RepositoryID: f.repos["app"], ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "review", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt"}}})
	reviewID, runID := started.Steps[0].ReviewID, started.Steps[0].RunID
	access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, reviewID)
	if err != nil || access.State != "ACTIVE" {
		t.Fatalf("access=%+v %v", access, err)
	}
	read, err := f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &access.Target, Path: "same.txt"})
	if err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "legacy app update", Constraints: []string{"bounded"}, CodexInstruction: "update app", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "app repository", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true}}, AcceptanceCriteria: []string{"same.txt remains present"}, CodexInstruction: "Verify the app repository"}
	submission := review.SubmitInput{UpperTarget: access.Target, RequestID: "v1-report", Report: review.ReviewReport{SchemaVersion: 1, Summary: "legacy report", Disposition: review.DispositionApprovalReady, Coverage: []string{"same.txt"}, Findings: []review.Finding{}, Limitations: []string{"read only"}, RecommendedNextSteps: []string{"approve"}, EvidenceRefs: []review.EvidenceRef{{EvidenceID: read.EvidenceID, Path: "same.txt", Digest: read.Digest}}, WorkSpec: &work, VerificationSpec: &verification}}
	// Reject v2 verification authority before finalizing the v1 report, so an
	// unapprovable proposal cannot close the grant or create a pending decision.
	for _, field := range []string{"repository", "repository-set", "directory"} {
		invalid := submission
		invalid.RequestID = "v1-invalid-" + field
		checks := task.CloneVerificationSpec(verification)
		switch field {
		case "repository":
			checks.Checks[0].RepositoryID = f.repos["backend"]
		case "repository-set":
			checks.Checks[0].RepositoryIDs = []string{f.repos["backend"]}
		case "directory":
			checks.Checks[0].WorkingDirectory = "other"
		}
		invalid.Report.VerificationSpec = &checks
		tables := definitionFormalEveryTable(t, f)
		before := definitionFormalSnapshot(t, f, tables)
		if _, err := f.facade.SubmitReviewResult(f.ctx, invalid); !errors.Is(err, review.ErrReportInvalid) {
			t.Fatalf("v1 accepted %s authority: %v", field, err)
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "rejected v1 verification authority")
	}
	receipt, err := f.facade.SubmitReviewResult(f.ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := f.facade.SubmitReviewResult(f.ctx, submission); err != nil || replay.ReportID != receipt.ReportID {
		t.Fatalf("report replay=%+v %v", replay, err)
	}
	var raw, reportPayloadHash, beforeWorktree string
	if err := f.plane.DB.SQL().QueryRow(`SELECT report_json,payload_sha256 FROM review_reports WHERE id=?`, receipt.ReportID).Scan(&raw, &reportPayloadHash); err != nil {
		t.Fatal(err)
	}
	if err := f.plane.DB.SQL().QueryRow(`SELECT worktree_id FROM runs WHERE id=?`, runID).Scan(&beforeWorktree); err != nil || beforeWorktree == "" {
		t.Fatalf("worktree=%q %v", beforeWorktree, err)
	}
	config, err := f.plane.CodexConfigs.Get(f.ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	backend := config.Selection("V1_COMPATIBILITY_TEST")
	runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	approvalInput := wp.ResolveInput{AISessionID: f.session, TaskID: taskID, FeatureAreaID: areaID, ReviewID: reviewID, DecisionID: wp.DecisionID(reviewID), Option: "APPROVE", RequestID: "v1-approve", BackendSelection: &backend, NextRunRuntimeConfig: &runtime, StartEffect: wp.StartEffectNoStart}
	var approved wp.Resolution
	if viaConfirmation {
		action := "APPROVE"
		if autoStart {
			action = "APPROVE_AND_START"
		}
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "v1-approve-prepare", Purpose: c.Execution, Action: action, Execution: exactReviewApprovalBody(t, f.ctx, f.facade, taskID, areaID, reviewID, action), Variants: []c.Variant{{ID: "approve", Summary: "Approve the scalar plan without starting implementation", ImplementationMode: task.ImplementationCodingAgent, WorkSpec: &work, VerificationSpec: &verification}}})
		if err != nil {
			t.Fatal(err)
		}
		options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
		if err != nil || !options.Eligibility.Available || len(options.BackendCandidates) == 0 {
			t.Fatalf("scalar approval options: %+v %v", options, err)
		}
		request := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, ContentHash: prepared.ContentHash, VariantID: "approve"}
		for _, kind := range []string{"missing", "wrong-backend"} {
			invalid := request
			invalid.RequestID = "v1-approve-" + kind
			if kind == "wrong-backend" {
				invalid.Selection = c.Selection{Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: "invalid"}, RuntimeConfig: &runtime}
			}
			rejected, err := f.facade.CommitConfirmation(f.ctx, invalid)
			if err == nil || rejected.Commit != nil || rejected.Submission == nil || rejected.Submission.Outcome != c.RejectedNotConsumed {
				t.Fatalf("scalar invalid selection consumed confirmation: %+v %v", rejected, err)
			}
		}
		request.RequestID = "v1-approve-confirm"
		request.Selection = c.Selection{Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}, RuntimeConfig: &runtime}
		committed, err := f.facade.CommitConfirmationAndStart(f.ctx, request)
		expectedSteps, expectedRuns := 1, 1
		if autoStart {
			expectedSteps, expectedRuns = 2, 2
		}
		if err != nil || committed.State != "COMPLETED" || committed.Commit == nil || committed.Commit.BackendSelection == nil || len(committed.Steps) != expectedSteps || committed.Steps[0].Kind != "APPROVE" {
			t.Fatalf("scalar confirmation rejected the advertised Coding selection: %+v %v", committed, err)
		}
		if replay, err := f.facade.CommitConfirmationAndStart(f.ctx, request); err != nil || c.ValueHash(replay.Steps) != c.ValueHash(committed.Steps) {
			t.Fatalf("confirmation replay: %+v %v", replay, err)
		}
		current, err := f.facade.GetTask(f.ctx, taskID)
		if err != nil || !autoStart && current.Phase != task.PhaseImplementationReady {
			t.Fatalf("approval-only Task: %+v %v", current, err)
		}
		var runs int
		if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=?`, taskID).Scan(&runs); err != nil || runs != expectedRuns {
			t.Fatalf("approval-only dispatched implementation: runs=%d err=%v", runs, err)
		}
		if autoStart && (committed.Steps[1].RunID == "" || committed.Steps[1].Effect != "RUNTIME_HANDOFF_ACCEPTED") {
			t.Fatalf("approval did not dispatch implementation: %+v", committed.Steps)
		}
		approved = wp.Resolution{Task: &current, StartEffect: wp.StartEffectNoStart}
	} else {
		approved, err = f.facade.ResolveReviewApproval(f.ctx, approvalInput)
	}
	if err != nil || approved.Task == nil || approved.Task.CurrentDefinitionRevisionID == "" || approved.StartEffect != wp.StartEffectNoStart {
		t.Fatalf("approval=%+v err=%v", approved, err)
	}
	if !viaConfirmation {
		replayed, err := f.facade.ResolveReviewApproval(f.ctx, approvalInput)
		if err != nil || replayed.Task == nil || replayed.Task.CurrentDefinitionRevisionID != approved.Task.CurrentDefinitionRevisionID {
			t.Fatalf("approval replay=%+v err=%v", replayed, err)
		}
	}
	revision, err := f.facade.TaskDefinitionRevisionForSession(f.ctx, f.session, approved.Task.CurrentDefinitionRevisionID)
	if err != nil || revision.Definition.Version != 1 || revision.Definition.RepositoryID != f.repos["app"] || len(revision.Definition.RepositoryTargets) != 0 {
		t.Fatalf("v1 revision=%+v err=%v", revision, err)
	}
	var revisionRaw, revisionHash string
	if err = f.plane.DB.SQL().QueryRow(`SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=?`, revision.ID).Scan(&revisionRaw, &revisionHash); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(revisionRaw))
	if revisionHash != hex.EncodeToString(sum[:]) || revision.PayloadSHA256 != revisionHash {
		t.Fatal("v1 revision hash mismatch")
	}
	var afterWorktree, rawAfter, hashAfter string
	if err := f.plane.DB.SQL().QueryRow(`SELECT worktree_id FROM runs WHERE id=?`, runID).Scan(&afterWorktree); err != nil || afterWorktree != beforeWorktree {
		t.Fatalf("worktree reuse=%q want=%q err=%v", afterWorktree, beforeWorktree, err)
	}
	if err := f.plane.DB.SQL().QueryRow(`SELECT report_json,payload_sha256 FROM review_reports WHERE id=?`, receipt.ReportID).Scan(&rawAfter, &hashAfter); err != nil || rawAfter != raw || hashAfter != reportPayloadHash {
		t.Fatalf("v1 report mutation err=%v", err)
	}
}
