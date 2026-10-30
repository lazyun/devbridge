// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

// schemaV30 records Feature Area Review start commands independently from
// Review history. Rejected admission is durable and idempotent, but never
// becomes a product Review. Accepted commands bind their immutable request
// identity to the Review and Run committed by the same transaction.
const schemaV30 = `
CREATE TABLE feature_area_review_start_requests (
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 request_id TEXT NOT NULL CHECK(length(request_id)>0 AND length(request_id)<=128),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64 AND payload_sha256 GLOB '[0-9a-f]*'),
 outcome TEXT NOT NULL CHECK(outcome IN ('ACCEPTED','FEATURE_AREA_BUSY')),
 review_id TEXT REFERENCES reviews(id),
 run_id TEXT REFERENCES runs(id),
 owner_operation_kind TEXT CHECK(owner_operation_kind IN ('REVIEW','PREPARATION','EXECUTION')),
 owner_operation_id TEXT,
 owner_run_id TEXT REFERENCES runs(id),
 created_at TEXT NOT NULL,
 PRIMARY KEY(feature_area_id,request_id),
 CHECK(
  (outcome='ACCEPTED' AND review_id IS NOT NULL AND run_id IS NOT NULL AND owner_operation_kind IS NULL AND owner_operation_id IS NULL AND owner_run_id IS NULL) OR
  (outcome='FEATURE_AREA_BUSY' AND review_id IS NULL AND run_id IS NULL AND owner_operation_kind IS NOT NULL AND owner_operation_id IS NOT NULL AND owner_run_id IS NOT NULL)
 )
);
CREATE UNIQUE INDEX feature_area_review_start_accepted_review ON feature_area_review_start_requests(review_id) WHERE review_id IS NOT NULL;
CREATE UNIQUE INDEX feature_area_review_start_accepted_run ON feature_area_review_start_requests(run_id) WHERE run_id IS NOT NULL;
CREATE TRIGGER feature_area_review_start_requests_immutable BEFORE UPDATE ON feature_area_review_start_requests
BEGIN SELECT RAISE(ABORT,'Feature Area Review start receipt is immutable'); END;
CREATE TRIGGER feature_area_review_start_requests_no_delete BEFORE DELETE ON feature_area_review_start_requests
BEGIN SELECT RAISE(ABORT,'Feature Area Review start receipt is durable history'); END;
`
