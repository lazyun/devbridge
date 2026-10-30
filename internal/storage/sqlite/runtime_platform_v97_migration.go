package sqlite

const runtimePlatformV97SQL = `
CREATE TABLE runtime_platform_lineages (
 platform_lineage_id TEXT PRIMARY KEY CHECK(length(trim(platform_lineage_id))>0),
 runtime_role TEXT NOT NULL CHECK(runtime_role IN ('IMPLEMENTATION','VERIFICATION','REVIEW','INSPECTION')),
 high_water INTEGER NOT NULL DEFAULT 0 CHECK(high_water>=0),
 version INTEGER NOT NULL DEFAULT 0 CHECK(version>=high_water),
 UNIQUE(platform_lineage_id,runtime_role)
);
CREATE TABLE runtime_platform_descriptors (
 descriptor_id TEXT PRIMARY KEY CHECK(length(trim(descriptor_id))>0),
 platform_lineage_id TEXT NOT NULL,
 runtime_role TEXT NOT NULL,
 adapter_id TEXT NOT NULL CHECK(length(trim(adapter_id))>0),
 adapter_implementation_digest TEXT NOT NULL CHECK(length(adapter_implementation_digest)=64 AND adapter_implementation_digest NOT GLOB '*[^0-9a-f]*'),
 protocol_id TEXT NOT NULL CHECK(length(trim(protocol_id))>0),
 protocol_schema_digest TEXT NOT NULL CHECK(length(protocol_schema_digest)=64 AND protocol_schema_digest NOT GLOB '*[^0-9a-f]*'),
 capability_digest TEXT NOT NULL CHECK(length(capability_digest)=64 AND capability_digest NOT GLOB '*[^0-9a-f]*'),
 descriptor_version INTEGER NOT NULL CHECK(descriptor_version>0),
 FOREIGN KEY(platform_lineage_id,runtime_role) REFERENCES runtime_platform_lineages(platform_lineage_id,runtime_role)
);
CREATE TABLE runtime_platform_compatibility_evidence (
 evidence_hash TEXT PRIMARY KEY CHECK(length(evidence_hash)=64 AND evidence_hash NOT GLOB '*[^0-9a-f]*'),
 descriptor_id TEXT NOT NULL REFERENCES runtime_platform_descriptors(descriptor_id),
 platform_lineage_id TEXT NOT NULL,
 runtime_role TEXT NOT NULL,
 adapter_implementation_digest TEXT NOT NULL CHECK(length(adapter_implementation_digest)=64 AND adapter_implementation_digest NOT GLOB '*[^0-9a-f]*'),
 protocol_schema_digest TEXT NOT NULL CHECK(length(protocol_schema_digest)=64 AND protocol_schema_digest NOT GLOB '*[^0-9a-f]*'),
 compatibility_policy_version TEXT NOT NULL CHECK(length(trim(compatibility_policy_version))>0),
 measured_compatibility INTEGER NOT NULL CHECK(measured_compatibility=1),
 collector_identity TEXT NOT NULL CHECK(length(trim(collector_identity))>0),
 collection_artifact_hash TEXT NOT NULL CHECK(length(collection_artifact_hash)=64 AND collection_artifact_hash NOT GLOB '*[^0-9a-f]*'),
 source TEXT NOT NULL CHECK(source='CORE_COMPATIBILITY_COLLECTION')
);
CREATE TABLE runtime_platform_requests (
 request_id TEXT PRIMARY KEY CHECK(length(trim(request_id))>0),
 operation TEXT NOT NULL CHECK(operation IN ('QUALIFY','SUPERSEDE','INVALIDATE')),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64 AND payload_hash NOT GLOB '*[^0-9a-f]*'),
 qualification_id TEXT NOT NULL,
 platform_lineage_id TEXT NOT NULL REFERENCES runtime_platform_lineages(platform_lineage_id),
 generation INTEGER NOT NULL CHECK(generation>0),
 expected_lineage_version INTEGER NOT NULL CHECK(expected_lineage_version>=0),
 predecessor_qualification_id TEXT NOT NULL DEFAULT '',
 predecessor_generation INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL CHECK(length(created_at)>0),
 issuance_binding TEXT,
 invalidation_binding TEXT,
 CHECK((operation='SUPERSEDE' AND length(predecessor_qualification_id)>0 AND predecessor_generation>0) OR (operation<>'SUPERSEDE' AND predecessor_qualification_id='' AND predecessor_generation=0)),
 CHECK((operation IN ('QUALIFY','SUPERSEDE') AND issuance_binding IS NOT NULL AND issuance_binding=request_id AND invalidation_binding IS NULL) OR (operation='INVALIDATE' AND invalidation_binding IS NOT NULL AND invalidation_binding=request_id AND issuance_binding IS NULL)),
 FOREIGN KEY(qualification_id,platform_lineage_id,generation,issuance_binding) REFERENCES runtime_platform_qualifications(qualification_id,platform_lineage_id,generation,issuance_request_id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(qualification_id,platform_lineage_id,generation,invalidation_binding) REFERENCES runtime_platform_qualifications(qualification_id,platform_lineage_id,generation,invalidation_request_id) DEFERRABLE INITIALLY DEFERRED
);
CREATE TABLE runtime_platform_qualifications (
 qualification_id TEXT PRIMARY KEY CHECK(length(trim(qualification_id))>0),
 platform_lineage_id TEXT NOT NULL REFERENCES runtime_platform_lineages(platform_lineage_id),
 generation INTEGER NOT NULL CHECK(generation>0),
 state TEXT NOT NULL CHECK(state IN ('CURRENT','SUPERSEDED','INVALIDATED')),
 descriptor_id TEXT NOT NULL REFERENCES runtime_platform_descriptors(descriptor_id),
 compatibility_policy_version TEXT NOT NULL,
 compatibility_evidence_hash TEXT NOT NULL REFERENCES runtime_platform_compatibility_evidence(evidence_hash),
 outcome TEXT NOT NULL CHECK(outcome='QUALIFIED'),
 issuance_request_id TEXT NOT NULL UNIQUE REFERENCES runtime_platform_requests(request_id),
 issuance_payload_hash TEXT NOT NULL CHECK(length(issuance_payload_hash)=64 AND issuance_payload_hash NOT GLOB '*[^0-9a-f]*'),
 created_at TEXT NOT NULL CHECK(length(created_at)>0),
 superseded_at TEXT NOT NULL DEFAULT '',
 invalidated_at TEXT NOT NULL DEFAULT '',
 invalidation_reason TEXT NOT NULL DEFAULT '',
 lineage_version INTEGER NOT NULL CHECK(lineage_version>0),
 invalidation_request_id TEXT UNIQUE REFERENCES runtime_platform_requests(request_id),
 invalidation_payload_hash TEXT NOT NULL DEFAULT '',
 UNIQUE(platform_lineage_id,generation),
 UNIQUE(qualification_id,platform_lineage_id,generation,issuance_request_id),
 UNIQUE(qualification_id,platform_lineage_id,generation,invalidation_request_id),
 CHECK((state='CURRENT' AND superseded_at='') OR state<>'CURRENT'),
 CHECK((state='SUPERSEDED' AND superseded_at<>'') OR state<>'SUPERSEDED'),
 CHECK((state='INVALIDATED' AND invalidated_at<>'' AND length(trim(invalidation_reason))>0 AND invalidation_request_id IS NOT NULL AND length(invalidation_payload_hash)=64 AND invalidation_payload_hash NOT GLOB '*[^0-9a-f]*') OR (state<>'INVALIDATED' AND invalidated_at='' AND invalidation_reason='' AND invalidation_request_id IS NULL AND invalidation_payload_hash=''))
);
CREATE UNIQUE INDEX runtime_platform_one_current ON runtime_platform_qualifications(platform_lineage_id) WHERE state='CURRENT';
CREATE TRIGGER runtime_platform_descriptor_immutable BEFORE UPDATE ON runtime_platform_descriptors BEGIN SELECT RAISE(ABORT,'platform descriptor immutable'); END;
CREATE TRIGGER runtime_platform_descriptor_durable BEFORE DELETE ON runtime_platform_descriptors BEGIN SELECT RAISE(ABORT,'platform descriptor durable'); END;
CREATE TRIGGER runtime_platform_evidence_immutable BEFORE UPDATE ON runtime_platform_compatibility_evidence BEGIN SELECT RAISE(ABORT,'platform evidence immutable'); END;
CREATE TRIGGER runtime_platform_evidence_durable BEFORE DELETE ON runtime_platform_compatibility_evidence BEGIN SELECT RAISE(ABORT,'platform evidence durable'); END;
CREATE TRIGGER runtime_platform_request_immutable BEFORE UPDATE ON runtime_platform_requests BEGIN SELECT RAISE(ABORT,'platform request immutable'); END;
CREATE TRIGGER runtime_platform_request_durable BEFORE DELETE ON runtime_platform_requests BEGIN SELECT RAISE(ABORT,'platform request durable'); END;
CREATE TRIGGER runtime_platform_qualification_durable BEFORE DELETE ON runtime_platform_qualifications BEGIN SELECT RAISE(ABORT,'platform qualification durable'); END;
CREATE TRIGGER runtime_platform_lineage_durable BEFORE DELETE ON runtime_platform_lineages BEGIN SELECT RAISE(ABORT,'platform lineage durable'); END;
CREATE TRIGGER runtime_platform_lineage_initial BEFORE INSERT ON runtime_platform_lineages WHEN NEW.high_water<>0 OR NEW.version<>0 BEGIN SELECT RAISE(ABORT,'platform lineage must start empty'); END;
CREATE TRIGGER runtime_platform_lineage_advance BEFORE UPDATE ON runtime_platform_lineages
WHEN NEW.platform_lineage_id IS NOT OLD.platform_lineage_id OR NEW.runtime_role IS NOT OLD.runtime_role
 OR NEW.version<>OLD.version+1 OR NEW.high_water NOT IN(OLD.high_water,OLD.high_water+1)
 OR NEW.high_water<>(SELECT COALESCE(MAX(generation),0) FROM runtime_platform_qualifications WHERE platform_lineage_id=OLD.platform_lineage_id)
 OR NEW.version<>(SELECT COALESCE(MAX(lineage_version),0) FROM runtime_platform_qualifications WHERE platform_lineage_id=OLD.platform_lineage_id)
BEGIN SELECT RAISE(ABORT,'platform lineage advance requires exact lifecycle write'); END;
CREATE TRIGGER runtime_platform_evidence_owner BEFORE INSERT ON runtime_platform_compatibility_evidence
WHEN NOT EXISTS(SELECT 1 FROM runtime_platform_descriptors d WHERE d.descriptor_id=NEW.descriptor_id AND d.platform_lineage_id=NEW.platform_lineage_id AND d.runtime_role=NEW.runtime_role AND d.adapter_implementation_digest=NEW.adapter_implementation_digest AND d.protocol_schema_digest=NEW.protocol_schema_digest)
BEGIN SELECT RAISE(ABORT,'platform evidence descriptor mismatch'); END;
CREATE TRIGGER runtime_platform_qualification_insert BEFORE INSERT ON runtime_platform_qualifications
WHEN NEW.state<>'CURRENT' OR NEW.superseded_at<>'' OR NEW.invalidated_at<>''
 OR NOT EXISTS(SELECT 1 FROM runtime_platform_lineages l JOIN runtime_platform_descriptors d ON d.platform_lineage_id=l.platform_lineage_id JOIN runtime_platform_compatibility_evidence e ON e.descriptor_id=d.descriptor_id JOIN runtime_platform_requests r ON r.request_id=NEW.issuance_request_id
 WHERE l.platform_lineage_id=NEW.platform_lineage_id AND NEW.generation=l.high_water+1 AND NEW.lineage_version=l.version+1
 AND d.descriptor_id=NEW.descriptor_id AND e.evidence_hash=NEW.compatibility_evidence_hash AND e.compatibility_policy_version=NEW.compatibility_policy_version AND e.measured_compatibility=1 AND e.source='CORE_COMPATIBILITY_COLLECTION'
 AND r.qualification_id=NEW.qualification_id AND r.platform_lineage_id=NEW.platform_lineage_id AND r.generation=NEW.generation AND r.payload_hash=NEW.issuance_payload_hash AND r.expected_lineage_version=l.version AND r.created_at=NEW.created_at
 AND ((r.operation='QUALIFY' AND NOT EXISTS(SELECT 1 FROM runtime_platform_qualifications old WHERE old.platform_lineage_id=l.platform_lineage_id AND old.state='CURRENT'))
 OR (r.operation='SUPERSEDE' AND EXISTS(SELECT 1 FROM runtime_platform_qualifications old WHERE old.qualification_id=r.predecessor_qualification_id AND old.generation=r.predecessor_generation AND old.platform_lineage_id=l.platform_lineage_id AND old.state='SUPERSEDED' AND old.superseded_at=NEW.created_at AND old.lineage_version=NEW.lineage_version))))
BEGIN SELECT RAISE(ABORT,'platform qualification source or generation mismatch'); END;
CREATE TRIGGER runtime_platform_qualification_insert_lineage AFTER INSERT ON runtime_platform_qualifications
BEGIN UPDATE runtime_platform_lineages SET high_water=NEW.generation,version=NEW.lineage_version WHERE platform_lineage_id=NEW.platform_lineage_id; END;
CREATE TRIGGER runtime_platform_qualification_identity BEFORE UPDATE ON runtime_platform_qualifications
WHEN NEW.qualification_id IS NOT OLD.qualification_id OR NEW.platform_lineage_id IS NOT OLD.platform_lineage_id OR NEW.generation IS NOT OLD.generation OR NEW.descriptor_id IS NOT OLD.descriptor_id OR NEW.compatibility_policy_version IS NOT OLD.compatibility_policy_version OR NEW.compatibility_evidence_hash IS NOT OLD.compatibility_evidence_hash OR NEW.outcome IS NOT OLD.outcome OR NEW.issuance_request_id IS NOT OLD.issuance_request_id OR NEW.issuance_payload_hash IS NOT OLD.issuance_payload_hash OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT,'platform qualification identity immutable'); END;
CREATE TRIGGER runtime_platform_qualification_lifecycle BEFORE UPDATE ON runtime_platform_qualifications
WHEN NOT (
 (OLD.state='CURRENT' AND NEW.state='SUPERSEDED' AND NEW.invalidated_at=OLD.invalidated_at AND NEW.invalidation_reason=OLD.invalidation_reason AND NEW.invalidation_request_id IS OLD.invalidation_request_id AND NEW.invalidation_payload_hash=OLD.invalidation_payload_hash
 AND EXISTS(SELECT 1 FROM runtime_platform_requests r JOIN runtime_platform_lineages l ON l.platform_lineage_id=r.platform_lineage_id WHERE r.operation='SUPERSEDE' AND r.platform_lineage_id=OLD.platform_lineage_id AND r.predecessor_qualification_id=OLD.qualification_id AND r.predecessor_generation=OLD.generation AND r.expected_lineage_version=l.version AND r.generation=l.high_water+1 AND r.created_at=NEW.superseded_at AND NEW.lineage_version=l.version+1))
 OR (OLD.state IN ('CURRENT','SUPERSEDED') AND NEW.state='INVALIDATED' AND NEW.superseded_at=OLD.superseded_at
 AND EXISTS(SELECT 1 FROM runtime_platform_requests r JOIN runtime_platform_lineages l ON l.platform_lineage_id=r.platform_lineage_id WHERE r.request_id=NEW.invalidation_request_id AND r.operation='INVALIDATE' AND r.qualification_id=OLD.qualification_id AND r.platform_lineage_id=OLD.platform_lineage_id AND r.generation=OLD.generation AND r.payload_hash=NEW.invalidation_payload_hash AND r.expected_lineage_version=l.version AND r.created_at=NEW.invalidated_at AND NEW.lineage_version=l.version+1))
)
BEGIN SELECT RAISE(ABORT,'platform qualification lifecycle mismatch'); END;
CREATE TRIGGER runtime_platform_qualification_invalidation_lineage AFTER UPDATE ON runtime_platform_qualifications WHEN NEW.state='INVALIDATED'
BEGIN UPDATE runtime_platform_lineages SET version=NEW.lineage_version WHERE platform_lineage_id=NEW.platform_lineage_id; END;
`
