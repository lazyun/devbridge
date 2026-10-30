package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/delivery"
)

// Read immutable request identity before requiring live writer authority.
// Callers repeat the lookup under the writer lock before any fresh effect.
func readTaskRetryReceipt(ctx context.Context, q definitionQuerier, project string, in delivery.RetryInput) (delivery.RetryReceipt, bool, error) {
	var got delivery.RetryReceipt
	err := q.QueryRowContext(ctx, `SELECT project_id,task_id,repository_id,COALESCE(predecessor_operation_id,''),operation_id,request_id FROM task_delivery_operations WHERE project_id=? AND request_id=?`, project, in.RequestID).Scan(&got.ProjectID, &got.TaskID, &got.RepositoryID, &got.PredecessorOperationID, &got.OperationID, &got.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return delivery.RetryReceipt{}, false, nil
	}
	if err != nil {
		return delivery.RetryReceipt{}, false, err
	}
	if got.TaskID != in.TaskID || got.RepositoryID != in.RepositoryID || got.PredecessorOperationID != in.PredecessorOperationID {
		return delivery.RetryReceipt{}, true, delivery.ErrRequestConflict
	}
	return got, true, nil
}

// Release and retry share a request namespace. Resolve both kinds before
// consulting mutable admission, even when the replay changed its command kind.
func readMergeRetryReceipt(ctx context.Context, q definitionQuerier, project string, in delivery.MergeRetryInput) (delivery.MergeRetryReceipt, bool, error) {
	got := delivery.MergeRetryReceipt{ProjectID: project, PlanReleased: true}
	got.ReleasePlan = true
	err := q.QueryRowContext(ctx, `SELECT feature_area_id,merge_request_id,repository_id,predecessor_operation_id,request_id FROM merge_plan_releases WHERE project_id=? AND request_id=?`, project, in.RequestID).Scan(&got.FeatureAreaID, &got.MergeRequestID, &got.RepositoryID, &got.PredecessorOperationID, &got.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		got = delivery.MergeRetryReceipt{}
		err = q.QueryRowContext(ctx, `SELECT project_id,feature_area_id,merge_request_id,repository_id,COALESCE(predecessor_operation_id,''),request_id,operation_id FROM merge_delivery_operations WHERE project_id=? AND request_id=?`, project, in.RequestID).Scan(&got.ProjectID, &got.FeatureAreaID, &got.MergeRequestID, &got.RepositoryID, &got.PredecessorOperationID, &got.RequestID, &got.OperationID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return delivery.MergeRetryReceipt{}, false, nil
	}
	if err != nil {
		return delivery.MergeRetryReceipt{}, false, err
	}
	if got.MergeRetryInput != in {
		return delivery.MergeRetryReceipt{}, true, delivery.ErrRequestConflict
	}
	return got, true, nil
}
