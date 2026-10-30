package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/task"
)

func TestRecoveryCanceledOwnerDoesNotReenterTaskRecovery(t *testing.T) {
	for _, mode := range []string{"live", "already-recovering", "historical-repair"} {
		t.Run(mode, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				if mode == "already-recovering" {
					if err := markTaskRecoveryRequiredTx(ctx, f.db.sql, source.ProjectID, source.TaskID, "fixture-existing-recovery", time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
				in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "cancel-recovery-lifecycle"}
				if _, _, dispatch, err := store.BeginCancel(ctx, record.ID, in); err != nil || !dispatch {
					t.Fatalf("begin cancellation: dispatch=%v err=%v", dispatch, err)
				}
				if _, _, err := store.FinishCancel(ctx, record.ID, in, record.Admission); err != nil {
					t.Fatal(err)
				}
				owner, err := NewTaskStore(f.db).Get(ctx, source.TaskID)
				if err != nil || owner.Phase == task.PhaseRecoveryRequired {
					t.Fatalf("cancellation did not close recovery phase: %s %v", owner.Phase, err)
				}
				if mode == "historical-repair" {
					if err := markTaskRecoveryRequiredTx(ctx, f.db.sql, source.ProjectID, source.TaskID, "fixture-old-cancellation", time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
				for _, cold := range []bool{false, true} {
					if cold {
						if err := f.db.Close(); err != nil {
							t.Fatal(err)
						}
						f.db = openTestDB(t, f.root)
						t.Cleanup(func() { f.db.Close() })
					}
					if err := NewRecoveryStore(f.db).RecoverTaskLifecycles(ctx, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
					owner, err := NewTaskStore(f.db).Get(ctx, source.TaskID)
					if err != nil || owner.Phase == task.PhaseRecoveryRequired {
						t.Fatalf("completed cancellation reentered task recovery (cold=%v): phase=%s err=%v", cold, owner.Phase, err)
					}
					tx, err := f.db.sql.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					predecessor, err := NewExecutionProposalStore(f.db).Get(ctx, source.OperationID)
					if err == nil {
						err = validateAcceptanceEligibilityTx(ctx, tx, owner, predecessor, false)
					}
					tx.Rollback()
					if err != nil {
						t.Fatalf("closed cancellation blocked owner continuation: %v", err)
					}
				}
			})
		})
	}
}

func TestRecoveryCancellationPhaseFailureRollsBack(t *testing.T) {
	runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
		if err := markTaskRecoveryRequiredTx(ctx, f.db.sql, source.ProjectID, source.TaskID, "fixture-cancel-atomicity", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "cancel-atomic-lifecycle"}
		before, receipt, dispatch, err := store.BeginCancel(ctx, record.ID, in)
		if err != nil || !dispatch {
			t.Fatalf("begin=%+v dispatch=%v err=%v", receipt, dispatch, err)
		}
		if _, err = f.db.sql.Exec(`CREATE TEMP TRIGGER cancel_phase_failure BEFORE UPDATE OF phase ON tasks WHEN NEW.phase='ACCEPTANCE_PENDING' BEGIN SELECT RAISE(ABORT,'injected cancellation phase failure'); END`); err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.FinishCancel(ctx, record.ID, in, record.Admission); err == nil {
			t.Fatal("injected phase failure did not abort cancellation")
		}
		after, err := store.Read(ctx, record.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("failed cancellation changed recovery record: %v", err)
		}
		got, err := NewExecutionCancellationStore(f.db).Get(ctx, in.ProjectID, in.RequestID)
		if err != nil || !reflect.DeepEqual(receipt, got) {
			t.Fatalf("failed cancellation changed receipt: %+v %v", got, err)
		}
		for _, original := range []run.Run{source, child} {
			current, err := NewRunStore(f.db).Get(ctx, original.ID)
			if err != nil || current.Status != original.Status {
				t.Fatalf("failed cancellation terminalized %s: %s %v", original.ID, current.Status, err)
			}
		}
		if _, err = f.db.sql.Exec(`DROP TRIGGER cancel_phase_failure`); err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.FinishCancel(ctx, record.ID, in, record.Admission); err != nil {
			t.Fatal(err)
		}
		owner, err := NewTaskStore(f.db).Get(ctx, source.TaskID)
		if err != nil || owner.Phase != task.PhaseAcceptancePending {
			t.Fatalf("exact retry failed to close recovery: %s %v", owner.Phase, err)
		}
	})
}

