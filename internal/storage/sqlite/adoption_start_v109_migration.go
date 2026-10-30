package sqlite

// A start request references the original confirmation; it grants no admission,
// selection source, or preparation authority. Historical requests are not inferred.
const adoptionStartV109SQL = `
CREATE TABLE ai_admitted_execution_start_requests (
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 request_id TEXT NOT NULL CHECK(length(request_id)>0),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 request_json TEXT NOT NULL CHECK(json_valid(request_json)),
 created_at TEXT NOT NULL,
 PRIMARY KEY(ai_session_id,request_id)
);
CREATE TRIGGER ai_admitted_execution_start_immutable BEFORE UPDATE ON ai_admitted_execution_start_requests BEGIN SELECT RAISE(ABORT,'Admitted start request is immutable'); END;
CREATE TRIGGER ai_admitted_execution_start_durable BEFORE DELETE ON ai_admitted_execution_start_requests BEGIN SELECT RAISE(ABORT,'Admitted start request is durable'); END;
CREATE TRIGGER ai_admitted_execution_start_source BEFORE INSERT ON ai_admitted_execution_start_requests
WHEN NOT EXISTS(SELECT 1 FROM formal_adoption_business b
 JOIN formal_adoption_receipts r ON r.ai_session_id=b.ai_session_id AND r.request_id=b.request_id
 JOIN confirmation_intents i ON i.ai_session_id=b.ai_session_id AND i.intent_id=b.intent_id
 JOIN tasks t ON t.id=b.task_id AND t.project_id=b.project_id
 JOIN execution_proposals e ON e.id=b.execution_id AND e.task_id=t.id AND e.task_definition_revision_id=b.revision_id
 JOIN execution_proposal_admissions a ON a.proposal_id=e.id AND a.admission_request_id=b.admission_request_id AND a.admission_request_id=json_extract(i.intent_json,'$.steps[0].requestId') AND a.runtime_scope_plan_id=json_extract(b.business_json,'$.plan.planId')
 JOIN runtime_preparation_authorities p ON p.plan_id=a.runtime_preparation_authority_id
 WHERE b.ai_session_id=NEW.ai_session_id AND b.intent_id=NEW.intent_id AND b.task_id=NEW.task_id AND b.execution_id=NEW.execution_id
 AND json_type(b.business_json,'$.priorRuntimeApproval') IS NULL
 AND json_extract(i.intent_json,'$.state')='COMPLETED'
 AND json_extract(i.intent_json,'$.steps[0].effect')='PREPARATION_READY'
 AND t.current_execution_id=e.id AND t.current_definition_revision_id=b.revision_id AND t.status='ACTIVE' AND t.phase='IMPLEMENTATION_READY'
 AND e.status='ADMITTED' AND e.run_id IS NULL AND NOT EXISTS(SELECT 1 FROM runs WHERE execution_proposal_id=e.id)
 AND p.disposition='CURRENT'
 AND json_extract(NEW.request_json,'$.aiSessionId')=NEW.ai_session_id
 AND json_extract(NEW.request_json,'$.requestId')=NEW.request_id
 AND json_extract(NEW.request_json,'$.taskId')=NEW.task_id
 AND json_extract(NEW.request_json,'$.expectedTarget.executionId')=NEW.execution_id)
BEGIN SELECT RAISE(ABORT,'Admitted start requires the exact completed current admission'); END;
`
