package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
)

func TestCancellationTerminalReceiptIgnoresLateInterruptOutcome(t *testing.T) {
	for _, terminal := range []string{"canceled", "normal"} {
		for _, lost := range []bool{false, true} {
			t.Run(terminal+map[bool]string{false: "-ack", true: "-lost"}[lost], func(t *testing.T) {
				f, r, _, in := cancellationStoreFixture(t, "terminal-callback")
				ctx := context.Background()
				store := NewExecutionCancellationStore(f.db)
				target := dispatchCancellation(t, f, in)
				state := "ACKNOWLEDGED"
				if lost {
					state = "OUTCOME_UNKNOWN"
				}
				if err := store.RecordInterrupt(ctx, in.ProjectID, in.RequestID, state); err != nil {
					t.Fatal(err)
				}
				if pending, err := hasPendingDeliveryCancellation(ctx, f.db.sql, r.TaskID); err != nil || !pending {
					t.Fatalf("unobserved terminal escaped fence: pending=%v err=%v", pending, err)
				}
				if terminal == "canceled" {
					if err := cancelWithSafeIntent(t, f, r, target); err != nil {
						t.Fatal(err)
					}
				} else {
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
				}
				before, err := store.Get(ctx, in.ProjectID, in.RequestID)
				if err != nil || before.BackendDisposition != "TERMINATED" || before.ResolvedAt == nil {
					t.Fatalf("terminal receipt=%+v err=%v", before, err)
				}
				if pending, err := hasPendingDeliveryCancellation(ctx, f.db.sql, r.TaskID); err != nil || pending {
					t.Errorf("proven quiescent terminal still blocks delivery: pending=%v err=%v", pending, err)
				}
				if lost {
					for _, mutation := range []string{
						"state='RECOVERY_REQUIRED'",
						"resolved_at=NULL",
						"backend_disposition='UNKNOWN_ORPHANED'",
						"turn_id='foreign-turn'",
						"thread_id='foreign-thread'",
						"state='CANCELED',terminal_observed=0",
					} {
						tx, err := f.db.sql.BeginTx(ctx, nil)
						if err != nil {
							t.Fatal(err)
						}
						if _, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET `+mutation+` WHERE project_id=? AND request_id=?`, in.ProjectID, in.RequestID); err != nil {
							tx.Rollback()
							t.Fatal(err)
						}
						pending, err := hasPendingDeliveryCancellation(ctx, tx, r.TaskID)
						tx.Rollback()
						if err != nil || !pending {
							t.Errorf("%s escaped cancellation fence: pending=%v err=%v", mutation, pending, err)
						}
					}
				}
				for _, late := range []string{"OUTCOME_UNKNOWN", "ACKNOWLEDGED"} {
					if err = store.RecordInterrupt(ctx, in.ProjectID, in.RequestID, late); err != nil {
						t.Fatal(err)
					}
					after, e := store.Get(ctx, in.ProjectID, in.RequestID)
					if e != nil || !reflect.DeepEqual(before, after) {
						t.Errorf("late %s rewrote terminal receipt: before=%s after=%s err=%v", late, before.BackendInterruptState, after.BackendInterruptState, e)
					}
				}
			})
		}
	}
}
