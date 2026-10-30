package sqlite

import (
	"context"
	"strings"
	"testing"
)

func TestExecutionRuntimeConfigV46WidensReasoningEffortWithoutLosingState(t *testing.T) {
	db := reviewDepthLegacyDB(t, "REVIEWING", "ACTIVE", "CANCELED", false)
	if err := ApplyMigrations(context.Background(), db, []Migration{
		{Version: 44, ForeignKeysOff: true, Apply: migrateFinalV44},
		{Version: 45, ForeignKeysOff: true, Apply: migrateReviewDepthV45},
	}); err != nil {
		t.Fatal(err)
	}
	var triggersBefore, indexesBefore int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger'`).Scan(&triggersBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name IN ('tasks','reviews') AND sql IS NOT NULL`).Scan(&indexesBefore); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(context.Background(), db, DefaultMigrations()[:4]); err != nil {
		t.Fatal(err)
	}
	for table, effort := range map[string]string{"tasks": "max", "reviews": "ultra"} {
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ddl, "reasoning_effort IN ('low','medium','high','xhigh','max','ultra')") {
			t.Fatalf("%s constraint was not widened: %s", table, ddl)
		}
		if _, err := db.Exec(`UPDATE `+sqlIdentifier(table)+` SET reasoning_effort=?`, effort); err != nil {
			t.Fatalf("%s rejected %s: %v", table, effort, err)
		}
	}
	triggerRows, err := db.Query(`SELECT name,sql FROM sqlite_master WHERE type='trigger' AND tbl_name='execution_proposals' AND name<>'execution_proposals_runtime_config_immutable'`)
	if err != nil {
		t.Fatal(err)
	}
	var proposalTriggers []schemaObject
	for triggerRows.Next() {
		var object schemaObject
		object.kind = "trigger"
		if err := triggerRows.Scan(&object.name, &object.sql); err != nil {
			t.Fatal(err)
		}
		proposalTriggers = append(proposalTriggers, object)
	}
	triggerRows.Close()
	for _, object := range proposalTriggers {
		if _, err := db.Exec(`DROP TRIGGER ` + sqlIdentifier(object.name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO task_plans(id,ai_session_id,project_id,task_id,title,work_spec_json,verification_spec_json,status,created_at,updated_at) VALUES('plan-v46','session','p','task','Runtime config trigger fixture','{}','{}','PENDING_CONFIRMATION','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO execution_proposals(id,ai_session_id,project_id,task_id,task_plan_id,status,created_at,updated_at,work_spec_json,verification_spec_json,outcome,runtime_config_json) VALUES('execution-v46','session','p','task','plan-v46','PENDING_CONFIRMATION','now','now','{}','{}','PENDING','{"model":"gpt-6-astra","reasoningEffort":"ultra","codexFastMode":true,"serviceTier":"priority"}')`); err != nil {
		t.Fatal(err)
	}
	for _, object := range proposalTriggers {
		if _, err := db.Exec(object.sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE execution_proposals SET runtime_config_json='{"model":"gpt-6-astra","reasoningEffort":"max","codexFastMode":false,"serviceTier":""}' WHERE id='execution-v46'`); err == nil || !strings.Contains(err.Error(), "Execution runtime config is immutable") {
		t.Fatalf("Execution runtime config mutation error=%v", err)
	}
	var taskID, reviewID, taskEffort, reviewEffort string
	if err := db.QueryRow(`SELECT id,reasoning_effort FROM tasks`).Scan(&taskID, &taskEffort); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id,reasoning_effort FROM reviews`).Scan(&reviewID, &reviewEffort); err != nil {
		t.Fatal(err)
	}
	if taskID != "task" || reviewID != "review_legacy" || taskEffort != "max" || reviewEffort != "ultra" {
		t.Fatalf("state changed: task=%s/%s review=%s/%s", taskID, taskEffort, reviewID, reviewEffort)
	}
	var triggersAfter, indexesAfter int
	_ = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger'`).Scan(&triggersAfter)
	_ = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name IN ('tasks','reviews') AND sql IS NOT NULL`).Scan(&indexesAfter)
	if triggersAfter != triggersBefore+2 || indexesAfter != indexesBefore {
		t.Fatalf("schema objects changed: triggers %d/%d indexes %d/%d", triggersBefore, triggersAfter, indexesBefore, indexesAfter)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a foreign key violation")
	}
}
