package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestPendingRecoveryCancellationCanObserveTerminationAfterRestart(t *testing.T) {
	for _, state := range []string{"running", "adopted", "failed"} {
		t.Run(state, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				if state == "failed" {
					var err error
					record, err = store.Fail(ctx, record.ID, record.RuntimeEpoch, "diagnostic interrupted")
					if err != nil {
						t.Fatal(err)
					}
				}
				in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "pending-cancel-restart"}
				if _, _, dispatch, err := store.BeginCancel(ctx, record.ID, in); err != nil || !dispatch {
					t.Fatalf("initial cancellation: dispatch=%v %v", dispatch, err)
				}
				if _, err := store.RecordCancelInterrupt(ctx, record.ID, in, "OUTCOME_UNKNOWN"); err != nil {
					t.Fatal(err)
				}
				before, err := NewAgentConversationStore(f.db).Get(ctx, record.Spec.AgentConversationID)
				if err != nil {
					t.Fatal(err)
				}
				originalEpoch := f.db.epoch
				if err = f.db.Close(); err != nil {
					t.Fatal(err)
				}
				cold := openTestDB(t, f.root)
				defer cold.Close()
				store = NewRecoveryStatusStore(cold)
				ctx = context.Background()
				if state == "adopted" {
					record, err = store.Adopt(ctx, record.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, receipt, dispatch, err := store.BeginCancel(ctx, record.ID, in); err != nil || dispatch || receipt.State != "RECOVERY_REQUIRED" {
					t.Fatalf("cold replay resent interrupt or lost uncertainty: %+v dispatch=%v err=%v", receipt, dispatch, err)
				}
				admission := record.Admission
				admission.ObservedAt = time.Now().UTC()
				if _, _, err := store.FinishCancel(runtimeepoch.WithFence(ctx, originalEpoch, child.ID), record.ID, in, admission); !errors.Is(err, runtimeepoch.ErrFenced) {
					t.Fatalf("old callback became a new observer: %v", err)
				}
				for _, missing := range []string{"terminal", "idle", "pending", "quiescence", "freshness"} {
					bad := admission
					switch missing {
					case "terminal":
						bad.SourceTurnTerminal = false
					case "idle":
						bad.ThreadIdle = false
					case "pending":
						bad.NoPendingRequests = false
					case "quiescence":
						bad.Quiesced = false
					case "freshness":
						bad.ObservedAt = time.Now().Add(-2 * time.Minute)
					}
					if _, _, err := store.FinishCancel(ctx, record.ID, in, bad); !errors.Is(err, executioncancel.ErrOutcomeUnknown) {
						t.Fatalf("missing %s was not fenced: %v", missing, err)
					}
				}
				changed := in
				changed.Reason = "different request"
				if _, _, err := store.FinishCancel(ctx, record.ID, changed, admission); !errors.Is(err, executioncancel.ErrRequestConflict) {
					t.Fatalf("changed payload accepted: %v", err)
				}
				closed, receipt, err := store.FinishCancel(ctx, record.ID, in, admission)
				if err != nil || receipt.State != "CANCELED" || receipt.QuiescenceState != "QUIESCED" || receipt.BackendInterruptState != "OUTCOME_UNKNOWN" {
					t.Fatalf("cold exact observation failed: %+v %v", receipt, err)
				}
				if err = validateIndependentRecoveryCancellationTx(ctx, cold.sql, closed); err != nil {
					t.Fatal("source fence not released", err)
				}
				var epoch string
				if err = cold.sql.QueryRow(`SELECT runtime_epoch FROM execution_cancellations WHERE project_id=? AND request_id=?`, in.ProjectID, in.RequestID).Scan(&epoch); err != nil || epoch != originalEpoch {
					t.Fatalf("original interrupt epoch rewritten: %s %v", epoch, err)
				}
				after, err := NewAgentConversationStore(cold).Get(ctx, before.ID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("cancellation rewrote source conversation: %+v %v", after, err)
				}
			})
		})
	}
}
