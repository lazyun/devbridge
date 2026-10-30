package sqlite

// The final formal producer is introduced by v102. Its table may be referenced
// before it is created; no source can be inserted before that producer exists.
// No historical command, approval or qualification is backfilled.
const runtimeSelectionV101SQL = `
CREATE TABLE runtime_selection_sources (
 source_id TEXT PRIMARY KEY CHECK(length(source_id)=64 AND source_id NOT GLOB '*[^0-9a-f]*'),
 project_id TEXT NOT NULL REFERENCES projects(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 runtime_role TEXT NOT NULL CHECK(runtime_role='IMPLEMENTATION'),
 entry TEXT NOT NULL CHECK(entry IN ('RUNTIME_AUTHORIZED_REVIEW_APPROVAL_OR_CONFIRMATION','DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION')),
 ai_session_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 approval_request_id TEXT,
 adoption_request_id TEXT,
 receipt_identity TEXT NOT NULL UNIQUE CHECK(length(receipt_identity)=64),
 receipt_sha256 TEXT NOT NULL CHECK(length(receipt_sha256)=64),
 qualification_id TEXT NOT NULL REFERENCES runtime_platform_qualifications(qualification_id),
 qualification_generation INTEGER NOT NULL CHECK(qualification_generation>0),
 platform_lineage_id TEXT NOT NULL REFERENCES runtime_platform_lineages(platform_lineage_id),
 descriptor_id TEXT NOT NULL REFERENCES runtime_platform_descriptors(descriptor_id),
 source_payload_hash TEXT NOT NULL CHECK(length(source_payload_hash)=64 AND source_payload_hash NOT GLOB '*[^0-9a-f]*'),
 source_json TEXT NOT NULL CHECK(json_valid(source_json)),
 UNIQUE(ai_session_id,request_id),
 UNIQUE(source_id,project_id,task_id,revision_id,runtime_role,source_payload_hash),
 CHECK((entry='RUNTIME_AUTHORIZED_REVIEW_APPROVAL_OR_CONFIRMATION' AND approval_request_id IS request_id AND adoption_request_id IS NULL) OR (entry='DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION' AND adoption_request_id IS request_id AND approval_request_id IS NULL)),
 FOREIGN KEY(ai_session_id,approval_request_id) REFERENCES formal_execution_receipts(ai_session_id,request_id),
 FOREIGN KEY(ai_session_id,adoption_request_id) REFERENCES formal_adoption_receipts(ai_session_id,request_id)
);
CREATE TABLE runtime_selection_bindings (
 binding_id TEXT PRIMARY KEY CHECK(length(binding_id)>0),
 project_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 revision_id TEXT NOT NULL,
 runtime_role TEXT NOT NULL CHECK(runtime_role IN ('IMPLEMENTATION','VERIFICATION','REVIEW','INSPECTION')),
 source_id TEXT NOT NULL,
 source_payload_hash TEXT NOT NULL,
 entry TEXT NOT NULL CHECK(entry IN ('RUNTIME_AUTHORIZED_REVIEW_APPROVAL_OR_CONFIRMATION','DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION')),
 issuance_request_id TEXT NOT NULL UNIQUE CHECK(length(issuance_request_id)>0),
 issuance_payload_hash TEXT NOT NULL CHECK(length(issuance_payload_hash)=64 AND issuance_payload_hash NOT GLOB '*[^0-9a-f]*'),
 qualification_id TEXT NOT NULL REFERENCES runtime_platform_qualifications(qualification_id),
 qualification_generation INTEGER NOT NULL CHECK(qualification_generation>0),
 expected_lineage_version INTEGER NOT NULL CHECK(expected_lineage_version>0),
 snapshot_schema_id TEXT NOT NULL CHECK(snapshot_schema_id='NO_RUN_CURRENT_RUNTIME_AUTHORITY_V1'),
 authority_snapshot_hash TEXT NOT NULL CHECK(length(authority_snapshot_hash)=64 AND authority_snapshot_hash NOT GLOB '*[^0-9a-f]*'),
 binding_json TEXT NOT NULL CHECK(json_valid(binding_json)),
 created_at TEXT NOT NULL CHECK(length(created_at)>0),
 UNIQUE(project_id,task_id,revision_id,runtime_role),
 FOREIGN KEY(source_id,project_id,task_id,revision_id,runtime_role,source_payload_hash) REFERENCES runtime_selection_sources(source_id,project_id,task_id,revision_id,runtime_role,source_payload_hash)
);
CREATE TRIGGER runtime_selection_source_identity BEFORE INSERT ON runtime_selection_sources
WHEN NEW.approval_request_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM formal_execution_receipts r
 JOIN runtime_platform_qualifications q ON q.qualification_id=NEW.qualification_id
 JOIN task_definition_revisions v ON v.id=NEW.revision_id AND v.task_id=NEW.task_id
 JOIN tasks t ON t.id=v.task_id AND t.project_id=NEW.project_id
 WHERE r.ai_session_id=NEW.ai_session_id AND r.request_id=NEW.request_id
 AND r.receipt_identity=NEW.receipt_identity AND r.receipt_sha256=NEW.receipt_sha256
 AND q.generation=NEW.qualification_generation AND q.platform_lineage_id=NEW.platform_lineage_id AND q.descriptor_id=NEW.descriptor_id
 AND json_extract(NEW.source_json,'$.sourceId')=NEW.source_id
 AND json_extract(NEW.source_json,'$.owner.projectId')=NEW.project_id
 AND json_extract(NEW.source_json,'$.owner.taskId')=NEW.task_id
 AND json_extract(NEW.source_json,'$.owner.taskDefinitionRevisionId')=NEW.revision_id
 AND json_extract(NEW.source_json,'$.owner.runtimeRole')=NEW.runtime_role
 AND json_extract(NEW.source_json,'$.entry')=NEW.entry
 AND json_extract(NEW.source_json,'$.aiSessionId')=NEW.ai_session_id
 AND json_extract(NEW.source_json,'$.requestId')=NEW.request_id
 AND json_extract(NEW.source_json,'$.sourceReceiptIdentity')=NEW.receipt_identity
 AND json_extract(NEW.source_json,'$.sourceReceiptHash')=NEW.receipt_sha256
 AND json_extract(NEW.source_json,'$.platform.qualificationId')=NEW.qualification_id
 AND json_extract(NEW.source_json,'$.platform.qualificationGeneration')=NEW.qualification_generation
 AND json_extract(NEW.source_json,'$.platform.platformLineageId')=NEW.platform_lineage_id
 AND json_extract(NEW.source_json,'$.platform.descriptorId')=NEW.descriptor_id
 AND json_extract(NEW.source_json,'$.platform.compatibilityEvidenceHash')=q.compatibility_evidence_hash
 AND json_extract(NEW.source_json,'$.platform.qualificationPayloadHash')=q.issuance_payload_hash
 AND json_extract(NEW.source_json,'$.sourcePayloadHash')=NEW.source_payload_hash
 AND json_extract(NEW.source_json,'$.current')=1
 AND json_extract(NEW.source_json,'$.originalPurpose')='EXECUTION'
 AND json_extract(NEW.source_json,'$.originalAction')='APPROVE_AND_START'
 AND json_extract(NEW.source_json,'$.producerKind')='FORMAL_APPROVAL_COMMIT'
 AND NEW.entry='RUNTIME_AUTHORIZED_REVIEW_APPROVAL_OR_CONFIRMATION'
 AND json_extract(r.receipt_json,'$.archive.fence.projectId')=NEW.project_id
 AND json_extract(r.receipt_json,'$.archive.fence.taskId')=NEW.task_id
 AND json_extract(r.receipt_json,'$.archive.revisionId')=NEW.revision_id
 AND json_extract(r.receipt_json,'$.archive.platformReference')=json_extract(NEW.source_json,'$.platform')
 AND json_extract(r.receipt_json,'$.archive.identity.typedPayloadHash')=json_extract(NEW.source_json,'$.sourceReceiptPayloadHash')
 AND json_extract(r.receipt_json,'$.archive.identity.submittedHash')=json_extract(NEW.source_json,'$.sourceReceiptSubmittedHash')
 AND json_extract(r.receipt_json,'$.t1.identity.sourceOriginId')=NEW.source_id
 AND json_extract(r.receipt_json,'$.businessOutcome')='COMMITTED'
 AND json_extract(r.receipt_json,'$.consumptionDisposition')='CONSUMED'
 AND json_extract(r.receipt_json,'$.role')='CURRENT_FORMAL_COMMIT_RECEIPT')
BEGIN SELECT RAISE(ABORT,'runtime source requires the exact final formal archive'); END;
CREATE TRIGGER runtime_selection_adoption_source_identity BEFORE INSERT ON runtime_selection_sources
WHEN NEW.adoption_request_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM formal_adoption_receipts r
 JOIN runtime_platform_qualifications q ON q.qualification_id=NEW.qualification_id
 JOIN task_definition_revisions v ON v.id=NEW.revision_id AND v.task_id=NEW.task_id
 JOIN tasks t ON t.id=v.task_id AND t.project_id=NEW.project_id
 WHERE r.ai_session_id=NEW.ai_session_id AND r.request_id=NEW.request_id
 AND r.receipt_identity=NEW.receipt_identity AND r.receipt_sha256=NEW.receipt_sha256
 AND q.generation=NEW.qualification_generation AND q.platform_lineage_id=NEW.platform_lineage_id AND q.descriptor_id=NEW.descriptor_id
 AND json_extract(NEW.source_json,'$.sourceId')=NEW.source_id
 AND json_extract(NEW.source_json,'$.owner.projectId')=NEW.project_id
 AND json_extract(NEW.source_json,'$.owner.taskId')=NEW.task_id
 AND json_extract(NEW.source_json,'$.owner.taskDefinitionRevisionId')=NEW.revision_id
 AND json_extract(NEW.source_json,'$.owner.runtimeRole')=NEW.runtime_role
 AND json_extract(NEW.source_json,'$.entry')=NEW.entry
 AND json_extract(NEW.source_json,'$.aiSessionId')=NEW.ai_session_id
 AND json_extract(NEW.source_json,'$.requestId')=NEW.request_id
 AND json_extract(NEW.source_json,'$.sourceReceiptIdentity')=NEW.receipt_identity
 AND json_extract(NEW.source_json,'$.sourceReceiptHash')=NEW.receipt_sha256
 AND json_extract(NEW.source_json,'$.platform.qualificationId')=NEW.qualification_id
 AND json_extract(NEW.source_json,'$.platform.qualificationGeneration')=NEW.qualification_generation
 AND json_extract(NEW.source_json,'$.platform.platformLineageId')=NEW.platform_lineage_id
 AND json_extract(NEW.source_json,'$.platform.descriptorId')=NEW.descriptor_id
 AND json_extract(NEW.source_json,'$.platform.compatibilityEvidenceHash')=q.compatibility_evidence_hash
 AND json_extract(NEW.source_json,'$.platform.qualificationPayloadHash')=q.issuance_payload_hash
 AND json_extract(NEW.source_json,'$.sourcePayloadHash')=NEW.source_payload_hash
 AND json_extract(NEW.source_json,'$.current')=1
 AND json_extract(NEW.source_json,'$.originalPurpose')='EXECUTION'
 AND json_extract(NEW.source_json,'$.originalAction')='START_APPROVED'
 AND json_extract(NEW.source_json,'$.producerKind')='FORMAL_CONFIRMATION_COMMIT'
 AND NEW.entry='DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION'
 AND json_extract(r.receipt_json,'$.archive.projectId')=NEW.project_id
 AND json_extract(r.receipt_json,'$.archive.taskId')=NEW.task_id
 AND json_extract(r.receipt_json,'$.archive.revisionId')=NEW.revision_id
 AND json_extract(r.receipt_json,'$.archive.platformReference')=json_extract(NEW.source_json,'$.platform')
 AND json_extract(r.receipt_json,'$.archive.identity.typedPayloadHash')=json_extract(NEW.source_json,'$.sourceReceiptPayloadHash')
 AND json_extract(r.receipt_json,'$.archive.identity.submittedHash')=json_extract(NEW.source_json,'$.sourceReceiptSubmittedHash')
 AND json_extract(r.receipt_json,'$.archive.executionId')=json_extract(NEW.source_json,'$.resultId')
 AND json_extract(r.receipt_json,'$.archive.priorApproval.receiptIdentity')=json_extract(NEW.source_json,'$.priorApprovalReceiptIdentity')
 AND json_extract(r.receipt_json,'$.businessOutcome')='COMMITTED'
 AND json_extract(r.receipt_json,'$.consumptionDisposition')='CONSUMED'
 AND json_extract(r.receipt_json,'$.role')='CURRENT_FORMAL_COMMIT_RECEIPT')
BEGIN SELECT RAISE(ABORT,'runtime source requires the exact final formal archive'); END;
CREATE TRIGGER runtime_selection_binding_identity BEFORE INSERT ON runtime_selection_bindings
WHEN NOT EXISTS(SELECT 1 FROM runtime_selection_sources s
 JOIN tasks t ON t.id=s.task_id AND t.project_id=s.project_id AND t.current_definition_revision_id=s.revision_id
 JOIN runtime_platform_qualifications q ON q.qualification_id=s.qualification_id AND q.generation=s.qualification_generation
 JOIN runtime_platform_lineages l ON l.platform_lineage_id=q.platform_lineage_id AND l.runtime_role=NEW.runtime_role
 WHERE s.source_id=NEW.source_id AND s.project_id=NEW.project_id AND s.task_id=NEW.task_id AND s.revision_id=NEW.revision_id AND s.runtime_role=NEW.runtime_role AND s.entry=NEW.entry AND s.source_payload_hash=NEW.source_payload_hash
 AND q.qualification_id=NEW.qualification_id AND q.generation=NEW.qualification_generation AND q.state='CURRENT' AND l.version=NEW.expected_lineage_version
 AND json_extract(NEW.binding_json,'$.bindingId')=NEW.binding_id
 AND json_extract(NEW.binding_json,'$.owner')=json_extract(s.source_json,'$.owner')
 AND json_extract(NEW.binding_json,'$.platform')=json_extract(s.source_json,'$.platform')
 AND json_extract(NEW.binding_json,'$.sourceId')=NEW.source_id
 AND json_extract(NEW.binding_json,'$.sourcePayloadHash')=NEW.source_payload_hash
 AND json_extract(NEW.binding_json,'$.entry')=NEW.entry
 AND json_extract(NEW.binding_json,'$.issuanceRequestId')=NEW.issuance_request_id
 AND json_extract(NEW.binding_json,'$.issuancePayloadHash')=NEW.issuance_payload_hash
 AND json_extract(NEW.binding_json,'$.snapshotSchemaId')=NEW.snapshot_schema_id
 AND json_extract(NEW.binding_json,'$.authoritySnapshotHash')=NEW.authority_snapshot_hash)
BEGIN SELECT RAISE(ABORT,'runtime binding requires exact current owner and qualification'); END;
CREATE TRIGGER runtime_selection_source_immutable BEFORE UPDATE ON runtime_selection_sources BEGIN SELECT RAISE(ABORT,'runtime source immutable'); END;
CREATE TRIGGER runtime_selection_source_durable BEFORE DELETE ON runtime_selection_sources BEGIN SELECT RAISE(ABORT,'runtime source durable'); END;
CREATE TRIGGER runtime_selection_binding_immutable BEFORE UPDATE ON runtime_selection_bindings BEGIN SELECT RAISE(ABORT,'runtime binding immutable'); END;
CREATE TRIGGER runtime_selection_binding_durable BEFORE DELETE ON runtime_selection_bindings BEGIN SELECT RAISE(ABORT,'runtime binding durable'); END;
`
