package sqlite

// Legacy reservations predate immutable dispatch outcomes. Never backfill
// evidence from current Git state: existing pending rows remain UNKNOWN.
const legacyMergeV120SQL = `
CREATE TABLE legacy_merge_outcomes (
 request_id TEXT PRIMARY KEY REFERENCES feature_area_commit_receipts(request_id),
 resulting_dev_head TEXT NOT NULL CHECK(length(resulting_dev_head)>0),
 created_at TEXT NOT NULL
);
CREATE TRIGGER legacy_merge_outcome_owner BEFORE INSERT ON legacy_merge_outcomes
WHEN NOT EXISTS(SELECT 1 FROM feature_area_commit_receipts WHERE request_id=NEW.request_id AND protocol_version=1 AND completed_at IS NULL)
BEGIN SELECT RAISE(ABORT,'Legacy merge outcome requires pending v1 reservation'); END;
CREATE TRIGGER legacy_merge_outcome_immutable BEFORE UPDATE ON legacy_merge_outcomes
BEGIN SELECT RAISE(ABORT,'Legacy merge outcome is immutable'); END;
CREATE TRIGGER legacy_merge_outcome_durable BEFORE DELETE ON legacy_merge_outcomes
BEGIN SELECT RAISE(ABORT,'Legacy merge outcome is durable'); END;
CREATE TRIGGER legacy_merge_identity BEFORE UPDATE ON feature_area_commit_receipts
WHEN OLD.protocol_version=1 AND (
 NEW.request_id IS NOT OLD.request_id OR NEW.feature_area_id IS NOT OLD.feature_area_id
 OR NEW.repository_id IS NOT OLD.repository_id OR NEW.development_ref IS NOT OLD.development_ref
 OR NEW.source_head IS NOT OLD.source_head OR NEW.expected_dev_head IS NOT OLD.expected_dev_head
 OR NEW.source_path IS NOT OLD.source_path OR NEW.source_branch IS NOT OLD.source_branch OR NEW.target_path IS NOT OLD.target_path)
BEGIN SELECT RAISE(ABORT,'Legacy merge reservation is immutable'); END;
DROP TRIGGER IF EXISTS development_selection_commit_fence;
CREATE TRIGGER development_selection_commit_fence BEFORE UPDATE ON development_cycles
WHEN EXISTS(SELECT 1 FROM feature_area_commit_receipts r WHERE r.completed_at IS NULL AND (
 (r.protocol_version=1 AND r.repository_id=OLD.repository_id)
 OR EXISTS(SELECT 1 FROM feature_area_commit_members m WHERE m.request_id=r.request_id AND m.repository_id=OLD.repository_id)))
BEGIN SELECT RAISE(ABORT,'GIT_FINALIZATION_PENDING'); END;
`
