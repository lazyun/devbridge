package sqlite

// Review result materialization records the read-side interpretation of the
// immutable raw Coding Review result.  It is deliberately separate from a
// review approval decision: a completed model turn may be malformed or may
// propose a target set outside the frozen Review workset.
const reviewResultMaterializationV85SQL = `
CREATE TABLE review_result_materializations (
 review_id TEXT PRIMARY KEY REFERENCES reviews(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 review_ordinal INTEGER NOT NULL CHECK(review_ordinal>0),
 run_id TEXT NOT NULL REFERENCES runs(id),
 outcome TEXT NOT NULL CHECK(outcome IN ('VALID','INVALID','SCOPE_CONFLICT')),
 raw_result_sha256 TEXT NOT NULL CHECK(length(raw_result_sha256)=64),
 formal_result_sha256 TEXT CHECK(formal_result_sha256 IS NULL OR length(formal_result_sha256)=64),
 formal_result_json TEXT CHECK(formal_result_json IS NULL OR json_valid(formal_result_json)),
 repository_target_set_sha256 TEXT CHECK(repository_target_set_sha256 IS NULL OR length(repository_target_set_sha256)=64),
 work_spec_sha256 TEXT CHECK(work_spec_sha256 IS NULL OR length(work_spec_sha256)=64),
 verification_spec_sha256 TEXT CHECK(verification_spec_sha256 IS NULL OR length(verification_spec_sha256)=64),
 reason_code TEXT,
 reason_detail TEXT,
 created_at TEXT NOT NULL,
 CHECK((outcome='VALID' AND formal_result_sha256 IS NOT NULL AND formal_result_json IS NOT NULL AND repository_target_set_sha256 IS NOT NULL AND work_spec_sha256 IS NOT NULL AND verification_spec_sha256 IS NOT NULL AND reason_code IS NULL AND reason_detail IS NULL)
    OR (outcome<>'VALID' AND formal_result_sha256 IS NULL AND formal_result_json IS NULL AND repository_target_set_sha256 IS NULL AND work_spec_sha256 IS NULL AND verification_spec_sha256 IS NULL AND reason_code IS NOT NULL AND reason_detail IS NOT NULL))
);
CREATE INDEX review_result_materializations_run ON review_result_materializations(run_id);
CREATE TRIGGER review_result_materialization_immutable BEFORE UPDATE ON review_result_materializations BEGIN SELECT RAISE(ABORT,'Review result materialization is immutable'); END;
CREATE TRIGGER review_result_materialization_no_delete BEFORE DELETE ON review_result_materializations BEGIN SELECT RAISE(ABORT,'Review result materialization is durable'); END;

-- FURTHER_REVIEW_REQUIRED is a formal, user-resolvable review conclusion. It
-- can create the same immutable decision envelope, but never an APPROVE path.
DROP TRIGGER IF EXISTS review_approval_disposition_guard;
CREATE TRIGGER review_approval_disposition_guard BEFORE INSERT ON review_approval_decisions
WHEN NOT EXISTS(SELECT 1 FROM reviews v JOIN tasks t ON t.id=v.task_id WHERE v.id=NEW.review_id AND v.review_disposition IN ('APPROVAL_READY','FURTHER_REVIEW_REQUIRED') AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase IN ('REVIEWING','REVIEW_APPROVAL_PENDING'))
BEGIN SELECT RAISE(ABORT,'Review approval requires a current formal review conclusion'); END;
`
