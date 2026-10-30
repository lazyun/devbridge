package sqlite

const backendPlatformV103SQL = `
CREATE TABLE review_definition_admissions (
 review_id TEXT PRIMARY KEY REFERENCES reviews(id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 revision_hash TEXT NOT NULL CHECK(length(revision_hash)=64)
);
CREATE TRIGGER review_definition_admission_source BEFORE INSERT ON review_definition_admissions
WHEN NOT EXISTS(SELECT 1 FROM reviews v JOIN tasks t ON t.id=v.task_id JOIN task_definition_revisions d ON d.id=t.current_definition_revision_id AND d.task_id=t.id WHERE v.id=NEW.review_id AND v.project_id=NEW.project_id AND v.task_id=NEW.task_id AND t.project_id=v.project_id AND d.id=NEW.revision_id AND d.payload_sha256=NEW.revision_hash)
BEGIN SELECT RAISE(ABORT,'review requires exact authored definition'); END;
CREATE TRIGGER review_definition_admission_immutable_update BEFORE UPDATE ON review_definition_admissions BEGIN SELECT RAISE(ABORT,'review definition admission is immutable'); END;
CREATE TRIGGER review_definition_admission_immutable_delete BEFORE DELETE ON review_definition_admissions BEGIN SELECT RAISE(ABORT,'review definition admission is immutable'); END;
CREATE TABLE backend_platform_qualification_requests (
 request_id TEXT PRIMARY KEY,
 backend_id TEXT NOT NULL REFERENCES codex_configs(id),
 backend_fingerprint TEXT NOT NULL CHECK(length(backend_fingerprint)=64),
 runtime_role TEXT NOT NULL CHECK(runtime_role IN ('REVIEW','IMPLEMENTATION','VERIFICATION','INSPECTION')),
 descriptor_id TEXT NOT NULL,
 descriptor_hash TEXT NOT NULL CHECK(length(descriptor_hash)=64)
);
CREATE TRIGGER backend_platform_request_immutable_update BEFORE UPDATE ON backend_platform_qualification_requests BEGIN SELECT RAISE(ABORT,'backend platform request is immutable'); END;
CREATE TRIGGER backend_platform_request_immutable_delete BEFORE DELETE ON backend_platform_qualification_requests BEGIN SELECT RAISE(ABORT,'backend platform request is immutable'); END;
CREATE TABLE backend_platform_qualifications (
 backend_id TEXT NOT NULL REFERENCES codex_configs(id),
 backend_fingerprint TEXT NOT NULL CHECK(length(backend_fingerprint)=64),
 runtime_role TEXT NOT NULL,
 descriptor_id TEXT NOT NULL REFERENCES runtime_platform_descriptors(descriptor_id),
 request_id TEXT NOT NULL UNIQUE,
 qualification_id TEXT NOT NULL REFERENCES runtime_platform_qualifications(qualification_id),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 PRIMARY KEY(backend_id,backend_fingerprint,runtime_role,descriptor_id)
);
CREATE TRIGGER backend_platform_mapping_source BEFORE INSERT ON backend_platform_qualifications
WHEN NOT EXISTS(SELECT 1 FROM backend_platform_qualification_requests r WHERE r.request_id=NEW.request_id AND r.backend_id=NEW.backend_id AND r.backend_fingerprint=NEW.backend_fingerprint AND r.runtime_role=NEW.runtime_role AND r.descriptor_id=NEW.descriptor_id AND r.descriptor_hash=json_extract(NEW.payload_json,'$.platform.descriptorHash'))
BEGIN SELECT RAISE(ABORT,'backend platform mapping requires exact request'); END;
CREATE TRIGGER backend_platform_immutable_update BEFORE UPDATE ON backend_platform_qualifications BEGIN SELECT RAISE(ABORT,'backend platform mapping is immutable'); END;
CREATE TRIGGER backend_platform_immutable_delete BEFORE DELETE ON backend_platform_qualifications BEGIN SELECT RAISE(ABORT,'backend platform mapping is immutable'); END;
`
