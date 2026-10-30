package sqlite

import (
	"testing"
)

func TestOperatorStartV99UpgradeLeavesExistingConfirmationsUnchanged(t *testing.T) {
	store, ctx, intent := historicalCompletionFixture(t, 99)
	intent = finishConfirmation(t, store, ctx, intent)
	before := completionSnapshot(t, store)
	root := store.db.stateRoot
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		db, err := Open(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		store = NewConfirmationStore(db)
		if after := completionSnapshot(t, store); after != before {
			t.Fatal("migration rewrote existing confirmations")
		}
		var rows, versions int
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM operator_execution_start_commands`).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("historical starts were backfilled: %d %v", rows, err)
		}
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=100`).Scan(&versions); err != nil || versions != 1 {
			t.Fatalf("v100 markers=%d %v", versions, err)
		}
		read, err := store.Get(ctx, intent.AISessionID, intent.IntentID)
		if err != nil || read.State != intent.State {
			t.Fatalf("historical receipt changed: %s %v", read.State, err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
