package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// deliveryTaskState reads the authoritative lifecycle columns directly; no
// projection or reconciler may repair a Task that diverged from its receipt.
func deliveryTaskState(t *testing.T, f *reviewApprovalFixture, taskID string) (task.Status, task.Phase) {
	t.Helper()
	var status, phase string
	if err := f.db.sql.QueryRow(`SELECT status,phase FROM tasks WHERE id=?`, taskID).Scan(&status, &phase); err != nil {
		t.Fatal(err)
	}
	return task.Status(status), task.Phase(phase)
}

func deliveryReceiptState(t *testing.T, f *reviewApprovalFixture, taskID string) (sql.NullString, string) {
	t.Helper()
	var completed sql.NullString
	var lastError string
	if err := f.db.sql.QueryRow(`SELECT completed_at,COALESCE(last_error,'') FROM task_commit_receipts WHERE task_id=?`, taskID).Scan(&completed, &lastError); err != nil {
		t.Fatal(err)
	}
	return completed, lastError
}

// T1: the acceptance transaction must durably admit delivery (Decision receipt
// + frozen receipt + Phase DELIVERING while Status stays ACTIVE) before any Git
// effect. A lost checkpoint write keeps the Task in DELIVERING and the exact
// receipt remains the only recovery authority.
func TestTaskDeliveryAdmissionKeepsTaskDeliveringUntilGitCheckpoint(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-delivering-admission")
	execution = terminalExecutionWithReport(t, f, execution)
	repo, source, base := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	if _, err := f.db.sql.Exec(`CREATE TRIGGER fail_commit_checkpoint BEFORE UPDATE OF commit_sha ON tasks BEGIN SELECT RAISE(ABORT,'simulated checkpoint response loss'); END`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	out, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "accept-delivering"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.CommitError, "simulated checkpoint response loss") {
		t.Fatalf("checkpoint fault did not surface: %+v", out)
	}
	if out.Task.CommitSHA != "" {
		t.Fatalf("checkpoint written despite injected loss: %+v", out.Task)
	}
	if status, phase := deliveryTaskState(t, f, owner.ID); status != task.StatusActive || phase != task.PhaseDelivering {
		t.Fatalf("delivery admission persisted %s/%s, want ACTIVE/DELIVERING", status, phase)
	}
	var decisions int
	if err = f.db.sql.QueryRow(`SELECT count(*) FROM decision_resolution_receipts WHERE decision_id=?`, wp.TaskAcceptanceDecisionID(execution.ID)).Scan(&decisions); err != nil || decisions != 1 {
		t.Fatalf("decision receipts=%d err=%v", decisions, err)
	}
	completed, lastError := deliveryReceiptState(t, f, owner.ID)
	if completed.Valid || lastError == "" {
		t.Fatalf("receipt completed=%+v lastError=%q", completed, lastError)
	}
	head := commitTestGit(t, source, "rev-parse", "HEAD")
	if head == base {
		t.Fatal("Git effect was skipped; the receipt must observe the real commit")
	}
	if got := commitTestGit(t, repo, "rev-parse", "HEAD"); got != base {
		t.Fatal("task delivery mutated the development reference")
	}
	if _, err = f.db.sql.Exec(`DROP TRIGGER fail_commit_checkpoint`); err != nil {
		t.Fatal(err)
	}
	committed, err := f.store.RetryTaskCommit(ctx, f.project, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Status != task.StatusSucceeded || committed.CommitSHA != head {
		t.Fatalf("completion transaction=%+v want commit %s", committed, head)
	}
	if committed.Phase != task.PhaseDelivering {
		t.Fatalf("terminal phase changed unexpectedly: %s", committed.Phase)
	}
	if completed, _ = deliveryReceiptState(t, f, owner.ID); !completed.Valid {
		t.Fatal("receipt remained incomplete after checkpoint")
	}
}

// T2: delivery admission is atomic. A receipt that cannot be created aborts the
// whole acceptance transaction; no Decision may be consumed and no Git effect
// may run while the Task still shows ACCEPTANCE_PENDING.
func TestTaskDeliveryAdmissionRollsBackDecisionAndReceiptAtomically(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-delivering-atomic")
	execution = terminalExecutionWithReport(t, f, execution)
	repo, source, base := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	if _, err := f.db.sql.Exec(`CREATE TRIGGER fail_receipt_insert BEFORE INSERT ON task_commit_receipts BEGIN SELECT RAISE(ABORT,'simulated receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "accept-aborted"})
	if err == nil || !strings.Contains(err.Error(), "simulated receipt failure") {
		t.Fatalf("admission did not roll back: %v", err)
	}
	if status, phase := deliveryTaskState(t, f, owner.ID); status != task.StatusActive || phase != task.PhaseAcceptancePending {
		t.Fatalf("aborted acceptance persisted %s/%s", status, phase)
	}
	var decisions, receipts int
	if err = f.db.sql.QueryRow(`SELECT count(*) FROM decision_resolution_receipts WHERE decision_id=?`, wp.TaskAcceptanceDecisionID(execution.ID)).Scan(&decisions); err != nil || decisions != 0 {
		t.Fatalf("decision leaked into aborted acceptance: %d %v", decisions, err)
	}
	if err = f.db.sql.QueryRow(`SELECT count(*) FROM task_commit_receipts WHERE task_id=?`, owner.ID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("receipt leaked into aborted acceptance: %d %v", receipts, err)
	}
	if got := commitTestGit(t, source, "rev-parse", "HEAD"); got != base {
		t.Fatal("Git ran for a rolled-back acceptance")
	}
	if got := commitTestGit(t, repo, "rev-parse", "HEAD"); got != base {
		t.Fatal("development reference moved for a rolled-back acceptance")
	}
	if _, err = f.db.sql.Exec(`DROP TRIGGER fail_receipt_insert`); err != nil {
		t.Fatal(err)
	}
	out, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "accept-recovered"})
	if err != nil || out.CommitError != "" {
		t.Fatalf("recovered acceptance=%+v err=%v", out, err)
	}
	if out.Task.Status != task.StatusSucceeded || out.Task.CommitSHA == "" || out.Task.Phase != task.PhaseDelivering {
		t.Fatalf("delivery completion=%+v want SUCCEEDED with checkpoint and DELIVERING phase", out.Task)
	}
	if completed, lastError := deliveryReceiptState(t, f, owner.ID); !completed.Valid || lastError != "" {
		t.Fatalf("receipt completed=%+v lastError=%q", completed, lastError)
	}
}

// T3: a confirmed snapshot drift is a hard delivery conflict. The Task leaves
// the DELIVERING fast path into RECOVERY_REQUIRED inside the authoritative
// transaction, and blind retry of the Git effect stays forbidden.
func TestTaskDeliveryConfirmedSnapshotDriftMarksRecoveryRequired(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-delivering-drift")
	execution = terminalExecutionWithReport(t, f, execution)
	_, source, base := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	commitTestWrite(t, filepath.Join(source, "drift.txt"), "later\n")
	ctx := context.Background()
	out, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "accept-drift"})
	if err != nil {
		t.Fatal(err)
	}
	if out.CommitError != git.ErrSnapshotDrift.Error() {
		t.Fatalf("drift not reported: %+v", out)
	}
	if status, phase := deliveryTaskState(t, f, owner.ID); status != task.StatusActive || phase != task.PhaseRecoveryRequired {
		t.Fatalf("drift persisted %s/%s, want ACTIVE/RECOVERY_REQUIRED", status, phase)
	}
	if got := commitTestGit(t, source, "rev-parse", "HEAD"); got != base {
		t.Fatal("drifted worktree was committed")
	}
	if completed, lastError := deliveryReceiptState(t, f, owner.ID); completed.Valid || lastError == "" {
		t.Fatalf("drift receipt completed=%+v lastError=%q", completed, lastError)
	}
	guidance, err := f.store.Read(ctx, f.project, f.area)
	if err != nil || guidance.CurrentState != "TASK_RECOVERY_REQUIRED" || guidance.NextAction.Code != "RESOLVE_TASK_RECOVERY" {
		t.Fatalf("drift guidance=%+v err=%v", guidance, err)
	}
	if _, err = f.store.RetryTaskCommit(ctx, f.project, owner.ID); err == nil || !strings.Contains(err.Error(), "TASK_NOT_ACCEPTED") {
		t.Fatalf("blind retry bypassed recovery: %v", err)
	}
	if status, phase := deliveryTaskState(t, f, owner.ID); status != task.StatusActive || phase != task.PhaseRecoveryRequired {
		t.Fatalf("retry mutated recovery state: %s/%s", status, phase)
	}
}
