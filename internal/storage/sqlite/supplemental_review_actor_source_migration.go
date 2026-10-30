package sqlite

// V64 retains the historical Session column as evidence, but makes the
// admission source an explicit union for new requests. Historical rows remain
// source-unknown: they are not relabeled as an AI Session or local operator.
const supplementalReviewActorSourceMigrationSQL = `
PRAGMA legacy_alter_table=ON;
DROP TRIGGER supplemental_request_immutable;
DROP TRIGGER supplemental_request_nodelete;
ALTER TABLE task_supplemental_review_requests RENAME TO _source63_supplemental_review_requests;
CREATE TABLE task_supplemental_review_requests (
 id TEXT PRIMARY KEY,
 ai_session_id TEXT REFERENCES ai_sessions(id),
 actor_kind TEXT,
 actor_id TEXT,
 project_id TEXT NOT NULL REFERENCES projects(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 request_id TEXT NOT NULL,
 payload_sha256 TEXT NOT NULL,
 purpose TEXT NOT NULL,
 source_phase TEXT NOT NULL,
 source_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 source_review_id TEXT NOT NULL REFERENCES reviews(id),
 source_execution_id TEXT NOT NULL,
 source_decision_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 CHECK((actor_kind IS NULL) = (actor_id IS NULL)),
 CHECK(actor_kind IS NULL OR actor_kind IN ('AI_SESSION','LOCAL_OPERATOR')),
 CHECK(actor_kind <> 'AI_SESSION' OR (ai_session_id IS NOT NULL AND actor_id=ai_session_id)),
 CHECK(actor_kind <> 'LOCAL_OPERATOR' OR ai_session_id IS NULL)
);
INSERT INTO task_supplemental_review_requests(id,ai_session_id,project_id,task_id,feature_area_id,request_id,payload_sha256,purpose,source_phase,source_revision_id,source_review_id,source_execution_id,source_decision_id,created_at)
 SELECT id,ai_session_id,project_id,task_id,feature_area_id,request_id,payload_sha256,purpose,source_phase,source_revision_id,source_review_id,source_execution_id,source_decision_id,created_at FROM _source63_supplemental_review_requests;
DROP TABLE _source63_supplemental_review_requests;
CREATE UNIQUE INDEX supplemental_review_actor_request ON task_supplemental_review_requests(project_id,actor_kind,actor_id,request_id) WHERE actor_kind IS NOT NULL;
CREATE TRIGGER supplemental_request_immutable BEFORE UPDATE ON task_supplemental_review_requests BEGIN SELECT RAISE(ABORT,'Supplemental request is immutable'); END;
CREATE TRIGGER supplemental_request_nodelete BEFORE DELETE ON task_supplemental_review_requests BEGIN SELECT RAISE(ABORT,'Supplemental request is immutable'); END;
PRAGMA legacy_alter_table=OFF;
`
