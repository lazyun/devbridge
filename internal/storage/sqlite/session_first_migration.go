package sqlite

import (
	"context"
	"database/sql"
	"strings"
)

// Existing Session identities and timestamps are copied unchanged; historical
// caller scopes remain NULL. Dependent tables keep referencing ai_sessions.
func migrateSessionFirstV51(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND sql IS NOT NULL ORDER BY rowid`)
	if err != nil {
		return err
	}
	type trigger struct{ name, sql string }
	var triggers []trigger
	for rows.Next() {
		var v trigger
		if err := rows.Scan(&v.name, &v.sql); err != nil {
			rows.Close()
			return err
		}
		triggers = append(triggers, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Temporarily remove cross-table SQL so DROP/RENAME cannot leave a dangling
	// trigger reference. Restore every original definition except the deliberately
	// replaced NULL-aware Session binding constraint below.
	for _, v := range triggers {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(v.name, `"`, `""`)+`"`); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
CREATE TABLE ai_sessions_v51 (
 id TEXT PRIMARY KEY NOT NULL,
 project_id TEXT REFERENCES projects(id) CHECK(project_id IS NULL OR length(project_id)>0),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 caller_scope TEXT CHECK(caller_scope IS NULL OR length(caller_scope)>0)
);
INSERT INTO ai_sessions_v51(id,project_id,created_at,updated_at) SELECT id,project_id,created_at,updated_at FROM ai_sessions;
DROP TABLE ai_sessions;
ALTER TABLE ai_sessions_v51 RENAME TO ai_sessions;
CREATE INDEX ai_sessions_project_created ON ai_sessions(project_id,created_at,id);
CREATE UNIQUE INDEX ai_sessions_id_project ON ai_sessions(id,project_id);
CREATE TABLE ai_session_requests (
 caller_scope TEXT NOT NULL CHECK(length(caller_scope)>0),request_id TEXT NOT NULL CHECK(length(request_id)>0),
 operation_kind TEXT NOT NULL CHECK(operation_kind IN ('INITIALIZE','BIND_PROJECT')),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 ai_session_id TEXT NOT NULL,project_id TEXT,created_at TEXT NOT NULL,
 PRIMARY KEY(caller_scope,request_id),
 CHECK((operation_kind='INITIALIZE' AND project_id IS NULL) OR (operation_kind='BIND_PROJECT' AND project_id IS NOT NULL))
);
CREATE TRIGGER ai_sessions_starts_unbound BEFORE INSERT ON ai_sessions
WHEN NEW.project_id IS NOT NULL OR NEW.caller_scope IS NULL
BEGIN SELECT RAISE(ABORT,'Session initialization requires an unbound scoped Session'); END;
CREATE TRIGGER ai_session_requests_exact_owner BEFORE INSERT ON ai_session_requests
WHEN NOT EXISTS(SELECT 1 FROM ai_sessions s WHERE s.id=NEW.ai_session_id
 AND (s.caller_scope=NEW.caller_scope OR s.caller_scope IS NULL)
 AND ((NEW.operation_kind='INITIALIZE' AND s.project_id IS NULL)
 OR (NEW.operation_kind='BIND_PROJECT' AND s.project_id=NEW.project_id)))
BEGIN SELECT RAISE(ABORT,'Session request requires its exact current owner'); END;
CREATE TRIGGER ai_session_requests_immutable BEFORE UPDATE ON ai_session_requests BEGIN SELECT RAISE(ABORT,'Session request receipt is immutable'); END;
CREATE TRIGGER ai_session_requests_durable BEFORE DELETE ON ai_session_requests BEGIN SELECT RAISE(ABORT,'Session request receipt is durable'); END;
CREATE TRIGGER ai_sessions_identity_immutable BEFORE UPDATE OF id,caller_scope,created_at ON ai_sessions
WHEN NEW.id IS NOT OLD.id OR NEW.caller_scope IS NOT OLD.caller_scope OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT,'AI session identity is immutable'); END;
CREATE TRIGGER ai_sessions_project_immutable BEFORE UPDATE OF project_id ON ai_sessions
WHEN OLD.project_id IS NOT NULL AND NEW.project_id IS NOT OLD.project_id
BEGIN SELECT RAISE(ABORT,'AI session project is immutable'); END;
`)
	if err != nil {
		return err
	}
	for _, v := range triggers {
		if v.name == "ai_sessions_project_immutable" {
			continue
		}
		if _, err := tx.ExecContext(ctx, v.sql); err != nil {
			return err
		}
	}
	return nil
}
