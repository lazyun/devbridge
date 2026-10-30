package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func reviewDepthLegacyDB(t *testing.T, phase, status, runStatus string, approval bool) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "legacy.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ApplyMigrations(context.Background(), db, []Migration{{Version: 44, ForeignKeysOff: true, Apply: migrateFinalV44}}); err != nil {
		t.Fatal(err)
	}
	// Seed exact historical snapshots with table checks and foreign keys enabled.
	// Reinstall every historical trigger before invoking the migration.
	var triggers []schemaObject
	for _, object := range finalSchemaObjects() {
		if object.kind == "trigger" {
			triggers = append(triggers, object)
			if _, err := db.Exec("DROP TRIGGER " + sqlIdentifier(object.name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	queries := []string{
		`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Project','now','now')`,
		`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('repo','p','Repository','/fixture','now','now')`,
		`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('session','p','now','now')`,
		`INSERT INTO feature_areas(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at) VALUES('area','p','session','Area','Intent','ACTIVE','now','now')`,
		`INSERT INTO tasks(id,project_id,repository_id,codex_config_id,feature_area_id,title,goal,constraints_json,base_commit,status,phase,created_at,updated_at) VALUES('task','p','repo','codex_config_default','area','Semantic title','Goal','[]','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','` + status + `','` + phase + `','now','now')`,
	}
	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if runStatus != "" {
		instruction := " \n审查 Ω !\t\n "
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(instruction)))
		_, err = db.Exec(`INSERT INTO reviews(id,project_id,feature_area_id,created_by_ai_session_id,repository_id,codex_config_id,base_commit,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at,task_id,ordinal,final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at) VALUES('review_legacy','p','area','session','repo','codex_config_default','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','REPOSITORY','','{"version":1,"paths":[]}','Intent',?,'now','task',1,?,?,?,'now')`, instruction, instruction, hash, len(instruction))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO runs(id,task_id,attempt,status,created_at,updated_at,purpose,review_id,project_id,feature_area_id,operation_kind,operation_id,result_text) VALUES('run','task',1,?,'now','now','SCOPE_REVIEW','review_legacy','p','area','REVIEW','review_legacy',?)`, runStatus, `{"version":1,"summary":"Historical full review"}`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE reviews SET linked_run_id='run'; UPDATE tasks SET current_review_id='review_legacy'; INSERT INTO task_review_sources(task_id,review_id,development_ref,created_at) VALUES('task','review_legacy','refs/heads/dev','now')`); err != nil {
			t.Fatal(err)
		}
		if approval {
			if _, err := db.Exec(`INSERT INTO review_approval_decisions(decision_id,review_id,project_id,feature_area_id,task_id,start_request_id,provenance_json,provenance_sha256,result_sha256,result_text,created_at) VALUES('decision','review_legacy','p','area','task','request','{}',?,?,?,'now')`, strings.Repeat("b", 64), strings.Repeat("c", 64), `{"version":1,"summary":"Historical full review"}`); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, object := range triggers {
		if _, err := db.Exec(object.sql); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestReviewDepthV45MigrationPreservesQuiescentStateAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, phase, status, runStatus, required, disposition string
		approval                                              bool
	}{
		{name: "new gate", phase: "REVIEW_REQUIRED", status: "ACTIVE", required: "FAST"},
		{name: "revised gate", phase: "REVIEW_REQUIRED", status: "ACTIVE", runStatus: "SUCCEEDED", required: "FAST", disposition: "APPROVAL_READY", approval: true},
		{name: "infra retry", phase: "REVIEWING", status: "ACTIVE", runStatus: "INFRASTRUCTURE_FAILED", required: "DEEP"},
		{name: "canceled attempt", phase: "REVIEWING", status: "ACTIVE", runStatus: "CANCELED", required: "DEEP"},
		{name: "pending approval", phase: "REVIEW_APPROVAL_PENDING", status: "ACTIVE", runStatus: "SUCCEEDED", disposition: "APPROVAL_READY", approval: true},
		{name: "implementation", phase: "IMPLEMENTATION_READY", status: "ACTIVE", runStatus: "SUCCEEDED", disposition: "APPROVAL_READY", approval: true},
		{name: "terminal", phase: "REVIEWING", status: "CANCELED", runStatus: "CANCELED"},
		{name: "historical malformed result", phase: "REVIEW_REQUIRED", status: "ACTIVE", runStatus: "SUCCEEDED", required: "FAST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := reviewDepthLegacyDB(t, tc.phase, tc.status, tc.runStatus, tc.approval)
			var before string
			if tc.runStatus != "" {
				if err := db.QueryRow(`SELECT json_array(v.instruction,v.final_codex_prompt,v.prompt_sha256,v.prompt_bytes,r.result_text,v.linked_run_id,t.current_review_id,t.updated_at) FROM reviews v JOIN runs r ON r.id=v.linked_run_id JOIN tasks t ON t.id=v.task_id`).Scan(&before); err != nil {
					t.Fatal(err)
				}
			}
			if err := ApplyMigrations(context.Background(), db, DefaultMigrations()); err != nil {
				t.Fatal(err)
			}
			if err := ApplyMigrations(context.Background(), db, DefaultMigrations()); err != nil {
				t.Fatal(err)
			}
			var phase, status, required string
			if err := db.QueryRow(`SELECT phase,status,COALESCE(required_review_depth,'') FROM tasks WHERE id='task'`).Scan(&phase, &status, &required); err != nil || phase != tc.phase || status != tc.status || required != tc.required {
				t.Fatalf("state=%s/%s/%s err=%v", phase, status, required, err)
			}
			var version, reviews, runs, decisions int
			db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version)
			db.QueryRow(`SELECT count(*) FROM reviews`).Scan(&reviews)
			db.QueryRow(`SELECT count(*) FROM runs`).Scan(&runs)
			db.QueryRow(`SELECT count(*) FROM review_approval_decisions`).Scan(&decisions)
			wantCount, wantDecisions := 0, 0
			if tc.runStatus != "" {
				wantCount = 1
			}
			if tc.approval {
				wantDecisions = 1
			}
			if version != DefaultMigrations()[len(DefaultMigrations())-1].Version || reviews != wantCount || runs != wantCount || decisions != wantDecisions {
				t.Fatalf("migration changed cardinality: version=%d reviews=%d runs=%d decisions=%d", version, reviews, runs, decisions)
			}
			if tc.runStatus != "" {
				var depth, disposition, after string
				if err := db.QueryRow(`SELECT review_depth,COALESCE(review_disposition,'') FROM reviews`).Scan(&depth, &disposition); err != nil || depth != "DEEP" || disposition != tc.disposition {
					t.Fatalf("depth=%s disposition=%s err=%v", depth, disposition, err)
				}
				db.QueryRow(`SELECT json_array(v.instruction,v.final_codex_prompt,v.prompt_sha256,v.prompt_bytes,r.result_text,v.linked_run_id,t.current_review_id,t.updated_at) FROM reviews v JOIN runs r ON r.id=v.linked_run_id JOIN tasks t ON t.id=v.task_id`).Scan(&after)
				if after != before {
					t.Fatalf("historical evidence changed: %s -> %s", before, after)
				}
				if _, err := db.Exec(`UPDATE reviews SET review_depth='FAST'`); err == nil {
					t.Fatal("historical depth mutable")
				}
			}
			rows, err := db.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() {
				t.Fatal("migration damaged ownership")
			}
		})
	}
}

