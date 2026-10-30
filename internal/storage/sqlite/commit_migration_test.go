package sqlite

import (
	"context"
	"testing"
)

func TestV42ToV43AddsOnlyNullableCheckpointsAndReceipts(t *testing.T) {
	f, owner := newTaskOwnedFixture(t)
	db := restoreHistoricalMigrationFixture(t, f.db, 42)
	if e := ApplyMigrations(context.Background(), db.sql, historicalMigrations()); e != nil {
		t.Fatal(e)
	}
	var version, receipts int
	var untouched bool
	if e := db.sql.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); e != nil || version != 43 {
		t.Fatalf("%d %v", version, e)
	}
	if e := db.sql.QueryRow(`SELECT commit_sha IS NULL AND committed_at IS NULL AND merged_commit IS NULL AND merged_at IS NULL FROM tasks WHERE id=?`, owner.ID).Scan(&untouched); e != nil || !untouched {
		t.Fatalf("backfilled history: %v %v", untouched, e)
	}
	if e := db.sql.QueryRow(`SELECT (SELECT count(*) FROM task_commit_receipts)+(SELECT count(*) FROM feature_area_commit_receipts)`).Scan(&receipts); e != nil || receipts != 0 {
		t.Fatalf("inferred receipts: %d %v", receipts, e)
	}
	if e := ApplyMigrations(context.Background(), db.sql, historicalMigrations()); e != nil {
		t.Fatal(e)
	}
}