func TestRecoveryPreparationCancellationClosesTaskRecovery(t *testing.T) {
	atRecoveryPreparationObservation(t, func(f *agentStorageFixture, record rs.Record, failure runtimepreparation.FailureSettlement) {
		ctx := context.Background()
		if err := NewRunStore(f.db).SettleRecoveryPreparationFailure(ctx, record.ID, record.Spec.Generation, failure); err != nil {
			t.Fatal(err)
		}
		if err := markTaskRecoveryRequiredTx(ctx, f.db.sql, record.Spec.ProjectID, record.Spec.TaskID, "fixture-preparation-failed", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		in := executioncancel.Input{ProjectID: record.Spec.ProjectID, AISessionID: f.session.ID, OwnerType: record.Spec.OperationKind, OwnerID: record.Spec.OperationID, RunID: record.RecoveryRunID, RequestID: "cancel-preparation-lifecycle"}
		_, receipt, dispatch, err := NewRecoveryStatusStore(f.db).BeginCancel(ctx, record.ID, in)
		if err != nil || dispatch || receipt.BackendDisposition != "NO_BACKEND" {
			t.Fatalf("cancel preparation: %+v %v %v", receipt, dispatch, err)
		}
		if pending, err := hasPendingDeliveryCancellation(ctx, f.db.sql, record.Spec.TaskID); err != nil || pending {
			t.Fatalf("settled no-backend recovery cancellation blocks later delivery: %v %v", pending, err)
		}
		for range 2 {
			if err = NewRecoveryStore(f.db).RecoverTaskLifecycles(ctx, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			owner, err := NewTaskStore(f.db).Get(ctx, record.Spec.TaskID)
			if err != nil || owner.Phase != task.PhaseAcceptancePending {
				t.Fatalf("settled preparation left recovery stuck: %+v %v", owner, err)
			}
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.db = openTestDB(t, f.root)
		}
		defer f.db.Close()
	})
}

func TestRecoveryCancellationLifecycleKeepsUnprovenEvidenceFenced(t *testing.T) {
	for _, mutation := range []string{"not-canceled", "receipt", "child-not-terminal"} {
		t.Run(mutation, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "cancel-recovery-negative"}
				if _, _, _, err := store.BeginCancel(ctx, record.ID, in); err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.FinishCancel(ctx, record.ID, in, record.Admission); err != nil {
					t.Fatal(err)
				}
				var err error
				switch mutation {
				case "not-canceled":
					_, err = f.db.sql.Exec(`UPDATE recovery_status_records SET record_json=json_set(record_json,'$.failureReason','DIAGNOSTIC_FAILED') WHERE id=?`, record.ID)
				case "receipt":
					purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "execution_cancellations")
					_, err = f.db.sql.Exec(`UPDATE execution_cancellations SET result_json='{}' WHERE request_id=?`, in.RequestID)
				case "child-not-terminal":
					purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "runs")
					_, err = f.db.sql.Exec(`UPDATE runs SET status='RUNNING',finished_at=NULL WHERE id=?`, child.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = NewRecoveryStore(f.db).RecoverTaskLifecycles(ctx, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				owner, err := NewTaskStore(f.db).Get(ctx, source.TaskID)
				if err != nil || owner.Phase != task.PhaseRecoveryRequired {
					t.Fatalf("unproven cancellation escaped recovery: %s %v", owner.Phase, err)
				}
			})
		})
	}
}
