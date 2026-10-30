package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestUpperReviewResultBlockedFinalizesAndReplays(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	f.candidate.ExecutorKind = review.ExecutorUpperLLM
	f.candidate.Instruction = "review"
	// Upper Reviews carry neither Coding backend selection nor runtime config.
	f.candidate.Model = ""
	f.candidate.ReasoningEffort = ""
	f.candidate.CodexConfigID = ""
	f.candidate.BackendSelection = nil
	a := f.start(t)
	reviewID, runID := a.Review.ID, a.Run.ID
	r := a.Run
	attachCommitWorkspace(t, f, f.candidate.TaskID, runID)
	var err error
	r, err = NewRunStore(f.db).Update(ctx, runID, func(v *run.Run) error {
		v.WorkspaceID = "git-workspace"
		v.WorktreeID = "commit-worktree"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, r.ID, run.StatusRunning, "", "")
	in := review.SubmitInput{UpperTarget: review.UpperTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.candidate.TaskID, ReviewID: reviewID, RunID: runID, RuntimeEpoch: r.RuntimeEpoch, GrantGeneration: 1}, RequestID: "request", Report: review.ReviewReport{SchemaVersion: 1, Summary: "blocked", Disposition: review.DispositionBlocked, Coverage: []string{"repository"}, Findings: []review.Finding{}, Limitations: []string{"missing dependency"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}}}
	if _, err := NewUpperReviewAccessStore(f.db).Prepare(ctx, in.UpperTarget); err != nil {
		t.Fatalf("prepare Upper Review access: %v", err)
	}
	store := NewUpperReviewResultStore(f.db)
	approval := in
	approval.RequestID = "approval-without-read"
	approval.Report = review.ReviewReport{SchemaVersion: 1, Summary: "ready", Disposition: review.DispositionApprovalReady, Coverage: []string{"repository"}, Findings: []review.Finding{}, Limitations: []string{"none"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}, WorkSpec: &task.WorkSpec{Goal: "implement", Constraints: []string{"bounded"}, CodexInstruction: "implement bounded change", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: []string{"internal"}}}, VerificationSpec: &task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "repository", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"tests pass"}, CodexInstruction: "run tests"}}
	if _, err := store.SubmitReviewResult(ctx, approval); !errors.Is(err, review.ErrEvidenceMismatch) {
		t.Fatalf("approval without read evidence err=%v", err)
	}
	if _, err := f.db.SQL().Exec(`CREATE TRIGGER upper_result_rollback BEFORE UPDATE OF state ON review_grants WHEN NEW.state='CLOSED' BEGIN SELECT RAISE(ABORT,'injected upper finalization failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitReviewResult(ctx, in); err == nil {
		t.Fatal("injected finalization failure committed")
	}
	var reports, receipts, claims int
	var runStatus, grantState string
	if err := f.db.SQL().QueryRow(`SELECT COUNT(*) FROM review_reports WHERE review_id=?`, reviewID).Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SQL().QueryRow(`SELECT COUNT(*) FROM review_result_receipts WHERE review_id=?`, reviewID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SQL().QueryRow(`SELECT COUNT(*) FROM feature_area_agent_claims WHERE run_id=?`, runID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SQL().QueryRow(`SELECT status FROM runs WHERE id=?`, runID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SQL().QueryRow(`SELECT state FROM review_grants WHERE review_id=?`, reviewID).Scan(&grantState); err != nil {
		t.Fatal(err)
	}
	if reports != 0 || receipts != 0 || claims != 1 || runStatus != string(run.StatusRunning) || grantState != "ACTIVE" {
		t.Fatalf("rollback reports=%d receipts=%d claims=%d run=%s grant=%s", reports, receipts, claims, runStatus, grantState)
	}
	if _, err := f.db.SQL().Exec(`DROP TRIGGER upper_result_rollback`); err != nil {
		t.Fatal(err)
	}
	// A completion row without its concrete immutable report receipt must
	// never become a durable but unreadable terminal state.
	receiptTx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = receiptTx.ExecContext(ctx, `INSERT INTO review_result_receipts(review_id,request_id,ai_session_id,payload_sha256,payload_json,state,created_at) VALUES(?,?,?,?,?,'FINALIZING',?)`, reviewID, "invalid-completion", f.session, "digest", "{}", formatTime(testNow)); err != nil {
		t.Fatal(err)
	}
	if _, err = receiptTx.ExecContext(ctx, `UPDATE review_result_receipts SET state='COMPLETED' WHERE review_id=?`, reviewID); err == nil {
		t.Fatal("incomplete completion receipt accepted")
	}
	if err = receiptTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	first, err := store.SubmitReviewResult(ctx, in)
	if err != nil {
		t.Fatalf("submit=%+v err=%v", first, err)
	}
	if first.Status != "COMPLETED" || first.Disposition != review.DispositionBlocked {
		t.Fatalf("submit=%+v err=%v", first, err)
	}
	replay, err := store.SubmitReviewResult(ctx, in)
	if err != nil || replay != first {
		t.Fatalf("replay=%+v want=%+v err=%v", replay, first, err)
	}
	changed := in
	changed.Report.Summary = "different payload"
	if _, err := store.SubmitReviewResult(ctx, changed); !errors.Is(err, review.ErrRequestConflict) {
		t.Fatalf("same request different payload err=%v", err)
	}
	report, err := store.GetReviewReportForSession(ctx, f.session, reviewID)
	if err != nil || report.Report.Summary != in.Report.Summary {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	foreign := aisession.Session{ID: "agent-session-upper-result-foreign", ProjectID: f.project, CreatedAt: testNow, UpdatedAt: testNow}
	seedBoundSession(t, f.db, foreign)
	if _, err := store.GetReviewReportForSession(ctx, foreign.ID, reviewID); err == nil {
		t.Fatal("foreign session read the report")
	}
	if _, err := store.GetReviewResultReceiptForSession(ctx, foreign.ID, reviewID, in.RequestID); err == nil {
		t.Fatal("foreign session read receipt")
	}
	second := in
	second.RequestID = "second-request"
	// A distinct request cannot target an already-finalized Review. Lifecycle
	// fencing is evaluated before the finalized-report lookup for new requests.
	if _, err := store.SubmitReviewResult(ctx, second); err != review.ErrTargetStale {
		t.Fatalf("second request err=%v", err)
	}
	updated, err := NewRunStore(f.db).Get(ctx, runID)
	if err != nil || updated.Status != run.StatusSucceeded {
		t.Fatalf("run=%+v err=%v", updated, err)
	}
	var phase, grant string
	if err := f.db.sql.QueryRow(`SELECT phase FROM tasks WHERE id=?`, f.candidate.TaskID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT state FROM review_grants WHERE review_id=?`, reviewID).Scan(&grant); err != nil {
		t.Fatal(err)
	}
	if phase != string(task.PhaseReviewRequired) || grant != "CLOSED" {
		t.Fatalf("phase=%s grant=%s", phase, grant)
	}
}
