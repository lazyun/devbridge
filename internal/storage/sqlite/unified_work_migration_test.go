package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestUnifiedWorkV35ForwardMigrationPreservesReviewHistory(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "migration.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err = ApplyMigrations(ctx, raw, historicalMigrations()[:35]); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	statements := []string{
		`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Migration',?,?)`,
		`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('repo','p','Repo','/fixture-only',?,?)`,
		`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('session','p',?,?)`,
		`INSERT INTO feature_areas(id,project_id,title,problem_statement,status,created_at,updated_at) VALUES('fa','p','Area','Problem','DISCOVERING',?,?)`,
	}
	for _, q := range statements {
		if _, err = raw.Exec(q, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = raw.Exec(`INSERT INTO reviews(id,project_id,feature_area_id,created_by_ai_session_id,repository_id,codex_config_id,base_commit,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at) VALUES('review_old','p','fa','session','repo','codex_config_default','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','REPOSITORY','History','{"version":1,"paths":[]}','Original Review','Inspect',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`INSERT INTO runs(id,project_id,feature_area_id,operation_kind,operation_id,review_id,attempt,status,purpose,result_text,created_at,updated_at) VALUES('original-run','p','fa','REVIEW','review_old','review_old',1,'SUCCEEDED','SCOPE_REVIEW','Original result',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`UPDATE reviews SET linked_run_id='original-run' WHERE id='review_old'`); err != nil {
		t.Fatal(err)
	}
	if err = ApplyMigrations(ctx, raw, historicalMigrations()); err != nil {
		t.Fatal(err)
	}
	var typ, scope, status string
	var revision interface{}
	if err = raw.QueryRow(`SELECT task_type,task_scope,status,current_definition_revision_id FROM tasks WHERE id='work_review_old'`).Scan(&typ, &scope, &status, &revision); err != nil {
		t.Fatal(err)
	}
	if typ != "REVIEW" || scope != "FEATURE_AREA" || status != "SUCCEEDED" || revision != nil {
		t.Fatal("historical v43 backfill changed")
	}
	var result, taskID, created string
	if err = raw.QueryRow(`SELECT result_text,task_id,created_at FROM runs WHERE id='original-run'`).Scan(&result, &taskID, &created); err != nil {
		t.Fatal(err)
	}
	if result != "Original result" || taskID != "work_review_old" || created != stamp {
		t.Fatalf("history changed: %q %q %q", result, taskID, created)
	}
	if err = ApplyMigrations(ctx, raw, DefaultMigrations()); err == nil {
		t.Fatal("populated v43 implicitly migrated pseudo Task")
	}
	var count int
	if err = raw.QueryRow(`SELECT COUNT(*) FROM tasks WHERE task_type='REVIEW'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate backfill: %d %v", count, err)
	}
	if _, err = raw.Exec(`UPDATE tasks SET task_type='DEVELOPMENT' WHERE id=?`, "work_review_old"); err == nil {
		t.Fatal("envelope acquired Development authority")
	}
	if _, err = raw.Exec(`INSERT INTO task_definition_revisions(id,task_id,ordinal,payload_json,payload_sha256,created_at) VALUES('fake',?,1,'{}',?,?)`, "work_review_old", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", stamp); err == nil {
		t.Fatal("fabricated Development receipt/revision")
	}
	if _, err = raw.Exec(`INSERT INTO runs(id,project_id,feature_area_id,operation_kind,operation_id,review_id,attempt,status,purpose,created_at,updated_at) VALUES('taskless','p','fa','REVIEW','review_old','review_old',2,'CREATED','SCOPE_REVIEW',?,?)`, stamp, stamp); err == nil {
		t.Fatal("new taskless Agent admission accepted")
	}
}
