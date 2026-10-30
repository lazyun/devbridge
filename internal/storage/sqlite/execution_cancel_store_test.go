package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
)

func cancellationStoreFixture(t *testing.T, label string, verification ...bool) (agentStorageFixture, run.Run, agentconversation.Conversation, executioncancel.Input) {
	t.Helper()
	f := newAgentStorageFixture(t, label)
	ctx := context.Background()
	if len(verification) > 0 && verification[0] {
		f.task.VerificationSpec = task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "fixture", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"tests pass"}, CodexInstruction: "Run fixture tests."}
	}
	id := seedExecutionOwner(t, f.db, f.task, f.session, "execution-"+label)
	proposals := NewExecutionProposalStore(f.db)
	_, r, err := proposals.ConsumeAndCreateRun(ctx, id, "run-"+label, testNow)
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, r.ID, run.StatusPreparing, "", "")
	conversations := NewAgentConversationStore(f.db)
	conversation, err := conversations.BeginRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	config, err := NewCodexConfigStore(f.db).Get(ctx, f.task.CodexConfigID)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversations.BindThreadWithRuntimeProvenance(ctx, r.ID, "thread-"+label, f.task.Model, "execution-cancellation-fixture", config.Selection("execution-cancellation-fixture")); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, r.ID, run.StatusRunning, "thread-"+label, "turn-"+label)
	if err := conversations.MarkTurnPending(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = NewRunStore(f.db).Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, r, conversation, executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, ExecutionID: r.ExecutionProposalID, RunID: r.ID, RequestID: "cancel-" + label, Reason: "Preserve partial work for inspection"}
}

func assertCancellationClaim(t *testing.T, f agentStorageFixture, r run.Run, held bool) {
	t.Helper()
	claim, err := NewAgentConversationStore(f.db).GetClaim(context.Background(), f.area.ID)
	if held && (err != nil || claim.RunID != r.ID || claim.OperationID != r.ExecutionProposalID || claim.RuntimeEpoch != r.RuntimeEpoch) {
		t.Fatalf("exact retained claim=%+v err=%v", claim, err)
	}
	if !held && !errors.Is(err, agentconversation.ErrNotFound) {
		t.Fatalf("claim should be released: %+v err=%v", claim, err)
	}
}

func dispatchCancellation(t *testing.T, f agentStorageFixture, in executioncancel.Input) executioncancel.Target {
	t.Helper()
	ctx := context.Background()
	store := NewExecutionCancellationStore(f.db)
	if receipt, dispatch, err := store.Begin(ctx, in, true); err != nil || !dispatch || receipt.State != "CANCEL_REQUESTED" {
		t.Fatalf("Begin=%+v dispatch=%t err=%v", receipt, dispatch, err)
	}
	target, claimed, err := store.ClaimInterrupt(ctx, in.ProjectID, in.RequestID)
	if err != nil || !claimed {
		t.Fatalf("ClaimInterrupt=%+v claimed=%t err=%v", target, claimed, err)
	}
	return target
}

func cancelWithSafeIntent(t *testing.T, f agentStorageFixture, r run.Run, target executioncancel.Target) error {
	t.Helper()
	ctx := context.Background()
	if err := NewAgentConversationStore(f.db).RecordFinalization(ctx, r.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: target.TurnID, Quiesced: true, Canceled: true}); err != nil {
		return err
	}
	_, err := run.NewService(NewRunStore(f.db), nil, nil).Fail(ctx, r.ID, run.Failure{Kind: run.FailureCanceled, Code: "EXECUTION_CANCELED", Message: "Exact backend Turn interrupted safely"})
	return err
}

