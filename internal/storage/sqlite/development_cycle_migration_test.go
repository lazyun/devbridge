// Historical validation of pre-v0.1 schema fixtures only; not the current migration contract.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDevelopmentCycleMigrationDoesNotFabricateOrReinterpretHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err = ApplyMigrations(ctx, raw, historicalMigrations()[:32]); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	for _, q := range []string{
		`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Historical project',?,?)`,
		`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('repo','p','Repo','/fixture-only',?,?)`,
		`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('session','p',?,?)`,
		`INSERT INTO feature_areas(id,project_id,title,problem_statement,status,created_at,updated_at) VALUES('fa','p','Area','Problem','DISCOVERING',?,?)`,
	} {
		if _, err = raw.ExecContext(ctx, q, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO reviews(id,project_id,feature_area_id,created_by_ai_session_id,repository_id,codex_config_id,base_commit,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at) VALUES('review_legacy','p','fa','session','repo','codex_config_default','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','REPOSITORY','Historical review','{"version":1,"paths":[]}','Understand capability','Inspect source',?)`, stamp); err != nil {
		t.Fatal(err)
	}

	// Seed the predecessor schema directly: current stores now require v36
	// columns. Retain nonempty Task/revision/Preparation history in this v33
	// no-reinterpretation regression as well as the Review history above.
	for _, q := range []string{
		`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('module','p','legacy','Legacy',?,?)`,
		`INSERT INTO feature_areas(id,project_id,title,problem_statement,status,created_at,updated_at) VALUES('fa_active','p','Development','Original problem','ACTIVE',?,?)`,
		`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at,feature_area_id) VALUES('historical-task','p','module','repo','codex_config_default','Original goal','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','PLANNING',?,?,'fa_active')`,
	} {
		if _, err = raw.ExecContext(ctx, q, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO task_definition_revisions(id,task_id,ordinal,payload_json,payload_sha256,created_at) VALUES('historical-revision','historical-task',1,'{"repositoryId":"repo","featureAreaId":"fa_active","projectId":"p","goal":"Original goal"}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `UPDATE tasks SET current_definition_revision_id='historical-revision' WHERE id='historical-task'`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO task_preparations(id,project_id,feature_area_id,created_by_ai_session_id,task_id,title,base_commit,work_spec_json,verification_spec_json,feature_area_review_findings,preparation_instruction,status,created_at,updated_at,task_definition_revision_id,ordinal,repository_id) VALUES('task_preparation_historical','p','fa_active','session','historical-task','Original preparation','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','{}','{}','Original findings','Original instruction','DRAFT',?,?,'historical-revision',1,'repo')`, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	// Snapshot every pre-existing table, including complete provenance/revision
	// columns. The migration may add new tables only; no old content may change.
	tables, err := raw.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != 'schema_migrations' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := tables.Err(); err != nil {
		t.Fatal(err)
	}
	tables.Close()
	snapshot := func() map[string]string {
		t.Helper()
		result := map[string]string{}
		for _, name := range names {
			rows, e := raw.Query(`SELECT * FROM "` + name + `" ORDER BY rowid`)
			if e != nil {
				t.Fatal(e)
			}
			columns, e := rows.Columns()
			if e != nil {
				t.Fatal(e)
			}
			values := [][]any{}
			for rows.Next() {
				row := make([]any, len(columns))
				ptrs := make([]any, len(columns))
				for i := range row {
					ptrs[i] = &row[i]
				}
				if e := rows.Scan(ptrs...); e != nil {
					t.Fatal(e)
				}
				values = append(values, row)
			}
			if e := rows.Err(); e != nil {
				t.Fatal(e)
			}
			rows.Close()
			b, e := json.Marshal(values)
			if e != nil {
				t.Fatal(e)
			}
			result[name] = string(b)
		}
		return result
	}
	before := snapshot()
	if err = ApplyMigrations(ctx, raw, historicalMigrations()[:33]); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("migration changed historical tables")
	}
	var count int
	if err = raw.QueryRow(`SELECT COUNT(*) FROM development_cycles WHERE project_id=?`, "p").Scan(&count); err != nil || count != 0 {
		t.Fatalf("fabricated selection: %d %v", count, err)
	}
}
