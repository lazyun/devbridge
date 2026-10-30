package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReviewApprovalV38MigrationPreservesLegacyTaskRevisionAndReceipt(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "v37.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err = ApplyMigrations(ctx, raw, historicalMigrations()[:37]); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	for _, q := range []string{
		`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','History',?,?)`,
		`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('repo','p','Repository','/historical-only',?,?)`,
		`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('session','p',?,?)`,
		`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('module','p','history','History',?,?)`,
		`INSERT INTO feature_areas(id,project_id,title,problem_statement,status,created_at,updated_at) VALUES('area','p','Area','Original requirement','ACTIVE',?,?)`,
		`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at,feature_area_id) VALUES('task','p','module','repo','codex_config_default','Original goal','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','PLANNING',?,?,'area')`,
	} {
		if _, err = raw.Exec(q, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = raw.Exec(`INSERT INTO task_definition_revisions(id,task_id,ordinal,payload_json,payload_sha256,created_at) VALUES('revision','task',1,'{"projectId":"p","featureAreaId":"area","repositoryId":"repo"}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`UPDATE tasks SET current_definition_revision_id='revision' WHERE id='task'`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`INSERT INTO decision_resolution_receipts(decision_id,project_id,decision_kind,subject_kind,subject_id,option,request_id,result_kind,result_id,resolved_at) VALUES('TASK_CONFIRMATION:old-plan','p','TASK_CONFIRMATION','TASK_PLAN','old-plan','CANCEL','old-request','TASK_PLAN','old-plan',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	snapshot := func() []string {
		t.Helper()
		var row, payload, receipt string
		if err := raw.QueryRow(`SELECT id||'|'||goal||'|'||status||'|'||current_definition_revision_id||'|'||created_at FROM tasks WHERE id='task'`).Scan(&row); err != nil {
			t.Fatal(err)
		}
		if err := raw.QueryRow(`SELECT payload_json||'|'||payload_sha256 FROM task_definition_revisions WHERE id='revision'`).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if err := raw.QueryRow(`SELECT decision_kind||'|'||option||'|'||request_id||'|'||resolved_at FROM decision_resolution_receipts`).Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		return []string{row, payload, receipt}
	}
	before := snapshot()
	for i := 0; i < 2; i++ {
		if err = ApplyMigrations(ctx, raw, historicalMigrations()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("migration rewrote legacy authority")
	}
	for _, table := range []string{"review_workflow_sources", "review_approval_decisions", "review_approval_receipt_context", "review_approval_materializations"} {
		var n int
		if err = raw.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("invented %s: %d err=%v", table, n, err)
		}
	}
	var approval sql.NullString
	if err = raw.QueryRow(`SELECT review_approval_id FROM tasks WHERE id='task'`).Scan(&approval); err != nil || approval.Valid {
		t.Fatal("legacy Task received fake Approval")
	}
	rows, err := raw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration foreign-key violation")
	}
}
