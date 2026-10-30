package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskOwnedSchemaFreshAndEmptyV43Upgrade(t *testing.T) {
	for _, from := range []int{0, 43} {
		t.Run(map[int]string{0: "fresh", 43: "v43"}[from], func(t *testing.T) {
			ctx := context.Background()
			raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "schema.db")+"?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if from == 43 {
				if err = ApplyMigrations(ctx, raw, []Migration{{Version: 43, ForeignKeysOff: true, Apply: migrateFinalV43}}); err != nil {
					t.Fatal(err)
				}
				if _, err = raw.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('retained','Retained',?,?)`, formatTime(testNow), formatTime(testNow)); err != nil {
					t.Fatal(err)
				}
			}
			if err = ApplyMigrations(ctx, raw, DefaultMigrations()); err != nil {
				t.Fatal(err)
			}
			if err = ApplyMigrations(ctx, raw, DefaultMigrations()); err != nil {
				t.Fatal(err)
			}
			var version int
			if err = raw.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != DefaultMigrations()[len(DefaultMigrations())-1].Version {
				t.Fatalf("version=%d %v", version, err)
			}
			var shape string
			if err = raw.QueryRow(`SELECT sql FROM sqlite_master WHERE name='tasks'`).Scan(&shape); err != nil || !strings.Contains(shape, "REVIEW_REQUIRED") || strings.Contains(shape, "'REVIEW','INSPECTION'") {
				t.Fatalf("Task schema=%s %v", shape, err)
			}
			for _, name := range []string{"task_create_requests", "task_review_sources", "task_current_review_guard", "task_current_execution_guard"} {
				var n int
				if err = raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name=?`, name).Scan(&n); err != nil || n != 1 {
					t.Fatalf("missing %s %v", name, err)
				}
			}
			if from == 43 {
				var name string
				if err = raw.QueryRow(`SELECT name FROM projects WHERE id='retained'`).Scan(&name); err != nil || name != "Retained" {
					t.Fatal("lost project", err)
				}
			}
			rows, err := raw.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			if rows.Next() {
				t.Fatal("foreign key corruption")
			}
			rows.Close()
		})
	}
}

func TestTaskOwnedSchemaRejectsPopulatedV43WithoutGuessing(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "schema.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err = ApplyMigrations(ctx, raw, []Migration{{Version: 43, ForeignKeysOff: true, Apply: migrateFinalV43}}); err != nil {
		t.Fatal(err)
	}
	// A minimal corrupt development row suffices for the preflight count: the
	// migration must reject before it reads, guesses or rewrites any old lineage.
	if _, err = raw.Exec(`PRAGMA foreign_keys=OFF; PRAGMA ignore_check_constraints=ON; DROP TRIGGER tasks_current_insert; DROP TRIGGER approved_task_insert; INSERT INTO tasks(id,project_id,goal,constraints_json,base_commit,status,created_at,updated_at) VALUES('old','missing','old intent','[]','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','READY',?,?)`, formatTime(testNow), formatTime(testNow)); err != nil {
		t.Fatal(err)
	}
	if err = ApplyMigrations(ctx, raw, DefaultMigrations()); err == nil || !strings.Contains(err.Error(), "pre-Task workflow") {
		t.Fatalf("guessed old ownership: %v", err)
	}
	var version, count int
	raw.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version)
	raw.QueryRow(`SELECT count(*) FROM tasks WHERE id='old'`).Scan(&count)
	if version != 43 || count != 1 {
		t.Fatal("failed migration altered source")
	}
}
