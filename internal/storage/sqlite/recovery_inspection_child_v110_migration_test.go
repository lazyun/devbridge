package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
)

func TestRecoveryInspectionChildV110MigrationPreservesHistoryAndRollsBack(t *testing.T) {
	ctx := context.Background()
	db := openSelectionMigrationDB(t, migrationsThroughVersion(109))
	if err := NewCodexConfigStore(db).Create(ctx, codexconfig.Config{ID: "inspection-v110-history", Name: "Retained backend", Enabled: true, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	tables := selectionMigrationTables(t, db)
	before := platformTableSnapshot(t, db, tables)
	var original string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='recovery_status_inspection_child_v83'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	// A broken predecessor must roll back even the owner triggers that have
	// already been rewritten, and must not record an applied migration.
	if _, err := db.sql.Exec(`DROP TRIGGER recovery_status_inspection_child_v83`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(110)); err == nil {
		t.Fatal("migration accepted a missing predecessor guard")
	}
	var ddl string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='runs_operation_owner_insert'`).Scan(&ddl); err != nil || strings.Contains(ddl, "independent_inspection_child") {
		t.Fatalf("partial trigger migration escaped rollback: %v", err)
	}
	var version int
	if err := db.sql.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 109 {
		t.Fatalf("failed migration version=%d err=%v", version, err)
	}
	if _, err := db.sql.Exec(original); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(110)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runs_operation_owner_insert", "runs_agent_execution_insert", "recovery_status_inspection_child_v83"} {
		if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&ddl); err != nil || !strings.Contains(ddl, "independent_inspection_child") {
			t.Fatalf("independent child guard %s: %v", name, err)
		}
	}
	if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(110)); err != nil {
		t.Fatal(err)
	}
	if after := platformTableSnapshot(t, db, tables); before != after {
		t.Fatal("Inspection child migration changed historical business facts")
	}
	rows, err := db.sql.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("Inspection migration broke foreign keys")
	}
}
