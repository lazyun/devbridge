package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func migrationsThroughV88Test(version int) []Migration {
	all := DefaultMigrations()
	for i, migration := range all {
		if migration.Version > version {
			return all[:i]
		}
	}
	return all
}

func v87RawDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	if err := ApplyMigrations(context.Background(), raw, migrationsThroughV88Test(87)); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := raw.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 87 {
		t.Fatalf("fixture schema version: %d %v", version, err)
	}
	return raw
}

// withMigrationSeedTriggersParked builds the pre-v88 anomalous initial facts:
// production lifecycle triggers cannot create legacy READY/RUNNING states, so
// the same-package fixture parks every trigger for the seed transaction and
// restores each exact definition afterwards. No production surface changes.
func withMigrationSeedTriggersParked(t *testing.T, raw *sql.DB, seed func(*sql.Tx) error) {
	t.Helper()
	ctx := context.Background()
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND sql IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	type parked struct{ name, sql string }
	var definitions []parked
	for rows.Next() {
		var value parked
		if err := rows.Scan(&value.name, &value.sql); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		definitions = append(definitions, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range definitions {
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER "+sqlIdentifier(value.name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed(tx); err != nil {
		t.Fatal(err)
	}
	for _, value := range definitions {
		if _, err := tx.ExecContext(ctx, value.sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskLifecycleMigrationV88(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	raw := v87RawDB(t, root)
	const at = "2026-09-01T00:00:00Z"
	type expectation struct {
		id           string
		wantStatus   string
		wantPhase    string
		preStatus    string
		prePhase     string
		taskType     string
		legacyRecord int
	}
	cases := []expectation{
		{id: "v88-ready", preStatus: "READY", prePhase: "REVIEWING", wantStatus: "ACTIVE", wantPhase: "REVIEWING"},
		{id: "v88-running", preStatus: "RUNNING", prePhase: "IMPLEMENTATION_READY", wantStatus: "ACTIVE", wantPhase: "IMPLEMENTATION_READY"},
		{id: "v88-planning", preStatus: "PLANNING", prePhase: "REVIEW_REQUIRED", wantStatus: "ACTIVE", wantPhase: "REVIEW_REQUIRED"},
		{id: "v88-failed", preStatus: "FAILED", prePhase: "VERIFICATION_READY", wantStatus: "ACTIVE", wantPhase: "RECOVERY_REQUIRED"},
		{id: "v88-inflight-impl", preStatus: "ACTIVE", prePhase: "IMPLEMENTING", wantStatus: "ACTIVE", wantPhase: "RECOVERY_REQUIRED"},
		{id: "v88-inflight-verify", preStatus: "ACTIVE", prePhase: "VERIFYING", wantStatus: "ACTIVE", wantPhase: "RECOVERY_REQUIRED"},
		{id: "v88-cancel-safe", preStatus: "ACTIVE", prePhase: "IMPLEMENTING", wantStatus: "CANCELED", wantPhase: "IMPLEMENTING"},
		{id: "v88-cancel-unknown", preStatus: "ACTIVE", prePhase: "VERIFICATION_READY", wantStatus: "ACTIVE", wantPhase: "RECOVERY_REQUIRED"},
		{id: "v88-recovery-pending", preStatus: "ACTIVE", prePhase: "REVIEWING", wantStatus: "ACTIVE", wantPhase: "RECOVERY_REQUIRED"},
		{id: "v88-recovery-resolved", preStatus: "ACTIVE", prePhase: "REVIEWING", wantStatus: "ACTIVE", wantPhase: "REVIEWING"},
		{id: "v88-delivered", preStatus: "ACTIVE", prePhase: "ACCEPTANCE_PENDING", wantStatus: "SUCCEEDED", wantPhase: "ACCEPTANCE_PENDING"},
		{id: "v88-delivering-receipt", preStatus: "ACTIVE", prePhase: "ACCEPTANCE_PENDING", wantStatus: "ACTIVE", wantPhase: "DELIVERING"},
		{id: "v88-delivering-no-receipt", preStatus: "ACTIVE", prePhase: "ACCEPTANCE_PENDING", wantStatus: "ACTIVE", wantPhase: "DELIVERING"},
		{id: "v88-delivery-errored", preStatus: "ACTIVE", prePhase: "ACCEPTANCE_PENDING", wantStatus: "ACTIVE", wantPhase: "RECOVERY_REQUIRED"},
		{id: "v88-succeeded-preserved", preStatus: "SUCCEEDED", prePhase: "ACCEPTANCE_PENDING", wantStatus: "SUCCEEDED", wantPhase: "ACCEPTANCE_PENDING"},
		{id: "v88-canceled-preserved", preStatus: "CANCELED", prePhase: "IMPLEMENTING", wantStatus: "CANCELED", wantPhase: "IMPLEMENTING"},
		{id: "v88-inspection", preStatus: "READY", prePhase: "REVIEW_REQUIRED", wantStatus: "READY", wantPhase: "REVIEW_REQUIRED", taskType: "INSPECTION"},
		{id: "v88-legacy", preStatus: "READY", prePhase: "REVIEW_REQUIRED", wantStatus: "READY", wantPhase: "REVIEW_REQUIRED", legacyRecord: 1},
		{id: "v88-rejected", preStatus: "ACTIVE", prePhase: "ACCEPTANCE_PENDING", wantStatus: "ACTIVE", wantPhase: "ACCEPTANCE_PENDING"},
	}
	var triggersBefore int
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger'`).Scan(&triggersBefore); err != nil {
		t.Fatal(err)
	}
	withMigrationSeedTriggersParked(t, raw, func(tx *sql.Tx) error {
		mustExec := func(query string, args ...any) {
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				t.Fatal(err)
			}
		}
		mustExec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('v88-project','v88',?,?)`, at, at)
		mustExec(`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('v88-session','v88-project',?,?)`, at, at)
		mustExec(`INSERT INTO feature_areas(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at) VALUES('v88-area','v88-project','v88-session','area','intent','ACTIVE',?,?)`, at, at)
		mustExec(`INSERT INTO runtime_epochs(id,created_at) VALUES('v88-epoch',?)`, at)
		for _, c := range cases {
			taskType, legacy := "DEVELOPMENT", 0
			if c.taskType != "" {
				taskType = c.taskType
			}
			legacy = c.legacyRecord
			mustExec(`INSERT INTO tasks(id,project_id,feature_area_id,goal,constraints_json,base_commit,status,phase,task_type,legacy_read_only,title,created_at,updated_at,work_spec_json,verification_spec_json,problem_statement) VALUES(?,'v88-project','v88-area','goal','{"version":1,"items":[]}','base',?,?,?,?,'t',?,?, '{}','{}','intent')`,
				c.id, c.preStatus, c.prePhase, taskType, legacy, at, at)
		}
		mustExec(`INSERT INTO execution_cancellations(project_id,request_id,reason,requested_at,runtime_epoch,state,interrupt_state,terminal_observed,resolved_at,owner_type,owner_id,task_id,requested_by_ai_session_id) VALUES('v88-project','cancel-safe','cleanup',?,'v88-epoch','CANCELED','ACKNOWLEDGED',1,?,'TASK',?,?, 'v88-session')`, at, at, "v88-cancel-safe", "v88-cancel-safe")
		mustExec(`INSERT INTO execution_cancellations(project_id,request_id,reason,requested_at,runtime_epoch,state,interrupt_state,terminal_observed,resolved_at,owner_type,owner_id,task_id,requested_by_ai_session_id) VALUES('v88-project','cancel-unknown','cleanup',?,'v88-epoch','CANCEL_REQUESTED','OUTCOME_UNKNOWN',0,NULL,'TASK',?,?, 'v88-session')`, at, "v88-cancel-unknown", "v88-cancel-unknown")
		for _, pair := range [][2]string{{"v88-run-recovery-pending", "v88-recovery-pending"}, {"v88-run-recovery-resolved", "v88-recovery-resolved"}} {
			mustExec(`INSERT INTO runs(id,task_id,project_id,feature_area_id,operation_kind,operation_id,purpose,attempt,status,runtime_epoch,codex_instruction,review_scope_paths_json,selected_modules_json,created_at,updated_at) VALUES(?,?,'v88-project','v88-area','EXECUTION','op-1','IMPLEMENTATION',1,'SUCCEEDED','v88-epoch','','{"version":1,"paths":[]}','{"version":1,"items":[]}',?,?)`, pair[0], pair[1], at, at)
		}
		mustExec(`INSERT INTO recovery_status_records(id,source_run_id,source_turn_id,generation,request_id,runtime_epoch,state,spec_json,record_json) VALUES('recovery-pending','v88-run-recovery-pending','turn-1',1,'recovery-request-1','v88-epoch','REPORT_READY','{}','{}')`)
		mustExec(`INSERT INTO recovery_status_records(id,source_run_id,source_turn_id,generation,request_id,runtime_epoch,state,spec_json,record_json) VALUES('recovery-resolved','v88-run-recovery-resolved','turn-1',1,'recovery-request-2','v88-epoch','RESOLVED','{}','{}')`)
		accept := func(id, taskID, option string) {
			mustExec(`INSERT INTO decision_resolution_receipts(decision_id,project_id,decision_kind,subject_kind,subject_id,option,request_id,result_kind,result_id,resolved_at) VALUES(?,'v88-project','TASK_ACCEPTANCE','EXECUTION_PROPOSAL',?,?,?, 'DEVELOPMENT_TASK',?,?)`, id, taskID, option, "request-"+id, taskID, at)
		}
		accept("decision-delivered", "v88-delivered", "ACCEPT")
		accept("decision-delivering", "v88-delivering-receipt", "ACCEPT")
		accept("decision-delivering-no-receipt", "v88-delivering-no-receipt", "ACCEPT")
		accept("decision-errored", "v88-delivery-errored", "ACCEPT")
		accept("decision-rejected", "v88-rejected", "REQUEST_CHANGES")
		mustExec(`INSERT INTO task_commit_receipts(task_id,feature_area_id,revision_id,verified_snapshot,workspace_id,workspace_path,branch,resulting_commit,completed_at,last_error) VALUES('v88-delivered','v88-area','rev-1','snapshot','ws-1','/ws/1','branch-1','cccccccccccccccccccccccccccccccccccccccc',?,'')`, at)
		mustExec(`INSERT INTO task_commit_receipts(task_id,feature_area_id,revision_id,verified_snapshot,workspace_id,workspace_path,branch,resulting_commit,completed_at,last_error) VALUES('v88-delivering-receipt','v88-area','rev-1','snapshot','ws-1','/ws/1','branch-1','',NULL,'')`)
		mustExec(`INSERT INTO task_commit_receipts(task_id,feature_area_id,revision_id,verified_snapshot,workspace_id,workspace_path,branch,resulting_commit,completed_at,last_error) VALUES('v88-delivery-errored','v88-area','rev-1','snapshot','ws-1','/ws/1','branch-1','',NULL,'git remote rejected')`)
		return nil
	})
	var receiptsBefore int
	if err := raw.QueryRow(`SELECT count(*) FROM decision_resolution_receipts`).Scan(&receiptsBefore); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, raw, migrationsThroughV88Test(88)); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		var status, phase string
		if err := raw.QueryRowContext(ctx, `SELECT status,phase FROM tasks WHERE id=?`, c.id).Scan(&status, &phase); err != nil {
			t.Fatal(err)
		}
		if status != c.wantStatus || phase != c.wantPhase {
			t.Fatalf("%s normalized to %s/%s, want %s/%s", c.id, status, phase, c.wantStatus, c.wantPhase)
		}
	}
	var version int
	if err := raw.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 88 {
		t.Fatalf("migration version: %d %v", version, err)
	}
	var ddl string
	if err := raw.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'`).Scan(&ddl); err != nil || !strings.Contains(ddl, "'ACCEPTANCE_PENDING','DELIVERING','RECOVERY_REQUIRED'") {
		t.Fatalf("phase constraint not extended: %v %s", err, ddl)
	}
	if !strings.Contains(ddl, "reasoning_effort IN ('low','medium','high','xhigh','max','ultra')") {
		t.Fatalf("v46 reasoning contract lost: %s", ddl)
	}
	var claimObjects, triggersAfter int
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name LIKE 'review_materialization_claim%' OR name='review_result_materialization_claims'`).Scan(&claimObjects); err != nil || claimObjects != 6 {
		t.Fatalf("materialization claim objects: %d %v", claimObjects, err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger'`).Scan(&triggersAfter); err != nil || triggersAfter != triggersBefore+5 {
		t.Fatalf("trigger inventory changed: before=%d after=%d err=%v", triggersBefore, triggersAfter, err)
	}
	var receiptsAfter int
	if err := raw.QueryRow(`SELECT count(*) FROM decision_resolution_receipts`).Scan(&receiptsAfter); err != nil || receiptsAfter != receiptsBefore {
		t.Fatalf("acceptance receipts not preserved: %d %v", receiptsAfter, err)
	}
	// Repeated application is a no-op.
	if err := ApplyMigrations(ctx, raw, migrationsThroughV88Test(88)); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	// Cold restart goes through the real Open contract verifier.
	db, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE tasks SET phase='RECOVERY_REQUIRED',updated_at=? WHERE id='v88-ready' AND status='ACTIVE'`, at); err != nil {
		t.Fatalf("new phase rejected: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE tasks SET phase='NOPE' WHERE id='v88-planning'`); err == nil {
		t.Fatal("unknown phase accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var stillNormal string
	if err := db.sql.QueryRowContext(ctx, `SELECT status||'/'||phase FROM tasks WHERE id='v88-delivering-receipt'`).Scan(&stillNormal); err != nil || stillNormal != "ACTIVE/DELIVERING" {
		t.Fatalf("repeat open changed lifecycle: %s %v", stillNormal, err)
	}
}
