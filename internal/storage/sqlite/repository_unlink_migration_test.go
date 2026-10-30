package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func migrationsThroughV84(t *testing.T) []Migration {
	t.Helper()
	all := DefaultMigrations()
	for i, migration := range all {
		if migration.Version == 84 {
			return all[:i+1]
		}
	}
	t.Fatal("v84 migration missing")
	return nil
}

func TestRepositoryUnlinkV84MigratesPopulatedV83AndIsRepeatable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v83.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	migrations := migrationsThroughV84(t)
	if err := ApplyMigrations(ctx, raw, migrations[:len(migrations)-1]); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','project','2026-09-17T00:00:00Z','2026-09-17T00:00:00Z'); INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('r','p','stable-alias','/historical/path','2026-09-17T00:00:00Z','2026-09-17T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := raw.QueryRowContext(ctx, `SELECT id||'|'||project_id||'|'||name||'|'||canonical_path||'|'||created_at||'|'||updated_at FROM repositories WHERE id='r'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, raw, migrations); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, raw, migrations); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var state string
	var generation, topology, receipts int64
	if err := raw.QueryRowContext(ctx, `SELECT registration_state,registration_generation FROM repositories WHERE id='r'`).Scan(&state, &generation); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT revision FROM project_repository_topologies WHERE project_id='p'`).Scan(&topology); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_repository_unlink_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := raw.QueryRowContext(ctx, `SELECT id||'|'||project_id||'|'||name||'|'||canonical_path||'|'||created_at||'|'||updated_at FROM repositories WHERE id='r'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if state != "ACTIVE" || generation != 1 || topology != 1 || receipts != 0 || before != after {
		t.Fatalf("migration state=%s generation=%d topology=%d receipts=%d history %q -> %q", state, generation, topology, receipts, before, after)
	}
	rows, err := raw.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v84 left a foreign-key violation")
	}
}

func TestRepositoryUnlinkV84FailureRollsBackAtomically(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rollback.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	migrations := migrationsThroughV84(t)
	if err := ApplyMigrations(ctx, raw, migrations[:len(migrations)-1]); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA foreign_keys=OFF; INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('orphan','missing','orphan','/orphan','2026-09-17T00:00:00Z','2026-09-17T00:00:00Z'); PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	err = ApplyMigrations(ctx, raw, migrations)
	if err == nil || !strings.Contains(err.Error(), "foreign key violation") {
		t.Fatalf("migration err=%v", err)
	}
	var version int
	if err := raw.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 83 {
		t.Fatalf("version=%d, want 83", version)
	}
	var columns int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('repositories') WHERE name='registration_state'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("failed migration leaked repository lifecycle columns")
	}
	var tables int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='project_repository_unlink_receipts'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("failed migration leaked receipt table")
	}
}
