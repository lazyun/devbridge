package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestRuntimePreparationMigrationV90ColdReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "devbridge.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	if err := ApplyMigrations(ctx, db, migrationsThroughVersion(90)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	if err := ApplyMigrations(ctx, db, migrationsThroughVersion(90)); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 90 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	for _, table := range []string{"runtime_scope_plans", "runtime_preparation_authorities", "runtime_preparation_attempts", "runtime_workspace_preparation_claims", "runtime_workspace_observations", "runtime_workset_bindings", "runtime_start_authorities", "runtime_preparation_receipts"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing %s: %v", table, err)
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN (?,?)`, table+"_immutable_UPDATE", table+"_immutable_DELETE").Scan(&n); err != nil || n != 2 {
			t.Fatalf("missing immutable triggers for %s: count=%d err=%v", table, n, err)
		}
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
