package sqlite

import (
	"context"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"strings"
	"testing"
)

func TestTaskHoldPreservesEvidenceAndReplaysWithoutReapplying(t *testing.T) {
	f, owner, e := acceptedExecutionFixture(t, "-hold")
	e = terminalExecutionWithReport(t, f, e)
	ctx := aisession.WithCallerScope(context.Background(), "owned-caller")
	in := task.HoldInput{ProjectID: f.project, TaskID: owner.ID, FeatureAreaID: f.area, ExecutionID: e.ID, RevisionID: owner.CurrentDefinitionRevisionID, Action: "DEFER_TASK", RequestID: "hold-one", Actor: "session:" + f.session, Note: "验证未通过；保留结果，之后继续修订。"}
	before := loadTaskAcceptanceHistory(t, f, owner.ID)
	if _, err := f.store.ChangeTaskHold(ctx, in); err != nil {
		t.Fatal(err)
	}
	hold, err := f.store.ReadTaskHold(ctx, f.project, owner.ID)
	if err != nil || hold == nil || hold.Note != in.Note {
		t.Fatalf("missing checkpoint: %+v %v", hold, err)
	}
	after := loadTaskAcceptanceHistory(t, f, owner.ID)
	if len(after.Runs) != len(before.Runs) || len(after.Receipts) != len(before.Receipts) || after.Task.Phase != before.Task.Phase || after.Task.Status != task.StatusActive {
		t.Fatal("hold changed engineering evidence or lifecycle")
	}
	if _, err = f.db.sql.Exec(`UPDATE tasks SET phase='VERIFICATION_READY' WHERE id=?`, owner.ID); err == nil || !strings.Contains(err.Error(), "TASK_DEFERRED") {
		t.Fatalf("held task advanced: %v", err)
	}
	if _, err = f.store.ChangeTaskHold(ctx, in); err != nil {
		t.Fatal(err)
	}
	changed := in
	changed.Note = "different"
	if _, err = f.store.ChangeTaskHold(ctx, changed); err == nil {
		t.Fatal("same request accepted another note")
	}
	resume := in
	resume.Action = "CONTINUE_TASK"
	resume.RequestID = "continue-one"
	if _, err = f.store.ChangeTaskHold(ctx, resume); err != nil {
		t.Fatal(err)
	}
	if hold, err = f.store.ReadTaskHold(ctx, f.project, owner.ID); err != nil || hold != nil {
		t.Fatalf("checkpoint not released: %+v %v", hold, err)
	}
	if _, err = f.store.ChangeTaskHold(ctx, in); err != nil {
		t.Fatal(err)
	}
	if hold, _ = f.store.ReadTaskHold(ctx, f.project, owner.ID); hold != nil {
		t.Fatal("replayed old hold paused the Task again")
	}
	if continued, err := f.store.ReadTaskContinuation(ctx, f.project, owner.ID); err != nil || !continued {
		t.Fatalf("continuation not durable: %v %v", continued, err)
	}
	foreign := resume
	foreign.ProjectID = "foreign"
	foreign.RequestID = "foreign"
	if _, err = f.store.ChangeTaskHold(ctx, foreign); err == nil {
		t.Fatal("foreign project accepted")
	}
}

func TestVerificationRecoveryContinuationRequiresExplicitChoice(t *testing.T) {
	f, owner, e := acceptedExecutionFixture(t, "-explicit-continuation")
	terminalExecutionEvidence(t, f, e, run.VerificationCheckFailed, "VERIFICATION_MUTATED_WORKTREE")
	ctx := aisession.WithCallerScope(context.Background(), "owned-caller")
	before, err := f.store.TaskReviewRequestEligibility(ctx, f.session, owner.ID)
	if err != nil || before.Reason != "TASK_CONTINUATION_CONFIRMATION_REQUIRED" {
		t.Fatalf("unconfirmed recovery qualification %+v %v", before, err)
	}
	in := task.HoldInput{ProjectID: f.project, TaskID: owner.ID, FeatureAreaID: f.area, ExecutionID: e.ID, RevisionID: owner.CurrentDefinitionRevisionID, Action: "CONTINUE_TASK", RequestID: "continue-recovery", Actor: "session:" + f.session}
	if _, err = f.store.ChangeTaskHold(ctx, in); err != nil {
		t.Fatal(err)
	}
	after, err := f.store.TaskReviewRequestEligibility(ctx, f.session, owner.ID)
	if err != nil || after.Reason == "TASK_CONTINUATION_CONFIRMATION_REQUIRED" {
		t.Fatalf("confirmed recovery still stuck %+v %v", after, err)
	}
	// The explicit choice never fabricates a passing result or changes the Task.
	current, err := NewTaskStore(f.db).Get(ctx, owner.ID)
	if err != nil || current.Status != task.StatusActive || current.Phase != task.PhaseRecoveryRequired {
		t.Fatalf("continuation changed old evidence %+v %v", current, err)
	}
	in.Action = "DEFER_TASK"
	in.RequestID = "defer-recovery"
	in.Note = "先保留当前验证失败结果。"
	if _, err = f.store.ChangeTaskHold(ctx, in); err != nil {
		t.Fatal(err)
	}
	held, err := f.store.TaskReviewRequestEligibility(ctx, f.session, owner.ID)
	if err != nil || held.Reason != "TASK_DEFERRED" {
		t.Fatalf("held task can advance %+v %v", held, err)
	}
}
