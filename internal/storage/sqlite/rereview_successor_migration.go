package sqlite

const reReviewSuccessorV86SQL = `
CREATE TABLE review_re_review_requests (
 decision_id TEXT PRIMARY KEY REFERENCES decision_resolution_receipts(decision_id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 previous_review_id TEXT NOT NULL REFERENCES reviews(id),
 previous_review_ordinal INTEGER NOT NULL CHECK(previous_review_ordinal>0),
 previous_formal_result_sha256 TEXT NOT NULL CHECK(length(previous_formal_result_sha256)=64),
 successor_review_id TEXT NOT NULL UNIQUE REFERENCES reviews(id),
 successor_run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 start_request_id TEXT NOT NULL UNIQUE,
 start_payload_sha256 TEXT NOT NULL CHECK(length(start_payload_sha256)=64),
 request_id TEXT NOT NULL,
 feedback TEXT NOT NULL CHECK(length(feedback)>0 AND length(feedback)<=8192 AND instr(feedback,char(0))=0),
 feedback_sha256 TEXT NOT NULL CHECK(length(feedback_sha256)=64),
 created_at TEXT NOT NULL,
 UNIQUE(task_id,previous_review_id),
 CHECK(successor_review_id<>previous_review_id)
);
CREATE INDEX review_re_review_requests_task ON review_re_review_requests(task_id,created_at);
CREATE TRIGGER review_re_review_requests_immutable BEFORE UPDATE ON review_re_review_requests BEGIN SELECT RAISE(ABORT,'Re-review request is immutable'); END;
CREATE TRIGGER review_re_review_requests_no_delete BEFORE DELETE ON review_re_review_requests BEGIN SELECT RAISE(ABORT,'Re-review request is durable'); END;
CREATE TRIGGER review_re_review_requests_exact_successor BEFORE INSERT ON review_re_review_requests
WHEN NOT EXISTS(SELECT 1 FROM reviews p JOIN reviews s ON s.id=NEW.successor_review_id JOIN runs r ON r.id=NEW.successor_run_id JOIN feature_area_review_start_requests a ON a.review_id=s.id AND a.run_id=r.id AND a.request_id=NEW.start_request_id AND a.payload_sha256=NEW.start_payload_sha256 AND a.outcome='ACCEPTED'
 WHERE p.id=NEW.previous_review_id AND p.task_id=NEW.task_id AND p.ordinal=NEW.previous_review_ordinal
   AND s.task_id=p.task_id AND s.ordinal=p.ordinal+1 AND s.linked_run_id=r.id
   AND r.review_id=s.id AND r.task_id=p.task_id)
BEGIN SELECT RAISE(ABORT,'Re-review successor must be the exact next Review admission'); END;
`
