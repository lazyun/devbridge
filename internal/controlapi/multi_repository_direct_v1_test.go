package controlapi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestMultiRepositoryDirectG8V1FileResultCommitMergeCompatibility(t *testing.T) {
	f := newUpperLifecycleFixture(t)
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
	work := task.WorkSpec{ImplementationMode: task.ImplementationDirectFile, Goal: "legacy app update", Constraints: []string{"bounded"}, CodexInstruction: "update app", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "app repository", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true}}, AcceptanceCriteria: []string{"same.txt remains present"}, CodexInstruction: "Verify the app repository"}
	submission := review.SubmitInput{UpperTarget: access.Target, RequestID: "v1-report", Report: review.ReviewReport{SchemaVersion: 1, Summary: "legacy report", Disposition: review.DispositionApprovalReady, Coverage: []string{"same.txt"}, Findings: []review.Finding{}, Limitations: []string{"read only"}, RecommendedNextSteps: []string{"approve"}, EvidenceRefs: []review.EvidenceRef{{EvidenceID: read.EvidenceID, Path: "same.txt", Digest: read.Digest}}, WorkSpec: &work, VerificationSpec: &verification}}
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
	waived := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Explicit isolated v1 fixture user waiver"}}
	approval := commit(c.PrepareInput{RequestID: "v1-file-approval", Purpose: c.Execution, Action: "APPROVE_AND_START", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, taskID, areaID, reviewID, "APPROVE_AND_START"), Variants: []c.Variant{{ID: "files", Summary: "Explicit single repository file implementation with user waived command verification", ImplementationMode: task.ImplementationDirectFile, WorkSpec: &work, VerificationSpec: &waived}}})
	current, err := f.facade.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, taskID)
	if err != nil || state.State != "ACTIVE" {
		t.Fatalf("v1 grant %+v %v", state, err)
	}
	in := directfile.Input{Scope: directfile.Scope{AISessionID: f.session, FeatureAreaID: areaID, WorkflowID: state.WorkflowID}, Path: "same.txt", Content: "single-repo compatibility update\n"}
	if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	resultInput := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: taskID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "v1-file-result", Summary: "v1 file result", VerificationNotes: "explicit user waiver"}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, resultInput)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := f.facade.SubmitFileImplementationResult(f.ctx, resultInput); err != nil || replay.ID != result.ID {
		t.Fatalf("v1 result replay %v", err)
	}
	accept := directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: taskID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "v1-accept-files"}
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, accept)
	if err != nil || !accepted.Accepted || accepted.CommitError != "" {
		t.Fatalf("v1 acceptance %+v %v", accepted, err)
	}
	delivered, err := f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, taskID)
	if err != nil || delivered.CommitSHA == "" {
		t.Fatalf("v1 commit %+v %v", delivered, err)
	}
	merged, err := f.facade.CommitFeatureArea(f.ctx, f.session, areaID, "v1-merge-files")
	if err != nil || merged.MergedCommit == "" {
		t.Fatalf("v1 merge %+v %v", merged, err)
	}
	_ = approval
	revision, err := f.facade.TaskDefinitionRevisionForSession(f.ctx, f.session, current.CurrentDefinitionRevisionID)
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
