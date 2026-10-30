package sqlite

import (
	"context"
	"database/sql"
	"strings"
)

// V57 makes current operator provenance distinct from historical AI ownership.
// The runner disables foreign keys during this transactional table rebuild;
// all old rows, indexes and triggers are preserved, then foreign keys checked.
func migrateOperatorCancellationV57(ctx context.Context, tx *sql.Tx) error {
	type definition struct{ kind, name, sql string }
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL AND (type='trigger' OR (type='index' AND tbl_name='execution_cancellations')) ORDER BY rowid`)
	if err != nil {
		return err
	}
	var definitions []definition
	for rows.Next() {
		var d definition
		if err = rows.Scan(&d.kind, &d.name, &d.sql); err != nil {
			rows.Close()
			return err
		}
		definitions = append(definitions, d)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	// Cross-table triggers must be absent only while their referenced table is
	// replaced, exactly as in the original Session table migration.
	for _, d := range definitions {
		if d.kind == "trigger" {
			if _, err = tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(d.name, `"`, `""`)+`"`); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `
CREATE TABLE execution_cancellations_v57 (
 project_id TEXT NOT NULL REFERENCES projects(id),
 request_id TEXT NOT NULL CHECK(length(request_id)>0 AND length(request_id)<=128),
 execution_id TEXT REFERENCES execution_proposals(id),run_id TEXT REFERENCES runs(id),
 requested_by_ai_session_id TEXT REFERENCES ai_sessions(id),
 requested_by_operator_id TEXT CHECK(requested_by_operator_id IS NULL OR (length(requested_by_operator_id)>0 AND length(requested_by_operator_id)<=128)),
 reason TEXT NOT NULL CHECK(length(reason)<=2048),requested_at TEXT NOT NULL,runtime_epoch TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('CANCEL_REQUESTED','CANCELED','CANCEL_NOT_APPLIED','ALREADY_TERMINAL','RECOVERY_REQUIRED')),
 interrupt_state TEXT NOT NULL CHECK(interrupt_state IN ('NOT_SENT','REQUESTED','ACKNOWLEDGED','OUTCOME_UNKNOWN')),
 thread_id TEXT,turn_id TEXT,terminal_observed INTEGER NOT NULL DEFAULT 0 CHECK(terminal_observed IN (0,1)),resolved_at TEXT,
 owner_type TEXT NOT NULL DEFAULT 'EXECUTION' CHECK(owner_type IN ('REVIEW','EXECUTION','TASK')),owner_id TEXT,task_id TEXT REFERENCES tasks(id),
 backend_disposition TEXT NOT NULL DEFAULT 'ACTIVE' CHECK(backend_disposition IN ('ACTIVE','TERMINATED','NO_BACKEND','UNKNOWN_ORPHANED')),
 request_payload_json TEXT CHECK(request_payload_json IS NULL OR json_valid(request_payload_json)),
 result_json TEXT CHECK(result_json IS NULL OR json_valid(result_json)),
 CHECK((requested_by_ai_session_id IS NOT NULL AND requested_by_operator_id IS NULL) OR (requested_by_ai_session_id IS NULL AND requested_by_operator_id IS NOT NULL)),
 PRIMARY KEY(project_id,request_id)
);
INSERT INTO execution_cancellations_v57(project_id,request_id,execution_id,run_id,requested_by_ai_session_id,reason,requested_at,runtime_epoch,state,interrupt_state,thread_id,turn_id,terminal_observed,resolved_at,owner_type,owner_id,task_id,backend_disposition,request_payload_json,result_json)
 SELECT project_id,request_id,execution_id,run_id,requested_by_ai_session_id,reason,requested_at,runtime_epoch,state,interrupt_state,thread_id,turn_id,terminal_observed,resolved_at,owner_type,owner_id,task_id,backend_disposition,request_payload_json,result_json FROM execution_cancellations;
DROP TABLE execution_cancellations;
ALTER TABLE execution_cancellations_v57 RENAME TO execution_cancellations;
ALTER TABLE agent_operation_recoveries ADD COLUMN operator_principal_id TEXT CHECK(operator_principal_id IS NULL OR (length(operator_principal_id)>0 AND length(operator_principal_id)<=128));
CREATE TRIGGER cancellation_actor_immutable BEFORE UPDATE OF requested_by_ai_session_id,requested_by_operator_id ON execution_cancellations
WHEN NEW.requested_by_ai_session_id IS NOT OLD.requested_by_ai_session_id OR NEW.requested_by_operator_id IS NOT OLD.requested_by_operator_id
BEGIN SELECT RAISE(ABORT,'Cancellation actor is immutable'); END;
`)
	if err != nil {
		return err
	}
	for _, d := range definitions {
		if _, err = tx.ExecContext(ctx, d.sql); err != nil {
			return err
		}
	}
	return nil
}
