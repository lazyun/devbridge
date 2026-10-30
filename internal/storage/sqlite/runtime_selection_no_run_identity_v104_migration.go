package sqlite

// Old receipts remain unchanged. Their missing original actor cannot be
// reconstructed from the current Task, a candidate author or caller hash.
const runtimeSelectionNoRunIdentityV104SQL = `
CREATE TABLE no_run_successor_command_identities (
 request_id TEXT PRIMARY KEY REFERENCES no_run_successor_command_receipts(request_id) DEFERRABLE INITIALLY DEFERRED,
 project_id TEXT NOT NULL REFERENCES projects(id),actor_authority TEXT NOT NULL CHECK(length(actor_authority)>0),
 task_id TEXT NOT NULL REFERENCES tasks(id),predecessor_execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 identity_json TEXT NOT NULL CHECK(json_valid(identity_json)),identity_sha256 TEXT NOT NULL CHECK(length(identity_sha256)=64));
CREATE TRIGGER no_run_successor_command_identity_source BEFORE INSERT ON no_run_successor_command_identities
WHEN NOT EXISTS(SELECT 1 FROM execution_proposals e JOIN task_definition_revisions r ON r.id=e.task_definition_revision_id AND r.task_id=e.task_id
 WHERE e.id=NEW.predecessor_execution_id AND e.task_id=NEW.task_id AND e.project_id=NEW.project_id AND r.id=NEW.revision_id
 AND json_extract(NEW.identity_json,'$.requestId')=NEW.request_id AND json_extract(NEW.identity_json,'$.projectId')=NEW.project_id
 AND json_extract(NEW.identity_json,'$.actorAuthority')=NEW.actor_authority AND json_extract(NEW.identity_json,'$.taskId')=NEW.task_id
 AND json_extract(NEW.identity_json,'$.predecessorExecutionId')=NEW.predecessor_execution_id AND json_extract(NEW.identity_json,'$.expectedCurrentExecutionId')=NEW.predecessor_execution_id
 AND json_extract(NEW.identity_json,'$.expectedDefinitionRevisionId')=NEW.revision_id AND json_extract(NEW.identity_json,'$.expectedTaskState')='TASK_NO_RUN_SUCCESSOR_REQUIRED'
 AND json_extract(NEW.identity_json,'$.payloadSha256')=NEW.payload_sha256)
BEGIN SELECT RAISE(ABORT,'successor command exact identity required'); END;
CREATE TRIGGER no_run_successor_command_identity_receipt BEFORE INSERT ON no_run_successor_command_receipts
WHEN NOT EXISTS(SELECT 1 FROM no_run_successor_command_identities i WHERE i.request_id=NEW.request_id AND i.task_id=NEW.task_id AND i.predecessor_execution_id=NEW.predecessor_execution_id AND i.payload_sha256=NEW.payload_sha256)
BEGIN SELECT RAISE(ABORT,'successor command original identity required'); END;
CREATE TRIGGER no_run_successor_command_identity_immutable BEFORE UPDATE ON no_run_successor_command_identities BEGIN SELECT RAISE(ABORT,'successor command identity immutable'); END;
CREATE TRIGGER no_run_successor_command_identity_durable BEFORE DELETE ON no_run_successor_command_identities BEGIN SELECT RAISE(ABORT,'successor command identity durable'); END;
`