func TestExecutionCancellationGenericTerminalWritersRequireExactProtocol(t *testing.T) {
	for _, state := range []string{"no-intent", "intent-only", "interrupt-ack", "normal-terminal", "unknown-terminal"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			f, r, _, in := cancellationStoreFixture(t, "generic-cancel-"+state)
			cancellations := NewExecutionCancellationStore(f.db)
			if state != "no-intent" {
				if _, _, err := cancellations.Begin(ctx, in, true); err != nil {
					t.Fatal(err)
				}
			}
			if state == "interrupt-ack" {
				if _, _, err := cancellations.ClaimInterrupt(ctx, in.ProjectID, in.RequestID); err != nil {
					t.Fatal(err)
				}
				if err := cancellations.RecordInterrupt(ctx, in.ProjectID, in.RequestID, "ACKNOWLEDGED"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "normal-terminal" || state == "unknown-terminal" {
				facts := agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: r.TurnID, Quiesced: true}
				if state == "unknown-terminal" {
					facts = agentconversation.Finalization{Advance: agentconversation.Unknown, Quiesced: false}
				}
				if err := NewAgentConversationStore(f.db).RecordFinalization(ctx, r.ID, facts); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := run.NewService(NewRunStore(f.db), nil, nil).Fail(ctx, r.ID, run.Failure{Kind: run.FailureCanceled, Message: "generic canceled context"}); !errors.Is(err, agentconversation.ErrUnsafe) {
				t.Fatalf("generic Run.Fail cancellation err=%v", err)
			}
			if _, err := NewRunStore(f.db).Update(ctx, r.ID, func(v *run.Run) error { v.Status = run.StatusCanceled; return nil }); !errors.Is(err, agentconversation.ErrUnsafe) {
				t.Fatalf("generic Run.Update cancellation err=%v", err)
			}
			if _, err := NewTaskStore(f.db).Update(ctx, f.task.ID, func(v *task.Task) error { v.Status = task.StatusCanceled; return nil }); !errors.Is(err, ErrConflict) {
				t.Fatalf("generic Task cancellation err=%v", err)
			}
			if _, err := NewExecutionProposalStore(f.db).Complete(ctx, r.ExecutionProposalID, executionproposal.OutcomeCanceled, "", "", testNow); !errors.Is(err, executionproposal.ErrConflict) {
				t.Fatalf("generic Execution cancellation err=%v", err)
			}
			current, _ := NewRunStore(f.db).Get(ctx, r.ID)
			currentTask, _ := NewTaskStore(f.db).Get(ctx, f.task.ID)
			currentExecution, _ := NewExecutionProposalStore(f.db).Get(ctx, r.ExecutionProposalID)
			if current.Status != run.StatusRunning || current.FinishedAt != nil || currentTask.Status != task.StatusActive || currentExecution.Outcome != executionproposal.OutcomeRunning {
				t.Fatalf("generic cancellation published terminal: run=%s task=%s execution=%s", current.Status, currentTask.Status, currentExecution.Outcome)
			}
			assertCancellationClaim(t, f, r, true)
		})
	}
}

