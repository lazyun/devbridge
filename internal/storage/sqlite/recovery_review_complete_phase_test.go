package sqlite

import (
	"context"
	"errors"
	"testing"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

func TestReviewRecoveryCompletePhaseBoundary(t *testing.T) {
	for _, phase := range []string{"REVIEWING", "RECOVERY_REQUIRED", "IMPLEMENTING", "REVIEW_APPROVAL_PENDING"} {
		t.Run(phase, func(t *testing.T) {
			f := newReviewApprovalFixture(t)
			admission := f.start(t)
			ctx := context.Background()
			if _, err := f.db.sql.ExecContext(ctx, `UPDATE tasks SET phase=? WHERE id=?`, phase, admission.Review.TaskID); err != nil {
				t.Fatal(err)
			}
			tx, err := f.db.beginWrite(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = resolveReviewRecoveryCompleteTx(ctx, tx, &rs.Record{}, admission.Run, rs.FactualObservation{OwnerResult: []byte(approvedResult(t))}, f.now)
			if phase == "REVIEWING" || phase == "RECOVERY_REQUIRED" {
				if err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				phase = "REVIEW_APPROVAL_PENDING"
				if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions WHERE review_id=?`, admission.Review.ID); n != 1 {
					t.Fatalf("expected one recovered Decision, got %d", n)
				}
			} else {
				if !errors.Is(err, rs.ErrConflict) {
					t.Fatalf("unrelated phase accepted: %v", err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions WHERE review_id=?`, admission.Review.ID); n != 0 {
					t.Fatalf("rejected phase wrote Decision: %d", n)
				}
				if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materializations WHERE review_id=?`, admission.Review.ID); n != 0 {
					t.Fatalf("rejected phase wrote result: %d", n)
				}
			}
			var got string
			if err := f.db.sql.QueryRowContext(ctx, `SELECT phase FROM tasks WHERE id=?`, admission.Review.TaskID).Scan(&got); err != nil || got != phase {
				t.Fatalf("Task phase=%q want=%q error=%v", got, phase, err)
			}
		})
	}
}