func TestReviewDepthV45MigrationFailsClosedWithoutChangingLegacyHistory(t *testing.T) {
	for _, runStatus := range []string{"RUNNING", "UNKNOWN", "UNKNOWN_ORPHANED", "SUCCEEDED", ""} {
		t.Run(runStatus, func(t *testing.T) {
			db := reviewDepthLegacyDB(t, "REVIEWING", "ACTIVE", runStatus, false)
			err := ApplyMigrations(context.Background(), db, DefaultMigrations())
			if err == nil || !strings.Contains(err.Error(), "explicitly rebuild") || !strings.Contains(err.Error(), "preserved") {
				t.Fatalf("ambiguous history accepted: %v", err)
			}
			var version, count, newColumns int
			db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version)
			db.QueryRow(`SELECT count(*) FROM tasks WHERE id='task' AND phase='REVIEWING' AND status='ACTIVE'`).Scan(&count)
			db.QueryRow(`SELECT count(*) FROM pragma_table_info('reviews') WHERE name='review_depth'`).Scan(&newColumns)
			if version != 44 || count != 1 || newColumns != 0 {
				t.Fatal("failed migration changed source")
			}
		})
	}
}

func TestReviewDepthV45SchemaRequiresExplicitDepthAndClearsTerminalFloor(t *testing.T) {
	db := reviewDepthLegacyDB(t, "REVIEW_REQUIRED", "ACTIVE", "", false)
	if err := ApplyMigrations(context.Background(), db, DefaultMigrations()); err != nil {
		t.Fatal(err)
	}
	var required int
	var defaultValue sql.NullString
	if err := db.QueryRow(`SELECT "notnull",dflt_value FROM pragma_table_info('reviews') WHERE name='review_depth'`).Scan(&required, &defaultValue); err != nil || required != 1 || defaultValue.Valid {
		t.Fatalf("depth must be explicit: required=%d default=%v err=%v", required, defaultValue, err)
	}
	if _, err := db.Exec(`UPDATE tasks SET required_review_depth='SHALLOW'`); err == nil {
		t.Fatal("invalid minimum depth accepted")
	}
	if _, err := db.Exec(`UPDATE tasks SET status='CANCELED' WHERE id='task'`); err != nil {
		t.Fatal(err)
	}
	var floor sql.NullString
	if err := db.QueryRow(`SELECT required_review_depth FROM tasks`).Scan(&floor); err != nil || floor.Valid {
		t.Fatalf("terminal depth remains authoritative: %v %v", floor, err)
	}
}

