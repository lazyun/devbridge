package sqlite

const upperReviewWorkerV137SQL = `
CREATE TABLE upper_review_worker_claims (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 review_id TEXT NOT NULL REFERENCES reviews(id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 caller_scope TEXT NOT NULL CHECK(length(caller_scope)>0),
 request_id TEXT NOT NULL CHECK(length(request_id)>0),
 worker_id TEXT NOT NULL CHECK(length(worker_id)>0),
 target_json TEXT NOT NULL CHECK(json_valid(target_json)),
 binding_sha256 TEXT NOT NULL CHECK(length(binding_sha256)=64),
 receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json)),
 state TEXT NOT NULL CHECK(state IN ('DISPATCH_INTENT','COMPLETED')),
 report_receipt_json TEXT CHECK(report_receipt_json IS NULL OR json_valid(report_receipt_json)),
 UNIQUE(caller_scope,ai_session_id,request_id)
);
CREATE TRIGGER upper_review_worker_claim_admission BEFORE INSERT ON upper_review_worker_claims
WHEN NEW.state<>'DISPATCH_INTENT' OR NEW.report_receipt_json IS NOT NULL OR NOT EXISTS(
 SELECT 1 FROM reviews v JOIN runs r ON r.id=v.linked_run_id JOIN review_grants g ON g.review_id=v.id
 JOIN tasks t ON t.id=v.task_id JOIN ai_sessions a ON a.id=v.created_by_ai_session_id
 JOIN feature_area_agent_claims c ON c.feature_area_id=v.feature_area_id
 WHERE r.id=NEW.run_id AND v.id=NEW.review_id AND v.executor_kind='UPPER_LLM' AND a.id=NEW.ai_session_id AND a.caller_scope=NEW.caller_scope
 AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING'
 AND r.status IN ('RUNNING','COLLECTING') AND g.state IN ('ACTIVE','AWAITING_REPORT')
 AND g.run_id=r.id AND g.runtime_epoch=r.runtime_epoch AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND c.operation_kind='REVIEW' AND c.operation_id=v.id AND c.run_id=r.id AND c.runtime_epoch=r.runtime_epoch
 AND json_extract(NEW.target_json,'$.aiSessionId')=a.id AND json_extract(NEW.target_json,'$.reviewId')=v.id
 AND json_extract(NEW.target_json,'$.runId')=r.id AND json_extract(NEW.target_json,'$.taskId')=t.id
 AND json_extract(NEW.target_json,'$.featureAreaId')=v.feature_area_id AND json_extract(NEW.target_json,'$.runtimeEpoch')=r.runtime_epoch
 AND json_extract(NEW.target_json,'$.grantGeneration')=g.generation)
BEGIN SELECT RAISE(ABORT,'Upper Review worker target is stale'); END;
CREATE TRIGGER upper_review_worker_claim_completion BEFORE UPDATE ON upper_review_worker_claims
WHEN NOT EXISTS(SELECT 1 FROM review_result_receipts x JOIN review_reports p ON p.id=json_extract(x.receipt_json,'$.reportId')
 WHERE x.review_id=OLD.review_id AND x.ai_session_id=OLD.ai_session_id AND x.state='COMPLETED' AND x.receipt_json=NEW.report_receipt_json AND p.run_id=OLD.run_id)
BEGIN SELECT RAISE(ABORT,'Upper Review worker completion requires committed report receipt'); END;
CREATE TRIGGER upper_review_worker_claim_immutable BEFORE UPDATE ON upper_review_worker_claims
WHEN NEW.run_id IS NOT OLD.run_id OR NEW.review_id IS NOT OLD.review_id OR NEW.ai_session_id IS NOT OLD.ai_session_id OR NEW.caller_scope IS NOT OLD.caller_scope OR NEW.request_id IS NOT OLD.request_id OR NEW.worker_id IS NOT OLD.worker_id OR NEW.target_json IS NOT OLD.target_json OR NEW.binding_sha256 IS NOT OLD.binding_sha256 OR NEW.receipt_json IS NOT OLD.receipt_json OR OLD.state='COMPLETED' OR NEW.state<>'COMPLETED' OR NEW.report_receipt_json IS NULL
BEGIN SELECT RAISE(ABORT,'Upper Review worker claim is immutable'); END;
CREATE TRIGGER upper_review_worker_claim_no_delete BEFORE DELETE ON upper_review_worker_claims
BEGIN SELECT RAISE(ABORT,'Upper Review worker claim is durable'); END;
`
