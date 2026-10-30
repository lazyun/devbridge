package sqlite

import (
	"context"
	"database/sql"
)

func migrateDirectFileTaskV52(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE direct_file_task_authorizations (
 workflow_id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 caller_scope TEXT,
 project_id TEXT NOT NULL REFERENCES projects(id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 task_definition_revision_id TEXT NOT NULL UNIQUE REFERENCES task_definition_revisions(id),
 decision_id TEXT NOT NULL REFERENCES decision_resolution_receipts(decision_id),
 implementation_mode TEXT NOT NULL CHECK(implementation_mode='DIRECT_FILE'),
 registration_kind TEXT NOT NULL CHECK(registration_kind IN ('APPROVED_DEFINITION','EXPLICIT_CHANNEL_REGISTRATION')),
 execution_scope_json TEXT NOT NULL CHECK(json_valid(execution_scope_json)),
 state TEXT NOT NULL CHECK(state IN ('PREPARING','ACTIVE','CLOSED')),
 repository_id TEXT NOT NULL REFERENCES repositories(id),
 workspace_id TEXT REFERENCES workspaces(id),
 worktree_id TEXT REFERENCES worktrees(id),
 canonical_path TEXT,
 root_identity TEXT,
 failure_code TEXT NOT NULL DEFAULT '',
 failure_reason TEXT NOT NULL DEFAULT '',
 started_at TEXT,
 last_operation TEXT NOT NULL DEFAULT '',
 operation_count INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK(state<>'ACTIVE' OR (workspace_id IS NOT NULL AND worktree_id IS NOT NULL AND canonical_path IS NOT NULL AND root_identity IS NOT NULL))
);
CREATE UNIQUE INDEX direct_file_task_area_owner ON direct_file_task_authorizations(feature_area_id) WHERE state IN ('ACTIVE','PREPARING');
CREATE TRIGGER direct_file_task_identity_immutable BEFORE UPDATE ON direct_file_task_authorizations
WHEN NEW.workflow_id IS NOT OLD.workflow_id OR NEW.ai_session_id IS NOT OLD.ai_session_id OR NEW.caller_scope IS NOT OLD.caller_scope OR NEW.project_id IS NOT OLD.project_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.task_id IS NOT OLD.task_id OR NEW.task_definition_revision_id IS NOT OLD.task_definition_revision_id OR NEW.decision_id IS NOT OLD.decision_id OR NEW.implementation_mode IS NOT OLD.implementation_mode OR NEW.registration_kind IS NOT OLD.registration_kind OR NEW.execution_scope_json IS NOT OLD.execution_scope_json OR NEW.repository_id IS NOT OLD.repository_id OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT,'Direct File approval identity is immutable'); END;
CREATE TRIGGER direct_file_task_closed_final BEFORE UPDATE ON direct_file_task_authorizations
WHEN OLD.state='CLOSED' AND NEW.state<>'CLOSED'
BEGIN SELECT RAISE(ABORT,'Closed Direct File authorization cannot reopen'); END;
CREATE TRIGGER direct_file_task_pinned_root BEFORE UPDATE ON direct_file_task_authorizations
WHEN OLD.state='ACTIVE' AND (NEW.workspace_id IS NOT OLD.workspace_id OR NEW.worktree_id IS NOT OLD.worktree_id OR NEW.canonical_path IS NOT OLD.canonical_path OR NEW.root_identity IS NOT OLD.root_identity)
BEGIN SELECT RAISE(ABORT,'Active Direct File worktree identity is pinned'); END;
CREATE TRIGGER direct_file_task_no_delete BEFORE DELETE ON direct_file_task_authorizations
BEGIN SELECT RAISE(ABORT,'Direct File authorization history is durable'); END;
CREATE TRIGGER direct_file_task_lifecycle AFTER UPDATE OF status,phase,current_definition_revision_id ON tasks
WHEN NEW.status<>'ACTIVE' OR NEW.phase NOT IN ('IMPLEMENTATION_READY','IMPLEMENTING') OR NEW.current_definition_revision_id IS NOT OLD.current_definition_revision_id
BEGIN UPDATE direct_file_task_authorizations SET state='CLOSED',failure_code='TASK_AUTHORIZATION_EXPIRED',failure_reason='Task lifecycle or approved revision changed',updated_at=NEW.updated_at WHERE task_id=NEW.id AND state<>'CLOSED'; END;
CREATE TRIGGER direct_file_area_lifecycle AFTER UPDATE OF status,current_workspace_id ON feature_areas
WHEN NEW.status NOT IN ('DISCOVERING','ACTIVE') OR NEW.current_workspace_id IS NOT OLD.current_workspace_id
BEGIN UPDATE direct_file_task_authorizations SET state='CLOSED',failure_code='WORKSPACE_AUTHORIZATION_EXPIRED',failure_reason='FeatureArea or pinned workspace changed',updated_at=NEW.updated_at WHERE feature_area_id=NEW.id AND state='ACTIVE';
 UPDATE direct_file_task_authorizations SET state='CLOSED',failure_code='FEATURE_AREA_CLOSED',failure_reason='FeatureArea closed',updated_at=NEW.updated_at WHERE feature_area_id=NEW.id AND state='PREPARING' AND NEW.status NOT IN ('DISCOVERING','ACTIVE'); END;
`)
	return err
}
