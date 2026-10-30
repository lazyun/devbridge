// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

// Additive migration: immutable definition intent and per-attempt repository
// provenance have separate lifetimes. Historical rows are never rewritten.
const schemaV29 = `
-- Each immutable Preparation may retain its own pending confirmation.
DROP INDEX task_plans_one_open_per_task;
DROP TRIGGER task_preparations_current_definition;
CREATE TRIGGER task_preparations_current_definition BEFORE INSERT ON task_preparations
WHEN NEW.task_definition_revision_id IS NULL OR NOT EXISTS(
 SELECT 1 FROM tasks t JOIN task_definition_revisions d ON d.task_id=t.id JOIN feature_areas f ON f.id=t.feature_area_id
 WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND t.repository_id=NEW.repository_id
 AND t.legacy_read_only=0 AND f.legacy_read_only=0 AND f.status='ACTIVE' AND d.id=NEW.task_definition_revision_id
 AND json_extract(d.payload_json,'$.repositoryId')=NEW.repository_id
 AND json_extract(d.payload_json,'$.featureAreaId')=NEW.feature_area_id
 AND json_extract(d.payload_json,'$.projectId')=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'Preparation requires exact Task definition and ACTIVE FeatureArea'); END;
CREATE TRIGGER task_preparations_one_live BEFORE INSERT ON task_preparations
WHEN NEW.status IN ('DRAFT','PREPARING') AND EXISTS(SELECT 1 FROM task_preparations WHERE task_id=NEW.task_id AND status IN ('DRAFT','PREPARING'))
BEGIN SELECT RAISE(ABORT,'Task already has a live Preparation'); END;

DROP TRIGGER workspace_runtime_quarantine_immutable;
DROP TRIGGER workspaces_task_immutable;
DROP TRIGGER workspaces_worktree_immutable;
DROP TRIGGER runs_workspace_owner_guard;
DROP TRIGGER workspaces_review_immutable;
DROP TRIGGER workspaces_legacy_thread_immutable;
DROP TRIGGER workspaces_no_current_thread;
CREATE TABLE workspaces_v29 (
 id TEXT PRIMARY KEY,
 task_id TEXT REFERENCES tasks(id),
 worktree_id TEXT UNIQUE REFERENCES worktrees(id),
 legacy_thread_id TEXT,
 status TEXT NOT NULL CHECK(status IN ('READY','CONTINUITY_UNAVAILABLE')),
 token_baseline_available INTEGER NOT NULL CHECK(token_baseline_available IN (0,1)),
 token_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_input_tokens >= 0),
 token_cached_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_cached_input_tokens >= 0),
 token_cache_write_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_cache_write_input_tokens >= 0),
 token_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_output_tokens >= 0),
 token_reasoning_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_reasoning_output_tokens >= 0),
 token_total_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_total_tokens >= 0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL, runtime_quarantined INTEGER NOT NULL DEFAULT 0 CHECK(runtime_quarantined IN (0,1)),
 review_id TEXT UNIQUE REFERENCES reviews(id),
 preparation_id TEXT UNIQUE REFERENCES task_preparations(id),
 CHECK((task_id IS NOT NULL AND review_id IS NULL) OR (task_id IS NULL AND review_id IS NOT NULL)),
 UNIQUE(id,task_id)
);
INSERT INTO workspaces_v29(id,task_id,worktree_id,legacy_thread_id,status,token_baseline_available,token_input_tokens,token_cached_input_tokens,token_cache_write_input_tokens,token_output_tokens,token_reasoning_output_tokens,token_total_tokens,created_at,updated_at,runtime_quarantined,review_id) SELECT id,task_id,worktree_id,legacy_thread_id,status,token_baseline_available,token_input_tokens,token_cached_input_tokens,token_cache_write_input_tokens,token_output_tokens,token_reasoning_output_tokens,token_total_tokens,created_at,updated_at,runtime_quarantined,review_id FROM workspaces;
DROP TABLE workspaces;
ALTER TABLE workspaces_v29 RENAME TO workspaces;
CREATE INDEX workspaces_status_updated ON workspaces(status,updated_at,id);
CREATE UNIQUE INDEX workspaces_legacy_task ON workspaces(task_id) WHERE preparation_id IS NULL;
CREATE TRIGGER workspace_runtime_quarantine_immutable BEFORE UPDATE OF runtime_quarantined ON workspaces
WHEN OLD.runtime_quarantined=1 AND NEW.runtime_quarantined<>1
BEGIN SELECT RAISE(ABORT,'abandoned runtime workspace must remain isolated'); END;
CREATE TRIGGER workspaces_task_immutable
BEFORE UPDATE OF task_id ON workspaces
WHEN NEW.task_id <> OLD.task_id
BEGIN
 SELECT RAISE(ABORT, 'workspace task is immutable');
END;
CREATE TRIGGER workspaces_worktree_immutable
BEFORE UPDATE OF worktree_id ON workspaces
WHEN OLD.worktree_id IS NOT NULL AND (NEW.worktree_id IS NULL OR NEW.worktree_id <> OLD.worktree_id)
BEGIN
 SELECT RAISE(ABORT, 'workspace worktree is immutable');
END;
CREATE TRIGGER runs_workspace_owner_guard BEFORE UPDATE OF workspace_id ON runs
WHEN NEW.workspace_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM workspaces w WHERE w.id=NEW.workspace_id AND ((NEW.operation_kind='REVIEW' AND w.review_id=NEW.review_id AND w.task_id IS NULL) OR (NEW.operation_kind<>'REVIEW' AND w.task_id=NEW.task_id AND w.review_id IS NULL)))
BEGIN SELECT RAISE(ABORT,'Run workspace must match operation environment'); END;
CREATE TRIGGER workspaces_review_immutable BEFORE UPDATE OF review_id ON workspaces WHEN NEW.review_id IS NOT OLD.review_id
BEGIN SELECT RAISE(ABORT,'Workspace Review owner is immutable'); END;
CREATE TRIGGER workspaces_legacy_thread_immutable BEFORE UPDATE OF legacy_thread_id ON workspaces WHEN NEW.legacy_thread_id IS NOT OLD.legacy_thread_id
BEGIN SELECT RAISE(ABORT,'legacy Workspace thread is audit history'); END;
CREATE TRIGGER workspaces_no_current_thread BEFORE INSERT ON workspaces WHEN NEW.legacy_thread_id IS NOT NULL
BEGIN SELECT RAISE(ABORT,'Workspace cannot own canonical Agent thread'); END;
CREATE TRIGGER workspaces_preparation_owner BEFORE INSERT ON workspaces
WHEN NEW.preparation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM task_preparations WHERE id=NEW.preparation_id AND task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT,'Workspace requires exact Preparation Task owner'); END;
CREATE TRIGGER workspaces_preparation_immutable BEFORE UPDATE OF preparation_id ON workspaces
WHEN NEW.preparation_id IS NOT OLD.preparation_id
BEGIN SELECT RAISE(ABORT,'Workspace Preparation is immutable'); END;
`
