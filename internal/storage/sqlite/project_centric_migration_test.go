package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectCentricMigrationPreservesV72WorkspaceIdentityAndUnknownFacts(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "v72.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var legacy []Migration
	for _, m := range DefaultMigrations() {
		if m.Version <= 72 {
			legacy = append(legacy, m)
		}
	}
	if err = ApplyMigrations(ctx, raw, legacy); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Legacy','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`,
		`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('r','p','Source','/historical/source','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`,
		`INSERT INTO ai_sessions(id,caller_scope,created_at,updated_at) VALUES('s','fixture','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`,
		`UPDATE ai_sessions SET project_id='p' WHERE id='s'`,
	} {
		if _, err = raw.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	baseline := strings.Repeat("a", 40)
	for _, status := range []string{"READY", "CONTINUITY_UNAVAILABLE"} {
		if _, err = raw.ExecContext(ctx, `INSERT INTO feature_areas(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at) VALUES(?,'p','s','Historical area','Preserve identity','DISCOVERING','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`, "area-"+status); err != nil {
			t.Fatal(err)
		}
		if _, err = raw.ExecContext(ctx, `INSERT INTO workspaces(id,project_id,repository_id,feature_area_id,base_commit,status,token_baseline_available,created_at,updated_at) VALUES(?,'p','r',?,?,?,0,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`, "ws-"+status, "area-"+status, baseline, status); err != nil {
			t.Fatal(err)
		}
		if _, err = raw.ExecContext(ctx, `UPDATE feature_areas SET current_workspace_id=? WHERE id=?`, "ws-"+status, "area-"+status); err != nil {
			t.Fatal(err)
		}
	}
	const snapshotSQL = `SELECT group_concat(json_array(id,project_id,repository_id,feature_area_id,base_commit,worktree_id,status,created_at,updated_at),'|') FROM (SELECT * FROM workspaces ORDER BY id)`
	var before, after string
	if err = raw.QueryRowContext(ctx, snapshotSQL).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = ApplyMigrations(ctx, raw, DefaultMigrations()); err != nil {
		t.Fatal(err)
	}
	if err = ApplyMigrations(ctx, raw, DefaultMigrations()); err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRowContext(ctx, snapshotSQL).Scan(&after); err != nil || before != after {
		t.Fatalf("legacy identity changed: %v\n%s\n%s", err, before, after)
	}
	for status, want := range map[string]string{"READY": "PENDING", "CONTINUITY_UNAVAILABLE": "UNKNOWN"} {
		var id, repo, origin, preparation, failure string
		var version int
		if err = raw.QueryRowContext(ctx, `SELECT id,repository_id,identity_version,origin_commit,preparation,failure FROM workspace_members WHERE workspace_id=?`, "ws-"+status).Scan(&id, &repo, &version, &origin, &preparation, &failure); err != nil {
			t.Fatal(err)
		}
		if id != "member_ws-"+status || repo != "r" || version != 1 || origin != baseline || preparation != want {
			t.Fatalf("legacy projection %s %s %d %s %s", id, repo, version, origin, preparation)
		}
		if want == "UNKNOWN" && failure == "" {
			t.Fatal("unknown legacy availability lacks explicit reason")
		}
	}
}
