package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestAppServerContinuityMigrationV72V78V79PreservesHistory(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "history.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	through := func(version int) []Migration {
		var out []Migration
		for _, m := range DefaultMigrations() {
			if m.Version <= version {
				out = append(out, m)
			}
		}
		return out
	}
	if err = ApplyMigrations(ctx, raw, through(72)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects(id,name,created_at,updated_at) VALUES('historical-project','Original project','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`,
		`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('historical-repository','historical-project','Original repository','/historical/source','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`,
		`INSERT INTO runtime_epochs(id,created_at) VALUES('historical-epoch','2026-09-01T00:00:00Z')`,
	} {
		if _, err = raw.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		t.Helper()
		var value string
		if e := raw.QueryRowContext(ctx, `SELECT json_array(p.id,p.name,p.created_at,p.updated_at,r.id,r.project_id,r.name,r.canonical_path,r.created_at,r.updated_at,e.id,e.created_at) FROM projects p JOIN repositories r ON r.project_id=p.id CROSS JOIN runtime_epochs e WHERE p.id='historical-project' AND e.id='historical-epoch'`).Scan(&value); e != nil {
			t.Fatal(e)
		}
		return value
	}
	original := read()
	for _, version := range []int{78, 79, 79} {
		if err = ApplyMigrations(ctx, raw, through(version)); err != nil {
			t.Fatal(err)
		}
		if read() != original {
			t.Fatalf("v%d rewrote historical facts", version)
		}
	}
	for _, table := range []string{"app_server_run_bindings", "app_server_reattachments", "app_server_observations", "runtime_user_decisions"} {
		var n int
		if err = raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("invented %s history: %d %v", table, n, err)
		}
	}
	for _, name := range []string{"app_server_binding_immutable", "app_server_binding_no_delete", "app_server_reattachment_immutable", "app_server_reattachment_no_delete", "runs_runtime_epoch_immutable", "claims_runtime_epoch_immutable", "probe_work_binding_immutable", "agent_runtime_identity_immutable", "runtime_user_decisions_no_update", "runtime_user_decisions_no_delete"} {
		var n int
		if err = raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing constraint %s %v", name, err)
		}
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO app_server_run_bindings(run_id,binding_json,created_at) VALUES('invented-run','{}','2026-09-01T00:00:00Z')`); err == nil {
		t.Fatal("binding foreign key bypass")
	}
	rows, err := raw.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("historical FK corruption")
	}
}
