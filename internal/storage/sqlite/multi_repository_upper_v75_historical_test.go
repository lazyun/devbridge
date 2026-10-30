package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/worktree"
)

// This starts from the actual v74 migration boundary, creates a durable v1
// worktree under historical constraints, then upgrades only through v75.
func TestV75PreservesHistoricalWorktreeCreatorAndRemovesRunUniqueness(t *testing.T) {
	historicalMultiUpgrade(t, false)
}
func TestV76PreservesV1DirectFileAndDeliveryHistory(t *testing.T) { historicalMultiUpgrade(t, true) }
func historicalMultiUpgrade(t *testing.T, upgradeDirect bool) {
	ctx := context.Background()
	root := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	all := DefaultMigrations()
	var v74, v75 []Migration
	for _, m := range all {
		if m.Version <= 74 {
			v74 = append(v74, m)
		}
		if m.Version <= 75 {
			v75 = append(v75, m)
		}
	}
	if err = ApplyMigrations(ctx, raw, v74); err != nil {
		t.Fatalf("install v74: %v", err)
	}
	// Seed historical SQL at the v74 boundary, without calling current stores
	// whose write contract includes v75 columns. Triggers and FKs stay enabled.
	stamp := formatTime(testNow)
	base := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	payload := `{ "version":1, "taskId":"historical-task", "goal":"historical goal", "constraints":[], "repositoryId":"historical-repo", "baseCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }`
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(id,name,created_at,updated_at) VALUES('historical-project','History',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('historical-repo','historical-project','repo','/historical/repo',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO ai_sessions(id,caller_scope,created_at,updated_at) VALUES('historical-session','historical-scope',?,?)`, []any{stamp, stamp}},
		{`UPDATE ai_sessions SET project_id='historical-project' WHERE id='historical-session'`, nil},
		{`INSERT INTO feature_areas(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at) VALUES('historical-area','historical-project','historical-session','History','historical goal','ACTIVE',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('historical-module','historical-project','core','Core',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO tasks(id,required_review_depth,module_id,project_id,repository_id,feature_area_id,goal,constraints_json,base_commit,status,phase,title,work_owner_id,problem_statement,created_at,updated_at) VALUES('historical-task','FAST','historical-module','historical-project','historical-repo','historical-area','historical goal','{"version":1,"items":[]}',?,'ACTIVE','REVIEW_REQUIRED','History',NULL,'historical goal',?,?)`, []any{base, stamp, stamp}},
		{`INSERT INTO reviews(id,project_id,feature_area_id,created_by_ai_session_id,repository_id,base_commit,task_id,ordinal,executor_kind,review_depth,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at) VALUES('review_historical','historical-project','historical-area','historical-session','historical-repo',?,'historical-task',1,'UPPER_LLM','FAST','REPOSITORY','','{"version":1,"paths":[]}','historical goal','Inspect',?)`, []any{base, stamp}},
		{`UPDATE tasks SET current_review_id='review_historical',phase='REVIEWING' WHERE id='historical-task'`, nil},
		{`INSERT INTO agent_executions(id,project_id,task_id,feature_area_id,kind,semantic_owner_kind,semantic_owner_id,provenance_kind,created_at) VALUES('historical-agent','historical-project','historical-task','historical-area','REVIEW','REVIEW','review_historical','NATIVE',?)`, []any{stamp}},
		{`INSERT INTO runs(id,project_id,feature_area_id,operation_kind,operation_id,agent_execution_id,task_id,review_id,attempt,status,purpose,created_at,updated_at) VALUES('historical-run','historical-project','historical-area','REVIEW','review_historical','historical-agent','historical-task','review_historical',1,'SUCCEEDED','SCOPE_REVIEW',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO worktrees(id,repository_id,run_id,path,branch,base_commit,status,created_at,updated_at) VALUES('historical-worktree','historical-repo','historical-run','/historical/worktree','history',?,'READY',?,?)`, []any{base, stamp, stamp}},
		{`INSERT INTO workspaces(id,project_id,repository_id,feature_area_id,base_commit,worktree_id,status,token_baseline_available,created_at,updated_at) VALUES('historical-workspace','historical-project','historical-repo','historical-area',?,'historical-worktree','READY',1,?,?)`, []any{base, stamp, stamp}},
		{`INSERT INTO review_grants(review_id,feature_area_id,project_id,repository_id,mode,initial_scope_kind,initial_scope_paths_json,selected_modules_json,created_at,run_id,generation,state,workspace_id,worktree_id,baseline) VALUES('review_historical','historical-area','historical-project','historical-repo','REPOSITORY','REPOSITORY','{"version":1,"paths":[]}','{"version":1,"items":[]}',?,'historical-run',3,'CLOSED','historical-workspace','historical-worktree',?)`, []any{stamp, base}},
		{`INSERT INTO task_definition_revisions(id,task_id,ordinal,payload_json,payload_sha256,created_at) VALUES('historical-revision','historical-task',1,?,?,?)`, []any{payload, digest, stamp}},
	}
	for _, stmt := range statements {
		if _, err = raw.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			t.Fatalf("historical seed %s: %v", stmt.query, err)
		}
	}
	old := worktree.Worktree{ID: "historical-worktree", RunID: "historical-run", RepositoryID: "historical-repo", BaseCommit: base}
	if err = ApplyMigrations(ctx, raw, v75); err != nil {
		t.Fatalf("upgrade v74 to v75: %v", err)
	}
	var creator, repo, gotBase string
	if err = raw.QueryRow(`SELECT run_id,repository_id,base_commit FROM worktrees WHERE id=?`, old.ID).Scan(&creator, &repo, &gotBase); err != nil || creator != old.RunID || repo != old.RepositoryID || gotBase != old.BaseCommit {
		t.Fatalf("historical worktree creator=%q repo=%q base=%q err=%v", creator, repo, gotBase, err)
	}
	var gotPayload, gotDigest string
	if err = raw.QueryRow(`SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id='historical-revision'`).Scan(&gotPayload, &gotDigest); err != nil || gotPayload != payload || gotDigest != digest {
		t.Fatalf("v1 bytes/hash rewritten: %q %q %v", gotPayload, gotDigest, err)
	}
	var workspaceRepo, workspaceBase, grantRepo, grantWorkspace, grantWorktree, grantBaseline, grantState string
	var protocol, generation int
	if err = raw.QueryRow(`SELECT repository_id,base_commit FROM workspaces WHERE id='historical-workspace'`).Scan(&workspaceRepo, &workspaceBase); err != nil || workspaceRepo != old.RepositoryID || workspaceBase != base {
		t.Fatalf("legacy workspace changed: %s %s %v", workspaceRepo, workspaceBase, err)
	}
	if err = raw.QueryRow(`SELECT repository_id,workspace_id,worktree_id,baseline,state,generation,protocol_version FROM review_grants WHERE review_id='review_historical'`).Scan(&grantRepo, &grantWorkspace, &grantWorktree, &grantBaseline, &grantState, &generation, &protocol); err != nil || grantRepo != old.RepositoryID || grantWorkspace != "historical-workspace" || grantWorktree != old.ID || grantBaseline != base || grantState != "CLOSED" || generation != 3 || protocol != 1 {
		t.Fatalf("legacy grant changed: %s %s %s %s %s %d %d %v", grantRepo, grantWorkspace, grantWorktree, grantBaseline, grantState, generation, protocol, err)
	}
	if err = ApplyMigrations(ctx, raw, v75); err != nil {
		t.Fatalf("idempotent migration reopen: %v", err)
	}
	if upgradeDirect {
		// The v75 history is raw immutable evidence, deliberately retaining its
		// original whitespace and unknown legacy payload fields.
		legacy := `{ "id":"old-result", "snapshot":"legacy-snapshot", "legacyExtension":true }`
		legacyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(legacy)))
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO decision_resolution_receipts(decision_id,project_id,decision_kind,subject_kind,subject_id,option,request_id,result_kind,result_id,resolved_at) VALUES('old-approval','historical-project','LEGACY_REVIEW','TASK_REVIEW_GATE','historical-task','APPROVE','old-approve-request','TASK','historical-task',?)`, []any{stamp}},
			{`INSERT INTO direct_file_task_authorizations(workflow_id,ai_session_id,project_id,feature_area_id,task_id,task_definition_revision_id,decision_id,implementation_mode,registration_kind,execution_scope_json,state,repository_id,workspace_id,worktree_id,canonical_path,root_identity,created_at,updated_at) VALUES('old-grant','historical-session','historical-project','historical-area','historical-task','historical-revision','old-approval','DIRECT_FILE','APPROVED_DEFINITION','{"kind":"REPOSITORY"}','CLOSED','historical-repo','historical-workspace','historical-worktree','/historical/worktree','old-inode',?,?)`, []any{stamp, stamp}},
			{`INSERT INTO direct_file_implementation_results(id,ai_session_id,project_id,task_id,revision_id,workflow_id,request_id,payload_sha256,result_json,created_at) VALUES('old-result','historical-session','historical-project','historical-task','historical-revision','old-grant','old-result-request',?,?,?)`, []any{legacyHash, legacy, stamp}},
			{`INSERT INTO direct_file_implementation_acceptances(ai_session_id,request_id,payload_sha256,result_id,created_at) VALUES('historical-session','old-accept-request',?,'old-result',?)`, []any{legacyHash, stamp}},
			{`INSERT INTO task_commit_receipts(task_id,feature_area_id,revision_id,verified_snapshot,workspace_id,workspace_path,branch,resulting_commit,completed_at) VALUES('historical-task','historical-area','historical-revision','legacy-snapshot','historical-workspace','/historical/worktree','history',?,?)`, []any{base, stamp}},
			{`INSERT INTO feature_area_commit_receipts(request_id,feature_area_id,repository_id,development_ref,source_head,expected_dev_head,resulting_dev_head,source_path,source_branch,target_path,completed_at) VALUES('old-merge-request','historical-area','historical-repo','refs/heads/dev',?,?,?,'/historical/worktree','history','/historical/repo',?)`, []any{base, base, base, stamp}},
		}
		for _, stmt := range statements {
			if _, err = raw.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
				t.Fatalf("v75 file/delivery seed: %v", err)
			}
		}
		if err = ApplyMigrations(ctx, raw, all); err != nil {
			t.Fatalf("v75 to v76: %v", err)
		}
		var got, hash, state string
		var version int
		if err = raw.QueryRow(`SELECT result_json,payload_sha256 FROM direct_file_implementation_results WHERE id='old-result'`).Scan(&got, &hash); err != nil || got != legacy || hash != legacyHash {
			t.Fatalf("v1 result changed %q %q %v", got, hash, err)
		}
		if err = raw.QueryRow(`SELECT state,protocol_version FROM direct_file_task_authorizations WHERE workflow_id='old-grant'`).Scan(&state, &version); err != nil || state != "CLOSED" || version != 1 {
			t.Fatalf("legacy grant promoted %s %d %v", state, version, err)
		}
		if err = raw.QueryRow(`SELECT resulting_dev_head,protocol_version FROM feature_area_commit_receipts WHERE request_id='old-merge-request'`).Scan(&got, &version); err != nil || got != base || version != 1 {
			t.Fatalf("legacy merge changed %s %d %v", got, version, err)
		}
		if err = raw.QueryRow(`SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id='historical-revision'`).Scan(&got, &hash); err != nil || got != payload || hash != digest {
			t.Fatal("v76 rewrote v1 revision")
		}
		var n int
		if err = raw.QueryRow(`SELECT (SELECT count(*) FROM direct_file_task_members)+(SELECT count(*) FROM task_commit_members)+(SELECT count(*) FROM feature_area_commit_members)`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("history invented v2 authority %d %v", n, err)
		}
		if err = ApplyMigrations(ctx, raw, all); err != nil {
			t.Fatal(err)
		}
	}
	var unique int
	if err = raw.QueryRow(`SELECT count(*) FROM pragma_index_list('worktrees') i WHERE i.[unique]=1 AND EXISTS(SELECT 1 FROM pragma_index_info(i.name) c WHERE c.name='run_id')`).Scan(&unique); err != nil {
		t.Fatal(err)
	}
	if unique != 0 {
		t.Fatalf("v75 retained unique worktree run ownership: %d", unique)
	}
	rows, err := raw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v75 left foreign-key violations")
	}
}
