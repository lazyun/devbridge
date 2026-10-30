package sqlite

// Local operator approval/start parents record one browser business command
// and its two resumable Core steps without borrowing confirmation identities.
const localApproveAndStartMigrationSQL = `
CREATE TABLE local_approve_and_start_commands (
 project_id TEXT NOT NULL REFERENCES projects(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 review_id TEXT NOT NULL REFERENCES reviews(id),
 decision_id TEXT NOT NULL,
 actor_id TEXT NOT NULL,
 raw_request_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 target_json TEXT NOT NULL CHECK(json_valid(target_json)),
 approval_request_id TEXT NOT NULL,
 start_request_id TEXT NOT NULL,
 execution_instructions_json TEXT NOT NULL CHECK(json_valid(execution_instructions_json)),
 runtime_config_json TEXT NOT NULL CHECK(json_valid(runtime_config_json)),
 revision_id TEXT,
 execution_id TEXT,
 run_id TEXT REFERENCES runs(id),
 handoff_state TEXT,
 error_code TEXT,
 state TEXT NOT NULL CHECK(state IN ('PENDING_APPROVAL','APPROVED','RUN_RESERVED','STARTED','DIRECT_FILE_READY','PARTIAL','REJECTED','UNKNOWN')),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,actor_id,request_id)
);
CREATE UNIQUE INDEX local_approve_and_start_request_steps ON local_approve_and_start_commands(project_id,actor_id,approval_request_id,start_request_id);
CREATE UNIQUE INDEX local_approve_and_start_scoped_request ON local_approve_and_start_commands(project_id,request_id);
CREATE TRIGGER local_approve_and_start_immutable BEFORE UPDATE OF project_id,task_id,feature_area_id,review_id,decision_id,actor_id,raw_request_id,request_id,payload_sha256,target_json,approval_request_id,start_request_id,execution_instructions_json,runtime_config_json,created_at ON local_approve_and_start_commands BEGIN SELECT RAISE(ABORT,'Local approve and start command is immutable'); END;
CREATE TRIGGER local_approve_and_start_progress_immutable BEFORE UPDATE OF revision_id,execution_id,run_id ON local_approve_and_start_commands
WHEN (OLD.revision_id IS NOT NULL AND NEW.revision_id IS NOT OLD.revision_id)
 OR (OLD.execution_id IS NOT NULL AND NEW.execution_id IS NOT OLD.execution_id)
 OR (OLD.run_id IS NOT NULL AND NEW.run_id IS NOT OLD.run_id)
BEGIN SELECT RAISE(ABORT,'Local approve and start progress identity is immutable'); END;
CREATE TRIGGER local_approve_and_start_state_transition BEFORE UPDATE OF state ON local_approve_and_start_commands
WHEN NOT (
 (OLD.state='PENDING_APPROVAL' AND NEW.state IN ('PENDING_APPROVAL','APPROVED','REJECTED'))
 OR (OLD.state='APPROVED' AND NEW.state IN ('RUN_RESERVED','DIRECT_FILE_READY','PARTIAL','UNKNOWN'))
 OR (OLD.state='RUN_RESERVED' AND NEW.state IN ('STARTED','PARTIAL','UNKNOWN'))
 OR (OLD.state='UNKNOWN' AND NEW.state IN ('RUN_RESERVED','STARTED','PARTIAL','UNKNOWN'))
 OR (OLD.state IN ('STARTED','DIRECT_FILE_READY','PARTIAL','REJECTED') AND NEW.state=OLD.state)
)
BEGIN SELECT RAISE(ABORT,'Local approve and start state transition is invalid'); END;
CREATE TRIGGER local_approve_and_start_no_delete BEFORE DELETE ON local_approve_and_start_commands BEGIN SELECT RAISE(ABORT,'Local approve and start command is durable'); END;
`
