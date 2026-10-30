package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// v76 keeps existing workflow/result/delivery parents and extends their exact
// per-member evidence. Historical v1 payloads, receipts and scalar roots remain.
func migrateMultiRepositoryDirectV76(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name IN ('direct_file_task_authorizations','feature_area_commit_receipts') AND sql IS NOT NULL)`)
	if err != nil {
		return err
	}
	var objects []schemaObject
	for rows.Next() {
		var o schemaObject
		if err = rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range objects {
		if _, err = tx.ExecContext(ctx, "DROP "+strings.ToUpper(o.kind)+" "+sqlIdentifier(o.name)); err != nil {
			return err
		}
	}
	for _, table := range []string{"direct_file_task_authorizations", "feature_area_commit_receipts"} {
		var ddl string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return err
		}
		old := "repository_id TEXT NOT NULL REFERENCES repositories(id)"
		if strings.Count(ddl, old) != 1 {
			return fmt.Errorf("unexpected v75 %s repository schema", table)
		}
		ddl = strings.Replace(ddl, old, "repository_id TEXT REFERENCES repositories(id)", 1)
		if table == "direct_file_task_authorizations" {
			ddl = strings.Replace(ddl, "state IN ('PREPARING','ACTIVE','CLOSED')", "state IN ('PREPARING','ACTIVE','CLOSED','RECOVERY_REQUIRED')", 1)
			ddl = strings.Replace(ddl, "CHECK(state<>'ACTIVE' OR (workspace_id IS NOT NULL AND worktree_id IS NOT NULL AND canonical_path IS NOT NULL AND root_identity IS NOT NULL))", "CHECK(state<>'ACTIVE' OR (workspace_id IS NOT NULL AND (repository_id IS NULL OR (worktree_id IS NOT NULL AND canonical_path IS NOT NULL AND root_identity IS NOT NULL))))", 1)
		}
		ddl = "CREATE TABLE " + sqlIdentifier("_v76_"+table) + " " + ddl[strings.Index(ddl, "("):]
		for _, stmt := range []string{ddl, "INSERT INTO " + sqlIdentifier("_v76_"+table) + " SELECT * FROM " + sqlIdentifier(table), "DROP TABLE " + sqlIdentifier(table), "ALTER TABLE " + sqlIdentifier("_v76_"+table) + " RENAME TO " + sqlIdentifier(table)} {
			if _, err = tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, multiRepositoryDirectSchema); err != nil {
		return err
	}
	for _, o := range objects {
		if strings.Contains(o.sql, "direct_file_task_authorizations") {
			o.sql = strings.ReplaceAll(o.sql, "state IN ('ACTIVE','PREPARING')", "state IN ('ACTIVE','PREPARING','RECOVERY_REQUIRED')")
			o.sql = strings.ReplaceAll(o.sql, "state IN ('PREPARING','ACTIVE')", "state IN ('PREPARING','ACTIVE','RECOVERY_REQUIRED')")
		}
		if o.name == "commit_claim_fence" || o.name == "commit_task_creation_fence" {
			o.sql = strings.ReplaceAll(o.sql, "status='SUCCEEDED' AND commit_sha IS NULL)", "status='SUCCEEDED' AND commit_sha IS NULL AND NOT EXISTS(SELECT 1 FROM task_commit_receipts done WHERE done.task_id=tasks.id AND done.protocol_version=2 AND done.completed_at IS NOT NULL))")
		}
		if _, err = tx.ExecContext(ctx, o.sql); err != nil {
			return fmt.Errorf("v76 recreate %s: %w", o.name, err)
		}
	}
	_, err = tx.ExecContext(ctx, multiRepositoryDirectGuards)
	return err
}

const multiRepositoryDirectSchema = `
ALTER TABLE direct_file_task_authorizations ADD COLUMN protocol_version INTEGER NOT NULL DEFAULT 1 CHECK(protocol_version IN (1,2));
ALTER TABLE direct_file_task_authorizations ADD COLUMN repository_targets_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(repository_targets_json));
ALTER TABLE direct_file_task_authorizations ADD COLUMN runtime_epoch TEXT NOT NULL DEFAULT '';
ALTER TABLE direct_file_task_authorizations ADD COLUMN generation INTEGER NOT NULL DEFAULT 1 CHECK(generation>0);
ALTER TABLE direct_file_task_authorizations ADD COLUMN start_request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE direct_file_task_authorizations ADD COLUMN confirmation_intent_id TEXT NOT NULL DEFAULT '';
CREATE TABLE direct_file_task_members (
 workflow_id TEXT NOT NULL REFERENCES direct_file_task_authorizations(workflow_id),repository_id TEXT NOT NULL REFERENCES repositories(id),
 member_id TEXT NOT NULL,member_version INTEGER NOT NULL CHECK(member_version>0),target_json TEXT NOT NULL CHECK(json_valid(target_json)),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id),worktree_id TEXT REFERENCES worktrees(id),canonical_path TEXT NOT NULL DEFAULT '',root_identity TEXT NOT NULL DEFAULT '',
 origin_commit TEXT NOT NULL,start_head TEXT NOT NULL DEFAULT '',start_snapshot TEXT NOT NULL DEFAULT '',
 preparation TEXT NOT NULL DEFAULT 'PENDING' CHECK(preparation IN ('PENDING','READY','FAILED')),failure TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(workflow_id,repository_id),UNIQUE(workflow_id,member_id)
);
CREATE TABLE direct_file_start_receipts (
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),request_id TEXT NOT NULL,payload_sha256 TEXT NOT NULL,payload_json TEXT NOT NULL,
 workflow_id TEXT NOT NULL REFERENCES direct_file_task_authorizations(workflow_id),action TEXT NOT NULL,runtime_epoch TEXT NOT NULL,generation INTEGER NOT NULL,
 receipt_json TEXT NOT NULL DEFAULT '{}',created_at TEXT NOT NULL,PRIMARY KEY(ai_session_id,request_id)
);
CREATE TABLE direct_file_operation_receipts (
 workflow_id TEXT NOT NULL REFERENCES direct_file_task_authorizations(workflow_id),request_id TEXT NOT NULL,repository_id TEXT NOT NULL,member_id TEXT NOT NULL,
 payload_sha256 TEXT NOT NULL,payload_json TEXT NOT NULL,operation TEXT NOT NULL,path TEXT NOT NULL,destination TEXT NOT NULL DEFAULT '',
 pre_state_json TEXT NOT NULL DEFAULT '{}',post_state_json TEXT NOT NULL DEFAULT '{}',
 state TEXT NOT NULL CHECK(state IN ('PENDING','UNKNOWN','COMPLETED','FAILED')),result_json TEXT NOT NULL DEFAULT '{}',error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,
 PRIMARY KEY(workflow_id,request_id),FOREIGN KEY(workflow_id,repository_id) REFERENCES direct_file_task_members(workflow_id,repository_id)
);
ALTER TABLE task_commit_receipts ADD COLUMN protocol_version INTEGER NOT NULL DEFAULT 1 CHECK(protocol_version IN (1,2));
ALTER TABLE task_commit_receipts ADD COLUMN result_id TEXT NOT NULL DEFAULT '';
CREATE TABLE task_commit_members (
 task_id TEXT NOT NULL REFERENCES task_commit_receipts(task_id),repository_id TEXT NOT NULL REFERENCES repositories(id),member_id TEXT NOT NULL,member_version INTEGER NOT NULL,worktree_id TEXT NOT NULL REFERENCES worktrees(id),workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),result_id TEXT NOT NULL REFERENCES direct_file_implementation_results(id),origin_commit TEXT NOT NULL,
 start_snapshot TEXT NOT NULL,verified_snapshot TEXT NOT NULL,expected_head TEXT NOT NULL,commit_tree TEXT NOT NULL DEFAULT '',workspace_path TEXT NOT NULL,branch TEXT NOT NULL,root_identity TEXT NOT NULL,changed_paths_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('PENDING','UNKNOWN','COMPLETED','NO_CHANGE','FAILED')),resulting_commit TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',completed_at TEXT,
 PRIMARY KEY(task_id,repository_id)
);
ALTER TABLE feature_area_commit_receipts ADD COLUMN protocol_version INTEGER NOT NULL DEFAULT 1 CHECK(protocol_version IN (1,2));
ALTER TABLE feature_area_commit_receipts ADD COLUMN plan_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE feature_area_commit_receipts ADD COLUMN plan_json TEXT NOT NULL DEFAULT '';
CREATE TABLE feature_area_commit_members (
 request_id TEXT NOT NULL REFERENCES feature_area_commit_receipts(request_id),repository_id TEXT NOT NULL REFERENCES repositories(id),member_id TEXT NOT NULL,member_version INTEGER NOT NULL,worktree_id TEXT NOT NULL REFERENCES worktrees(id),
 source_path TEXT NOT NULL,source_branch TEXT NOT NULL,source_head TEXT NOT NULL,expected_dev_head TEXT NOT NULL,development_ref TEXT NOT NULL,development_cycle_id TEXT NOT NULL,target_path TEXT NOT NULL,task_commits_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('PENDING','UNKNOWN','COMPLETED','NO_CHANGE','FAILED')),resulting_dev_head TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',completed_at TEXT,
 PRIMARY KEY(request_id,repository_id)
);
`
const multiRepositoryDirectGuards = `
CREATE TRIGGER direct_file_v2_identity BEFORE UPDATE ON direct_file_task_authorizations
WHEN NEW.protocol_version IS NOT OLD.protocol_version OR NEW.repository_targets_json IS NOT OLD.repository_targets_json OR NEW.start_request_id IS NOT OLD.start_request_id OR NEW.confirmation_intent_id IS NOT OLD.confirmation_intent_id
BEGIN SELECT RAISE(ABORT,'Direct File start identity is immutable'); END;
CREATE TRIGGER direct_file_v2_epoch AFTER UPDATE OF epoch_id ON runtime_authority WHEN NEW.epoch_id<>OLD.epoch_id
BEGIN UPDATE direct_file_task_authorizations SET state='RECOVERY_REQUIRED',failure_code='RUNTIME_EPOCH_CHANGED',failure_reason='Explicit recovery confirmation required' WHERE protocol_version=2 AND state IN ('ACTIVE','PREPARING') AND runtime_epoch<>NEW.epoch_id; END;
CREATE TRIGGER direct_file_v2_member_identity BEFORE UPDATE ON direct_file_task_members
WHEN NEW.workflow_id IS NOT OLD.workflow_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.member_id IS NOT OLD.member_id OR NEW.member_version IS NOT OLD.member_version OR NEW.target_json IS NOT OLD.target_json OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.origin_commit IS NOT OLD.origin_commit OR (OLD.worktree_id IS NOT NULL AND NEW.worktree_id IS NOT OLD.worktree_id) OR (OLD.start_snapshot<>'' AND (NEW.start_snapshot IS NOT OLD.start_snapshot OR NEW.start_head IS NOT OLD.start_head OR NEW.canonical_path IS NOT OLD.canonical_path OR NEW.root_identity IS NOT OLD.root_identity))
BEGIN SELECT RAISE(ABORT,'Direct File member identity is immutable'); END;
CREATE TRIGGER direct_file_v2_member_durable BEFORE DELETE ON direct_file_task_members BEGIN SELECT RAISE(ABORT,'Direct File member history is durable'); END;
CREATE TRIGGER direct_file_v2_operation_identity BEFORE UPDATE ON direct_file_operation_receipts
WHEN NEW.workflow_id IS NOT OLD.workflow_id OR NEW.request_id IS NOT OLD.request_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.member_id IS NOT OLD.member_id OR NEW.payload_sha256 IS NOT OLD.payload_sha256 OR NEW.payload_json IS NOT OLD.payload_json OR NEW.operation IS NOT OLD.operation OR NEW.path IS NOT OLD.path OR NEW.destination IS NOT OLD.destination OR NEW.created_at IS NOT OLD.created_at OR (OLD.state='COMPLETED' AND (NEW.state<>OLD.state OR NEW.result_json<>OLD.result_json))
BEGIN SELECT RAISE(ABORT,'Direct File mutation receipt is immutable'); END;
CREATE TRIGGER direct_file_v2_operation_durable BEFORE DELETE ON direct_file_operation_receipts BEGIN SELECT RAISE(ABORT,'Direct File mutation history is durable'); END;
CREATE TRIGGER direct_file_v2_start_durable BEFORE DELETE ON direct_file_start_receipts BEGIN SELECT RAISE(ABORT,'Direct File start history is durable'); END;
CREATE TRIGGER task_commit_member_identity BEFORE UPDATE ON task_commit_members
WHEN NEW.task_id IS NOT OLD.task_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.member_id IS NOT OLD.member_id OR NEW.member_version IS NOT OLD.member_version OR NEW.worktree_id IS NOT OLD.worktree_id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.revision_id IS NOT OLD.revision_id OR NEW.result_id IS NOT OLD.result_id OR NEW.origin_commit IS NOT OLD.origin_commit OR NEW.start_snapshot IS NOT OLD.start_snapshot OR NEW.verified_snapshot IS NOT OLD.verified_snapshot OR NEW.expected_head IS NOT OLD.expected_head OR NEW.workspace_path IS NOT OLD.workspace_path OR NEW.branch IS NOT OLD.branch OR NEW.root_identity IS NOT OLD.root_identity OR NEW.changed_paths_json IS NOT OLD.changed_paths_json OR (OLD.commit_tree<>'' AND NEW.commit_tree<>OLD.commit_tree) OR (OLD.state IN ('COMPLETED','NO_CHANGE') AND (NEW.state<>OLD.state OR NEW.resulting_commit<>OLD.resulting_commit OR NEW.completed_at IS NOT OLD.completed_at))
BEGIN SELECT RAISE(ABORT,'Task member delivery intent is immutable'); END;
CREATE TRIGGER task_commit_member_durable BEFORE DELETE ON task_commit_members BEGIN SELECT RAISE(ABORT,'Task member delivery history is durable'); END;
CREATE TRIGGER area_commit_member_identity BEFORE UPDATE ON feature_area_commit_members
WHEN NEW.request_id IS NOT OLD.request_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.member_id IS NOT OLD.member_id OR NEW.member_version IS NOT OLD.member_version OR NEW.worktree_id IS NOT OLD.worktree_id OR NEW.source_path IS NOT OLD.source_path OR NEW.source_branch IS NOT OLD.source_branch OR NEW.source_head IS NOT OLD.source_head OR NEW.expected_dev_head IS NOT OLD.expected_dev_head OR NEW.development_ref IS NOT OLD.development_ref OR NEW.development_cycle_id IS NOT OLD.development_cycle_id OR NEW.target_path IS NOT OLD.target_path OR NEW.task_commits_json IS NOT OLD.task_commits_json OR (OLD.state IN ('COMPLETED','NO_CHANGE') AND (NEW.state<>OLD.state OR NEW.resulting_dev_head<>OLD.resulting_dev_head OR NEW.completed_at IS NOT OLD.completed_at))
BEGIN SELECT RAISE(ABORT,'FeatureArea member delivery intent is immutable'); END;
CREATE TRIGGER area_commit_member_durable BEFORE DELETE ON feature_area_commit_members BEGIN SELECT RAISE(ABORT,'FeatureArea member delivery history is durable'); END;
CREATE TRIGGER direct_file_v2_start_identity BEFORE UPDATE ON direct_file_start_receipts
WHEN NEW.ai_session_id IS NOT OLD.ai_session_id OR NEW.request_id IS NOT OLD.request_id OR NEW.payload_sha256 IS NOT OLD.payload_sha256 OR NEW.payload_json IS NOT OLD.payload_json OR NEW.workflow_id IS NOT OLD.workflow_id OR NEW.action IS NOT OLD.action OR NEW.runtime_epoch IS NOT OLD.runtime_epoch OR NEW.generation IS NOT OLD.generation OR NEW.created_at IS NOT OLD.created_at OR (json_extract(OLD.receipt_json,'$.state') IN ('ACTIVE','CLOSED') AND NEW.receipt_json<>OLD.receipt_json)
BEGIN SELECT RAISE(ABORT,'Direct File start receipt identity is immutable'); END;
CREATE TRIGGER task_commit_v2_identity BEFORE UPDATE ON task_commit_receipts
WHEN NEW.protocol_version IS NOT OLD.protocol_version OR NEW.result_id IS NOT OLD.result_id OR (OLD.protocol_version=2 AND (NEW.task_id IS NOT OLD.task_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.revision_id IS NOT OLD.revision_id OR NEW.verified_snapshot IS NOT OLD.verified_snapshot OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.workspace_path IS NOT OLD.workspace_path OR NEW.branch IS NOT OLD.branch))
BEGIN SELECT RAISE(ABORT,'Task delivery identity is immutable'); END;
CREATE TRIGGER task_commit_v2_complete BEFORE UPDATE OF completed_at ON task_commit_receipts
WHEN NEW.protocol_version=2 AND NEW.completed_at IS NOT NULL AND (NOT EXISTS(SELECT 1 FROM task_commit_members WHERE task_id=NEW.task_id) OR EXISTS(SELECT 1 FROM task_commit_members WHERE task_id=NEW.task_id AND state NOT IN ('COMPLETED','NO_CHANGE')) OR (SELECT count(*) FROM task_commit_members WHERE task_id=NEW.task_id) <> (SELECT json_array_length(result_json,'$.members') FROM direct_file_implementation_results WHERE id=NEW.result_id))
BEGIN SELECT RAISE(ABORT,'All Task delivery members must complete'); END;
CREATE TRIGGER area_commit_v2_identity BEFORE UPDATE ON feature_area_commit_receipts
WHEN NEW.protocol_version IS NOT OLD.protocol_version OR NEW.plan_sha256 IS NOT OLD.plan_sha256 OR NEW.plan_json IS NOT OLD.plan_json OR (OLD.protocol_version=2 AND (NEW.request_id IS NOT OLD.request_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.repository_id IS NOT OLD.repository_id))
BEGIN SELECT RAISE(ABORT,'FeatureArea delivery plan is immutable'); END;
CREATE TRIGGER area_commit_v2_complete BEFORE UPDATE OF completed_at ON feature_area_commit_receipts
WHEN NEW.protocol_version=2 AND NEW.completed_at IS NOT NULL AND (NOT EXISTS(SELECT 1 FROM feature_area_commit_members WHERE request_id=NEW.request_id) OR EXISTS(SELECT 1 FROM feature_area_commit_members WHERE request_id=NEW.request_id AND state NOT IN ('COMPLETED','NO_CHANGE')) OR (SELECT count(*) FROM feature_area_commit_members WHERE request_id=NEW.request_id) <> json_array_length(NEW.plan_json,'$.members'))
BEGIN SELECT RAISE(ABORT,'All FeatureArea delivery members must complete'); END;
CREATE TRIGGER area_commit_v2_parent BEFORE INSERT ON feature_area_commit_receipts WHEN NEW.protocol_version=2 AND (NEW.repository_id IS NOT NULL OR NEW.development_ref<>'' OR NEW.source_head<>'' OR NEW.expected_dev_head<>'' OR NEW.source_path<>'' OR NEW.source_branch<>'' OR NEW.target_path<>'' OR NOT json_valid(NEW.plan_json) OR NEW.plan_sha256='')
BEGIN SELECT RAISE(ABORT,'Multi-repository delivery requires exact member plan'); END;

