package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/executioncancel"
)

// Cancellation and abandonment share the writer transaction. Applied members
// are first projected from their exact durable outcomes, then all delivery
// history is frozen. Completing this parent means ABANDONED, never delivered.
func abandonTaskDeliveryTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input, now time.Time) error {
	if in.OwnerType != "TASK" {
		return nil
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_commit_receipts WHERE task_id=? AND completed_at IS NULL)`, in.OwnerID).Scan(&pending); err != nil {
		return err
	}
	if !pending {
		return nil
	}
	if err := guardTaskDeliveryCancellationTx(ctx, tx, in); err != nil {
		return err
	}
	timestamp := formatTime(now)
	// Scalar deliveries have no task_commit_members row. Preserve their exact
	// applied checkpoint before freezing abandonment, without claiming success.
	var protocol int
	if err := tx.QueryRowContext(ctx, `SELECT protocol_version FROM task_commit_receipts WHERE task_id=?`, in.OwnerID).Scan(&protocol); err != nil {
		return err
	}
	if protocol == 1 {
		current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, in.OwnerID, in.ProjectID))
		if err != nil {
			return err
		}
		input := taskDeliveryDispatchInput{TaskID: current.ID, RepositoryID: current.RepositoryID, Message: taskCommitMessage(current)}
		if err = tx.QueryRowContext(ctx, `SELECT revision_id,workspace_path,branch,COALESCE(pre_commit_head,''),verified_snapshot FROM task_commit_receipts WHERE task_id=?`, current.ID).Scan(&input.RevisionID, &input.WorkspacePath, &input.Branch, &input.ExpectedHead, &input.Snapshot); err != nil {
			return err
		}
		saved, found, err := readTaskDeliveryOutcome(ctx, tx, input)
		if err != nil && !errors.Is(err, delivery.ErrRejected) {
			return err
		}
		if found && err == nil {
			if _, err = tx.ExecContext(ctx, `UPDATE tasks SET commit_sha=?,committed_at=? WHERE id=? AND commit_sha IS NULL`, saved.Commit, timestamp, current.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE task_commit_receipts SET resulting_commit=? WHERE task_id=? AND completed_at IS NULL`, saved.Commit, current.ID); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE task_commit_members SET
 state=(SELECT CASE r.changed WHEN 1 THEN 'COMPLETED' ELSE 'NO_CHANGE' END FROM task_delivery_current_operations c JOIN task_delivery_operation_outcomes r ON r.operation_id=c.operation_id WHERE c.task_id=task_commit_members.task_id AND c.repository_id=task_commit_members.repository_id),
 resulting_commit=(SELECT r.commit_oid FROM task_delivery_current_operations c JOIN task_delivery_operation_outcomes r ON r.operation_id=c.operation_id WHERE c.task_id=task_commit_members.task_id AND c.repository_id=task_commit_members.repository_id),
 commit_tree=(SELECT r.tree_oid FROM task_delivery_current_operations c JOIN task_delivery_operation_outcomes r ON r.operation_id=c.operation_id WHERE c.task_id=task_commit_members.task_id AND c.repository_id=task_commit_members.repository_id),
 completed_at=?,last_error='' WHERE task_id=? AND state NOT IN ('COMPLETED','NO_CHANGE') AND EXISTS(SELECT 1 FROM task_delivery_current_operations c JOIN task_delivery_operation_outcomes r ON r.operation_id=c.operation_id WHERE c.task_id=task_commit_members.task_id AND c.repository_id=task_commit_members.repository_id AND r.outcome='APPLIED')`, timestamp, in.OwnerID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_delivery_abandonments(task_id,project_id,request_id,members_json,operations_json,created_at)
 SELECT ?,?,?,
 (SELECT json_group_array(json_object('repositoryId',repository_id,'memberId',member_id,'state',state,'commit',resulting_commit,'tree',commit_tree,'completedAt',completed_at)) FROM (SELECT * FROM task_commit_members WHERE task_id=? ORDER BY repository_id)),
 (SELECT json_group_array(json_object('operationId',o.operation_id,'requestId',o.request_id,'repositoryId',o.repository_id,'payloadSha256',o.payload_sha256,'outcome',COALESCE(r.outcome,'NOT_DISPATCHED'),'commit',COALESCE(r.commit_oid,''),'error',COALESCE(r.error,''))) FROM task_delivery_operations o LEFT JOIN task_delivery_operation_outcomes r ON r.operation_id=o.operation_id WHERE o.task_id=?),?`, in.OwnerID, in.ProjectID, in.RequestID, in.OwnerID, in.OwnerID, timestamp)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_commit_receipts SET terminal_outcome='ABANDONED',completed_at=?,last_error='DELIVERY_ABANDONED' WHERE task_id=? AND completed_at IS NULL`, timestamp, in.OwnerID)
	return err
}
