package sqlite

import (
	"context"
	"database/sql"
)

func migrateConfirmationV56(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE confirmation_intents (
 intent_id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 project_id TEXT NOT NULL,
 worker_epoch TEXT NOT NULL DEFAULT '',
 prepare_request_id TEXT NOT NULL,
 prepare_hash TEXT NOT NULL,
 intent_json TEXT NOT NULL CHECK(json_valid(intent_json)),
 UNIQUE(ai_session_id,prepare_request_id)
 );
 CREATE TABLE confirmation_commit_requests(ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),request_id TEXT NOT NULL,intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),payload_sha256 TEXT NOT NULL,PRIMARY KEY(ai_session_id,request_id));
 CREATE TABLE confirmation_approval_sources(decision_id TEXT PRIMARY KEY REFERENCES review_approval_decisions(decision_id),intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id));
 CREATE TRIGGER confirmation_no_delete BEFORE DELETE ON confirmation_intents BEGIN SELECT RAISE(ABORT,'confirmation intent is durable'); END;
 CREATE TRIGGER confirmation_request_immutable BEFORE UPDATE ON confirmation_commit_requests BEGIN SELECT RAISE(ABORT,'confirmation request is immutable'); END;
 CREATE TRIGGER confirmation_request_no_delete BEFORE DELETE ON confirmation_commit_requests BEGIN SELECT RAISE(ABORT,'confirmation request is durable'); END;
 CREATE TRIGGER confirmation_approval_source_immutable BEFORE UPDATE ON confirmation_approval_sources BEGIN SELECT RAISE(ABORT,'confirmation approval source is immutable'); END;
 CREATE TRIGGER confirmation_approval_source_no_delete BEFORE DELETE ON confirmation_approval_sources BEGIN SELECT RAISE(ABORT,'confirmation approval source is durable'); END;
 CREATE TRIGGER confirmation_content_immutable BEFORE UPDATE ON confirmation_intents
 WHEN NEW.intent_id IS NOT OLD.intent_id OR NEW.ai_session_id IS NOT OLD.ai_session_id OR NEW.project_id IS NOT OLD.project_id OR NEW.prepare_request_id IS NOT OLD.prepare_request_id OR NEW.prepare_hash IS NOT OLD.prepare_hash OR json_extract(NEW.intent_json,'$.content') IS NOT json_extract(OLD.intent_json,'$.content') OR (json_type(OLD.intent_json,'$.commit') IS NOT NULL AND json_extract(NEW.intent_json,'$.commit') IS NOT json_extract(OLD.intent_json,'$.commit'))
 BEGIN SELECT RAISE(ABORT,'confirmation identity and selection are immutable'); END;
ALTER TABLE reviews ADD COLUMN runtime_config_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE reviews ADD COLUMN runtime_confirmation_id TEXT NOT NULL DEFAULT '';
CREATE TRIGGER review_runtime_config_immutable BEFORE UPDATE OF runtime_config_json,runtime_confirmation_id ON reviews WHEN NEW.runtime_config_json IS NOT OLD.runtime_config_json OR NEW.runtime_confirmation_id IS NOT OLD.runtime_confirmation_id BEGIN SELECT RAISE(ABORT,'review runtime configuration is immutable'); END;
CREATE TABLE direct_file_implementation_results (
 id TEXT PRIMARY KEY, ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id), project_id TEXT NOT NULL REFERENCES projects(id), task_id TEXT NOT NULL REFERENCES tasks(id), revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id), workflow_id TEXT NOT NULL REFERENCES direct_file_task_authorizations(workflow_id), request_id TEXT NOT NULL, payload_sha256 TEXT NOT NULL, result_json TEXT NOT NULL, created_at TEXT NOT NULL,
 UNIQUE(ai_session_id,request_id), UNIQUE(task_id,revision_id), UNIQUE(workflow_id)
);
CREATE TABLE direct_file_implementation_acceptances (
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id), request_id TEXT NOT NULL, payload_sha256 TEXT NOT NULL, result_id TEXT NOT NULL UNIQUE REFERENCES direct_file_implementation_results(id), created_at TEXT NOT NULL, PRIMARY KEY(ai_session_id,request_id)
);
CREATE TRIGGER file_result_immutable BEFORE UPDATE ON direct_file_implementation_results BEGIN SELECT RAISE(ABORT,'file result immutable'); END;
CREATE TRIGGER file_result_no_delete BEFORE DELETE ON direct_file_implementation_results BEGIN SELECT RAISE(ABORT,'file result immutable'); END;
CREATE TRIGGER file_acceptance_immutable BEFORE UPDATE ON direct_file_implementation_acceptances BEGIN SELECT RAISE(ABORT,'file acceptance immutable'); END;
CREATE TRIGGER file_acceptance_no_delete BEFORE DELETE ON direct_file_implementation_acceptances BEGIN SELECT RAISE(ABORT,'file acceptance immutable'); END;
CREATE TRIGGER file_result_task_phase_guard BEFORE UPDATE OF phase ON tasks
WHEN NEW.phase='ACCEPTANCE_PENDING' AND OLD.phase IN ('IMPLEMENTATION_READY','IMPLEMENTING') AND json_extract(OLD.work_spec_json,'$.implementationMode')='DIRECT_FILE'
AND NOT EXISTS(SELECT 1 FROM direct_file_implementation_results r JOIN direct_file_task_authorizations g ON g.workflow_id=r.workflow_id WHERE r.task_id=OLD.id AND r.revision_id=OLD.current_definition_revision_id AND r.project_id=OLD.project_id AND g.state='CLOSED' AND g.decision_id=OLD.review_approval_id AND g.task_id=OLD.id AND g.task_definition_revision_id=r.revision_id AND g.ai_session_id=r.ai_session_id)
BEGIN SELECT RAISE(ABORT,'file result required'); END;
CREATE TRIGGER file_result_task_accept_guard BEFORE UPDATE OF status ON tasks
WHEN NEW.status='SUCCEEDED' AND OLD.status='ACTIVE' AND json_extract(OLD.work_spec_json,'$.implementationMode')='DIRECT_FILE'
AND (OLD.phase<>'ACCEPTANCE_PENDING' OR NOT EXISTS(SELECT 1 FROM direct_file_implementation_acceptances a JOIN direct_file_implementation_results r ON r.id=a.result_id JOIN direct_file_task_authorizations g ON g.workflow_id=r.workflow_id WHERE r.task_id=OLD.id AND r.revision_id=OLD.current_definition_revision_id AND r.project_id=OLD.project_id AND g.state='CLOSED' AND g.decision_id=OLD.review_approval_id AND g.task_id=OLD.id AND g.task_definition_revision_id=r.revision_id AND a.ai_session_id=r.ai_session_id AND json_extract(OLD.verification_spec_json,'$.mode')='NONE'))
BEGIN SELECT RAISE(ABORT,'file result acceptance required'); END;

`)
	return err
}
