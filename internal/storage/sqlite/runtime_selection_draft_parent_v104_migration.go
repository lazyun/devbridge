package sqlite

// A separately versioned parent-only decoder preserves existing v99 archives.
const formalDefinitionDraftParentV104SQL = `
DROP TRIGGER formal_definition_fence_source;
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
 AND ((json_extract(r.payload_json,'$.version')=2 AND json_type(NEW.fence_json,'$.parentSchema') IS NULL)
 OR (json_extract(r.payload_json,'$.version')=1
 AND json_extract(NEW.fence_json,'$.parentSchema.schemaId')='TASK_DEFINITION_DRAFT'
 AND json_extract(NEW.fence_json,'$.parentSchema.definitionVersion')=1
 AND json_extract(NEW.fence_json,'$.parentSchema.factSchemaId')='FORMAL_TASK_DEFINITION_DRAFT'
 AND COALESCE(json_extract(r.payload_json,'$.reviewApprovalId'),'')=''
 AND COALESCE(json_extract(r.payload_json,'$.workSpec.implementationMode'),'')=''
 AND COALESCE(json_extract(r.payload_json,'$.workSpec.codexInstruction'),'')=''
 AND COALESCE(json_extract(r.payload_json,'$.workSpec.executionScope.kind'),'')=''))
 AND t.current_definition_revision_id=r.id
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
`
