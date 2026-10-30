package sqlite

import (
	"context"
	"database/sql"
)

// Associations deliberately retain dangling references after explicit data
// cleanup, so an exact old handle reports STALE rather than becoming UNBOUND.
// They contain no copied Session or workflow lifecycle state.
func migrateClientBindingsV50(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE client_binding_entries (
 selection_handle TEXT PRIMARY KEY NOT NULL,
 caller_scope TEXT NOT NULL CHECK(length(caller_scope)>0),
 presentation_request_id TEXT NOT NULL CHECK(length(presentation_request_id)>0),
 host_reference TEXT NOT NULL DEFAULT '',
 selection_id TEXT,
 ai_session_id TEXT,
 project_id TEXT,
 version INTEGER NOT NULL DEFAULT 1 CHECK(version IN (1,2)),
 created_at TEXT NOT NULL,
 bound_at TEXT,
 UNIQUE(caller_scope,presentation_request_id),
 CHECK((version=1 AND selection_id IS NULL AND ai_session_id IS NULL AND project_id IS NULL AND bound_at IS NULL)
  OR (version=2 AND selection_id IS NOT NULL AND ai_session_id IS NOT NULL AND project_id IS NOT NULL AND bound_at IS NOT NULL))
);
CREATE INDEX client_binding_host_reference ON client_binding_entries(caller_scope,host_reference) WHERE host_reference<>'';
CREATE TRIGGER client_binding_starts_unbound BEFORE INSERT ON client_binding_entries
WHEN NEW.version<>1
BEGIN SELECT RAISE(ABORT,'Client binding entry must start unbound'); END;
CREATE TRIGGER client_binding_identity_immutable BEFORE UPDATE ON client_binding_entries
WHEN OLD.version=2 OR NEW.selection_handle IS NOT OLD.selection_handle OR NEW.caller_scope IS NOT OLD.caller_scope
 OR NEW.presentation_request_id IS NOT OLD.presentation_request_id OR NEW.host_reference IS NOT OLD.host_reference
 OR NEW.created_at IS NOT OLD.created_at OR NEW.version<>2
BEGIN SELECT RAISE(ABORT,'Client binding identity is immutable'); END;
CREATE TRIGGER client_binding_requires_consumed_selection BEFORE UPDATE ON client_binding_entries
WHEN NEW.version=2 AND NOT EXISTS (
 SELECT 1 FROM ai_project_selections c JOIN ai_sessions s ON s.id=c.ai_session_id JOIN projects p ON p.id=s.project_id
 WHERE c.id=NEW.selection_id AND c.status='CONSUMED' AND c.ai_session_id=NEW.ai_session_id
  AND c.project_id=NEW.project_id AND s.project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'Client binding requires its exact consumed selection'); END;
`)
	return err
}
