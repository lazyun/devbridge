package sqlite

// Keep every original mapping receipt byte-for-byte. The mutable projection is
// only an explicit request head; readers never select a qualification by recency.
const backendPlatformV105SQL = `
DROP TRIGGER backend_platform_mapping_source;
DROP TRIGGER backend_platform_immutable_update;
DROP TRIGGER backend_platform_immutable_delete;
ALTER TABLE backend_platform_qualifications RENAME TO backend_platform_qualifications_v103;
CREATE TABLE backend_platform_qualifications (
 backend_id TEXT NOT NULL REFERENCES codex_configs(id),
 backend_fingerprint TEXT NOT NULL CHECK(length(backend_fingerprint)=64),
 runtime_role TEXT NOT NULL CHECK(runtime_role IN ('REVIEW','IMPLEMENTATION','VERIFICATION','INSPECTION')),
 descriptor_id TEXT NOT NULL REFERENCES runtime_platform_descriptors(descriptor_id),
 request_id TEXT PRIMARY KEY,
 qualification_id TEXT NOT NULL REFERENCES runtime_platform_qualifications(qualification_id),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 previous_request_id TEXT NOT NULL DEFAULT '',
 mapping_version INTEGER NOT NULL CHECK(mapping_version>0),
 CHECK((mapping_version=1 AND previous_request_id='') OR (mapping_version>1 AND previous_request_id<>'')),
 UNIQUE(backend_id,backend_fingerprint,runtime_role,descriptor_id,request_id),
 UNIQUE(backend_id,backend_fingerprint,runtime_role,descriptor_id,mapping_version)
);
INSERT INTO backend_platform_qualifications SELECT backend_id,backend_fingerprint,runtime_role,descriptor_id,request_id,qualification_id,payload_json,payload_sha256,'',1 FROM backend_platform_qualifications_v103;
DROP TABLE backend_platform_qualifications_v103;
CREATE TABLE backend_platform_mapping_heads (
 backend_id TEXT NOT NULL,
 backend_fingerprint TEXT NOT NULL,
 runtime_role TEXT NOT NULL,
 descriptor_id TEXT NOT NULL,
 request_id TEXT NOT NULL UNIQUE,
 version INTEGER NOT NULL CHECK(version>0),
 PRIMARY KEY(backend_id,backend_fingerprint,runtime_role,descriptor_id),
 FOREIGN KEY(backend_id,backend_fingerprint,runtime_role,descriptor_id,request_id) REFERENCES backend_platform_qualifications(backend_id,backend_fingerprint,runtime_role,descriptor_id,request_id)
);
INSERT INTO backend_platform_mapping_heads SELECT backend_id,backend_fingerprint,runtime_role,descriptor_id,request_id,mapping_version FROM backend_platform_qualifications;
CREATE TRIGGER backend_platform_mapping_source BEFORE INSERT ON backend_platform_qualifications
WHEN NOT EXISTS(SELECT 1 FROM backend_platform_qualification_requests r
 JOIN runtime_platform_qualifications q ON q.qualification_id=NEW.qualification_id AND q.state='CURRENT'
 JOIN runtime_platform_descriptors d ON d.descriptor_id=q.descriptor_id
 JOIN runtime_platform_lineages l ON l.platform_lineage_id=q.platform_lineage_id
 WHERE r.request_id=NEW.request_id AND r.backend_id=NEW.backend_id AND r.backend_fingerprint=NEW.backend_fingerprint AND r.runtime_role=NEW.runtime_role AND r.descriptor_id=NEW.descriptor_id
 AND r.descriptor_hash=json_extract(NEW.payload_json,'$.platform.descriptorHash') AND d.descriptor_id=NEW.descriptor_id AND d.runtime_role=NEW.runtime_role
 AND json_extract(NEW.payload_json,'$.backendId')=NEW.backend_id AND json_extract(NEW.payload_json,'$.backendFingerprint')=NEW.backend_fingerprint AND json_extract(NEW.payload_json,'$.runtimeRole')=NEW.runtime_role AND json_extract(NEW.payload_json,'$.requestId')=NEW.request_id
 AND json_extract(NEW.payload_json,'$.platform.descriptorId')=d.descriptor_id AND json_extract(NEW.payload_json,'$.platform.platformLineageId')=q.platform_lineage_id
 AND json_extract(NEW.payload_json,'$.platform.qualificationId')=q.qualification_id AND json_extract(NEW.payload_json,'$.platform.qualificationGeneration')=q.generation
 AND json_extract(NEW.payload_json,'$.platform.compatibilityEvidenceHash')=q.compatibility_evidence_hash AND json_extract(NEW.payload_json,'$.platform.compatibilityPolicyVersion')=q.compatibility_policy_version
 AND json_extract(NEW.payload_json,'$.platform.qualificationPayloadHash')=q.issuance_payload_hash AND json_extract(NEW.payload_json,'$.platform.lineageVersion')=l.version)
BEGIN SELECT RAISE(ABORT,'backend platform mapping requires exact current qualification and request'); END;
CREATE TRIGGER backend_platform_mapping_cas BEFORE INSERT ON backend_platform_qualifications
WHEN NOT (
 (NEW.mapping_version=1 AND NEW.previous_request_id='' AND NOT EXISTS(SELECT 1 FROM backend_platform_mapping_heads h WHERE h.backend_id=NEW.backend_id AND h.backend_fingerprint=NEW.backend_fingerprint AND h.runtime_role=NEW.runtime_role AND h.descriptor_id=NEW.descriptor_id))
 OR EXISTS(SELECT 1 FROM backend_platform_mapping_heads h JOIN backend_platform_qualifications m ON m.request_id=h.request_id JOIN runtime_platform_qualifications q ON q.qualification_id=m.qualification_id
 WHERE h.backend_id=NEW.backend_id AND h.backend_fingerprint=NEW.backend_fingerprint AND h.runtime_role=NEW.runtime_role AND h.descriptor_id=NEW.descriptor_id
 AND h.request_id=NEW.previous_request_id AND h.version+1=NEW.mapping_version AND q.state IN ('SUPERSEDED','INVALIDATED')))
BEGIN SELECT RAISE(ABORT,'backend platform mapping head conflict'); END;
CREATE TRIGGER backend_platform_immutable_update BEFORE UPDATE ON backend_platform_qualifications BEGIN SELECT RAISE(ABORT,'backend platform mapping is immutable'); END;
CREATE TRIGGER backend_platform_immutable_delete BEFORE DELETE ON backend_platform_qualifications BEGIN SELECT RAISE(ABORT,'backend platform mapping is immutable'); END;
CREATE TRIGGER backend_platform_head_insert BEFORE INSERT ON backend_platform_mapping_heads
WHEN NOT EXISTS(SELECT 1 FROM backend_platform_qualifications m WHERE m.request_id=NEW.request_id AND m.backend_id=NEW.backend_id AND m.backend_fingerprint=NEW.backend_fingerprint AND m.runtime_role=NEW.runtime_role AND m.descriptor_id=NEW.descriptor_id AND m.mapping_version=NEW.version AND m.previous_request_id='' AND NEW.version=1)
BEGIN SELECT RAISE(ABORT,'backend platform head requires first receipt'); END;
CREATE TRIGGER backend_platform_head_update BEFORE UPDATE ON backend_platform_mapping_heads
WHEN NEW.backend_id IS NOT OLD.backend_id OR NEW.backend_fingerprint IS NOT OLD.backend_fingerprint OR NEW.runtime_role IS NOT OLD.runtime_role OR NEW.descriptor_id IS NOT OLD.descriptor_id OR NEW.version<>OLD.version+1
 OR NOT EXISTS(SELECT 1 FROM backend_platform_qualifications m WHERE m.request_id=NEW.request_id AND m.backend_id=NEW.backend_id AND m.backend_fingerprint=NEW.backend_fingerprint AND m.runtime_role=NEW.runtime_role AND m.descriptor_id=NEW.descriptor_id AND m.mapping_version=NEW.version AND m.previous_request_id=OLD.request_id)
BEGIN SELECT RAISE(ABORT,'backend platform head requires exact next receipt'); END;
CREATE TRIGGER backend_platform_head_delete BEFORE DELETE ON backend_platform_mapping_heads BEGIN SELECT RAISE(ABORT,'backend platform head is durable'); END;
CREATE TRIGGER backend_platform_mapping_project AFTER INSERT ON backend_platform_qualifications
BEGIN
 INSERT INTO backend_platform_mapping_heads(backend_id,backend_fingerprint,runtime_role,descriptor_id,request_id,version)
 SELECT NEW.backend_id,NEW.backend_fingerprint,NEW.runtime_role,NEW.descriptor_id,NEW.request_id,NEW.mapping_version WHERE NEW.mapping_version=1;
 UPDATE backend_platform_mapping_heads SET request_id=NEW.request_id,version=NEW.mapping_version
 WHERE backend_id=NEW.backend_id AND backend_fingerprint=NEW.backend_fingerprint AND runtime_role=NEW.runtime_role AND descriptor_id=NEW.descriptor_id AND request_id=NEW.previous_request_id AND version=NEW.mapping_version-1;
END;
`
