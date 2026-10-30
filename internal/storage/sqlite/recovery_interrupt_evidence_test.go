package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/executioncancel"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

func TestRecoveryCancellationKeepsInterruptOutcomeIndependentOfTerminalProof(t *testing.T) {
	for _, outcome := range []string{"REQUESTED", "ACKNOWLEDGED", "OUTCOME_UNKNOWN"} {
		t.Run(outcome, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "recovery-interrupt-evidence"}
				if _, _, dispatch, err := store.BeginCancel(ctx, record.ID, in); err != nil || !dispatch {
					t.Fatalf("begin cancel: %v %v", dispatch, err)
				}
				if outcome != "REQUESTED" {
					changed := in
					changed.Reason = "foreign payload"
					if _, err := store.RecordCancelInterrupt(ctx, record.ID, changed, outcome); err == nil {
						t.Fatal("mismatched cancellation payload recorded transport evidence")
					}
					if _, err := store.RecordCancelInterrupt(ctx, record.ID, in, outcome); err != nil {
						t.Fatal(err)
					}
				}
				if outcome == "OUTCOME_UNKNOWN" {
					var pending bool
					if err := f.db.sql.QueryRow(`SELECT `+unresolvedCancellationInterruptCSQL+` FROM execution_cancellations c WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID).Scan(&pending); err != nil || !pending {
						t.Fatalf("unobserved child escaped dispatch fence: %v %v", pending, err)
					}
				}
				closed, receipt, err := store.FinishCancel(ctx, record.ID, in, record.Admission)
				if err != nil || receipt.State != "CANCELED" || receipt.QuiescenceState != "QUIESCED" || receipt.BackendInterruptState != outcome {
					t.Fatalf("terminal proof rewrote interrupt outcome: %+v err=%v", receipt, err)
				}
				if err = validateIndependentRecoveryCancellationTx(ctx, f.db.sql, closed); err != nil {
					t.Fatal("exact terminal proof cannot release source fence", err)
				}
				var pending bool
				if err = f.db.sql.QueryRow(`SELECT `+unresolvedCancellationInterruptCSQL+` FROM execution_cancellations c WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID).Scan(&pending); err != nil || pending {
					t.Fatalf("settled recovery still blocks delivery dispatch: %v %v", pending, err)
				}
				for _, late := range []string{"ACKNOWLEDGED", "OUTCOME_UNKNOWN"} {
					got, err := store.RecordCancelInterrupt(ctx, record.ID, in, late)
					if err != nil || !reflect.DeepEqual(got, receipt) {
						t.Fatalf("late callback changed frozen receipt: %+v %v", got, err)
					}
				}
				if outcome == "OUTCOME_UNKNOWN" {
					for _, update := range []string{"json_remove(record_json,'$.resolution')", "json_set(record_json,'$.failureReason','OTHER_FAILURE')"} {
						tx, err := f.db.sql.BeginTx(ctx, nil)
						if err != nil {
							t.Fatal(err)
						}
						// Corrupt the frozen evidence only inside this rolled-back
						// transaction; ordinary writes are already fenced by triggers.
						rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name='recovery_status_records'`)
						if err != nil {
							tx.Rollback()
							t.Fatal(err)
						}
						var triggers []string
						for rows.Next() {
							var name string
							if err = rows.Scan(&name); err != nil {
								rows.Close()
								tx.Rollback()
								t.Fatal(err)
							}
							triggers = append(triggers, name)
						}
						if err = rows.Err(); err != nil {
							rows.Close()
							tx.Rollback()
							t.Fatal(err)
						}
						rows.Close()
						for _, name := range triggers {
							if _, err = tx.Exec(`DROP TRIGGER ` + sqlIdentifier(name)); err != nil {
								tx.Rollback()
								t.Fatal(err)
							}
						}
						if _, err = tx.Exec(`UPDATE recovery_status_records SET record_json=`+update+` WHERE id=?`, record.ID); err != nil {
							tx.Rollback()
							t.Fatal(err)
						}
						err = tx.QueryRow(`SELECT `+unresolvedCancellationInterruptCSQL+` FROM execution_cancellations c WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID).Scan(&pending)
						tx.Rollback()
						if err != nil || !pending {
							t.Fatalf("inexact recovery closure escaped dispatch fence: %v %v", pending, err)
						}
					}
				}
				if err = f.db.Close(); err != nil {
					t.Fatal(err)
				}
				cold := openTestDB(t, f.root)
				defer cold.Close()
				_, replay, dispatch, err := NewRecoveryStatusStore(cold).BeginCancel(ctx, record.ID, in)
				if err != nil || dispatch || !reflect.DeepEqual(receipt, replay) {
					t.Fatalf("cold replay changed interrupt history: %+v %v %v", replay, dispatch, err)
				}
				if err = validateIndependentRecoveryCancellationTx(ctx, cold.sql, closed); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
