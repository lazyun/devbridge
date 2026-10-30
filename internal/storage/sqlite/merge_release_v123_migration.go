package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func migrateMergeReleaseV123(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, mergeReleaseV123SQL); err != nil {
		return err
	}
	// Preserve each existing guard, excluding explicitly released parents from
	// pending admission. Completion and immutable delivery evidence stay intact.
	for _, name := range []string{"commit_claim_fence", "commit_task_creation_fence", "development_selection_commit_fence", "feature_area_git_command_fence", "merge_parent_pending", "merge_dispatch_exact", "merge_operation_initial_exact"} {
		var original string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&original); err != nil {
			return err
		}
		updated := strings.ReplaceAll(original, "FROM feature_area_commit_receipts ", "FROM pending_feature_area_commits ")
		updated = strings.ReplaceAll(updated, "JOIN feature_area_commit_receipts ", "JOIN pending_feature_area_commits ")
		if updated == original {
			return fmt.Errorf("missing merge pending guard: %s", name)
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, updated); err != nil {
			return err
		}
	}
	return nil
}

// A released plan is not a successful merge. Its completed_at stays NULL and
// all member/operation/outcome history remains available for exact replay.
const mergeReleaseV123SQL = `
ALTER TABLE feature_area_commit_receipts ADD COLUMN released_at TEXT;
CREATE VIEW pending_feature_area_commits AS SELECT * FROM feature_area_commit_receipts WHERE completed_at IS NULL AND released_at IS NULL;
DROP INDEX feature_area_commit_pending;
CREATE UNIQUE INDEX feature_area_commit_pending ON feature_area_commit_receipts(feature_area_id) WHERE completed_at IS NULL AND released_at IS NULL;
DROP INDEX repository_commit_pending;
CREATE UNIQUE INDEX repository_commit_pending ON feature_area_commit_receipts(repository_id,development_ref) WHERE completed_at IS NULL AND released_at IS NULL;
CREATE TABLE merge_plan_releases (
 project_id TEXT NOT NULL REFERENCES projects(id), request_id TEXT NOT NULL,
 merge_request_id TEXT NOT NULL UNIQUE REFERENCES feature_area_commit_receipts(request_id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id), repository_id TEXT NOT NULL REFERENCES repositories(id),
 predecessor_operation_id TEXT NOT NULL REFERENCES merge_delivery_operations(operation_id),
 created_at TEXT NOT NULL, PRIMARY KEY(project_id,request_id));
CREATE VIEW merge_release_unsafe_members AS
 SELECT DISTINCT m.request_id FROM feature_area_commit_members m
 LEFT JOIN merge_delivery_current_operations c ON c.merge_request_id=m.request_id AND c.repository_id=m.repository_id
 LEFT JOIN merge_delivery_dispatches d ON d.operation_id=c.operation_id
 LEFT JOIN merge_delivery_outcomes r ON r.operation_id=c.operation_id
 WHERE NOT COALESCE(
 (m.state='COMPLETED' AND r.outcome='APPLIED' AND r.merge_oid=m.resulting_dev_head)
 OR (m.state NOT IN ('COMPLETED','NO_CHANGE') AND c.operation_id IS NOT NULL AND
 (d.operation_id IS NULL OR (r.outcome='REJECTED_NOT_CONSUMED' AND r.before_external_effect=1))),0)
 UNION SELECT p.request_id FROM feature_area_commit_receipts p WHERE p.protocol_version=2 AND
 (SELECT count(*) FROM feature_area_commit_members m WHERE m.request_id=p.request_id) <> json_array_length(p.plan_json,'$.members');
CREATE TRIGGER merge_release_exact BEFORE INSERT ON merge_plan_releases WHEN
 NOT EXISTS(SELECT 1 FROM merge_delivery_operations o JOIN merge_delivery_current_operations c ON c.operation_id=o.operation_id
 JOIN merge_delivery_outcomes r ON r.operation_id=o.operation_id JOIN pending_feature_area_commits p ON p.request_id=o.merge_request_id
 WHERE o.operation_id=NEW.predecessor_operation_id AND o.project_id=NEW.project_id AND o.feature_area_id=NEW.feature_area_id
 AND o.merge_request_id=NEW.merge_request_id AND o.repository_id=NEW.repository_id AND p.protocol_version=2
 AND r.outcome='REJECTED_NOT_CONSUMED' AND r.before_external_effect=1)
 OR EXISTS(SELECT 1 FROM merge_delivery_operations WHERE project_id=NEW.project_id AND request_id=NEW.request_id)
 OR NOT EXISTS(SELECT 1 FROM feature_area_commit_members WHERE request_id=NEW.merge_request_id)
 OR EXISTS(SELECT 1 FROM merge_release_unsafe_members WHERE request_id=NEW.merge_request_id)
BEGIN SELECT RAISE(ABORT,'Merge release requires exact settled evidence'); END;
CREATE TRIGGER merge_release_immutable BEFORE UPDATE ON merge_plan_releases BEGIN SELECT RAISE(ABORT,'Merge release immutable'); END;
CREATE TRIGGER merge_release_durable BEFORE DELETE ON merge_plan_releases BEGIN SELECT RAISE(ABORT,'Merge release durable'); END;
CREATE TRIGGER merge_release_parent BEFORE UPDATE OF released_at ON feature_area_commit_receipts
WHEN NEW.released_at IS NOT OLD.released_at AND (OLD.released_at IS NOT NULL OR NEW.completed_at IS NOT NULL OR NOT EXISTS(
 SELECT 1 FROM merge_plan_releases r WHERE r.merge_request_id=NEW.request_id AND r.created_at=NEW.released_at))
BEGIN SELECT RAISE(ABORT,'Merge release requires exact receipt'); END;
CREATE TRIGGER merge_release_no_insert BEFORE INSERT ON feature_area_commit_receipts WHEN NEW.released_at IS NOT NULL
BEGIN SELECT RAISE(ABORT,'Merge release requires existing plan'); END;
CREATE TRIGGER merge_released_parent_immutable BEFORE UPDATE ON feature_area_commit_receipts WHEN OLD.released_at IS NOT NULL
BEGIN SELECT RAISE(ABORT,'Released merge plan immutable'); END;
CREATE TRIGGER merge_released_member_immutable BEFORE UPDATE ON feature_area_commit_members WHEN EXISTS(
 SELECT 1 FROM feature_area_commit_receipts p WHERE p.request_id=OLD.request_id AND p.released_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'Released merge members immutable'); END;
CREATE TRIGGER merge_released_retry BEFORE INSERT ON merge_delivery_operations WHEN EXISTS(
 SELECT 1 FROM feature_area_commit_receipts p WHERE p.request_id=NEW.merge_request_id AND p.released_at IS NOT NULL)
 OR EXISTS(SELECT 1 FROM merge_plan_releases WHERE project_id=NEW.project_id AND request_id=NEW.request_id)
BEGIN SELECT RAISE(ABORT,'Released merge cannot acquire operation'); END;
CREATE TRIGGER merge_target_reservation_v2 BEFORE INSERT ON feature_area_commit_members WHEN EXISTS(
 SELECT 1 FROM pending_feature_area_commits p WHERE p.request_id<>NEW.request_id AND
 ((p.repository_id=NEW.repository_id AND p.development_ref=NEW.development_ref) OR EXISTS(
 SELECT 1 FROM feature_area_commit_members m WHERE m.request_id=p.request_id AND m.repository_id=NEW.repository_id AND m.development_ref=NEW.development_ref)))
BEGIN SELECT RAISE(ABORT,'GIT_FINALIZATION_PENDING'); END;
CREATE TRIGGER merge_target_reservation_v1 BEFORE INSERT ON feature_area_commit_receipts WHEN NEW.protocol_version=1 AND EXISTS(
 SELECT 1 FROM pending_feature_area_commits p JOIN feature_area_commit_members m ON m.request_id=p.request_id
 WHERE m.repository_id=NEW.repository_id AND m.development_ref=NEW.development_ref)
BEGIN SELECT RAISE(ABORT,'GIT_FINALIZATION_PENDING'); END;
`