func TestTaskCancellationCannotBypassSemanticTransition(t *testing.T) {
	ctx := context.Background()
	for _, activeRun := range []bool{false, true} {
		f := newAgentStorageFixture(t, "task-terminal-guard")
		if activeRun {
			op := prepareAgentOperation(t, f, agentconversation.Execution, "task-terminal")
			if _, err := op.make(ctx, "task-terminal-run"); err != nil {
				t.Fatal(err)
			}
		}
		before, err := NewTaskStore(f.db).Get(ctx, f.task.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range []task.Status{task.StatusCanceled, task.StatusSucceeded, task.StatusFailed, task.StatusRunning} {
			if _, err := NewTaskStore(f.db).Update(ctx, f.task.ID, func(v *task.Task) error { v.Status = status; return nil }); !errors.Is(err, ErrConflict) {
				t.Fatalf("generic terminal accepted: %s %v", status, err)
			}
		}
		after, _ := NewTaskStore(f.db).Get(ctx, f.task.ID)
		if after.Status != before.Status || after.Phase != before.Phase {
			t.Fatal("generic writer changed semantic authority")
		}
	}
}

func TestExecutionCancellationTerminalFailureRollsBackAtomicFinalization(t *testing.T) {
	ctx := context.Background()
	f, r, _, in := cancellationStoreFixture(t, "task-terminal-conflict")
	target := dispatchCancellation(t, f, in)
	if _, err := f.db.SQL().Exec(`CREATE TRIGGER cancel_terminal_fault BEFORE UPDATE ON runs WHEN NEW.status='CANCELED' BEGIN SELECT RAISE(ABORT,'injected terminal failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := cancelWithSafeIntent(t, f, r, target); err == nil {
		t.Fatal("partial cancellation committed")
	}
	receipt, err := NewExecutionCancellationStore(f.db).Get(ctx, in.ProjectID, in.RequestID)
	if err != nil || receipt.RunStatus != run.StatusRunning || receipt.ExecutionStatus != "RUNNING" || receipt.State != "CANCEL_REQUESTED" || receipt.ResolvedAt != nil {
		t.Fatalf("partial cancellation: %+v %v", receipt, err)
	}
	current, err := NewTaskStore(f.db).Get(ctx, f.task.ID)
	if err != nil || current.Status != task.StatusActive {
		t.Fatalf("Task changed: %+v %v", current, err)
	}
	assertCancellationClaim(t, f, r, true)
}

func TestExecutionCancellationStoreDurableReplayAndOneShotInterrupt(t *testing.T) {
	f, r, _, in := cancellationStoreFixture(t, "cancel-replay")
	ctx := context.Background()
	store := NewExecutionCancellationStore(f.db)
	first, dispatch, err := store.Begin(ctx, in, true)
	if err != nil || !dispatch || first.State != "CANCEL_REQUESTED" || first.BackendInterruptState != "NOT_SENT" || first.ResolvedAt != nil {
		t.Fatalf("first=%+v dispatch=%t err=%v", first, dispatch, err)
	}
	// Response loss replays the committed receipt even without a live runtime.
	replay, dispatch, err := store.Begin(ctx, in, false)
	if err != nil || dispatch || !reflect.DeepEqual(first, replay) {
		t.Fatalf("response-loss replay=%+v dispatch=%t err=%v", replay, dispatch, err)
	}
	freshSession := aisession.Session{ID: "fresh-cancel-session", ProjectID: f.project.ID, CreatedAt: testNow, UpdatedAt: testNow}
	seedBoundSession(t, f.db, freshSession)
	fresh := in
	fresh.AISessionID = freshSession.ID
	if _, _, err := store.Begin(ctx, fresh, false); !errors.Is(err, executioncancel.ErrRequestConflict) {
		t.Fatalf("different requester replay err=%v", err)
	}
	for _, change := range []func(*executioncancel.Input){func(v *executioncancel.Input) { v.Reason += " changed" }, func(v *executioncancel.Input) { v.RunID += "-other" }, func(v *executioncancel.Input) { v.ExecutionID += "-other" }} {
		wrong := in
		change(&wrong)
		if _, _, err := store.Begin(ctx, wrong, true); !errors.Is(err, executioncancel.ErrRequestConflict) {
			t.Fatalf("changed request payload err=%v", err)
		}
	}
	independent := in
	independent.RequestID += "-independent"
	if _, _, err := store.Begin(ctx, independent, true); !errors.Is(err, executioncancel.ErrRequestConflict) {
		t.Fatalf("duplicate logical cancellation err=%v", err)
	}
	target, claimed, err := store.ClaimInterrupt(ctx, in.ProjectID, in.RequestID)
	if err != nil || !claimed || target.RunID != r.ID || target.ThreadID != r.ThreadID || target.TurnID != r.TurnID || target.RuntimeEpoch != r.RuntimeEpoch {
		t.Fatalf("exact interrupt=%+v claimed=%t err=%v", target, claimed, err)
	}
	if _, claimed, err := store.ClaimInterrupt(ctx, in.ProjectID, in.RequestID); err != nil || claimed {
		t.Fatalf("duplicate interrupt claimed=%t err=%v", claimed, err)
	}
	if err := store.RecordInterrupt(ctx, in.ProjectID, in.RequestID, "ACKNOWLEDGED"); err != nil {
		t.Fatal(err)
	}
	ack, err := store.Get(ctx, in.ProjectID, in.RequestID)
	if err != nil || ack.State != "CANCEL_REQUESTED" || ack.RunStatus != run.StatusRunning || ack.ExecutionStatus != "RUNNING" || ack.QuiescenceState != "UNKNOWN" || ack.ResolvedAt != nil {
		t.Fatalf("ACK is not terminal: %+v err=%v", ack, err)
	}
	assertCancellationClaim(t, f, r, true)
}

func TestExecutionCancellationStoreRejectsExactTargetViolations(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		mutate func(*executioncancel.Input)
		live   bool
		want   error
	}{
		{"missing Run", func(v *executioncancel.Input) { v.RunID = "" }, true, executioncancel.ErrRunNotActive},
		{"stale Run", func(v *executioncancel.Input) { v.RunID = "missing-old-run" }, true, executioncancel.ErrRunNotActive},
		{"missing Execution", func(v *executioncancel.Input) { v.ExecutionID = "missing-execution" }, true, executioncancel.ErrExecutionNotRunning},
		{"wrong session Project", func(v *executioncancel.Input) { v.ProjectID = "foreign-project" }, true, executioncancel.ErrProjectMismatch},
		{"no live exact runtime", func(v *executioncancel.Input) {}, false, executioncancel.ErrRunNotActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r, _, in := cancellationStoreFixture(t, "target-"+safeTestID(tc.name))
			tc.mutate(&in)
			if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, in, tc.live); !errors.Is(err, tc.want) {
				t.Fatalf("Begin err=%v want=%v", err, tc.want)
			}
			assertCancellationClaim(t, f, r, true)
		})
	}
	t.Run("pending proposal has no Run", func(t *testing.T) {
		f := newAgentStorageFixture(t, "cancel-pending")
		op := prepareAgentOperation(t, f, agentconversation.Execution, "cancel-pending")
		in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, ExecutionID: op.id, RunID: "not-started", RequestID: "cancel-pending"}
		if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, in, true); !errors.Is(err, executioncancel.ErrExecutionNotRunning) {
			t.Fatalf("pending err=%v", err)
		}
	})
	for _, kind := range []agentconversation.OperationKind{agentconversation.Execution, agentconversation.Review} {
		t.Run("foreign owned "+string(kind), func(t *testing.T) {
			f, _, _, in := cancellationStoreFixture(t, "owner-"+string(kind))
			other := f.addAreaTask(t, "other-area-"+string(kind), "other-task-"+string(kind))
			foreign, _ := beginRecoveryRun(t, other, kind, "foreign-"+string(kind), true)
			in.RunID = foreign.ID
			if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, in, true); !errors.Is(err, executioncancel.ErrRunExecutionMismatch) {
				t.Fatalf("foreign %s err=%v", kind, err)
			}
		})
	}
	t.Run("foreign Project target", func(t *testing.T) {
		f, _, _, in := cancellationStoreFixture(t, "foreign-project")
		foreign := project.Project{ID: "other-project", Name: "Other", CreatedAt: testNow, UpdatedAt: testNow}
		if err := NewProjectStore(f.db).Create(ctx, foreign); err != nil {
			t.Fatal(err)
		}
		session := aisession.Session{ID: "foreign-session", ProjectID: foreign.ID, CreatedAt: testNow, UpdatedAt: testNow}
		seedBoundSession(t, f.db, session)
		in.ProjectID, in.AISessionID = foreign.ID, session.ID
		if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, in, true); !errors.Is(err, executioncancel.ErrProjectMismatch) {
			t.Fatalf("foreign Project err=%v", err)
		}
	})
}

func TestExecutionCancellationUpperReviewWithoutSupervisorTerminatesExactGrant(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		runStatus  run.Status
		grantState string
	}{
		{name: "active-running", runStatus: run.StatusRunning, grantState: "ACTIVE"},
		{name: "preparing-created", runStatus: run.StatusCreated, grantState: "PREPARING"},
		{name: "preparing-preparing", runStatus: run.StatusPreparing, grantState: "PREPARING"},
		{name: "preparing-running", runStatus: run.StatusRunning, grantState: "PREPARING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentStorageFixture(t, "upper-no-supervisor-cancel-"+tc.name)
			reviewID, runID := "review_upper_no_supervisor_"+safeTestID(tc.name), "run_upper_no_supervisor_"+safeTestID(tc.name)
			value := review.Review{ID: reviewID, ProjectID: f.project.ID, FeatureAreaID: f.area.ID, TaskID: f.task.ID, CreatedByAISessionID: f.session.ID, AuthorKind: string(review.AuthorAISession), AuthorID: f.session.ID, RepositoryID: f.task.RepositoryID, BaseCommit: f.task.BaseCommit, ReviewDepth: task.ReviewDepthFast, ExecutorKind: review.ExecutorUpperLLM, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "goal", Instruction: "review", CreatedAt: testNow}
			if err := NewReviewStore(f.db).Create(ctx, value); err != nil {
				t.Fatal(err)
			}
			if err := NewReviewGrantStore(f.db).Create(ctx, reviewgrant.Grant{ReviewID: reviewID, FeatureAreaID: f.area.ID, ProjectID: f.project.ID, RepositoryID: f.task.RepositoryID, Mode: "REPOSITORY", InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}, SelectedModules: []reviewgrant.ModuleSelection{}, CreatedAt: testNow}); err != nil {
				t.Fatal(err)
			}
			r, err := NewRunStore(f.db).CreateProductScopeReviewAttempt(ctx, reviewID, runID, "review", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{}, testNow)
			if err != nil {
				t.Fatal(err)
			}
			setAgentRunState(t, f.db, r.ID, tc.runStatus, "", "")
			if _, err := f.db.SQL().Exec(`UPDATE review_grants SET run_id=?,runtime_epoch=?,generation=1,state=?,baseline='' WHERE review_id=?`, r.ID, r.RuntimeEpoch, tc.grantState, reviewID); err != nil {
				t.Fatal(err)
			}
			in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: "REVIEW", OwnerID: reviewID, RunID: r.ID, RequestID: "cancel-upper-no-supervisor-" + tc.name, Reason: "withdraw review", ExpectedUpperVersion: &agentconversation.UpperVersion{AuthorityEpoch: f.db.epoch, GrantEpoch: r.RuntimeEpoch, GrantGeneration: 1}}
			receipt, dispatch, err := NewExecutionCancellationStore(f.db).Begin(ctx, in, false)
			if err != nil || dispatch || receipt.State != "CANCELED" || receipt.BackendDisposition != "NO_BACKEND" || receipt.RunStatus != run.StatusCanceled {
				t.Fatalf("receipt=%+v dispatch=%t err=%v", receipt, dispatch, err)
			}
			var grant, phase string
			var claims int
			if err := f.db.SQL().QueryRow(`SELECT state FROM review_grants WHERE review_id=?`, reviewID).Scan(&grant); err != nil {
				t.Fatal(err)
			}
			if err := f.db.SQL().QueryRow(`SELECT phase FROM tasks WHERE id=?`, f.task.ID).Scan(&phase); err != nil {
				t.Fatal(err)
			}
			if err := f.db.SQL().QueryRow(`SELECT COUNT(*) FROM feature_area_agent_claims WHERE run_id=?`, r.ID).Scan(&claims); err != nil {
				t.Fatal(err)
			}
			if grant != "CANCELED" || phase != string(task.PhaseReviewRequired) || claims != 0 {
				t.Fatalf("terminal Upper cancellation grant=%s task=%s claims=%d", grant, phase, claims)
			}
		})
	}
}

func TestExecutionCancellationStoreSafeTerminalIsAtomicAndKeepsConversation(t *testing.T) {
	f, r, original, in := cancellationStoreFixture(t, "cancel-safe")
	target := dispatchCancellation(t, f, in)
	if err := cancelWithSafeIntent(t, f, r, target); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	receipt, err := NewExecutionCancellationStore(f.db).Get(ctx, in.ProjectID, in.RequestID)
	if err != nil || receipt.State != "CANCELED" || receipt.RunStatus != run.StatusCanceled || receipt.ExecutionStatus != "CANCELED" || receipt.QuiescenceState != "QUIESCED" || receipt.RecoveryRequired || receipt.ResolvedAt == nil {
		t.Fatalf("safe receipt=%+v err=%v", receipt, err)
	}
	assertCancellationClaim(t, f, r, false)
	conversation, err := NewAgentConversationStore(f.db).Get(ctx, original.ID)
	if err != nil || conversation.EndedAt != nil || !conversation.ContinuationSafe || conversation.CurrentBoundaryTurnID != target.TurnID {
		t.Fatalf("safe cancellation reset conversation: %+v err=%v", conversation, err)
	}
	late := runtimeepoch.WithFence(ctx, r.RuntimeEpoch, r.ID)
	if _, err := run.NewService(NewRunStore(f.db), nil, nil).Succeed(late, r.ID, run.Result{}); err == nil {
		t.Fatal("late success replaced CANCELED")
	}
	lateInput := in
	lateInput.RequestID = "after-terminal"
	terminal, dispatch, err := NewExecutionCancellationStore(f.db).Begin(ctx, lateInput, false)
	if err != nil || dispatch || terminal.State != "ALREADY_TERMINAL" || terminal.RunStatus != run.StatusCanceled || terminal.ExecutionStatus != "CANCELED" {
		t.Fatalf("late cancellation=%+v dispatch=%t err=%v", terminal, dispatch, err)
	}
}

func TestExecutionCancellationStoreTerminalTransactionCrashWindows(t *testing.T) {
	for _, tc := range []struct{ name, trigger string }{
		{"boundary", `CREATE TRIGGER cancel_crash BEFORE UPDATE ON agent_operation_boundaries WHEN NEW.quiesced=1 BEGIN SELECT RAISE(ABORT,'cancel boundary crash'); END`},
		{"intent", `CREATE TRIGGER cancel_crash BEFORE UPDATE ON agent_finalization_intents WHEN NEW.applied_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'cancel intent crash'); END`},
		{"Execution", `CREATE TRIGGER cancel_crash BEFORE UPDATE ON execution_proposals WHEN NEW.outcome='CANCELED' BEGIN SELECT RAISE(ABORT,'cancel Execution crash'); END`},
		{"claim", `CREATE TRIGGER cancel_crash BEFORE DELETE ON feature_area_agent_claims BEGIN SELECT RAISE(ABORT,'cancel claim crash'); END`},
		{"receipt", `CREATE TRIGGER cancel_crash BEFORE UPDATE ON execution_cancellations WHEN NEW.state='CANCELED' BEGIN SELECT RAISE(ABORT,'cancel receipt crash'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r, _, in := cancellationStoreFixture(t, "cancel-crash-"+tc.name)
			target := dispatchCancellation(t, f, in)
			if _, err := f.db.SQL().Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}
			if err := cancelWithSafeIntent(t, f, r, target); err == nil {
				t.Fatal("terminal transaction committed through crash")
			}
			receipt, err := NewExecutionCancellationStore(f.db).Get(context.Background(), in.ProjectID, in.RequestID)
			if err != nil || receipt.State != "CANCEL_REQUESTED" || receipt.RunStatus != run.StatusRunning || receipt.ExecutionStatus != "RUNNING" || receipt.QuiescenceState != "UNKNOWN" {
				t.Fatalf("partially committed cancellation=%+v err=%v", receipt, err)
			}
			assertCancellationClaim(t, f, r, true)
			if _, err := f.db.SQL().Exec(`DROP TRIGGER cancel_crash`); err != nil {
				t.Fatal(err)
			}
			if err := cancelWithSafeIntent(t, f, r, target); err != nil {
				t.Fatalf("intent replay after rollback: %v", err)
			}
			assertCancellationClaim(t, f, r, false)
		})
	}
}

