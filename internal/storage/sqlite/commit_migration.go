package sqlite

const schemaV43Upgrade = `
ALTER TABLE tasks ADD COLUMN commit_sha TEXT;
ALTER TABLE tasks ADD COLUMN committed_at TEXT;
ALTER TABLE tasks ADD COLUMN merged_commit TEXT;
ALTER TABLE tasks ADD COLUMN merged_at TEXT;
ALTER TABLE feature_areas ADD COLUMN merged_commit TEXT;
ALTER TABLE feature_areas ADD COLUMN merged_at TEXT;
-- OBJECT table task_commit_receipts
CREATE TABLE task_commit_receipts (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id), feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 revision_id TEXT NOT NULL, verified_snapshot TEXT NOT NULL, pre_commit_head TEXT,
 resulting_commit TEXT, completed_at TEXT, last_error TEXT,
 workspace_id TEXT NOT NULL, workspace_path TEXT NOT NULL, branch TEXT NOT NULL
);
-- OBJECT table feature_area_commit_receipts
CREATE TABLE feature_area_commit_receipts (
 request_id TEXT PRIMARY KEY, feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 repository_id TEXT NOT NULL REFERENCES repositories(id), development_ref TEXT NOT NULL,
 source_head TEXT NOT NULL, expected_dev_head TEXT NOT NULL, resulting_dev_head TEXT,
 source_path TEXT NOT NULL, source_branch TEXT NOT NULL, target_path TEXT NOT NULL,
 completed_at TEXT, last_error TEXT
);
-- OBJECT index feature_area_commit_pending
CREATE UNIQUE INDEX feature_area_commit_pending ON feature_area_commit_receipts(feature_area_id) WHERE completed_at IS NULL;
-- OBJECT trigger commit_claim_fence
CREATE TRIGGER commit_claim_fence BEFORE INSERT ON feature_area_agent_claims
WHEN EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=NEW.feature_area_id AND task_type='DEVELOPMENT' AND status='SUCCEEDED' AND commit_sha IS NULL)
 OR EXISTS(SELECT 1 FROM task_commit_receipts WHERE feature_area_id=NEW.feature_area_id AND completed_at IS NULL)
 OR EXISTS(SELECT 1 FROM feature_area_commit_receipts WHERE feature_area_id=NEW.feature_area_id AND completed_at IS NULL)
BEGIN SELECT RAISE(ABORT,'GIT_FINALIZATION_PENDING'); END;
-- OBJECT trigger commit_task_creation_fence
CREATE TRIGGER commit_task_creation_fence BEFORE INSERT ON tasks WHEN NEW.task_type='DEVELOPMENT' AND (
 EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=NEW.feature_area_id AND task_type='DEVELOPMENT' AND status='SUCCEEDED' AND commit_sha IS NULL)
 OR EXISTS(SELECT 1 FROM task_commit_receipts WHERE feature_area_id=NEW.feature_area_id AND completed_at IS NULL)
 OR EXISTS(SELECT 1 FROM feature_area_commit_receipts WHERE feature_area_id=NEW.feature_area_id AND completed_at IS NULL))
BEGIN SELECT RAISE(ABORT,'GIT_FINALIZATION_PENDING'); END;
-- OBJECT index repository_commit_pending
CREATE UNIQUE INDEX repository_commit_pending ON feature_area_commit_receipts(repository_id,development_ref) WHERE completed_at IS NULL;
-- OBJECT trigger development_selection_commit_fence
CREATE TRIGGER development_selection_commit_fence BEFORE UPDATE ON development_cycles
WHEN EXISTS(SELECT 1 FROM feature_area_commit_receipts WHERE repository_id=OLD.repository_id AND development_ref=OLD.development_ref AND completed_at IS NULL)
BEGIN SELECT RAISE(ABORT,'GIT_FINALIZATION_PENDING'); END;
-- OBJECT trigger feature_area_git_command_fence
CREATE TRIGGER feature_area_git_command_fence BEFORE UPDATE OF status,current_workspace_id ON feature_areas
WHEN (NEW.status IS NOT OLD.status OR NEW.current_workspace_id IS NOT OLD.current_workspace_id) AND (
 EXISTS(SELECT 1 FROM task_commit_receipts WHERE feature_area_id=OLD.id AND completed_at IS NULL)
 OR EXISTS(SELECT 1 FROM feature_area_commit_receipts WHERE feature_area_id=OLD.id AND completed_at IS NULL))
BEGIN SELECT RAISE(ABORT,'GIT_FINALIZATION_PENDING'); END;
-- OBJECT trigger task_git_checkpoint_immutable
CREATE TRIGGER task_git_checkpoint_immutable BEFORE UPDATE OF commit_sha,committed_at,merged_commit,merged_at ON tasks
WHEN (OLD.commit_sha IS NOT NULL AND (NEW.commit_sha IS NOT OLD.commit_sha OR NEW.committed_at IS NOT OLD.committed_at))
 OR (OLD.merged_commit IS NOT NULL AND (NEW.merged_commit IS NOT OLD.merged_commit OR NEW.merged_at IS NOT OLD.merged_at))
 OR ((NEW.commit_sha IS NULL)<>(NEW.committed_at IS NULL)) OR ((NEW.merged_commit IS NULL)<>(NEW.merged_at IS NULL))
 OR (NEW.task_type<>'DEVELOPMENT' AND (NEW.commit_sha IS NOT NULL OR NEW.merged_commit IS NOT NULL))
 OR (NEW.merged_commit IS NOT NULL AND NEW.commit_sha IS NULL)
BEGIN SELECT RAISE(ABORT,'Task Git checkpoint is immutable and development-only'); END;
`
