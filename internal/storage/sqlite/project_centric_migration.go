package sqlite

// Version 73 adds resource membership and durable aggregate registration. It
// never rewrites historical definition bytes or approval receipts.
const projectCentricMigrationSQL = `
CREATE TABLE project_registration_receipts (
 request_id TEXT PRIMARY KEY, payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 result_json TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TRIGGER project_registration_receipt_immutable BEFORE UPDATE ON project_registration_receipts
BEGIN SELECT RAISE(ABORT,'registration receipt is immutable'); END;
CREATE TABLE workspace_members (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 repository_id TEXT NOT NULL REFERENCES repositories(id),
 identity_version INTEGER NOT NULL CHECK(identity_version=1),
 alias TEXT NOT NULL,
 origin_commit TEXT NOT NULL CHECK(length(origin_commit) IN (40,64)),
 origin_ref TEXT NOT NULL DEFAULT '', development_cycle_id TEXT NOT NULL DEFAULT '',
 worktree_id TEXT REFERENCES worktrees(id),
 preparation TEXT NOT NULL CHECK(preparation IN ('PENDING','READY','FAILED','UNKNOWN')),
 failure TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(workspace_id,repository_id), UNIQUE(workspace_id,alias)
);
INSERT INTO workspace_members(id,workspace_id,repository_id,identity_version,alias,origin_commit,worktree_id,preparation,created_at,updated_at)
 SELECT 'member_'||w.id,w.id,w.repository_id,1,w.repository_id,w.base_commit,w.worktree_id,
 CASE WHEN w.runtime_quarantined=1 OR w.status<>'READY' THEN 'UNKNOWN'
 WHEN w.worktree_id IS NULL THEN 'PENDING'
 WHEN t.status='READY' THEN 'READY' ELSE 'FAILED' END,w.created_at,w.updated_at
 FROM workspaces w LEFT JOIN worktrees t ON t.id=w.worktree_id;
UPDATE workspace_members SET failure='LEGACY_MEMBER_AVAILABILITY_UNCONFIRMED' WHERE preparation IN ('UNKNOWN','FAILED');
CREATE TRIGGER workspace_member_owner BEFORE INSERT ON workspace_members
WHEN NOT EXISTS(SELECT 1 FROM workspaces w JOIN repositories r ON r.project_id=w.project_id
 WHERE w.id=NEW.workspace_id AND r.id=NEW.repository_id AND w.runtime_quarantined=0)
BEGIN SELECT RAISE(ABORT,'member requires exact workspace repository ownership'); END;
CREATE TRIGGER workspace_member_identity_immutable BEFORE UPDATE ON workspace_members
WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.repository_id IS NOT OLD.repository_id
 OR NEW.identity_version IS NOT OLD.identity_version OR NEW.alias IS NOT OLD.alias OR NEW.origin_commit IS NOT OLD.origin_commit
 OR NEW.origin_ref IS NOT OLD.origin_ref OR NEW.development_cycle_id IS NOT OLD.development_cycle_id
 OR NEW.created_at IS NOT OLD.created_at OR (OLD.worktree_id IS NOT NULL AND NEW.worktree_id IS NOT OLD.worktree_id)
BEGIN SELECT RAISE(ABORT,'member identity and origin are immutable'); END;
CREATE TRIGGER workspace_member_worktree_owner BEFORE UPDATE OF worktree_id ON workspace_members
WHEN NEW.worktree_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM worktrees t JOIN runs r ON r.id=t.run_id JOIN workspaces w ON w.id=NEW.workspace_id
 WHERE t.id=NEW.worktree_id AND t.repository_id=NEW.repository_id AND t.base_commit=NEW.origin_commit
 AND r.project_id=w.project_id AND r.feature_area_id=w.feature_area_id)
BEGIN SELECT RAISE(ABORT,'member worktree ownership mismatch'); END;
CREATE TRIGGER workspace_member_insert_worktree_owner BEFORE INSERT ON workspace_members
WHEN NEW.worktree_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM worktrees t JOIN runs r ON r.id=t.run_id JOIN workspaces w ON w.id=NEW.workspace_id
 WHERE t.id=NEW.worktree_id AND t.repository_id=NEW.repository_id AND t.base_commit=NEW.origin_commit
 AND r.project_id=w.project_id AND r.feature_area_id=w.feature_area_id)
BEGIN SELECT RAISE(ABORT,'member worktree ownership mismatch'); END;
CREATE TRIGGER workspace_member_no_delete BEFORE DELETE ON workspace_members
BEGIN SELECT RAISE(ABORT,'workspace membership is retained as historical evidence'); END;
CREATE TRIGGER workspace_member_ready_insert BEFORE INSERT ON workspace_members
WHEN NEW.preparation='READY' AND NEW.worktree_id IS NULL
BEGIN SELECT RAISE(ABORT,'ready member requires a worktree'); END;
CREATE TRIGGER workspace_member_ready_update BEFORE UPDATE ON workspace_members
WHEN NEW.preparation='READY' AND NEW.worktree_id IS NULL
BEGIN SELECT RAISE(ABORT,'ready member requires a worktree'); END;
CREATE TRIGGER project_registration_receipt_no_delete BEFORE DELETE ON project_registration_receipts
BEGIN SELECT RAISE(ABORT,'registration receipt is retained as historical evidence'); END;
`