func TestExecutionCancellationStoreRestartPreservesKnownAndUnknownTruth(t *testing.T) {
	for _, safe := range []bool{true, false} {
		name := "unknown"
		if safe {
			name = "known"
		}
		t.Run(name, func(t *testing.T) {
			f, r, original, in := cancellationStoreFixture(t, "cancel-restart-"+name)
			target := dispatchCancellation(t, f, in)
			if safe {
				if err := NewAgentConversationStore(f.db).RecordFinalization(context.Background(), r.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: target.TurnID, Quiesced: true, Canceled: true}); err != nil {
					t.Fatal(err)
				}
			} else if err := NewExecutionCancellationStore(f.db).RecordInterrupt(context.Background(), in.ProjectID, in.RequestID, "OUTCOME_UNKNOWN"); err != nil {
				t.Fatal(err)
			}
			reopenAgentFixtureDB(t, &f)
			if err := NewRecoveryStore(f.db).RecoverAgentOperations(context.Background(), testNow.Add(6*time.Hour)); err != nil {
				t.Fatal(err)
			}
			receipt, err := NewExecutionCancellationStore(f.db).Get(context.Background(), in.ProjectID, in.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if safe && (receipt.State != "CANCELED" || receipt.RunStatus != run.StatusCanceled || receipt.ExecutionStatus != "CANCELED" || receipt.QuiescenceState != "QUIESCED" || receipt.RecoveryRequired) {
				t.Fatalf("known restart=%+v", receipt)
			}
			if !safe && (receipt.State != "CANCELED" || receipt.BackendDisposition != "UNKNOWN_ORPHANED" || receipt.RecoveryRequired || receipt.QuiescenceState != "UNKNOWN" || receipt.ResolvedAt == nil) {
				t.Fatalf("unknown restart=%+v", receipt)
			}
			assertCancellationClaim(t, f, r, false)
			conversation, err := NewAgentConversationStore(f.db).Get(context.Background(), original.ID)
			if err != nil || (conversation.EndedAt == nil) != safe || conversation.ContinuationSafe != safe {
				t.Fatalf("restart silently reset conversation=%+v err=%v", conversation, err)
			}
			if _, claimed, err := NewExecutionCancellationStore(f.db).ClaimInterrupt(context.Background(), in.ProjectID, in.RequestID); err != nil || claimed {
				t.Fatalf("restart sent duplicate interrupt: claimed=%t err=%v", claimed, err)
			}
		})
	}
}

