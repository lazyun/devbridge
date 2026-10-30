package sqlite

const directFileWorkerV138SQL = `
CREATE TABLE direct_file_worker_claims (
 workflow_id TEXT PRIMARY KEY REFERENCES direct_file_task_authorizations(workflow_id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 caller_scope TEXT NOT NULL CHECK(length(caller_scope)>0),
 request_id TEXT NOT NULL CHECK(length(request_id)>0),
 worker_id TEXT NOT NULL CHECK(length(worker_id)>0),
 target_json TEXT NOT NULL CHECK(json_valid(target_json)),
 binding_sha256 TEXT NOT NULL CHECK(length(binding_sha256)=64),
 receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json)),
 state TEXT NOT NULL CHECK(state IN ('DISPATCH_INTENT','COMPLETED')),
 result_id TEXT REFERENCES direct_file_implementation_results(id),
 UNIQUE(caller_scope,ai_session_id,request_id)
);
CREATE TRIGGER direct_file_worker_claim_admission BEFORE INSERT ON direct_file_worker_claims
WHEN NEW.state<>'DISPATCH_INTENT' OR NEW.result_id IS NOT NULL OR NOT EXISTS(
 SELECT 1 FROM direct_file_task_authorizations g JOIN tasks t ON t.id=g.task_id JOIN ai_sessions a ON a.id=g.ai_session_id
 WHERE g.workflow_id=NEW.workflow_id AND g.ai_session_id=NEW.ai_session_id AND g.caller_scope=NEW.caller_scope AND a.caller_scope=NEW.caller_scope
 AND g.protocol_version=2 AND g.implementation_mode='DIRECT_FILE' AND g.state='ACTIVE'
 AND g.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND t.status='ACTIVE' AND t.phase IN ('IMPLEMENTATION_READY','IMPLEMENTING') AND t.current_definition_revision_id=g.task_definition_revision_id AND t.review_approval_id=g.decision_id
 AND json_extract(NEW.target_json,'$.aiSessionId')=g.ai_session_id
 AND json_extract(NEW.target_json,'$.featureAreaId')=g.feature_area_id AND json_extract(NEW.target_json,'$.taskId')=g.task_id
 AND json_extract(NEW.target_json,'$.taskDefinitionRevisionId')=g.task_definition_revision_id AND json_extract(NEW.target_json,'$.decisionId')=g.decision_id
 AND json_extract(NEW.target_json,'$.workflowId')=g.workflow_id AND json_extract(NEW.target_json,'$.protocolVersion')=2
 AND json_extract(NEW.target_json,'$.runtimeEpoch')=g.runtime_epoch AND json_extract(NEW.target_json,'$.grantGeneration')=g.generation
 AND NOT EXISTS(SELECT 1 FROM direct_file_operation_receipts o WHERE o.workflow_id=g.workflow_id)
 AND NOT EXISTS(SELECT 1 FROM direct_file_implementation_results r WHERE r.workflow_id=g.workflow_id))
BEGIN SELECT RAISE(ABORT,'Direct File worker target is stale or already used'); END;
CREATE TRIGGER direct_file_worker_claim_completion BEFORE UPDATE ON direct_file_worker_claims
WHEN NOT EXISTS(SELECT 1 FROM direct_file_implementation_results r WHERE r.id=NEW.result_id AND r.workflow_id=OLD.workflow_id AND r.ai_session_id=OLD.ai_session_id AND r.task_id=json_extract(OLD.target_json,'$.taskId') AND r.revision_id=json_extract(OLD.target_json,'$.taskDefinitionRevisionId'))
BEGIN SELECT RAISE(ABORT,'Direct File worker completion requires committed result'); END;
CREATE TRIGGER direct_file_worker_claim_immutable BEFORE UPDATE ON direct_file_worker_claims
WHEN NEW.workflow_id IS NOT OLD.workflow_id OR NEW.ai_session_id IS NOT OLD.ai_session_id OR NEW.caller_scope IS NOT OLD.caller_scope OR NEW.request_id IS NOT OLD.request_id OR NEW.worker_id IS NOT OLD.worker_id OR NEW.target_json IS NOT OLD.target_json OR NEW.binding_sha256 IS NOT OLD.binding_sha256 OR NEW.receipt_json IS NOT OLD.receipt_json OR OLD.state='COMPLETED' OR NEW.state<>'COMPLETED' OR NEW.result_id IS NULL
BEGIN SELECT RAISE(ABORT,'Direct File worker claim is immutable'); END;
CREATE TRIGGER direct_file_worker_claim_no_delete BEFORE DELETE ON direct_file_worker_claims
BEGIN SELECT RAISE(ABORT,'Direct File worker claim is durable'); END;
`
