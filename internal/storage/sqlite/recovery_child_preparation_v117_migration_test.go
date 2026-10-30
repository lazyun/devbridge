package sqlite

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
)

func TestRecoveryChildPreparationV117MigrationAtomicHistory(t *testing.T) {
	ctx := context.Background()
	db := openSelectionMigrationDB(t, migrationsThroughVersion(116))
	if err := NewCodexConfigStore(db).Create(ctx, codexconfig.Config{ID: "recovery-preparation-history", Name: "Retained", Enabled: true, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	tables := selectionMigrationTables(t, db)
	before := platformTableSnapshot(t, db, tables)
	if _, err := db.sql.Exec(`CREATE TABLE recovery_child_turn_attempts_failed_guard(id TEXT); CREATE TRIGGER recovery_child_turn_attempts_failed_guard BEFORE INSERT ON recovery_child_turn_attempts_failed_guard BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(117)); err == nil {
		t.Fatal("migration accepted conflicting guard")
	}
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN('recovery_child_preparation_failures','recovery_child_preparation_abandonments')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial schema survived %d %v", n, err)
	}
	if _, err := db.sql.Exec(`DROP TABLE recovery_child_turn_attempts_failed_guard`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(117)); err != nil {
			t.Fatal(err)
		}
	}
	if after := platformTableSnapshot(t, db, tables); after != before {
		t.Fatal("migration rewrote history")
	}
	if err := ensureRecoveryChildPreparationGuards(ctx, db.sql); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TRIGGER recovery_child_thread_attempts_failed_guard`); err != nil {
		t.Fatal(err)
	}
	if err := ensureRecoveryChildPreparationGuards(ctx, db.sql); err == nil {
		t.Fatal("missing dispatch fence accepted")
	}
	if _, err := db.sql.Exec(`CREATE TRIGGER recovery_child_thread_attempts_failed_guard BEFORE INSERT ON recovery_child_thread_attempts BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if err := ensureRecoveryChildPreparationGuards(ctx, db.sql); err == nil {
		t.Fatal("empty replacement dispatch guard accepted")
	}
	rows, err := db.sql.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("migration foreign keys invalid")
	}
}
