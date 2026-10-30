package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

func TestRecoveryStatusCancellationScopePreservesSuccessorAdmission(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE execution_cancellations(task_id TEXT,run_id TEXT,state TEXT); INSERT INTO execution_cancellations VALUES('task','predecessor','CANCELED')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, tc := range []struct {
		run     string
		blocked bool
	}{{"predecessor", true}, {"successor", false}} {
		got, err := recoveryBlockedByCancellation(ctx, db, "task", tc.run)
		if err != nil || got != tc.blocked {
			t.Fatalf("%s blocked=%v err=%v", tc.run, got, err)
		}
	}
	if _, err = db.Exec(`UPDATE execution_cancellations SET state='RECOVERY_REQUIRED'`); err != nil {
		t.Fatal(err)
	}
	if got, err := recoveryBlockedByCancellation(ctx, db, "task", "successor"); err != nil || !got {
		t.Fatalf("unresolved task cancellation bypassed: %v %v", got, err)
	}
	if got, err := recoveryBlockedByCancellation(ctx, db, "other-task", "other-run"); err != nil || got {
		t.Fatalf("unrelated task blocked: %v %v", got, err)
	}
}
