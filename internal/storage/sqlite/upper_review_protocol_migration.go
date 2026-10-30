package sqlite

import (
	"context"
	"database/sql"
)

// migrateUpperReviewProtocolV49 adds payload/result provenance without inventing
// observation versions for historical rows. Existing migrations remain intact.
func migrateUpperReviewProtocolV49(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
ALTER TABLE execution_cancellations ADD COLUMN request_payload_json TEXT CHECK(request_payload_json IS NULL OR json_valid(request_payload_json));
ALTER TABLE execution_cancellations ADD COLUMN result_json TEXT CHECK(result_json IS NULL OR json_valid(result_json));
ALTER TABLE agent_operation_recoveries ADD COLUMN request_payload_json TEXT CHECK(request_payload_json IS NULL OR json_valid(request_payload_json));
ALTER TABLE agent_operation_recoveries ADD COLUMN result_json TEXT CHECK(result_json IS NULL OR json_valid(result_json));

CREATE TRIGGER upper_cancellation_payload_immutable BEFORE UPDATE OF request_payload_json ON execution_cancellations
WHEN NEW.request_payload_json IS NOT OLD.request_payload_json
BEGIN SELECT RAISE(ABORT,'Cancellation request payload is immutable'); END;
CREATE TRIGGER upper_cancellation_result_immutable BEFORE UPDATE ON execution_cancellations
WHEN OLD.result_json IS NOT NULL
BEGIN SELECT RAISE(ABORT,'Completed Upper cancellation receipt is immutable'); END;
CREATE TRIGGER upper_cancellation_result_terminal BEFORE UPDATE OF result_json ON execution_cancellations
WHEN NEW.result_json IS NOT NULL AND (NEW.resolved_at IS NULL OR NEW.state NOT IN ('CANCELED','ALREADY_TERMINAL','CANCEL_NOT_APPLIED'))
BEGIN SELECT RAISE(ABORT,'Upper cancellation result requires terminal convergence'); END;

DROP TRIGGER upper_review_grant_transition;
CREATE TRIGGER upper_review_grant_transition BEFORE UPDATE OF state ON review_grants
WHEN NEW.state<>OLD.state AND NOT (
 (OLD.run_id IS NULL AND OLD.state='ACTIVE' AND NEW.state IN ('PREPARING','AWAITING_REPORT')) OR
 (OLD.state='PREPARING' AND NEW.state IN ('ACTIVE','CANCELED','RECOVERY_REQUIRED')) OR
 (OLD.state='ACTIVE' AND NEW.state IN ('AWAITING_REPORT','FINALIZING','CANCELED','RECOVERY_REQUIRED')) OR
 (OLD.state='AWAITING_REPORT' AND NEW.state IN ('FINALIZING','CANCELED','RECOVERY_REQUIRED')) OR
 (OLD.state='FINALIZING' AND NEW.state='CLOSED' AND EXISTS(SELECT 1 FROM review_reports WHERE review_id=OLD.review_id AND run_id=OLD.run_id)) OR
 (OLD.state='RECOVERY_REQUIRED' AND NEW.state='PREPARING' AND NEW.generation=OLD.generation+1) OR
 (OLD.state IN ('RECOVERY_REQUIRED','FINALIZING') AND NEW.state='CANCELED'
  AND NOT EXISTS(SELECT 1 FROM review_reports p WHERE p.review_id=OLD.review_id)
  AND EXISTS(SELECT 1 FROM execution_cancellations c JOIN reviews v ON v.id=OLD.review_id
   JOIN runs r ON r.id=OLD.run_id JOIN feature_area_agent_claims a ON a.run_id=r.id
   WHERE v.executor_kind='UPPER_LLM' AND v.linked_run_id=r.id AND r.review_id=v.id
    AND r.operation_kind='REVIEW' AND r.operation_id=v.id AND r.task_id=v.task_id
    AND r.project_id=v.project_id AND r.feature_area_id=v.feature_area_id
    AND c.owner_type='REVIEW' AND c.owner_id=v.id AND c.run_id=r.id AND c.task_id=v.task_id AND c.project_id=v.project_id
    AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') AND c.runtime_epoch=OLD.runtime_epoch AND r.runtime_epoch=OLD.runtime_epoch
    AND a.feature_area_id=v.feature_area_id AND a.operation_kind='REVIEW' AND a.operation_id=v.id AND a.runtime_epoch=OLD.runtime_epoch
    AND (c.request_payload_json IS NULL OR
     (json_extract(c.request_payload_json,'$.expectedUpperVersion.grantEpoch')=OLD.runtime_epoch
      AND json_extract(c.request_payload_json,'$.expectedUpperVersion.grantGeneration')=OLD.generation)))))
BEGIN SELECT RAISE(ABORT,'Invalid Upper Review access transition'); END;

-- New authority is distinct from revoking an old grant. Startup classification
-- and exact cancellation remain allowed while retirement is converging.
CREATE TRIGGER upper_review_authorization_lifecycle BEFORE UPDATE OF state,runtime_epoch,generation ON review_grants
WHEN NEW.state IN ('PREPARING','ACTIVE','AWAITING_REPORT','FINALIZING')
 AND (NEW.state IS NOT OLD.state OR NEW.runtime_epoch IS NOT OLD.runtime_epoch OR NEW.generation<>OLD.generation)
 AND NOT EXISTS(SELECT 1 FROM reviews v JOIN tasks t ON t.id=v.task_id JOIN feature_areas f ON f.id=v.feature_area_id
  WHERE v.id=NEW.review_id AND v.executor_kind='UPPER_LLM' AND v.project_id=NEW.project_id AND v.feature_area_id=NEW.feature_area_id
   AND t.project_id=v.project_id AND t.feature_area_id=v.feature_area_id AND t.current_review_id=v.id
   AND t.status='ACTIVE' AND t.phase='REVIEWING' AND t.legacy_read_only=0
   AND f.project_id=v.project_id AND f.legacy_read_only=0 AND f.status IN ('DISCOVERING','ACTIVE'))
BEGIN SELECT RAISE(ABORT,'Upper Review lifecycle no longer permits authorization'); END;

CREATE TRIGGER upper_review_content_evidence_lifecycle BEFORE INSERT ON review_access_evidence
WHEN NEW.operation IN ('UPPER_LLM_FILE_READ','UPPER_LLM_FILE_LIST') AND NEW.outcome='SUCCESS'
 AND NOT EXISTS(SELECT 1 FROM reviews v JOIN tasks t ON t.id=v.task_id JOIN feature_areas f ON f.id=v.feature_area_id
  JOIN review_grants g ON g.review_id=v.id JOIN runs r ON r.id=g.run_id
  JOIN feature_area_agent_claims c ON c.feature_area_id=f.id
  WHERE v.id=NEW.review_id AND v.executor_kind='UPPER_LLM' AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING'
   AND t.legacy_read_only=0 AND t.project_id=v.project_id AND t.feature_area_id=f.id AND f.project_id=v.project_id
   AND f.legacy_read_only=0 AND f.status IN ('DISCOVERING','ACTIVE')
   AND g.state='ACTIVE' AND g.run_id=NEW.run_id AND g.runtime_epoch=NEW.runtime_epoch AND g.generation=NEW.grant_generation
   AND r.review_id=v.id AND r.runtime_epoch=g.runtime_epoch AND r.status='RUNNING'
   AND c.operation_kind='REVIEW' AND c.operation_id=v.id AND c.run_id=r.id AND c.runtime_epoch=g.runtime_epoch
   AND g.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
   AND NOT EXISTS(SELECT 1 FROM execution_cancellations x WHERE x.owner_type='REVIEW' AND x.owner_id=v.id AND x.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')))
BEGIN SELECT RAISE(ABORT,'Upper Review content authorization is stale'); END;

CREATE TRIGGER upper_review_report_lifecycle BEFORE INSERT ON review_reports
WHEN NOT EXISTS(SELECT 1 FROM reviews v JOIN tasks t ON t.id=v.task_id JOIN feature_areas f ON f.id=v.feature_area_id
 JOIN review_grants g ON g.review_id=v.id JOIN runs r ON r.id=g.run_id
 JOIN feature_area_agent_claims c ON c.feature_area_id=f.id
 WHERE v.id=NEW.review_id AND v.executor_kind='UPPER_LLM' AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING'
  AND t.legacy_read_only=0 AND t.project_id=v.project_id AND t.feature_area_id=f.id AND f.project_id=v.project_id
  AND f.legacy_read_only=0 AND f.status IN ('DISCOVERING','ACTIVE')
  AND g.state='FINALIZING' AND g.run_id=NEW.run_id AND r.review_id=v.id AND r.runtime_epoch=g.runtime_epoch AND r.status IN ('RUNNING','COLLECTING')
  AND c.operation_kind='REVIEW' AND c.operation_id=v.id AND c.run_id=r.id AND c.runtime_epoch=g.runtime_epoch
  AND g.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
  AND NOT EXISTS(SELECT 1 FROM execution_cancellations x WHERE x.owner_type='REVIEW' AND x.owner_id=v.id AND x.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')))
BEGIN SELECT RAISE(ABORT,'Upper Review report authorization is stale'); END;
`)
	return err
}
