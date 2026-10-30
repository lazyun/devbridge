package sqlite

// No historical requests are backfilled. The original expected pointer may be
// empty or a settled predecessor; the result is independently anchored by the
// exact LOCAL_OPERATOR execution_start_effect and admission receipt.
const operatorExecutionStartSelectionV100SQL = `
CREATE TABLE operator_execution_start_commands (
 project_id TEXT NOT NULL REFERENCES projects(id),
 actor_id TEXT NOT NULL CHECK(length(actor_id)>0),
 request_id TEXT NOT NULL CHECK(length(request_id)>0),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 revision_hash TEXT NOT NULL CHECK(length(revision_hash)=64),
 approval_id TEXT NOT NULL,
 expected_execution_id TEXT NOT NULL,
 expected_phase TEXT NOT NULL CHECK(expected_phase='IMPLEMENTATION_READY'),
 primary_member_id TEXT NOT NULL CHECK(length(primary_member_id)>0),
 workset_hash TEXT NOT NULL CHECK(length(workset_hash)=64),
 request_json TEXT NOT NULL CHECK(json_valid(request_json)),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 runtime_config_json TEXT NOT NULL CHECK(json_valid(runtime_config_json)),
 backend_selection_json TEXT NOT NULL CHECK(json_valid(backend_selection_json)),
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,actor_id,request_id)
);
CREATE TRIGGER operator_execution_start_command_immutable BEFORE UPDATE ON operator_execution_start_commands BEGIN SELECT RAISE(ABORT,'Operator start command is immutable'); END;
CREATE TRIGGER operator_execution_start_command_no_delete BEFORE DELETE ON operator_execution_start_commands BEGIN SELECT RAISE(ABORT,'Operator start command is durable'); END;
CREATE TRIGGER operator_execution_start_command_owner BEFORE INSERT ON operator_execution_start_commands
WHEN NOT EXISTS(SELECT 1 FROM tasks t JOIN task_definition_revisions d ON d.id=t.current_definition_revision_id
 WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id
 AND t.current_definition_revision_id=NEW.revision_id AND d.payload_sha256=NEW.revision_hash
 AND t.review_approval_id=NEW.approval_id AND COALESCE(t.current_execution_id,'')=NEW.expected_execution_id
 AND t.phase=NEW.expected_phase AND t.status='ACTIVE' AND t.legacy_read_only=0
 AND d.task_id=t.id AND json_extract(d.payload_json,'$.version')=2
 AND json_extract(d.payload_json,'$.workSpec.implementationMode')='CODING_AGENT')
BEGIN SELECT RAISE(ABORT,'Operator start command owner is not current'); END;
`