CREATE TRIGGER direct_file_v2_parent BEFORE INSERT ON direct_file_task_authorizations
WHEN NEW.protocol_version=2 AND (NEW.repository_id IS NOT NULL OR NEW.worktree_id IS NOT NULL OR NEW.canonical_path IS NOT NULL OR NEW.root_identity IS NOT NULL OR NEW.workspace_id IS NULL OR NEW.start_request_id='' OR NEW.confirmation_intent_id='' OR NEW.runtime_epoch='' OR json_array_length(NEW.repository_targets_json)=0)
BEGIN SELECT RAISE(ABORT,'Multi-repository Direct File requires frozen member authority'); END;
CREATE TRIGGER direct_file_v2_member_owner BEFORE INSERT ON direct_file_task_members
WHEN NOT EXISTS(SELECT 1 FROM direct_file_task_authorizations g JOIN workspace_members m ON m.workspace_id=g.workspace_id AND m.repository_id=NEW.repository_id JOIN json_each(g.repository_targets_json) j ON json_extract(j.value,'$.repositoryId')=NEW.repository_id WHERE g.workflow_id=NEW.workflow_id AND g.protocol_version=2 AND m.id=NEW.member_id AND m.identity_version=NEW.member_version AND m.origin_commit=NEW.origin_commit AND NEW.workspace_id=g.workspace_id AND json_extract(j.value,'$.memberId')=NEW.member_id AND json_extract(j.value,'$.memberVersion')=NEW.member_version AND json_extract(j.value,'$.originCommit')=NEW.origin_commit AND json(NEW.target_json)=json(j.value))
BEGIN SELECT RAISE(ABORT,'Direct File member must match frozen workspace authority'); END;
CREATE TRIGGER task_commit_member_owner BEFORE INSERT ON task_commit_members
WHEN NOT EXISTS(SELECT 1 FROM task_commit_receipts p JOIN direct_file_implementation_results r ON r.id=p.result_id JOIN json_each(r.result_json,'$.members') j ON json_extract(j.value,'$.repositoryId')=NEW.repository_id WHERE p.task_id=NEW.task_id AND p.protocol_version=2 AND r.task_id=NEW.task_id AND r.revision_id=NEW.revision_id AND r.id=NEW.result_id AND json_extract(j.value,'$.memberId')=NEW.member_id AND json_extract(j.value,'$.memberVersion')=NEW.member_version AND json_extract(j.value,'$.worktreeId')=NEW.worktree_id AND json_extract(j.value,'$.endSnapshot')=NEW.verified_snapshot)
BEGIN SELECT RAISE(ABORT,'Task delivery member requires accepted result evidence'); END;
CREATE TRIGGER area_commit_member_owner BEFORE INSERT ON feature_area_commit_members
WHEN NOT EXISTS(SELECT 1 FROM feature_area_commit_receipts p JOIN json_each(p.plan_json,'$.members') j ON json_extract(j.value,'$.repositoryId')=NEW.repository_id WHERE p.request_id=NEW.request_id AND p.protocol_version=2 AND json_extract(j.value,'$.memberId')=NEW.member_id AND json_extract(j.value,'$.memberVersion')=NEW.member_version AND json_extract(j.value,'$.sourceHead')=NEW.source_head AND json_extract(j.value,'$.developmentRef')=NEW.development_ref AND json_extract(j.value,'$.expectedDevHead')=NEW.expected_dev_head AND json(json_extract(j.value,'$.taskCommits'))=json(NEW.task_commits_json))
BEGIN SELECT RAISE(ABORT,'FeatureArea delivery member requires exact confirmed plan'); END;

`
