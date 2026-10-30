package sqlite

import (
	"context"
	"reflect"
	"testing"
)

func TestSettledNoRunCancellationDoesNotBlockSuccessorDelivery(t *testing.T) {
	f, owner, predecessor := acceptedExecutionFixture(t, "-no-run-delivery")
	ctx := context.Background()
	readyNoRunSuccessor(t, f, owner, predecessor, "cancel-before-successor-delivery")
	before, err := NewExecutionCancellationStore(f.db).Get(ctx, owner.ProjectID, "cancel-before-successor-delivery")
	if err != nil || before.BackendDisposition != "NO_BACKEND" || before.BackendInterruptState != "NOT_SENT" || before.RunID != "" {
		t.Fatalf("no-backend receipt: %+v %v", before, err)
	}
	for _, cold := range []bool{false, true} {
		if cold {
			reopenPostOrphanFixture(t, f)
		}
		pending, err := hasPendingDeliveryCancellation(ctx, f.db.sql, owner.ID)
		if err != nil || pending {
			t.Fatalf("settled no-Run predecessor blocks successor delivery (cold=%v): pending=%v %v", cold, pending, err)
		}
		after, err := NewExecutionCancellationStore(f.db).Get(ctx, owner.ProjectID, before.RequestID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("history rewritten: %+v %v", after, err)
		}
	}
}

// Corruption checks ensure that a label alone cannot turn unknown runtime
// work into a settled cancellation. Each mutation is isolated and rolled back.
func TestNoRunDeliveryRequiresCompleteSettlementProof(t *testing.T) {
	f, owner, predecessor := acceptedExecutionFixture(t, "-no-run-delivery-proof")
	ctx := context.Background()
	readyNoRunSuccessor(t, f, owner, predecessor, "cancel-proof")
	cases := []struct{ name, query string }{
		{"task cancellation", `UPDATE execution_cancellations SET owner_type='TASK',owner_id=task_id WHERE request_id='cancel-proof'`},
		{"unresolved", `UPDATE execution_cancellations SET resolved_at=NULL WHERE request_id='cancel-proof'`},
		{"unknown backend", `UPDATE execution_cancellations SET backend_disposition='UNKNOWN_ORPHANED' WHERE request_id='cancel-proof'`},
		{"unknown interrupt", `UPDATE execution_cancellations SET interrupt_state='OUTCOME_UNKNOWN' WHERE request_id='cancel-proof'`},
		{"pending request", `UPDATE execution_cancellations SET state='RECOVERY_REQUIRED' WHERE request_id='cancel-proof'`},
		{"thread exists", `UPDATE execution_cancellations SET thread_id='unexpected-thread' WHERE request_id='cancel-proof'`},
		{"wrong execution", `UPDATE execution_cancellations SET execution_id=NULL WHERE request_id='cancel-proof'`},
		{"owner not canceled", `UPDATE execution_proposals SET outcome='PENDING' WHERE id=(SELECT owner_id FROM execution_cancellations WHERE request_id='cancel-proof')`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := f.db.sql.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name IN ('execution_cancellations','execution_proposals')`)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for rows.Next() {
				var name string
				if err = rows.Scan(&name); err != nil {
					t.Fatal(err)
				}
				names = append(names, name)
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			for _, name := range names {
				if _, err = tx.ExecContext(ctx, "DROP TRIGGER "+sqlIdentifier(name)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = tx.ExecContext(ctx, tc.query); err != nil {
				t.Fatal(err)
			}
			pending, err := hasPendingDeliveryCancellation(ctx, tx, owner.ID)
			if err == nil && !pending {
				t.Fatal("incomplete no-backend proof bypassed delivery fence")
			}
		})
	}
}
