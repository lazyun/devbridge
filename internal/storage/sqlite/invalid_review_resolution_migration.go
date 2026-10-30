package sqlite

const invalidReviewResolutionV87SQL = `
DROP TRIGGER IF EXISTS review_approval_disposition_guard;
CREATE TRIGGER review_approval_disposition_guard BEFORE INSERT ON review_approval_decisions
WHEN NOT EXISTS(SELECT 1 FROM reviews v JOIN tasks t ON t.id=v.task_id WHERE v.id=NEW.review_id AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase IN ('REVIEWING','REVIEW_APPROVAL_PENDING') AND
 ((v.review_disposition IN ('APPROVAL_READY','FURTHER_REVIEW_REQUIRED') AND NEW.decision_id='REVIEW_APPROVAL:'||v.id)
  OR (NEW.decision_id='INVALID_REVIEW_RESULT_RESOLUTION:'||v.id AND EXISTS(SELECT 1 FROM review_result_materializations m WHERE m.review_id=v.id AND m.outcome IN ('INVALID','SCOPE_CONFLICT')))))
BEGIN SELECT RAISE(ABORT,'Review decision requires its exact current formal or invalid-result authority'); END;

CREATE TABLE review_invalid_result_resolutions (
 decision_id TEXT PRIMARY KEY REFERENCES decision_resolution_receipts(decision_id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 previous_review_id TEXT NOT NULL UNIQUE REFERENCES reviews(id),
 previous_review_ordinal INTEGER NOT NULL CHECK(previous_review_ordinal>0),
 source_outcome TEXT NOT NULL CHECK(source_outcome IN ('INVALID','SCOPE_CONFLICT')),
 source_raw_result_sha256 TEXT NOT NULL CHECK(length(source_raw_result_sha256)=64),
 successor_review_id TEXT REFERENCES reviews(id),
 successor_run_id TEXT REFERENCES runs(id),
 start_request_id TEXT,
 start_payload_sha256 TEXT CHECK(start_payload_sha256 IS NULL OR length(start_payload_sha256)=64),
 request_id TEXT NOT NULL,
 feedback TEXT,
 feedback_sha256 TEXT CHECK(feedback_sha256 IS NULL OR length(feedback_sha256)=64),
 action TEXT NOT NULL CHECK(action IN ('REQUEST_RE_REVIEW','ABANDON_TASK')),
 created_at TEXT NOT NULL,
 CHECK((action='REQUEST_RE_REVIEW' AND successor_review_id IS NOT NULL AND successor_run_id IS NOT NULL AND start_request_id IS NOT NULL AND feedback IS NOT NULL AND feedback_sha256 IS NOT NULL)
    OR (action='ABANDON_TASK' AND successor_review_id IS NULL AND successor_run_id IS NULL AND start_request_id IS NULL AND feedback IS NULL AND feedback_sha256 IS NULL))
);
CREATE TRIGGER review_invalid_result_resolutions_immutable BEFORE UPDATE ON review_invalid_result_resolutions BEGIN SELECT RAISE(ABORT,'Invalid Review resolution is immutable'); END;
CREATE TRIGGER review_invalid_result_resolutions_no_delete BEFORE DELETE ON review_invalid_result_resolutions BEGIN SELECT RAISE(ABORT,'Invalid Review resolution is durable'); END;
`