func TestReviewDepthV45SQLAdmissionCannotBypassMinimum(t *testing.T) {
	db := reviewDepthLegacyDB(t, "REVIEW_REQUIRED", "ACTIVE", "", false)
	if err := ApplyMigrations(context.Background(), db, DefaultMigrations()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('module','p','core','Core','now','now')`); err != nil {
		t.Fatal(err)
	}
	for _, floor := range []any{nil, "DEEP"} {
		_, err := db.Exec(`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,feature_area_id,task_type,task_scope,title,goal,constraints_json,base_commit,status,phase,required_review_depth,created_at,updated_at) VALUES('new-task','p','module','repo','codex_config_default','area','DEVELOPMENT','FEATURE_AREA','Semantic title','Goal','[]','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','ACTIVE','REVIEW_REQUIRED',?,'now','now')`, floor)
		if err == nil || !strings.Contains(err.Error(), "FAST minimum") {
			t.Fatalf("initial Task floor bypassed: floor=%v err=%v", floor, err)
		}
	}
	if _, err := db.Exec(`UPDATE tasks SET required_review_depth='DEEP' WHERE id='task'`); err != nil {
		t.Fatal(err)
	}
	insertReview := `INSERT INTO reviews(id,project_id,feature_area_id,created_by_ai_session_id,repository_id,codex_config_id,base_commit,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at,task_id,ordinal,review_depth) VALUES('review_direct','p','area','session','repo','codex_config_default','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','REPOSITORY','','{"version":1,"paths":[]}','Intent','Exact caller instruction','now','task',1,?)`
	if _, err := db.Exec(insertReview, "FAST"); err == nil || !strings.Contains(err.Error(), "REVIEW_DEPTH_TOO_SHALLOW") {
		t.Fatalf("direct SQL bypassed DEEP minimum: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM reviews`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected SQL created history: %d %v", count, err)
	}
	if _, err := db.Exec(insertReview, "DEEP"); err != nil {
		t.Fatalf("direct SQL DEEP admission blocked: %v", err)
	}
}
