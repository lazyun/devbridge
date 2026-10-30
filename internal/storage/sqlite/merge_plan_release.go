package sqlite

import (
	"context"
	"time"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/git"
)

// Explicit release terminates only the plan, never its immutable operations.
// It authorizes no Git and no new plan. A subsequent merge needs a fresh plan
// and confirmation; completed members remain discharged by their old receipts.
func (s *WorkflowProgressionStore) releaseFeatureAreaMerge(ctx context.Context, project string, in delivery.MergeRetryInput) (delivery.MergeRetryReceipt, error) {
	empty := delivery.MergeRetryReceipt{}
	if got, found, err := readMergeRetryReceipt(ctx, s.db.sql, project, in); found || err != nil {
		return got, err
	}
	unlock := lockGitCommand(s.db.stateRoot + "/merge/" + in.MergeRequestID)
	defer unlock()
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if got, found, err := readMergeRetryReceipt(ctx, tx, project, in); found || err != nil {
		return got, err
	}
	var conflicting bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM merge_delivery_operations WHERE project_id=? AND request_id=?) OR EXISTS(SELECT 1 FROM merge_plan_releases WHERE merge_request_id=?)`, project, in.RequestID, in.MergeRequestID).Scan(&conflicting); err != nil {
		return empty, err
	}
	if conflicting {
		return empty, delivery.ErrRequestConflict
	}
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM merge_delivery_current_operations c JOIN merge_delivery_operations o ON o.operation_id=c.operation_id JOIN merge_delivery_outcomes r ON r.operation_id=o.operation_id JOIN pending_feature_area_commits p ON p.request_id=c.merge_request_id WHERE o.operation_id=? AND o.project_id=? AND o.feature_area_id=? AND o.merge_request_id=? AND o.repository_id=? AND r.outcome='REJECTED_NOT_CONSUMED' AND r.before_external_effect=1 AND NOT EXISTS(SELECT 1 FROM merge_release_unsafe_members u WHERE u.request_id=p.request_id))`, in.PredecessorOperationID, project, in.FeatureAreaID, in.MergeRequestID, in.RepositoryID).Scan(&eligible)
	if err != nil {
		return empty, err
	}
	if !eligible {
		return empty, git.ErrRecoveryRequired
	}
	now := formatTime(time.Now().UTC())
	if _, err = tx.ExecContext(ctx, `INSERT INTO merge_plan_releases(project_id,request_id,merge_request_id,feature_area_id,repository_id,predecessor_operation_id,created_at) VALUES(?,?,?,?,?,?,?)`, project, in.RequestID, in.MergeRequestID, in.FeatureAreaID, in.RepositoryID, in.PredecessorOperationID, now); err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE feature_area_commit_receipts SET released_at=? WHERE request_id=? AND released_at IS NULL AND completed_at IS NULL`, now, in.MergeRequestID); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return delivery.MergeRetryReceipt{ProjectID: project, MergeRetryInput: in, PlanReleased: true}, nil
}
