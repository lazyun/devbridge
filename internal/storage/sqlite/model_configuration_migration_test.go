package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestDynamicModelsV39RemovesOnlyClosedModelChecks(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "v37.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	if err := ApplyMigrations(ctx, raw, historicalMigrations()[:38]); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := raw.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("fixture SQL %s: %v", query, err)
		}
	}
	exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Historical project',?,?)`, stamp, stamp)
	exec(`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('repo','p','Repository','/model-migration-fixture',?,?)`, stamp, stamp)
	exec(`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('module','p','core','Core',?,?)`, stamp, stamp)
	exec(`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('session','p',?,?)`, stamp, stamp)
	exec(`INSERT INTO feature_areas(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at) VALUES('area','p','session','Area','Historical capability','ACTIVE',?,?)`, stamp, stamp)
	exec(`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at,problem_statement,feature_area_id,model,reasoning_effort) VALUES('task-history','p','module','repo','codex_config_default','','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','PLANNING',?,?,'Historical task','area','gpt-5.6-sol','high')`, stamp, stamp)
	exec(`INSERT INTO reviews(id,project_id,feature_area_id,created_by_ai_session_id,repository_id,codex_config_id,base_commit,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at,model,reasoning_effort) VALUES('review_history','p','area','session','repo','codex_config_default','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','REPOSITORY','Historical review','{"version":1,"paths":[]}','Understand capability','Inspect source',?,'gpt-5.6-sol','high')`, stamp)

	var triggersBefore, indexesBefore int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger'`).Scan(&triggersBefore); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name IN ('tasks','reviews') AND sql IS NOT NULL`).Scan(&indexesBefore); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, raw, historicalMigrations()[:39]); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tasks", "reviews"} {
		var ddl string
		if err := raw.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(ddl, "model IN ('gpt-5.6-luna','gpt-5.6-terra','gpt-5.6-sol')") {
			t.Fatalf("%s retained closed model constraint: %s", table, ddl)
		}
		if !strings.Contains(ddl, "reasoning_effort IN ('low','medium','high','xhigh')") {
			t.Fatalf("%s lost reasoning constraint: %s", table, ddl)
		}
	}
	var historicalTaskModel, historicalReviewModel string
	if err := raw.QueryRow(`SELECT model FROM tasks WHERE id='task-history'`).Scan(&historicalTaskModel); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT model FROM reviews WHERE id='review_history'`).Scan(&historicalReviewModel); err != nil {
		t.Fatal(err)
	}
	if historicalTaskModel != "gpt-5.6-sol" || historicalReviewModel != "gpt-5.6-sol" {
		t.Fatalf("historical selections changed: task=%q review=%q", historicalTaskModel, historicalReviewModel)
	}
	if _, err := raw.Exec(`UPDATE tasks SET model='gpt-6-codex-preview' WHERE id='task-history'; UPDATE reviews SET model='gpt-6-codex-preview' WHERE id='review_history'`); err != nil {
		t.Fatalf("custom model rejected after v38: %v", err)
	}

	var triggersAfter, indexesAfter int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger'`).Scan(&triggersAfter); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name IN ('tasks','reviews') AND sql IS NOT NULL`).Scan(&indexesAfter); err != nil {
		t.Fatal(err)
	}
	if triggersAfter != triggersBefore {
		t.Fatalf("trigger count changed across v39: before=%d after=%d", triggersBefore, triggersAfter)
	}
	if indexesAfter != indexesBefore {
		t.Fatalf("task/review index count changed across v39: before=%d after=%d", indexesBefore, indexesAfter)
	}
	// The exact service/store integration is covered elsewhere; this assertion
	// guards the rebuilt table contract directly.
	var violations int
	rows, err := raw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		violations++
	}
	rows.Close()
	if violations != 0 {
		t.Fatalf("foreign key violations=%d", violations)
	}
}