func TestExecutionCancellationStoreNormalTerminalWinsWithoutRewritingHistory(t *testing.T) {
	f, r, _, in := cancellationStoreFixture(t, "normal-wins")
	ctx := context.Background()
	store := NewExecutionCancellationStore(f.db)
	if _, _, err := store.Begin(ctx, in, true); err != nil {
		t.Fatal(err)
	}
	if err := NewAgentConversationStore(f.db).RecordFinalization(ctx, r.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: r.TurnID, Quiesced: true}); err != nil {
		t.Fatal(err)
	}
	runs := run.NewService(NewRunStore(f.db), nil, nil)
	if _, err := runs.Transition(ctx, r.ID, run.StatusCollecting); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Succeed(ctx, r.ID, run.Result{}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecutionProposalStore(f.db).Complete(ctx, r.ExecutionProposalID, executionproposal.OutcomeSucceeded, "", "", testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Get(ctx, in.ProjectID, in.RequestID)
	if err != nil || receipt.State != "CANCEL_NOT_APPLIED" || receipt.RunStatus != run.StatusSucceeded || receipt.ExecutionStatus != "SUCCEEDED" {
		t.Fatalf("normal terminal winner=%+v err=%v", receipt, err)
	}
	assertCancellationClaim(t, f, r, false)
	late := in
	late.RequestID = "late-normal-terminal"
	terminal, dispatch, err := store.Begin(ctx, late, false)
	if err != nil || dispatch || terminal.State != "ALREADY_TERMINAL" || terminal.RunStatus != run.StatusSucceeded || terminal.ExecutionStatus != "SUCCEEDED" {
		t.Fatalf("late cancel rewrote normal history=%+v dispatch=%t err=%v", terminal, dispatch, err)
	}
}

func TestExecutionCancellationStoreVerificationIsExactActiveRun(t *testing.T) {
	f, implementation, original, in := cancellationStoreFixture(t, "verify-cancel", true)
	ctx := context.Background()
	conversations := NewAgentConversationStore(f.db)
	if err := conversations.RecordFinalization(ctx, implementation.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: implementation.TurnID, Quiesced: true}); err != nil {
		t.Fatal(err)
	}
	runs := run.NewService(NewRunStore(f.db), nil, nil)
	if _, err := runs.Transition(ctx, implementation.ID, run.StatusCollecting); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Succeed(ctx, implementation.ID, run.Result{}); err != nil {
		t.Fatal(err)
	}
	_, verification, err := NewExecutionProposalStore(f.db).ReserveVerificationRun(ctx, implementation.ExecutionProposalID, "run-active-verification", testNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, verification.ID, run.StatusPreparing, "", "")
	continued, err := conversations.BeginRun(ctx, verification.ID)
	if err != nil || continued.ID != original.ID {
		t.Fatalf("verification continuation=%+v err=%v", continued, err)
	}
	setAgentRunState(t, f.db, verification.ID, run.StatusRunning, implementation.ThreadID, "turn-verification")
	if err := conversations.MarkTurnPending(ctx, verification.ID); err != nil {
		t.Fatal(err)
	}
	verification, err = NewRunStore(f.db).Get(ctx, verification.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, in, true); !errors.Is(err, executioncancel.ErrRunNotActive) {
		t.Fatalf("old implementation Run cancellation err=%v", err)
	}
	in.RunID = verification.ID
	target := dispatchCancellation(t, f, in)
	if target.RunID != verification.ID || target.ThreadID != implementation.ThreadID || target.TurnID != "turn-verification" {
		t.Fatalf("verification interrupt target=%+v", target)
	}
	if err := cancelWithSafeIntent(t, f, verification, target); err != nil {
		t.Fatal(err)
	}
	receipt, err := NewExecutionCancellationStore(f.db).Get(ctx, in.ProjectID, in.RequestID)
	if err != nil || receipt.State != "CANCELED" || receipt.RunID != verification.ID || receipt.ExecutionStatus != "CANCELED" {
		t.Fatalf("verification cancellation=%+v err=%v", receipt, err)
	}
	assertCancellationClaim(t, f, verification, false)
	old, err := NewRunStore(f.db).Get(ctx, implementation.ID)
	if err != nil || old.Status != run.StatusSucceeded {
		t.Fatalf("verification cancel rewrote Implementation history=%+v err=%v", old, err)
	}
}

