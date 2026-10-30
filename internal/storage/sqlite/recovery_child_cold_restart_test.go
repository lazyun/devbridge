package sqlite

import (
	"context"
	"testing"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

func TestRecoveryChildColdRestartPreservesExactPreparation(t *testing.T) {
	for _, stage := range []string{"admitted", "run-created", "observed", "bound", "thread-claimed", "thread-receipted", "turn-claimed", "turn-receipted"} {
		t.Run(stage, func(t *testing.T) {
			restarted := false
			runCanonicalRecoveryChildFixtureStages(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				if !restarted {
					t.Fatal("restart checkpoint not reached")
				}
				if record.State != "RUNNING" || child.ThreadID != "independent-child-thread" || child.TurnID != "independent-child-turn" || child.RuntimeEpoch != f.db.epoch {
					t.Fatalf("child did not continue: %+v %+v", record, child)
				}
				if _, independent, err := readRecoveryDiagnosticEvidenceTx(ctx, f.db.sql, record, false); err != nil || !independent {
					t.Fatalf("cross-epoch evidence invalid: independent=%v err=%v", independent, err)
				}
				for _, table := range []string{"recovery_child_admissions", "runtime_workset_bindings", "runtime_start_authorities", "recovery_child_thread_attempts", "recovery_child_thread_receipts", "recovery_child_turn_attempts", "recovery_child_turn_receipts"} {
					var count int
					if err := f.db.sql.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 1 {
						t.Fatalf("%s count=%d err=%v", table, count, err)
					}
				}
			}, func(at string, f *agentStorageFixture, record *rs.Record, child *run.Run) {
				if at != stage {
					return
				}
				restarted = true
				ctx := context.Background()
				original, err := NewRecoveryStatusStore(f.db).ReadRecoveryChildPreparation(ctx, record.ID)
				if err != nil {
					t.Fatal(err)
				}
				epoch := f.db.epoch
				if err = f.db.Close(); err != nil {
					t.Fatal(err)
				}
				f.db = openTestDB(t, f.root)
				reopened := f.db
				t.Cleanup(func() { _ = reopened.Close() })
				if _, readErr := NewRecoveryStatusStore(f.db).ReadRecoveryChildPreparation(ctx, record.ID); readErr == nil {
					t.Fatal("unadopted preparation accepted by new writer")
				}
				*record, err = NewRecoveryStatusStore(f.db).Adopt(ctx, record.ID)
				if err != nil || record.RuntimeEpoch == epoch {
					t.Fatalf("adopt: %+v %v", record, err)
				}
				restored, err := NewRecoveryStatusStore(f.db).ReadRecoveryChildPreparation(ctx, record.ID)
				if err != nil || original.PlanID != restored.PlanID || original.PayloadSHA256 != restored.PayloadSHA256 || original.SourceRunID != restored.SourceRunID {
					t.Fatalf("frozen preparation changed: before=%+v after=%+v err=%v", original, restored, err)
				}
				if child != nil {
					*child, err = NewRunStore(f.db).Get(ctx, child.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
			})
		})
	}
}

func TestRecoveryChildPreparationAdoptionRejectsSkippedEpoch(t *testing.T) {
	runCanonicalRecoveryChildFixtureStages(t, nil, func(at string, f *agentStorageFixture, record *rs.Record, child *run.Run) {
		if at != "admitted" {
			return
		}
		ctx := context.Background()
		originalEpoch := record.RuntimeEpoch
		plan, err := NewRecoveryStatusStore(f.db).ReadRecoveryChildPreparation(ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		reopen := func() {
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.db = openTestDB(t, f.root)
			reopened := f.db
			t.Cleanup(func() { _ = reopened.Close() })
		}
		reopen()
		*record, err = NewRecoveryStatusStore(f.db).Adopt(ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		previous := record.RuntimeEpoch
		reopen()
		tx, err := f.db.beginWrite(ctx)
		if err != nil {
			t.Fatal(err)
		}
		attempted := *record
		attempted.RuntimeEpoch = f.db.epoch
		if err = saveRecoveryTx(ctx, tx, attempted); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_child_preparation_adoptions(recovery_id,child_plan_id,previous_epoch,current_epoch,created_at) VALUES(?,?,?,?,?)`, record.ID, plan.PlanID, originalEpoch, f.db.epoch, formatTime(testNow)); err == nil {
			t.Fatal("adoption skipped the last exact owner epoch")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_child_preparation_adoptions(recovery_id,child_plan_id,previous_epoch,current_epoch,created_at) VALUES(?,?,?,?,?)`, record.ID, plan.PlanID+"-foreign", previous, f.db.epoch, formatTime(testNow)); err == nil {
			t.Fatal("adoption accepted foreign plan")
		}
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		*record, err = NewRecoveryStatusStore(f.db).Adopt(ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		valid, err := recoveryDiagnosticEpochChainTx(ctx, f.db.sql, record.ID, "", originalEpoch, record.RuntimeEpoch)
		if err != nil || !valid {
			t.Fatalf("legitimate chain lost: %v %v", valid, err)
		}
		var count int
		if err = f.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM recovery_child_preparation_adoptions WHERE recovery_id=?`, record.ID).Scan(&count); err != nil || count != 2 {
			t.Fatalf("failed ledger insert changed history: count=%d err=%v", count, err)
		}
	})
}
