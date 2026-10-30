package sqlite

// Operator execution starts have a source-scoped immutable receipt. Existing
// runs are deliberately not backfilled: an old run does not prove which local
// operator requested a delivery attempt.
const operatorExecutionStartReceiptMigrationSQL = `
CREATE TABLE operator_task_execution_start_receipts (
 project_id TEXT NOT NULL REFERENCES projects(id),
 actor_kind TEXT NOT NULL CHECK(actor_kind='LOCAL_OPERATOR'),
 actor_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('START_EXECUTION','START_VERIFICATION')),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 task_definition_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 expected_execution_id TEXT NOT NULL,
 expected_phase TEXT NOT NULL CHECK(expected_phase IN ('IMPLEMENTATION_READY','VERIFICATION_READY')),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 run_id TEXT NOT NULL REFERENCES runs(id),
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,actor_kind,actor_id,request_id)
);
CREATE INDEX operator_task_execution_start_receipts_task ON operator_task_execution_start_receipts(task_id,created_at);
CREATE TRIGGER operator_task_execution_start_receipt_immutable BEFORE UPDATE ON operator_task_execution_start_receipts BEGIN SELECT RAISE(ABORT,'Operator execution start receipt is immutable'); END;
CREATE TRIGGER operator_task_execution_start_receipt_no_delete BEFORE DELETE ON operator_task_execution_start_receipts BEGIN SELECT RAISE(ABORT,'Operator execution start receipt is durable'); END;
`
