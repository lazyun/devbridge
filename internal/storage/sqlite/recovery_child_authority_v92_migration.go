package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// Each external child-thread or child-turn attempt is recorded before the RPC.
// A lost RPC response leaves the attempt without a receipt and must be read
// back, never dispatched again with a new request identifier.
const recoveryChildAuthorityV92SQL = `
CREATE TABLE recovery_child_admissions (
 recovery_id TEXT PRIMARY KEY REFERENCES recovery_status_records(id),
 recovery_generation INTEGER NOT NULL CHECK(recovery_generation>0),
 source_run_id TEXT NOT NULL REFERENCES runs(id),
 source_turn_id TEXT NOT NULL,
 source_thread_id TEXT NOT NULL,
 source_runtime_workset_hash TEXT NOT NULL CHECK(length(source_runtime_workset_hash)=64),
 source_workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 source_app_server_instance_id TEXT NOT NULL,
 child_plan_id TEXT NOT NULL UNIQUE REFERENCES runtime_scope_plans(plan_id),
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 admission_request_id TEXT NOT NULL UNIQUE,
 admission_payload_sha256 TEXT NOT NULL CHECK(length(admission_payload_sha256)=64),
 admitted_at TEXT NOT NULL,
 UNIQUE(source_run_id,source_turn_id,recovery_generation)
);
CREATE TABLE recovery_child_thread_attempts (
 recovery_id TEXT PRIMARY KEY REFERENCES recovery_child_admissions(recovery_id),
 child_run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 child_runtime_workset_hash TEXT NOT NULL CHECK(length(child_runtime_workset_hash)=64),
 child_workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 request_id TEXT NOT NULL UNIQUE,
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 attempted_at TEXT NOT NULL
);
CREATE TABLE recovery_child_thread_receipts (
 request_id TEXT PRIMARY KEY REFERENCES recovery_child_thread_attempts(request_id),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 outcome TEXT NOT NULL CHECK(outcome IN ('CREATED','FAILED','UNKNOWN')),
 child_thread_id TEXT UNIQUE,
 app_server_instance_id TEXT NOT NULL,
 observed_binding_hash TEXT NOT NULL DEFAULT '',
 failure_code TEXT NOT NULL DEFAULT '',
 recorded_at TEXT NOT NULL,
 CHECK((outcome='CREATED' AND child_thread_id IS NOT NULL AND child_thread_id<>'' AND length(observed_binding_hash)=64 AND failure_code='') OR
       (outcome IN ('FAILED','UNKNOWN') AND child_thread_id IS NULL AND failure_code<>''))
);
CREATE TABLE recovery_child_turn_attempts (
 recovery_id TEXT PRIMARY KEY REFERENCES recovery_child_admissions(recovery_id),
 thread_request_id TEXT NOT NULL UNIQUE REFERENCES recovery_child_thread_receipts(request_id),
 child_thread_id TEXT NOT NULL UNIQUE,
 request_id TEXT NOT NULL UNIQUE,
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 attempted_at TEXT NOT NULL
);
CREATE TABLE recovery_child_turn_receipts (
 request_id TEXT PRIMARY KEY REFERENCES recovery_child_turn_attempts(request_id),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 outcome TEXT NOT NULL CHECK(outcome IN ('DISPATCHED','FAILED','UNKNOWN')),
 child_thread_id TEXT NOT NULL,
 child_turn_id TEXT UNIQUE,
 dispatch_attempt_id TEXT NOT NULL UNIQUE,
 failure_code TEXT NOT NULL DEFAULT '',
 recorded_at TEXT NOT NULL,
 CHECK((outcome='DISPATCHED' AND child_turn_id IS NOT NULL AND child_turn_id<>'' AND failure_code='') OR
       (outcome IN ('FAILED','UNKNOWN') AND child_turn_id IS NULL AND failure_code<>''))
);
CREATE TRIGGER recovery_child_admissions_source_fence BEFORE INSERT ON recovery_child_admissions
WHEN NOT EXISTS(SELECT 1 FROM recovery_status_records s JOIN runtime_scope_plans p ON p.plan_id=NEW.child_plan_id
 WHERE s.id=NEW.recovery_id AND s.generation=NEW.recovery_generation AND s.source_run_id=NEW.source_run_id AND s.source_turn_id=NEW.source_turn_id
 AND s.runtime_epoch=NEW.runtime_epoch AND p.owner_kind='RECOVERY_STATUS' AND p.owner_id=NEW.recovery_id)
BEGIN SELECT RAISE(ABORT,'Recovery child admission source mismatch'); END;
CREATE TRIGGER recovery_child_thread_attempt_binding BEFORE INSERT ON recovery_child_thread_attempts
WHEN NOT EXISTS(SELECT 1 FROM recovery_child_admissions a JOIN runtime_workset_bindings b ON b.plan_id=a.child_plan_id
 JOIN runtime_start_authorities start ON start.plan_id=b.plan_id AND start.run_id=b.run_id
 JOIN runs source ON source.id=a.source_run_id
 WHERE a.recovery_id=NEW.recovery_id AND b.run_id=NEW.child_run_id AND b.workset_content_hash=NEW.child_runtime_workset_hash
 AND b.runtime_epoch=NEW.runtime_epoch AND start.runtime_epoch=NEW.runtime_epoch AND a.runtime_epoch=NEW.runtime_epoch
 AND source.thread_id=a.source_thread_id AND source.turn_id=a.source_turn_id AND source.runtime_workset_hash=a.source_runtime_workset_hash
 AND source.workspace_id=a.source_workspace_id AND NEW.child_workspace_id<>a.source_workspace_id
 AND NEW.child_workspace_id=json_extract(b.workset_json,'$.workspaceId')
 AND NOT EXISTS(SELECT 1 FROM json_each(b.workset_json,'$.members') child_member JOIN json_each(source.runtime_workset_json,'$.members') source_member
   ON json_extract(child_member.value,'$.canonicalRoot')=json_extract(source_member.value,'$.canonicalRoot')))
BEGIN SELECT RAISE(ABORT,'Recovery child Workset/start authority mismatch'); END;
CREATE TRIGGER recovery_child_thread_receipt_exact BEFORE INSERT ON recovery_child_thread_receipts
WHEN NOT EXISTS(SELECT 1 FROM recovery_child_thread_attempts a JOIN recovery_child_admissions c ON c.recovery_id=a.recovery_id
 WHERE a.request_id=NEW.request_id AND a.payload_sha256=NEW.payload_sha256 AND (NEW.child_thread_id IS NULL OR NEW.child_thread_id<>c.source_thread_id))
BEGIN SELECT RAISE(ABORT,'Recovery child Thread receipt mismatch'); END;
CREATE TRIGGER recovery_child_turn_attempt_exact BEFORE INSERT ON recovery_child_turn_attempts
WHEN NOT EXISTS(SELECT 1 FROM recovery_child_thread_attempts a JOIN recovery_child_thread_receipts r ON r.request_id=a.request_id
 WHERE a.recovery_id=NEW.recovery_id AND r.request_id=NEW.thread_request_id AND r.outcome='CREATED' AND r.child_thread_id=NEW.child_thread_id)
BEGIN SELECT RAISE(ABORT,'Recovery child Turn attempt mismatch'); END;
CREATE TRIGGER recovery_child_turn_receipt_exact BEFORE INSERT ON recovery_child_turn_receipts
WHEN NOT EXISTS(SELECT 1 FROM recovery_child_turn_attempts a WHERE a.request_id=NEW.request_id AND a.payload_sha256=NEW.payload_sha256 AND a.child_thread_id=NEW.child_thread_id)
BEGIN SELECT RAISE(ABORT,'Recovery child Turn receipt mismatch'); END;
`

func migrateRecoveryChildAuthorityV92(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"recovery_child_admissions", "recovery_child_thread_attempts", "recovery_child_thread_receipts", "recovery_child_turn_attempts", "recovery_child_turn_receipts"} {
		for _, operation := range []string{"UPDATE", "DELETE"} {
			statement := fmt.Sprintf("CREATE TRIGGER %s_immutable_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'Recovery child authority immutable'); END", table, operation, operation, table)
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	return nil
}
