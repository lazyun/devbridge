package sqlite

// These tables describe only newly prepared definition-only approvals. No
// historical confirmation, runtime selection, or qualification is backfilled.
// The business basis and final receipt are deliberately separate commits.
const formalDefinitionApprovalV99SQL = `
CREATE TABLE formal_definition_approval_fences (
 intent_id TEXT PRIMARY KEY REFERENCES confirmation_intents(intent_id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 intent_version INTEGER NOT NULL CHECK(intent_version>0),
 project_id TEXT NOT NULL REFERENCES projects(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 review_id TEXT NOT NULL REFERENCES reviews(id),
 decision_id TEXT NOT NULL REFERENCES review_approval_decisions(decision_id),
 parent_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 content_hash TEXT NOT NULL CHECK(length(content_hash)=64),
 fence_json TEXT NOT NULL CHECK(json_valid(fence_json)),
 fence_sha256 TEXT NOT NULL CHECK(length(fence_sha256)=64),
 UNIQUE(intent_id,ai_session_id,intent_version)
);
CREATE TABLE formal_definition_approval_business (
 ai_session_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 intent_id TEXT NOT NULL UNIQUE REFERENCES formal_definition_approval_fences(intent_id),
 approval_step_request_id TEXT NOT NULL,
 decision_id TEXT NOT NULL UNIQUE REFERENCES decision_resolution_receipts(decision_id),
 revision_id TEXT NOT NULL UNIQUE REFERENCES task_definition_revisions(id),
 snapshot_hash TEXT NOT NULL CHECK(length(snapshot_hash)=64),
 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
 PRIMARY KEY(ai_session_id,request_id),
 UNIQUE(ai_session_id,approval_step_request_id),
 UNIQUE(ai_session_id,request_id,snapshot_hash),
 FOREIGN KEY(ai_session_id,request_id) REFERENCES confirmation_commit_requests(ai_session_id,request_id)
);
CREATE TABLE formal_definition_approval_receipts (
 ai_session_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 snapshot_hash TEXT NOT NULL CHECK(length(snapshot_hash)=64),
 receipt_identity TEXT NOT NULL UNIQUE CHECK(length(receipt_identity)=64),
 receipt_sha256 TEXT NOT NULL CHECK(length(receipt_sha256)=64),
 receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json)),
 PRIMARY KEY(ai_session_id,request_id),
 FOREIGN KEY(ai_session_id,request_id,snapshot_hash) REFERENCES formal_definition_approval_business(ai_session_id,request_id,snapshot_hash)
);
CREATE TRIGGER formal_definition_fence_source BEFORE INSERT ON formal_definition_approval_fences
WHEN NOT EXISTS(SELECT 1 FROM confirmation_intents i
 JOIN tasks t ON t.id=NEW.task_id AND t.project_id=NEW.project_id
 JOIN reviews v ON v.id=NEW.review_id AND v.task_id=t.id AND v.project_id=t.project_id
 JOIN review_approval_decisions d ON d.decision_id=NEW.decision_id AND d.review_id=v.id AND d.task_id=t.id AND d.project_id=t.project_id
 JOIN task_definition_revisions r ON r.id=NEW.parent_revision_id AND r.task_id=t.id
 WHERE i.intent_id=NEW.intent_id AND i.ai_session_id=NEW.ai_session_id AND i.project_id=t.project_id
 AND json_extract(i.intent_json,'$.version')=NEW.intent_version
 AND i.prepare_hash=NEW.content_hash AND json_extract(i.intent_json,'$.state')='PENDING'
 AND json_extract(i.intent_json,'$.purpose')='EXECUTION' AND json_extract(i.intent_json,'$.action')='APPROVE'
 AND json_extract(i.intent_json,'$.content.execution.taskId')=t.id
 AND json_extract(i.intent_json,'$.content.execution.reviewId')=v.id
 AND json_extract(i.intent_json,'$.content.execution.decisionId')=d.decision_id
 AND json_extract(i.intent_json,'$.content.execution.taskDefinitionRevisionId')=r.id
 AND json_extract(r.payload_json,'$.version')=2 AND t.current_definition_revision_id=r.id
 AND json_extract(d.result_text,'$.version')=2
 AND json_extract(d.result_text,'$.workSpec.implementationMode')='CODING_AGENT'
 AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEW_APPROVAL_PENDING'
 AND json_extract(NEW.fence_json,'$.aiSessionId')=NEW.ai_session_id
 AND json_extract(NEW.fence_json,'$.intentId')=NEW.intent_id
 AND json_extract(NEW.fence_json,'$.intentVersion')=NEW.intent_version
 AND json_extract(NEW.fence_json,'$.projectId')=NEW.project_id
 AND json_extract(NEW.fence_json,'$.taskId')=NEW.task_id
 AND json_extract(NEW.fence_json,'$.reviewId')=NEW.review_id
 AND json_extract(NEW.fence_json,'$.decisionId')=NEW.decision_id
 AND json_extract(NEW.fence_json,'$.parentRevisionId')=NEW.parent_revision_id
 AND json_extract(NEW.fence_json,'$.parentRevisionHash')=r.payload_sha256
 AND json_extract(NEW.fence_json,'$.parentDefinitionJson')=r.payload_json
 AND json_extract(NEW.fence_json,'$.contentHash')=NEW.content_hash
 AND json_extract(NEW.fence_json,'$.frozenPrepareJson')=json_extract(i.intent_json,'$.content'))
BEGIN SELECT RAISE(ABORT,'formal approval prepare requires exact current durable source'); END;
CREATE TRIGGER formal_definition_business_source BEFORE INSERT ON formal_definition_approval_business
WHEN NOT EXISTS(SELECT 1 FROM formal_definition_approval_fences f
 JOIN confirmation_intents i ON i.intent_id=f.intent_id AND i.ai_session_id=f.ai_session_id
 JOIN confirmation_commit_requests c ON c.intent_id=i.intent_id AND c.ai_session_id=i.ai_session_id AND c.request_id=NEW.request_id AND c.outcome='CONSUMED'
 JOIN decision_resolution_receipts d ON d.decision_id=f.decision_id AND d.project_id=f.project_id AND d.subject_id=f.task_id AND d.result_id=f.task_id AND d.option='APPROVE' AND d.actor_kind='AI_SESSION' AND d.actor_id=f.ai_session_id
 JOIN confirmation_approval_sources a ON a.decision_id=d.decision_id AND a.intent_id=f.intent_id AND a.revision_id=NEW.revision_id
 JOIN review_approval_materializations m ON m.decision_id=d.decision_id AND m.task_id=f.task_id AND m.initial_revision_id=a.revision_id
 JOIN task_definition_revisions r ON r.id=a.revision_id AND r.task_id=f.task_id
 WHERE f.intent_id=NEW.intent_id AND f.ai_session_id=NEW.ai_session_id AND f.decision_id=NEW.decision_id
 AND d.request_id=NEW.approval_step_request_id AND r.id<>f.parent_revision_id
 AND json_extract(r.payload_json,'$.version')=2
 AND json_extract(r.payload_json,'$.workSpec.implementationMode')='CODING_AGENT'
 AND json_extract(i.intent_json,'$.commit.requestId')=NEW.request_id
 AND EXISTS(SELECT 1 FROM json_each(i.intent_json,'$.steps') step
   WHERE json_extract(step.value,'$.kind')='APPROVE'
   AND json_extract(step.value,'$.requestId')=NEW.approval_step_request_id
   AND json_extract(step.value,'$.state')='UNKNOWN')
 AND json_extract(NEW.snapshot_json,'$.fence')=f.fence_json
 AND json_extract(NEW.snapshot_json,'$.commit')=json_extract(i.intent_json,'$.commit')
 AND json_extract(NEW.snapshot_json,'$.typedPayloadHash')=c.payload_sha256
 AND json_extract(NEW.snapshot_json,'$.submittedHash')=c.submitted_sha256
 AND json_extract(NEW.snapshot_json,'$.originalRequestJson')=c.request_json
 AND json_extract(NEW.snapshot_json,'$.approvalStepRequestId')=NEW.approval_step_request_id
 AND json_extract(NEW.snapshot_json,'$.resultId')=NEW.decision_id
 AND json_extract(NEW.snapshot_json,'$.revisionId')=NEW.revision_id
 AND json_extract(NEW.snapshot_json,'$.revisionHash')=r.payload_sha256
 AND json_extract(NEW.snapshot_json,'$.definitionJson')=r.payload_json)
BEGIN SELECT RAISE(ABORT,'formal approval business basis requires exact committed approval'); END;
CREATE TRIGGER formal_definition_receipt_source BEFORE INSERT ON formal_definition_approval_receipts
WHEN NOT EXISTS(SELECT 1 FROM formal_definition_approval_business b WHERE b.ai_session_id=NEW.ai_session_id AND b.request_id=NEW.request_id AND b.snapshot_hash=NEW.snapshot_hash
 AND json_extract(NEW.receipt_json,'$.snapshot')=b.snapshot_json
 AND json_extract(NEW.receipt_json,'$.snapshotHash')=b.snapshot_hash
 AND json_extract(NEW.receipt_json,'$.receiptIdentity')=NEW.receipt_identity
 AND json_extract(NEW.receipt_json,'$.role')='CURRENT_FORMAL_COMMIT_RECEIPT'
 AND json_extract(NEW.receipt_json,'$.producerKind')='DEFINITION_ONLY_COMMIT'
 AND json_extract(NEW.receipt_json,'$.transitionId')='confirmation.commit'
 AND json_extract(NEW.receipt_json,'$.businessOutcome')='COMMITTED'
 AND json_extract(NEW.receipt_json,'$.consumptionDisposition')='CONSUMED')
BEGIN SELECT RAISE(ABORT,'formal approval receipt requires exact immutable business basis'); END;
CREATE TRIGGER formal_definition_confirmation_completion BEFORE UPDATE OF intent_json ON confirmation_intents
WHEN EXISTS(SELECT 1 FROM formal_definition_approval_fences f WHERE f.intent_id=NEW.intent_id)
 AND EXISTS(SELECT 1 FROM json_each(NEW.intent_json,'$.steps') step WHERE json_extract(step.value,'$.kind')='APPROVE' AND json_extract(step.value,'$.state')='COMPLETED'
 AND NOT EXISTS(SELECT 1 FROM formal_definition_approval_business b JOIN formal_definition_approval_receipts r ON r.ai_session_id=b.ai_session_id AND r.request_id=b.request_id AND r.snapshot_hash=b.snapshot_hash
 WHERE b.ai_session_id=NEW.ai_session_id AND b.intent_id=NEW.intent_id AND b.request_id=json_extract(NEW.intent_json,'$.commit.requestId') AND b.approval_step_request_id=json_extract(step.value,'$.requestId')
 AND b.revision_id=json_extract(step.value,'$.revisionId')
 AND json_extract(b.snapshot_json,'$.fence.taskId')=json_extract(step.value,'$.taskId')
 AND COALESCE(json_extract(step.value,'$.executionId'),'')=''
 AND COALESCE(json_extract(step.value,'$.runId'),'')=''
 AND COALESCE(json_extract(step.value,'$.workflowId'),'')=''))
BEGIN SELECT RAISE(ABORT,'formal approval completion requires final original receipt'); END;
CREATE TRIGGER formal_definition_fence_immutable BEFORE UPDATE ON formal_definition_approval_fences BEGIN SELECT RAISE(ABORT,'formal approval prepare is immutable'); END;
CREATE TRIGGER formal_definition_fence_durable BEFORE DELETE ON formal_definition_approval_fences BEGIN SELECT RAISE(ABORT,'formal approval prepare is durable'); END;
CREATE TRIGGER formal_definition_business_immutable BEFORE UPDATE ON formal_definition_approval_business BEGIN SELECT RAISE(ABORT,'formal approval business basis is immutable'); END;
CREATE TRIGGER formal_definition_business_durable BEFORE DELETE ON formal_definition_approval_business BEGIN SELECT RAISE(ABORT,'formal approval business basis is durable'); END;
CREATE TRIGGER formal_definition_receipt_immutable BEFORE UPDATE ON formal_definition_approval_receipts BEGIN SELECT RAISE(ABORT,'formal approval receipt is immutable'); END;
CREATE TRIGGER formal_definition_receipt_durable BEFORE DELETE ON formal_definition_approval_receipts BEGIN SELECT RAISE(ABORT,'formal approval receipt is durable'); END;
`
