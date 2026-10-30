package sqlite

// No rows are inferred from historical completion or consumption receipts.
const runtimeSelectionAdoptionV104SQL = formalDefinitionDraftParentV104SQL + runtimeSelectionFormalNoRunV104SQL + runtimeSelectionNoRunIdentityV104SQL + `
CREATE TABLE formal_adoption_fences (
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),intent_id TEXT NOT NULL UNIQUE REFERENCES confirmation_intents(intent_id),intent_version INTEGER NOT NULL CHECK(intent_version>0),
 project_id TEXT NOT NULL REFERENCES projects(id),task_id TEXT NOT NULL REFERENCES tasks(id),revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),content_hash TEXT NOT NULL CHECK(length(content_hash)=64),PRIMARY KEY(ai_session_id,intent_id));
CREATE TRIGGER formal_adoption_fence_source BEFORE INSERT ON formal_adoption_fences
WHEN NOT EXISTS(SELECT 1 FROM confirmation_intents i JOIN tasks t ON t.id=NEW.task_id AND t.project_id=NEW.project_id JOIN task_definition_revisions r ON r.id=NEW.revision_id AND r.task_id=t.id
 WHERE i.ai_session_id=NEW.ai_session_id AND i.intent_id=NEW.intent_id AND i.project_id=NEW.project_id AND i.prepare_hash=NEW.content_hash
 AND json_extract(i.intent_json,'$.version')=NEW.intent_version AND json_extract(i.intent_json,'$.state')='PENDING'
 AND json_extract(i.intent_json,'$.purpose')='EXECUTION' AND json_extract(i.intent_json,'$.action')='START_APPROVED'
 AND json_extract(i.intent_json,'$.content.execution.taskId')=t.id AND json_extract(i.intent_json,'$.content.execution.taskDefinitionRevisionId')=r.id
 AND t.current_definition_revision_id=r.id AND t.status='ACTIVE' AND t.phase='IMPLEMENTATION_READY'
 AND json_extract(r.payload_json,'$.version')=2 AND json_extract(r.payload_json,'$.workSpec.implementationMode')='CODING_AGENT'
 AND NOT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.id=t.current_execution_id AND e.task_definition_revision_id=r.id))
BEGIN SELECT RAISE(ABORT,'formal adoption prepare requires exact new selection owner'); END;
CREATE TRIGGER formal_adoption_fence_immutable BEFORE UPDATE ON formal_adoption_fences BEGIN SELECT RAISE(ABORT,'formal adoption fence immutable'); END;
CREATE TRIGGER formal_adoption_fence_durable BEFORE DELETE ON formal_adoption_fences BEGIN SELECT RAISE(ABORT,'formal adoption fence durable'); END;
CREATE TABLE formal_adoption_business (
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),request_id TEXT NOT NULL,
 intent_id TEXT NOT NULL UNIQUE REFERENCES formal_adoption_fences(intent_id),
 project_id TEXT NOT NULL REFERENCES projects(id),task_id TEXT NOT NULL REFERENCES tasks(id),
 revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 execution_id TEXT NOT NULL UNIQUE REFERENCES execution_proposals(id),
 admission_request_id TEXT NOT NULL UNIQUE,
 business_sha256 TEXT NOT NULL CHECK(length(business_sha256)=64 AND business_sha256 NOT GLOB '*[^0-9a-f]*'),
 business_json TEXT NOT NULL CHECK(json_valid(business_json)),PRIMARY KEY(ai_session_id,request_id),
 FOREIGN KEY(ai_session_id,request_id) REFERENCES confirmation_commit_requests(ai_session_id,request_id));
CREATE TABLE formal_adoption_receipts (
 ai_session_id TEXT NOT NULL,request_id TEXT NOT NULL,
 business_sha256 TEXT NOT NULL CHECK(length(business_sha256)=64),
 receipt_identity TEXT NOT NULL UNIQUE CHECK(length(receipt_identity)=64),
 receipt_sha256 TEXT NOT NULL CHECK(length(receipt_sha256)=64),
 receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json)),PRIMARY KEY(ai_session_id,request_id),
 FOREIGN KEY(ai_session_id,request_id) REFERENCES formal_adoption_business(ai_session_id,request_id));
CREATE TRIGGER formal_adoption_business_source BEFORE INSERT ON formal_adoption_business
WHEN NOT EXISTS(SELECT 1 FROM confirmation_intents i
 JOIN formal_adoption_fences armed ON armed.ai_session_id=i.ai_session_id AND armed.intent_id=i.intent_id AND armed.task_id=NEW.task_id AND armed.revision_id=NEW.revision_id AND armed.project_id=NEW.project_id
 JOIN confirmation_commit_requests c ON c.ai_session_id=i.ai_session_id AND c.intent_id=i.intent_id AND c.outcome='CONSUMED'
 JOIN tasks t ON t.id=NEW.task_id AND t.project_id=NEW.project_id
 JOIN formal_definition_approval_business prior ON prior.revision_id=NEW.revision_id AND json_extract(prior.snapshot_json,'$.fence.taskId')=t.id
 JOIN formal_definition_approval_receipts r ON r.ai_session_id=prior.ai_session_id AND r.request_id=prior.request_id
 JOIN execution_proposals e ON e.id=NEW.execution_id AND e.task_id=t.id AND e.task_definition_revision_id=NEW.revision_id
 JOIN execution_proposal_admissions a ON a.proposal_id=e.id AND a.admission_request_id=NEW.admission_request_id
 JOIN runtime_scope_plans p ON p.plan_id=a.runtime_scope_plan_id
 JOIN runtime_preparation_authorities auth ON auth.plan_id=a.runtime_preparation_authority_id
 JOIN runtime_platform_qualifications q ON q.qualification_id=json_extract(NEW.business_json,'$.platformReference.qualificationId')
 JOIN runtime_platform_lineages l ON l.platform_lineage_id=q.platform_lineage_id
 WHERE i.intent_id=NEW.intent_id AND i.ai_session_id=NEW.ai_session_id AND i.project_id=NEW.project_id AND c.request_id=NEW.request_id
 AND json_extract(i.intent_json,'$.purpose')='EXECUTION' AND json_extract(i.intent_json,'$.action')='START_APPROVED'
 AND json_extract(i.intent_json,'$.state') IN ('CONFIRMING','PARTIAL','UNKNOWN')
 AND json_extract(i.intent_json,'$.commit')=json_extract(NEW.business_json,'$.commit')
 AND i.prepare_hash=json_extract(NEW.business_json,'$.identity.contentHash')
 AND c.payload_sha256=json_extract(NEW.business_json,'$.identity.typedPayloadHash') AND c.submitted_sha256=json_extract(NEW.business_json,'$.identity.submittedHash')
 AND json_extract(NEW.business_json,'$.originalRequestJson')=c.request_json
 AND json_extract(NEW.business_json,'$.priorApproval')=r.receipt_json AND json_extract(NEW.business_json,'$.priorApprovalRole')='PRIOR_APPROVAL_RECEIPT'
 AND json_extract(NEW.business_json,'$.identity.aiSessionId')=NEW.ai_session_id AND json_extract(NEW.business_json,'$.identity.requestId')=NEW.request_id
 AND json_extract(NEW.business_json,'$.identity.intentId')=NEW.intent_id
 AND json_extract(NEW.business_json,'$.projectId')=NEW.project_id AND json_extract(NEW.business_json,'$.taskId')=NEW.task_id
 AND json_extract(NEW.business_json,'$.revisionId')=NEW.revision_id AND json_extract(NEW.business_json,'$.executionId')=NEW.execution_id
 AND json_extract(NEW.business_json,'$.admissionRequestId')=NEW.admission_request_id
 AND t.current_definition_revision_id=NEW.revision_id AND t.current_execution_id=NEW.execution_id AND t.status='ACTIVE' AND t.phase='IMPLEMENTATION_READY'
 AND e.status='ADMITTED' AND e.run_id IS NULL AND e.verification_run_id IS NULL
 AND auth.disposition='CURRENT' AND auth.owner_id=e.id AND auth.request_id=a.admission_request_id AND auth.payload_hash=a.payload_sha256
 AND p.content_hash=json_extract(NEW.business_json,'$.plan.contentHash') AND p.plan_id=json_extract(NEW.business_json,'$.preparationAuthorityId')
 AND q.state='CURRENT' AND q.generation=json_extract(NEW.business_json,'$.platformReference.qualificationGeneration')
 AND q.platform_lineage_id=json_extract(NEW.business_json,'$.platformReference.platformLineageId') AND q.descriptor_id=json_extract(NEW.business_json,'$.platformReference.descriptorId')
 AND l.version=json_extract(NEW.business_json,'$.platformReference.lineageVersion')
 AND EXISTS(SELECT 1 FROM json_each(i.intent_json,'$.steps') s WHERE json_extract(s.value,'$.kind')='START_IMPLEMENTATION' AND json_extract(s.value,'$.requestId')=NEW.admission_request_id AND json_extract(s.value,'$.state') IN ('PENDING','UNKNOWN')))
BEGIN SELECT RAISE(ABORT,'formal adoption requires exact current consumed selection and prior approval'); END;
CREATE TRIGGER formal_adoption_receipt_source BEFORE INSERT ON formal_adoption_receipts
WHEN NOT EXISTS(SELECT 1 FROM formal_adoption_business b WHERE b.ai_session_id=NEW.ai_session_id AND b.request_id=NEW.request_id AND b.business_sha256=NEW.business_sha256
 AND json_extract(NEW.receipt_json,'$.archive')=b.business_json AND json_extract(NEW.receipt_json,'$.businessHash')=b.business_sha256
 AND json_extract(NEW.receipt_json,'$.receiptIdentity')=NEW.receipt_identity AND json_extract(NEW.receipt_json,'$.role')='CURRENT_FORMAL_COMMIT_RECEIPT'
 AND json_extract(NEW.receipt_json,'$.producerKind')='FORMAL_CONFIRMATION_COMMIT' AND json_extract(NEW.receipt_json,'$.businessOutcome')='COMMITTED' AND json_extract(NEW.receipt_json,'$.consumptionDisposition')='CONSUMED')
BEGIN SELECT RAISE(ABORT,'formal adoption receipt requires exact original business archive'); END;
CREATE TRIGGER formal_adoption_confirmation_completion BEFORE UPDATE OF intent_json ON confirmation_intents
WHEN EXISTS(SELECT 1 FROM formal_adoption_fences f WHERE f.intent_id=NEW.intent_id)
 AND EXISTS(SELECT 1 FROM json_each(NEW.intent_json,'$.steps') step WHERE json_extract(step.value,'$.kind')='START_IMPLEMENTATION' AND json_extract(step.value,'$.state')='COMPLETED'
 AND NOT EXISTS(SELECT 1 FROM formal_adoption_business b JOIN formal_adoption_receipts r ON r.ai_session_id=b.ai_session_id AND r.request_id=b.request_id
 JOIN runtime_selection_sources s ON s.ai_session_id=b.ai_session_id AND s.request_id=b.request_id AND s.adoption_request_id=b.request_id
 JOIN runtime_selection_bindings binding ON binding.source_id=s.source_id AND binding.project_id=b.project_id AND binding.task_id=b.task_id AND binding.revision_id=b.revision_id AND binding.runtime_role='IMPLEMENTATION'
 JOIN runs run ON run.execution_proposal_id=b.execution_id AND run.task_id=b.task_id AND run.project_id=b.project_id AND run.purpose='IMPLEMENTATION'
 WHERE b.ai_session_id=NEW.ai_session_id AND b.intent_id=NEW.intent_id AND b.request_id=json_extract(NEW.intent_json,'$.commit.requestId')
 AND b.admission_request_id=json_extract(step.value,'$.requestId') AND b.task_id=json_extract(step.value,'$.taskId') AND b.revision_id=json_extract(step.value,'$.revisionId') AND b.execution_id=json_extract(step.value,'$.executionId') AND run.id=json_extract(step.value,'$.runId')))
BEGIN SELECT RAISE(ABORT,'formal adoption completion requires original archive binding and Run'); END;
CREATE TRIGGER formal_adoption_business_immutable BEFORE UPDATE ON formal_adoption_business BEGIN SELECT RAISE(ABORT,'formal adoption immutable'); END;
CREATE TRIGGER formal_adoption_business_durable BEFORE DELETE ON formal_adoption_business BEGIN SELECT RAISE(ABORT,'formal adoption durable'); END;
CREATE TRIGGER formal_adoption_receipt_immutable BEFORE UPDATE ON formal_adoption_receipts BEGIN SELECT RAISE(ABORT,'formal adoption immutable'); END;
CREATE TRIGGER formal_adoption_receipt_durable BEFORE DELETE ON formal_adoption_receipts BEGIN SELECT RAISE(ABORT,'formal adoption durable'); END;
`
