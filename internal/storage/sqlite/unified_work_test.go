package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
)

func unifiedOwnerFixture(t *testing.T, kind agentconversation.OperationKind) (agentStorageFixture, run.Run, executioncancel.Input) {
	t.Helper()
	if kind == agentconversation.Execution {
		f, r, _, in := cancellationStoreFixture(t, "unified-execution")
		return f, r, in
	}
	f := newAgentStorageFixture(t, "unified-"+string(kind))
	ctx := context.Background()
	op := prepareAgentOperation(t, f, kind, "unified-"+string(kind))
	r, err := op.make(ctx, "unified-run")
	if err != nil {
		t.Fatal(err)
	}
	value, err := NewTaskStore(f.db).Get(ctx, r.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if kind == agentconversation.Review && (value.Type != task.TypeDevelopment || value.CurrentDefinitionRevisionID != "" || value.CurrentReviewID != op.id) {
		t.Fatalf("Review envelope=%+v", value)
	}
	if kind == agentconversation.Execution && (value.ID != f.task.ID || value.Type != task.TypeDevelopment) {
		t.Fatalf("Plan split Task: %+v", value)
	}
	setAgentRunState(t, f.db, r.ID, run.StatusPreparing, "", "")
	c := NewAgentConversationStore(f.db)
	if _, err = c.BeginRun(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	config, err := NewCodexConfigStore(f.db).Get(ctx, f.task.CodexConfigID)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.BindThreadWithRuntimeProvenance(ctx, r.ID, "unified-thread", f.task.Model, "unified-fixture", config.Selection("agent-fixture")); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, r.ID, run.StatusRunning, "unified-thread", "unified-turn")
	if err = c.MarkTurnPending(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = NewRunStore(f.db).Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, r, executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(kind), OwnerID: op.id, RunID: r.ID, RequestID: "unified-cancel"}
}

func TestUnifiedCancellationOwnerMatrix(t *testing.T) {
	for _, kind := range []agentconversation.OperationKind{agentconversation.Review, agentconversation.Execution} {
		for _, unknown := range []bool{false, true} {
			t.Run(string(kind)+map[bool]string{false: "/known", true: "/orphan"}[unknown], func(t *testing.T) {
				ctx := context.Background()
				f, r, in := unifiedOwnerFixture(t, kind)
				store := NewExecutionCancellationStore(f.db)
				before, _ := NewTaskStore(f.db).Get(ctx, r.TaskID)
				target := dispatchCancellation(t, f, in)
				if _, again, err := store.ClaimInterrupt(ctx, in.ProjectID, in.RequestID); err != nil || again {
					t.Fatalf("duplicate interrupt: %v %v", again, err)
				}
				if err := store.RecordInterrupt(ctx, in.ProjectID, in.RequestID, "ACKNOWLEDGED"); err != nil {
					t.Fatal(err)
				}
				pending, _ := NewTaskStore(f.db).Get(ctx, r.TaskID)
				if pending.Work.CancellationState != "CANCEL_REQUESTED" {
					t.Fatalf("missing durable cancel projection: %+v", pending.Work)
				}
				if unknown {
					boundary, _ := NewAgentConversationStore(f.db).Boundary(ctx, r.ID)
					if err := store.FinishInterrupted(ctx, r.ID); err != nil {
						t.Fatal(err)
					}
					after, _ := NewAgentConversationStore(f.db).Boundary(ctx, r.ID)
					if !reflect.DeepEqual(boundary, after) || after.Quiesced {
						t.Fatalf("orphan rewrote UNKNOWN: %+v -> %+v", boundary, after)
					}
					if _, err := NewRunStore(f.db).Update(runtimeepoch.WithFence(ctx, r.RuntimeEpoch, r.ID), r.ID, func(v *run.Run) error { v.Status = run.StatusSucceeded; return nil }); !errors.Is(err, runtimeepoch.ErrFenced) {
						t.Fatalf("late writer: %v", err)
					}
				} else if err := cancelWithSafeIntent(t, f, r, target); err != nil {
					t.Fatal(err)
				}
				receipt, err := store.Get(ctx, in.ProjectID, in.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				if receipt.State != "CANCELED" || receipt.RecoveryRequired || receipt.ResolvedAt == nil {
					t.Fatalf("terminal receipt=%+v", receipt)
				}
				if unknown && (receipt.BackendDisposition != "UNKNOWN_ORPHANED" || receipt.QuiescenceState != "UNKNOWN") {
					t.Fatalf("invented death: %+v", receipt)
				}
				if !unknown && receipt.QuiescenceState != "QUIESCED" {
					t.Fatalf("lost known boundary: %+v", receipt)
				}
				value, err := NewTaskStore(f.db).Get(ctx, r.TaskID)
				expectedPhase := before.Phase
				// A canceled current Review reopens the same Task's review gate;
				// it does not alter the approved-definition fence (there is none
				// for a Review-only Task) or create a successor Task.
				if kind == agentconversation.Review {
					expectedPhase = task.PhaseReviewRequired
				}
				if err != nil || value.Status != task.StatusActive || value.Phase != expectedPhase || value.CurrentDefinitionRevisionID != before.CurrentDefinitionRevisionID {
					t.Fatalf("Task/revision=%+v %v", value, err)
				}
				if _, err := NewAgentConversationStore(f.db).GetClaim(ctx, f.area.ID); !errors.Is(err, agentconversation.ErrNotFound) {
					t.Fatalf("old claim: %v", err)
				}
				if _, again, err := store.Begin(ctx, in, false); err != nil || again {
					t.Fatalf("response-loss replay=%v %v", again, err)
				}
				in.OwnerID = "wrong-owner"
				in.ExecutionID = ""
				if _, _, err := store.Begin(ctx, in, false); !errors.Is(err, executioncancel.ErrRequestConflict) {
					t.Fatalf("changed replay: %v", err)
				}
			})
		}
	}
}

func TestUnifiedPreRunExecutionCancellation(t *testing.T) {
	for _, confirmed := range []bool{true} {
		t.Run(map[bool]string{false: "pending", true: "confirmed"}[confirmed], func(t *testing.T) {
			ctx := context.Background()
			f := newAgentStorageFixture(t, "pre-run")
			id := seedExecutionOwner(t, f.db, f.task, f.session, "pre-run-owner")
			// Task approval already authorizes this candidate.
			work, err := NewTaskStore(f.db).Get(ctx, f.task.ID)
			if err != nil || work.Work.OwnerType != "EXECUTION" || work.Work.OwnerID != id || work.Work.Phase != string(task.PhaseImplementationReady) || work.Work.RunID != "" {
				t.Fatalf("pre-run work projection=%+v %v", work.Work, err)
			}
			in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, ExecutionID: id, RequestID: "pre-run-cancel"}
			store := NewExecutionCancellationStore(f.db)
			value, dispatch, err := store.Begin(ctx, in, false)
			if err != nil || dispatch || value.State != "CANCELED" || value.RunID != "" || value.BackendDisposition != "NO_BACKEND" || value.BackendInterruptState != "NOT_SENT" {
				t.Fatalf("pre-run receipt=%+v %v %v", value, dispatch, err)
			}
			var count int
			if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM runs WHERE task_id=?`, f.task.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("unexpected Run beyond original Review: %d %v", count, err)
			}
			if _, _, err = NewExecutionProposalStore(f.db).ConsumeAndCreateRun(ctx, id, "late-run", testNow); err == nil {
				t.Fatal("canceled authorization consumed")
			}
			if _, err = NewExecutionProposalStore(f.db).Confirm(ctx, id, testNow); err == nil && !confirmed {
				t.Fatal("late Decision confirmed")
			}
			if _, dispatch, err = store.Begin(ctx, in, false); err != nil || dispatch {
				t.Fatalf("pre-run replay %v %v", dispatch, err)
			}
		})
	}
}

func TestUnifiedCancellationRestartOrphansWithoutResending(t *testing.T) {
	ctx := context.Background()
	f, r, in := unifiedOwnerFixture(t, agentconversation.Review)
	store := NewExecutionCancellationStore(f.db)
	dispatchCancellation(t, f, in)
	before, _ := NewAgentConversationStore(f.db).Boundary(ctx, r.ID)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := openTestDB(t, f.root)
	defer restarted.Close()
	if err := NewRecoveryStore(restarted).RecoverAgentOperations(ctx, testNow); err != nil {
		t.Fatal(err)
	}
	store = NewExecutionCancellationStore(restarted)
	value, err := store.Get(ctx, in.ProjectID, in.RequestID)
	if err != nil || value.BackendDisposition != "UNKNOWN_ORPHANED" || value.State != "CANCELED" {
		t.Fatalf("cold resume=%+v %v", value, err)
	}
	after, _ := NewAgentConversationStore(restarted).Boundary(ctx, r.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("restart rewrote UNKNOWN")
	}
	if err := NewRecoveryStore(restarted).RecoverAgentOperations(ctx, testNow); err != nil {
		t.Fatal(err)
	}
	if _, dispatch, err := store.ClaimInterrupt(ctx, in.ProjectID, in.RequestID); err != nil || dispatch {
		t.Fatalf("restart resent interrupt %v %v", dispatch, err)
	}
}

func TestUnifiedOrphanRollbackAndPermanentAuthority(t *testing.T) {
	for _, point := range []string{"terminal", "context", "disposition", "claim"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			f, r, in := unifiedOwnerFixture(t, agentconversation.Execution)
			dispatchCancellation(t, f, in)
			table, event, condition := "runs", "UPDATE", "NEW.status='CANCELED'"
			switch point {
			case "context":
				table, event, condition = "agent_conversations", "UPDATE", "NEW.ended_at IS NOT NULL"
			case "disposition":
				table, event, condition = "agent_orphan_dispositions", "INSERT", "1"
			case "claim":
				table, event, condition = "feature_area_agent_claims", "DELETE", "1"
			}
			if _, err := f.db.SQL().Exec(`CREATE TRIGGER unified_crash BEFORE ` + event + ` ON ` + table + ` WHEN ` + condition + ` BEGIN SELECT RAISE(ABORT,'injected orphan crash'); END`); err != nil {
				t.Fatal(err)
			}
			store := NewExecutionCancellationStore(f.db)
			if err := store.FinishInterrupted(ctx, r.ID); err == nil {
				t.Fatal("partial orphan committed")
			}
			current, err := NewTaskStore(f.db).Get(ctx, r.TaskID)
			if err != nil || current.Status == task.StatusCanceled {
				t.Fatalf("partial Task=%+v %v", current, err)
			}
			var count int
			if err = f.db.SQL().QueryRow(`SELECT COUNT(*) FROM agent_orphan_dispositions`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial fence=%d %v", count, err)
			}
			claim, err := NewAgentConversationStore(f.db).GetClaim(ctx, f.area.ID)
			if err != nil || claim.RunID != r.ID || claim.Kind != agentconversation.Execution || claim.OperationID != r.OperationID {
				t.Fatalf("partial claim=%+v %v", claim, err)
			}
			if _, err = f.db.SQL().Exec(`DROP TRIGGER unified_crash`); err != nil {
				t.Fatal(err)
			}
			if err = store.FinishInterrupted(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			oldCtx := runtimeepoch.WithFence(ctx, r.RuntimeEpoch, r.ID)
			if _, err = NewTaskStore(f.db).Update(oldCtx, r.TaskID, func(v *task.Task) error { v.Status = task.StatusSucceeded; return nil }); !errors.Is(err, runtimeepoch.ErrFenced) {
				t.Fatalf("old Task callback=%v", err)
			}
			if _, err = NewTaskStore(f.db).Update(ctx, r.TaskID, func(v *task.Task) error { v.Status = task.StatusSucceeded; return nil }); !errors.Is(err, ErrConflict) {
				t.Fatalf("operation completion bypassed Task Acceptance: %v", err)
			}
			if _, err = f.db.SQL().Exec(`UPDATE agent_orphan_dispositions SET disposition='UNKNOWN_ORPHANED' WHERE run_id=?`, r.ID); err == nil {
				t.Fatal("orphan fence mutable")
			}
			if err = store.FinishInterrupted(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnifiedReviewExecutionTerminalCancellationPreservesHistory(t *testing.T) {
	for _, kind := range []agentconversation.OperationKind{agentconversation.Review} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			f, r, in := unifiedOwnerFixture(t, kind)
			if err := NewAgentConversationStore(f.db).RecordFinalization(ctx, r.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: r.TurnID, Quiesced: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := run.NewService(NewRunStore(f.db), nil, nil).Fail(ctx, r.ID, run.Failure{Kind: run.FailureInfrastructure, Code: "fixture-terminal", Message: "fixture completed with failure"}); err != nil {
				t.Fatal(err)
			}

			before, err := NewRunStore(f.db).Get(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			receipt, dispatch, err := NewExecutionCancellationStore(f.db).Begin(ctx, in, false)
			if err != nil || dispatch || receipt.State != "ALREADY_TERMINAL" {
				t.Fatalf("terminal cancellation: %+v dispatch=%t err=%v", receipt, dispatch, err)
			}
			after, err := NewRunStore(f.db).Get(ctx, r.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("terminal history rewritten: %+v %v", after, err)
			}
		})
	}
}
