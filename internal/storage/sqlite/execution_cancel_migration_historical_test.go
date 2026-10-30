// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

// No historical Run, Execution, conversation or workspace is rewritten.
const schemaV34 = `
CREATE TABLE execution_cancellations (
 project_id TEXT NOT NULL REFERENCES projects(id),
 request_id TEXT NOT NULL CHECK(length(request_id)>0 AND length(request_id)<=128),
 execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 run_id TEXT NOT NULL REFERENCES runs(id),
 requested_by_ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 reason TEXT NOT NULL CHECK(length(reason)<=2048),
 requested_at TEXT NOT NULL,
 runtime_epoch TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('CANCEL_REQUESTED','CANCELED','CANCEL_NOT_APPLIED','ALREADY_TERMINAL','RECOVERY_REQUIRED')),
 interrupt_state TEXT NOT NULL CHECK(interrupt_state IN ('NOT_SENT','REQUESTED','ACKNOWLEDGED','OUTCOME_UNKNOWN')),
 thread_id TEXT,
 turn_id TEXT,
 terminal_observed INTEGER NOT NULL DEFAULT 0 CHECK(terminal_observed IN (0,1)),
 resolved_at TEXT,
 PRIMARY KEY(project_id,request_id)
);
CREATE UNIQUE INDEX execution_cancellation_active_run ON execution_cancellations(run_id) WHERE state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED');
CREATE INDEX execution_cancellation_execution ON execution_cancellations(project_id,execution_id,requested_at);
CREATE TRIGGER execution_cancellation_identity_immutable BEFORE UPDATE ON execution_cancellations
WHEN NEW.project_id<>OLD.project_id OR NEW.request_id<>OLD.request_id OR NEW.execution_id<>OLD.execution_id OR NEW.run_id<>OLD.run_id OR NEW.requested_by_ai_session_id<>OLD.requested_by_ai_session_id OR NEW.reason<>OLD.reason OR NEW.requested_at<>OLD.requested_at OR NEW.runtime_epoch<>OLD.runtime_epoch
BEGIN SELECT RAISE(ABORT,'cancellation request identity is immutable'); END;
CREATE TRIGGER execution_cancellation_no_delete BEFORE DELETE ON execution_cancellations
BEGIN SELECT RAISE(ABORT,'cancellation receipt is durable history'); END;
`