func TestExecutionCancellationStoreFencesClaimEpochAndUnsafeIntent(t *testing.T) {
	ctx := context.Background()
	t.Run("claim owner mismatch", func(t *testing.T) {
		f, _, _, in := cancellationStoreFixture(t, "claim-mismatch")
		if _, err := f.db.SQL().Exec(`UPDATE feature_area_agent_claims SET operation_id='stale-owner' WHERE run_id=?`, in.RunID); err == nil {
			t.Fatal("durable claim accepted an inexact Run owner")
		}
		// Simulate a missing claim in this isolated DB; cancellation must fail
		// closed even if a caller still believes its runtime is active.
		if _, err := f.db.SQL().Exec(`DELETE FROM feature_area_agent_claims WHERE run_id=?`, in.RunID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, in, true); !errors.Is(err, executioncancel.ErrClaimMismatch) {
			t.Fatalf("claim mismatch err=%v", err)
		}
	})
	t.Run("stale runtime epoch", func(t *testing.T) {
		f, _, _, in := cancellationStoreFixture(t, "epoch-mismatch")
		reopenAgentFixtureDB(t, &f)
		if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, in, true); !errors.Is(err, executioncancel.ErrRecoveryRequired) {
			t.Fatalf("epoch mismatch err=%v", err)
		}
	})
	t.Run("unknown finalization intent", func(t *testing.T) {
		f, r, _, in := cancellationStoreFixture(t, "unsafe-intent")
		if err := NewAgentConversationStore(f.db).RecordFinalization(ctx, r.ID, agentconversation.Finalization{Advance: agentconversation.Unknown, Quiesced: false}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, in, true); !errors.Is(err, executioncancel.ErrRecoveryRequired) {
			t.Fatalf("unknown intent err=%v", err)
		}
		assertCancellationClaim(t, f, r, true)
	})
}

