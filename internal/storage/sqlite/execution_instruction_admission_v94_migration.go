package sqlite

import (
	"context"
	"database/sql"
)

// v94 keeps historical Run-linked receipts while allowing a modern instruction
// admission receipt to commit before Core creates its preparation Run.
func migrateInstructionAdmissionV94(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
DROP TRIGGER task_execution_instruction_receipts_immutable;
DROP TRIGGER task_execution_instruction_receipts_nodelete;
CREATE TABLE task_execution_instruction_receipts_v94 (
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 request_id TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(id),
 payload_sha256 TEXT NOT NULL,
 source_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 run_id TEXT REFERENCES runs(id),
 created_at TEXT NOT NULL,
 PRIMARY KEY(ai_session_id,request_id)
);
INSERT INTO task_execution_instruction_receipts_v94 SELECT * FROM task_execution_instruction_receipts;
DROP TABLE task_execution_instruction_receipts;
ALTER TABLE task_execution_instruction_receipts_v94 RENAME TO task_execution_instruction_receipts;
CREATE TRIGGER task_execution_instruction_receipts_immutable BEFORE UPDATE ON task_execution_instruction_receipts BEGIN SELECT RAISE(ABORT,'Execution instruction receipt is immutable'); END;
CREATE TRIGGER task_execution_instruction_receipts_nodelete BEFORE DELETE ON task_execution_instruction_receipts BEGIN SELECT RAISE(ABORT,'Execution instruction receipt is immutable'); END;
`)
	return err
}
