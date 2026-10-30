// Historical validation of pre-v0.1 schema fixtures only; not the current migration contract.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/dataroot"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

func TestOwnershipMigrationPreservesLegacyHistoryAndRuntimeFences(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "previous.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	if err := ApplyMigrations(ctx, raw, historicalMigrations()[:27]); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := raw.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("fixture SQL %s: %v", query, err)
		}
	}
	exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Legacy project',?,?)`, stamp, stamp)
	exec(`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('repo','p','Repository','/legacy/repo',?,?)`, stamp, stamp)
	exec(`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('module','p','core','Core',?,?)`, stamp, stamp)
	exec(`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('session','p',?,?)`, stamp, stamp)
	for _, id := range []string{"active", "splitting"} {
		status := "ACTIVE"
		if id == "splitting" {
			status = "SPLITTING"
		}
		exec(`INSERT INTO feature_areas(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at) VALUES(?,'p','session',?,'Historical capability',?,?,?)`, id, id, status, stamp, stamp)
	}
	legacyContext := `{"version":1,"mode":"CONTINUE","sourceKind":"EXECUTION","sourceId":"old-execution"}`
	for _, id := range []string{"explicit", "carrier", "featureless"} {
		var area, agent any = "active", nil
		if id == "featureless" {
			area = nil
			agent = legacyContext
		}
		exec(`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at,problem_statement,feature_area_id,agent_context) VALUES(?,'p','module','repo','codex_config_default','','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','PLANNING',?,?,'Historical intent',?,?)`, id, stamp, stamp, area, agent)
	}
	for _, pair := range [][2]string{{"review_source", "explicit"}, {"review_carrier", "carrier"}} {
		exec(`INSERT INTO reviews(id,project_id,feature_area_id,created_by_ai_session_id,task_id,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at) VALUES(?,'p','active','session',?,'REPOSITORY','Historical review','{"version":1,"paths":[]}','Understand capability','Inspect source',?)`, pair[0], pair[1], stamp)
	}
	exec(`INSERT INTO scope_expansions(id,ai_session_id,project_id,task_id,current_scope_kind,current_scope_paths_json,additional_scope_kind,additional_scope_paths_json,reason,status,created_at,updated_at) VALUES('legacy-expansion','session','p','carrier','PATHS','{"version":1,"paths":["current"]}','PATHS','{"version":1,"paths":["additional"]}','Historical authorization','PENDING_CONFIRMATION',?,?)`, stamp, stamp)
	exec(`INSERT INTO runtime_epochs(id,created_at) VALUES('epoch-old',?)`, stamp)
	exec(`INSERT INTO runtime_authority(singleton,epoch_id) VALUES(1,'epoch-old')`)
	exec(`INSERT INTO runs(id,task_id,attempt,status,created_at,updated_at,purpose,codex_instruction,result_text,review_id,runtime_epoch) VALUES('run-review','explicit',1,'SUCCEEDED',?,?,'SCOPE_REVIEW','Inspect source','Historical finding','review_source','epoch-old')`, stamp, stamp)
	exec(`UPDATE reviews SET linked_run_id='run-review' WHERE id='review_source'`)
	work := task.WorkSpec{Goal: "Implement capability", CodexInstruction: "Exact historical instruction", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Historical explicit waiver"}}
	workJSON, err := encodeWorkSpec(work)
	if err != nil {
		t.Fatal(err)
	}
	verificationJSON, err := encodeVerificationSpec(verification)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO task_proposals(id,ai_session_id,project_id,kind,module_id,repository_id,codex_config_id,goal,constraints_json,base_revision,status,created_at,updated_at,work_spec_json,verification_spec_json) VALUES('legacy-task-proposal','session','p','CREATE_TASK','module','repo','codex_config_default',?,'{"version":1,"items":[]}','main','PENDING_CONFIRMATION',?,?,?,?)`, work.Goal, stamp, stamp, workJSON, verificationJSON)
	exec(`INSERT INTO task_preparations(id,project_id,feature_area_id,created_by_ai_session_id,task_id,source_review_id,title,base_commit,work_spec_json,verification_spec_json,feature_area_review_findings,status,created_at,updated_at,preparation_instruction) VALUES('task_preparation_legacy','p','active','session','explicit','review_source','Historical preparation','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,?,'Historical finding','DRAFT',?,?,'Read-only preparation')`, workJSON, verificationJSON, stamp, stamp)
	exec(`UPDATE task_preparations SET status='PREPARING' WHERE id='task_preparation_legacy'`)
	exec(`INSERT INTO task_plans(id,ai_session_id,project_id,task_id,title,work_spec_json,verification_spec_json,status,created_at,updated_at) VALUES('plan','session','p','explicit','Historical plan',?,?,'PENDING_CONFIRMATION',?,?)`, workJSON, verificationJSON, stamp, stamp)
	exec(`UPDATE task_plans SET status='CONFIRMED',confirmed_at=? WHERE id='plan'`, stamp)
	exec(`UPDATE tasks SET goal=?,work_spec_json=?,verification_spec_json=?,confirmed_task_plan_id='plan',status='READY' WHERE id='explicit'`, work.Goal, workJSON, verificationJSON)
	exec(`UPDATE task_plans SET status='APPLIED',applied_at=? WHERE id='plan'`, stamp)
	resultJSON := `{"version":1,"result":{"verdict":"READY","modificationPaths":["internal"]}}`
	exec(`UPDATE task_preparations SET status='READY',task_plan_id='plan',result_json=? WHERE id='task_preparation_legacy'`, resultJSON)
	exec(`INSERT INTO execution_proposals(id,ai_session_id,project_id,task_id,task_plan_id,status,created_at,updated_at,work_spec_json,verification_spec_json,outcome) VALUES('execution','session','p','explicit','plan','PENDING_CONFIRMATION',?,?,?,?, 'PENDING')`, stamp, stamp, workJSON, verificationJSON)
	// A featureless, unreserved old Execution previously reached a store-level
	// null-FeatureArea progress bypass. Preserve that exact legacy shape.
	exec(`INSERT INTO task_goals(id,task_id,project_id,position,title,created_at) VALUES('goal_legacy','featureless','p',0,'Historical progress',?)`, stamp)
	exec(`INSERT INTO task_plans(id,ai_session_id,project_id,task_id,title,work_spec_json,verification_spec_json,status,created_at,updated_at) VALUES('legacy-goal-plan','session','p','featureless','Historical plan',?,?,'PENDING_CONFIRMATION',?,?)`, workJSON, verificationJSON, stamp, stamp)
	exec(`UPDATE task_plans SET status='CONFIRMED',confirmed_at=? WHERE id='legacy-goal-plan'`, stamp)
	exec(`UPDATE tasks SET goal=?,work_spec_json=?,verification_spec_json=?,confirmed_task_plan_id='legacy-goal-plan',status='READY' WHERE id='featureless'`, work.Goal, workJSON, verificationJSON)
	exec(`UPDATE task_plans SET status='APPLIED',applied_at=? WHERE id='legacy-goal-plan'`, stamp)
	exec(`INSERT INTO execution_proposals(id,ai_session_id,project_id,task_id,task_plan_id,status,created_at,updated_at,work_spec_json,verification_spec_json,outcome) VALUES('legacy-goal-execution','session','p','featureless','legacy-goal-plan','PENDING_CONFIRMATION',?,?,?,?,'PENDING')`, stamp, stamp, workJSON, verificationJSON)
	exec(`INSERT INTO execution_goal_progress(execution_proposal_id,goal_id,project_id,status,updated_at) VALUES('legacy-goal-execution','goal_legacy','p','PENDING',?)`, stamp)
	exec(`INSERT INTO workspaces(id,task_id,thread_id,status,token_baseline_available,created_at,updated_at) VALUES('workspace','explicit','obsolete-workspace-thread','READY',0,?,?)`, stamp, stamp)
	exec(`INSERT INTO runs(id,task_id,attempt,status,created_at,updated_at,purpose,execution_proposal_id,thread_id,turn_id,runtime_epoch,workspace_id) VALUES('run-execution','explicit',2,'RUNNING',?,?,'IMPLEMENTATION','execution','canonical-thread','turn-active','epoch-old','workspace')`, stamp, stamp)
	exec(`UPDATE execution_proposals SET status='CONFIRMED',confirmed_at=? WHERE id='execution'`, stamp)
	exec(`UPDATE execution_proposals SET status='CONSUMED',consumed_at=?,run_id='run-execution',outcome='RUNNING' WHERE id='execution'`, stamp)
	exec(`INSERT INTO agent_conversations(id,project_id,feature_area_id,backend,backend_thread_id,host_tool_contract_version,created_at) VALUES('conversation','p','active','CODEX','canonical-thread',1,?)`, stamp)
	exec(`INSERT INTO agent_conversations(id,project_id,feature_area_id,backend,host_tool_contract_version,created_at) VALUES('split-conversation','p','splitting','CODEX',1,?)`, stamp)
	exec(`INSERT INTO feature_area_agent_claims(feature_area_id,operation_kind,operation_id,run_id,created_at,runtime_epoch) VALUES('active','EXECUTION','execution','run-execution',?,'epoch-old')`, stamp)
	exec(`INSERT INTO agent_operation_boundaries(run_id,agent_conversation_id,advance_state,quiesced) VALUES('run-execution','conversation','UNKNOWN',0)`)
	if err := ApplyMigrations(ctx, raw, historicalMigrations()[:41]); err != nil {
		t.Fatalf("v27 -> v28: %v", err)
	}
	assertString := func(query, want string) {
		t.Helper()
		var got string
		if err := raw.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("query=%s got=%q want=%q err=%v", query, got, want, err)
		}
	}
	assertString(`SELECT agent_context FROM legacy_task_audit WHERE task_id='featureless'`, legacyContext)
	assertString(`SELECT reason FROM legacy_task_audit WHERE task_id='carrier'`, "AMBIGUOUS_REVIEW_CARRIER")
	assertString(`SELECT CAST(legacy_read_only AS TEXT) FROM tasks WHERE id='carrier'`, "1")
	assertString(`SELECT CAST(legacy_read_only AS TEXT) FROM tasks WHERE id='explicit'`, "0")
	assertString(`SELECT legacy_status FROM feature_areas WHERE id='splitting'`, "SPLITTING")
	assertString(`SELECT CAST(legacy_read_only AS TEXT) FROM feature_areas WHERE id='splitting'`, "1")
	assertString(`SELECT CASE WHEN ended_at IS NOT NULL THEN 'ended' ELSE 'current' END FROM agent_conversations WHERE id='split-conversation'`, "ended")
	assertString(`SELECT feature_area_id FROM reviews WHERE id='review_carrier'`, "active")
	assertString(`SELECT legacy_task_id FROM reviews WHERE id='review_carrier'`, "carrier")
	assertString(`SELECT repository_id FROM reviews WHERE id='review_carrier'`, "repo")
	assertString(`SELECT legacy_task_id FROM scope_expansions WHERE id='legacy-expansion'`, "carrier")
	assertString(`SELECT COALESCE(review_id,'LEGACY_UNKNOWN') FROM scope_expansions WHERE id='legacy-expansion'`, "LEGACY_UNKNOWN")
	assertString(`SELECT reason FROM scope_expansions WHERE id='legacy-expansion'`, "Historical authorization")
	if _, err := raw.ExecContext(ctx, `UPDATE scope_expansions SET status='ALLOWED',decided_at=? WHERE id='legacy-expansion'`, stamp); err == nil {
		t.Fatal("legacy Task expansion became active Review authorization")
	}

	assertString(`SELECT legacy_thread_id FROM workspaces WHERE id='workspace'`, "obsolete-workspace-thread")
	assertString(`SELECT backend_thread_id FROM agent_conversations WHERE id='conversation'`, "canonical-thread")
	assertString(`SELECT CAST(ordinal AS TEXT) FROM task_preparations WHERE id='task_preparation_legacy'`, "1")
	assertString(`SELECT result_json FROM task_preparations WHERE id='task_preparation_legacy'`, resultJSON)
	assertString(`SELECT COALESCE(task_definition_revision_id,'LEGACY_UNKNOWN') FROM task_preparations WHERE id='task_preparation_legacy'`, "LEGACY_UNKNOWN")
	assertString(`SELECT COALESCE(task_definition_revision_id,'LEGACY_UNKNOWN') FROM execution_proposals WHERE id='execution'`, "LEGACY_UNKNOWN")
	assertString(`SELECT CAST(COUNT(*) AS TEXT) FROM task_definition_revisions`, "0")
	assertString(`SELECT operation_kind||':'||operation_id FROM runs WHERE id='run-review'`, "REVIEW:review_source")
	assertString(`SELECT operation_kind||':'||operation_id FROM runs WHERE id='run-execution'`, "EXECUTION:execution")
	assertString(`SELECT runtime_epoch FROM runs WHERE id='run-execution'`, "epoch-old")
	assertString(`SELECT runtime_epoch FROM feature_area_agent_claims WHERE run_id='run-execution'`, "epoch-old")
	assertString(`SELECT advance_state||':'||CAST(quiesced AS TEXT) FROM agent_operation_boundaries WHERE run_id='run-execution'`, "UNKNOWN:0")
	legacyProposals := NewTaskProposalStore(&DB{sql: raw})
	legacyProposal, err := legacyProposals.Get(ctx, "legacy-task-proposal")
	if err != nil || legacyProposal.WorkSpec.CodexInstruction != work.CodexInstruction || string(legacyProposal.Status) != "PENDING_CONFIRMATION" {
		t.Fatalf("legacy Task Proposal history lost: %+v err=%v", legacyProposal, err)
	}
	for _, query := range []string{
		`UPDATE task_proposals SET status='CONFIRMED',confirmed_at='2026-09-07T00:00:00Z' WHERE id='legacy-task-proposal'`,
		`DELETE FROM task_proposals WHERE id='legacy-task-proposal'`,
		`INSERT INTO task_proposals(id,ai_session_id,project_id,kind,module_id,repository_id,codex_config_id,goal,constraints_json,base_revision,status,created_at,updated_at,work_spec_json,verification_spec_json) SELECT 'new-task-proposal',ai_session_id,project_id,kind,module_id,repository_id,codex_config_id,goal,constraints_json,base_revision,status,created_at,updated_at,work_spec_json,verification_spec_json FROM task_proposals WHERE id='legacy-task-proposal'`,
	} {
		if _, err := raw.ExecContext(ctx, query); err == nil {
			t.Fatalf("retired Task Proposal write succeeded: %s", query)
		}
	}
	writer, err := dataroot.AcquireWriterLock(filepath.Join(t.TempDir(), "legacy-goal-writer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	// This historical-only fixture intentionally stops before current product
	// migrations. Add the nullable current reader columns so the ownership fence
	// can still be exercised through today's store without changing old rows.
	// These fields were introduced after this historical boundary and remain
	// absent for the preserved legacy proposal.
	if _, err := raw.ExecContext(ctx, `ALTER TABLE execution_proposals ADD COLUMN runtime_config_json TEXT`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `ALTER TABLE execution_proposals ADD COLUMN backend_selection_json TEXT`); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{
		`primary_member_id TEXT`,
		`approved_workset_hash TEXT`,
		`runtime_adapter_identity TEXT`,
		`runtime_protocol_identity TEXT`,
	} {
		if _, err := raw.ExecContext(ctx, `ALTER TABLE execution_proposals ADD COLUMN `+column); err != nil {
			t.Fatal(err)
		}
	}
	currentDB := &DB{sql: raw, writer: writer}
	if err := currentDB.allocateRuntimeEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecutionProposalStore(currentDB).Confirm(ctx, "legacy-goal-execution", testNow); !errors.Is(err, executionproposal.ErrConflict) {
		t.Fatalf("legacy Execution confirmation acquired new authorization: %v", err)
	}
	assertString(`SELECT status FROM execution_proposals WHERE id='legacy-goal-execution'`, "PENDING_CONFIRMATION")
	assertString(`SELECT COALESCE(confirmed_at,'') FROM execution_proposals WHERE id='legacy-goal-execution'`, "")
	goals := NewTaskStore(currentDB)
	if err := goals.UpdateGoalProgress(ctx, "legacy-goal-execution", "goal_legacy", taskgoal.StatusRunning, testNow); !errors.Is(err, agentconversation.ErrConflict) {
		t.Fatalf("current writer must reject legacy Goal progress by ownership, got %v", err)
	}
	assertString(`SELECT status FROM execution_goal_progress WHERE execution_proposal_id='legacy-goal-execution'`, "PENDING")
	assertString(`SELECT updated_at FROM execution_goal_progress WHERE execution_proposal_id='legacy-goal-execution'`, stamp)
	if err := goals.InitializeGoalProgress(ctx, "legacy-goal-execution", "featureless", "p", testNow); !errors.Is(err, task.ErrLegacyReadOnly) {
		t.Fatalf("legacy Goal progress initialization accepted: %v", err)
	}
	rows, err := raw.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID, foreignID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignID); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign key violation: %s row %d -> %s", table, rowID, parent)
	}
}