func TestExecutionCancellationStoreIntentCommitPrecedesInterruptAndTerminalObservation(t *testing.T) {
	ctx := context.Background()
	f, r, _, in := cancellationStoreFixture(t, "intent-ordering")
	store := NewExecutionCancellationStore(f.db)
	if _, err := f.db.SQL().Exec(`CREATE TRIGGER cancellation_intent_crash AFTER INSERT ON execution_cancellations BEGIN SELECT RAISE(ABORT,'intent crash'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Begin(ctx, in, true); err == nil {
		t.Fatal("cancellation intent committed through injected failure")
	}
	if _, claimed, err := store.ClaimInterrupt(ctx, in.ProjectID, in.RequestID); !errors.Is(err, sql.ErrNoRows) || claimed {
		t.Fatalf("interrupt without durable intent claimed=%t err=%v", claimed, err)
	}
	assertCancellationClaim(t, f, r, true)
	if _, err := f.db.SQL().Exec(`DROP TRIGGER cancellation_intent_crash`); err != nil {
		t.Fatal(err)
	}
	target := dispatchCancellation(t, f, in)
	for _, facts := range []agentconversation.Finalization{
		{Canceled: true, Advance: agentconversation.Unknown, Quiesced: false},
		{Canceled: true, Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: target.TurnID, Quiesced: false},
		{Canceled: true, Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: "foreign-turn", Quiesced: true},
	} {
		if err := NewAgentConversationStore(f.db).RecordFinalization(ctx, r.ID, facts); err == nil {
			t.Fatalf("unsafe cancellation terminal accepted: %+v", facts)
		}
		var observed int
		if err := f.db.SQL().QueryRow(`SELECT terminal_observed FROM execution_cancellations WHERE run_id=?`, r.ID).Scan(&observed); err != nil || observed != 0 {
			t.Fatalf("partial terminal observation=%d err=%v", observed, err)
		}
		assertCancellationClaim(t, f, r, true)
	}
}

func TestExecutionCancellationStoreUnknownIntentResumesWithoutRedispatch(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint(restart), func(t *testing.T) {
			ctx := context.Background()
			f, r, _, in := cancellationStoreFixture(t, "unknown-resume-"+fmt.Sprint(restart))
			dispatchCancellation(t, f, in)
			store := NewExecutionCancellationStore(f.db)
			if err := store.RecordInterrupt(ctx, in.ProjectID, in.RequestID, "OUTCOME_UNKNOWN"); err != nil {
				t.Fatal(err)
			}
			if restart {
				reopenAgentFixtureDB(t, &f)
				if err := NewRecoveryStore(f.db).RecoverAgentOperations(ctx, testNow.Add(8*time.Hour)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := NewAgentConversationStore(f.db).RecordFinalization(ctx, r.ID, agentconversation.Finalization{Advance: agentconversation.Unknown}); err != nil {
					t.Fatal(err)
				}
				if err := store.FinishInterrupted(ctx, r.ID); err != nil {
					t.Fatal(err)
				}
			}
			store = NewExecutionCancellationStore(f.db)
			original, err := store.Get(ctx, in.ProjectID, in.RequestID)
			if err != nil || original.State != "CANCELED" || original.BackendDisposition != "UNKNOWN_ORPHANED" || original.QuiescenceState != "UNKNOWN" || original.RecoveryRequired {
				t.Fatalf("orphan=%+v %v", original, err)
			}
			if replay, dispatch, err := store.Begin(ctx, in, false); err != nil || dispatch || !reflect.DeepEqual(original, replay) {
				t.Fatalf("replay=%+v %v", replay, err)
			}
			if _, send, err := store.ClaimInterrupt(ctx, in.ProjectID, in.RequestID); err != nil || send {
				t.Fatalf("redispatch=%v %v", send, err)
			}
			assertCancellationClaim(t, f, r, false)
		})
	}
}

func TestExecutionCancellationStoreAutomaticOrphanResolvesReceiptWithoutInventingQuiescence(t *testing.T) {
	ctx := context.Background()
	f, r, _, in := cancellationStoreFixture(t, "cancel-explicit-recovery")
	dispatchCancellation(t, f, in)
	if err := NewExecutionCancellationStore(f.db).RecordInterrupt(ctx, in.ProjectID, in.RequestID, "OUTCOME_UNKNOWN"); err != nil {
		t.Fatal(err)
	}
	reopenAgentFixtureDB(t, &f)
	if err := NewRecoveryStore(f.db).RecoverAgentOperations(ctx, testNow.Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	conversations := NewAgentConversationStore(f.db)
	boundary, err := conversations.Boundary(ctx, r.ID)
	if err != nil || boundary.Advance != agentconversation.Unknown || boundary.Quiesced {
		t.Fatalf("old unsafe boundary=%+v err=%v", boundary, err)
	}
	before, err := NewExecutionCancellationStore(f.db).Get(ctx, in.ProjectID, in.RequestID)
	if err != nil || before.RecoveryRequired || before.State != "CANCELED" || before.BackendDisposition != "UNKNOWN_ORPHANED" || before.ResolvedAt == nil {
		t.Fatalf("before explicit recovery=%+v err=%v", before, err)
	}
	store := NewExecutionCancellationStore(f.db)
	after, err := store.Get(ctx, in.ProjectID, in.RequestID)
	if err != nil || !reflect.DeepEqual(before, after) || after.QuiescenceState != "UNKNOWN" {
		t.Fatalf("automatic orphan=%+v %v", after, err)
	}
	if replay, dispatch, err := store.Begin(ctx, in, false); err != nil || dispatch || !reflect.DeepEqual(replay, after) {
		t.Fatalf("post-recovery cancellation replay=%+v dispatch=%t err=%v", replay, dispatch, err)
	}
	preserved, err := conversations.Boundary(ctx, r.ID)
	if err != nil || !reflect.DeepEqual(preserved, boundary) {
		t.Fatalf("explicit recovery invented a safe boundary=%+v want=%+v err=%v", preserved, boundary, err)
	}
	assertCancellationClaim(t, f, r, false)
	reopenAgentFixtureDB(t, &f)
	if persisted, err := NewExecutionCancellationStore(f.db).Get(ctx, in.ProjectID, in.RequestID); err != nil || !reflect.DeepEqual(persisted, after) {
		t.Fatalf("durable recovered receipt=%+v want=%+v err=%v", persisted, after, err)
	}
}
