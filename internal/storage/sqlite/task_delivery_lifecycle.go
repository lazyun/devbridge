package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/task"
)

// deliveryWriter lets each authoritative delivery transition run either inside
// an open transaction or through the pool for post-rollback recovery marks.
type deliveryWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// pendingDeliveryCancellationSQL is the durable non-terminal cancellation
// predicate. A completion transaction may never terminalize a Task whose
// cancellation lacks terminal runtime evidence or a proven no-backend settlement.
const pendingDeliveryCancellationSQL = `EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.task_id=? AND NOT (` + settledNoRunExecutionCancellationSQL + `) AND NOT EXISTS(SELECT 1 FROM json_each(?) settled WHERE json_extract(settled.value,'$.projectId')=c.project_id AND json_extract(settled.value,'$.requestId')=c.request_id) AND (c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') OR ` + unresolvedCancellationInterruptCSQL + ` OR c.backend_disposition='UNKNOWN_ORPHANED' OR (c.state='CANCELED' AND (c.terminal_observed<>1 OR c.resolved_at IS NULL))))`

func hasPendingDeliveryCancellation(ctx context.Context, q deliveryWriter, taskID string) (bool, error) {
	settled, err := settledRecoveryCancellationsTx(ctx, q, taskID)
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(settled)
	if err != nil {
		return false, err
	}
	var pending bool
	if err := q.QueryRowContext(ctx, `SELECT `+pendingDeliveryCancellationSQL, taskID, string(raw)).Scan(&pending); err != nil {
		return false, err
	}
	return pending, nil
}

// admitCodingTaskDeliveryTx is the ACCEPT branch's durable delivery admission.
// The Decision receipt, the frozen delivery intent, and this Phase transition
// share one transaction, so an acceptance can never be visible with the Task
// still parked in ACCEPTANCE_PENDING awaiting a reconciler.
func admitCodingTaskDeliveryTx(ctx context.Context, tx *sql.Tx, current task.Task, predecessor executionproposal.Proposal, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='DELIVERING',updated_at=? WHERE id=? AND status='ACTIVE'`, formatTime(now), current.ID)
	if err != nil {
		return executionProposalConflict(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return executionproposal.ErrConflict
	}
	payload, _ := event.Payload(map[string]any{"from": current.Status, "to": task.StatusActive, "phase_from": current.Phase, "phase_to": task.PhaseDelivering, "execution_proposal_id": predecessor.ID, "decision_id": decisionresolution.TaskAcceptanceDecisionID(predecessor.ID)})
	return appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: current.ProjectID, TaskID: current.ID, RunID: terminalExecutionRunID(predecessor), EntityType: "task", EntityID: current.ID, Payload: payload, CreatedAt: now})
}

// markTaskDeliveryRecovery moves an admitted-but-unfinished delivery into the
// durable RECOVERY_REQUIRED authority after a confirmed hard conflict. It only
// fires from ACTIVE+DELIVERING, so a concurrently terminalized or already
// recovering Task is never overwritten.
func markTaskDeliveryRecovery(ctx context.Context, q deliveryWriter, projectID, taskID string, now time.Time) error {
	result, err := q.ExecContext(ctx, `UPDATE tasks SET phase='RECOVERY_REQUIRED',updated_at=? WHERE id=? AND status='ACTIVE' AND phase='DELIVERING'`, formatTime(now), taskID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		payload, _ := event.Payload(map[string]any{"from": task.StatusActive, "to": task.StatusActive, "phase_from": task.PhaseDelivering, "phase_to": task.PhaseRecoveryRequired, "authorization_source": "TASK_DELIVERY"})
		return appendEvent(ctx, q, event.Event{Type: event.TaskStatusChanged, ProjectID: projectID, TaskID: taskID, EntityType: "task", EntityID: taskID, Payload: payload, CreatedAt: now})
	}
	return nil
}

// completeTaskCommitTx is the v1 completion transaction: the checkpoint and
// the receipt terminalization commit together, and only from the exact
// admitted lifecycle. A SQL error leaves the Task DELIVERING so the outcome
// can be observed, never blindly re-applied; a lifecycle that no longer
// matches is a confirmed conflict and lands in RECOVERY_REQUIRED.
func completeTaskCommitTx(ctx context.Context, db *DB, projectID, taskID, commitSHA string, now time.Time) error {
	tx, err := db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, phase string
	if err = tx.QueryRowContext(ctx, `SELECT status,phase FROM tasks WHERE id=? AND project_id=?`, taskID, projectID).Scan(&status, &phase); errors.Is(err, sql.ErrNoRows) {
		return errors.New("TASK_NOT_ACCEPTED")
	} else if err != nil {
		return err
	}
	if !taskCanCompleteDelivery(status, phase) {
		return errors.New("TASK_NOT_ACCEPTED")
	}
	pending, err := hasPendingDeliveryCancellation(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if pending {
		// A non-terminal cancellation outranks completion: the Git effect is
		// already durable, so recovery must observe it rather than re-run it.
		if err = markTaskDeliveryRecovery(ctx, tx, projectID, taskID, now); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return git.ErrRecoveryRequired
	}
	timestamp := formatTime(now)
	if _, err = tx.ExecContext(ctx, `UPDATE task_commit_receipts SET resulting_commit=?,completed_at=?,last_error=NULL WHERE task_id=? AND completed_at IS NULL`, commitSHA, timestamp, taskID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET status=CASE WHEN EXISTS(SELECT 1 FROM task_completion_intents c WHERE c.task_id=tasks.id AND c.completed_at IS NULL) THEN 'ACTIVE' ELSE 'SUCCEEDED' END,commit_sha=?,committed_at=?,updated_at=? WHERE id=? AND commit_sha IS NULL AND ((status='ACTIVE' AND phase='DELIVERING') OR status='SUCCEEDED')`, commitSHA, timestamp, timestamp, taskID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return rollbackErr
		}
		if err = markTaskDeliveryRecovery(ctx, db.sql, projectID, taskID, now); err != nil {
			return err
		}
		return git.ErrRecoveryRequired
	}
	return tx.Commit()
}

func taskCanCompleteDelivery(status, phase string) bool {
	return (status == string(task.StatusActive) && phase == string(task.PhaseDelivering)) || status == string(task.StatusSucceeded)
}

// completeMultiTaskDeliveryTx terminalizes a v2 delivery only after the
// current database state revalidates the frozen admission: Task/revision,
// receipt identity, fully terminal members, exact frozen member identities,
// worktrees at the frozen path/branch (missing checkouts require exact applied
// evidence), the current non-quarantined
// workspace, an ACTIVE FeatureArea, and no pending cancellation. Any conflict
// writes ACTIVE+RECOVERY_REQUIRED in the same authoritative transaction; the
// task receipt and every completed member intent stay immutable.
func (s *WorkflowProgressionStore) completeMultiTaskDeliveryTx(ctx context.Context, current task.Task, receiptOnly bool) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, phase, revision string
	if err = tx.QueryRowContext(ctx, `SELECT status,phase,current_definition_revision_id FROM tasks WHERE id=? AND project_id=?`, current.ID, current.ProjectID).Scan(&status, &phase, &revision); errors.Is(err, sql.ErrNoRows) {
		return errors.New("TASK_NOT_ACCEPTED")
	} else if err != nil {
		return err
	}
	if !taskCanCompleteDelivery(status, phase) && !(receiptOnly && status == "ACTIVE" && phase == "RECOVERY_REQUIRED") {
		return errors.New("TASK_NOT_ACCEPTED")
	}
	conflict := false
	var receiptRevision, receiptWorkspace string
	var receiptProtocol int
	var completed bool
	err = tx.QueryRowContext(ctx, `SELECT revision_id,workspace_id,protocol_version,completed_at IS NOT NULL FROM task_commit_receipts WHERE task_id=?`, current.ID).Scan(&receiptRevision, &receiptWorkspace, &receiptProtocol, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		conflict = true
	} else if err != nil {
		return err
	}
	if !conflict && (completed || receiptProtocol != 2 || receiptRevision != revision || revision != current.CurrentDefinitionRevisionID) {
		conflict = true
	}
	if !conflict {
		var pending bool
		if pending, err = hasPendingDeliveryCancellation(ctx, tx, current.ID); err != nil {
			return err
		}
		conflict = pending
	}
	if !conflict {
		var unfinished bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_commit_members WHERE task_id=? AND (state NOT IN ('COMPLETED','NO_CHANGE') OR (state='COMPLETED' AND resulting_commit='')))`, current.ID).Scan(&unfinished); err != nil {
			return err
		}
		conflict = unfinished
	}
	if !conflict {
		for _, target := range current.RepositoryTargets {
			var frozen bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_commit_members WHERE task_id=? AND repository_id=? AND member_id=? AND member_version=? AND origin_commit=? AND workspace_id=?)`, current.ID, target.RepositoryID, target.MemberID, target.MemberVersion, target.OriginCommit, receiptWorkspace).Scan(&frozen); err != nil {
				return err
			}
			if !frozen {
				conflict = true
				break
			}
		}
	}
	if !conflict {
		var stale int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM task_commit_members m
 LEFT JOIN worktrees wt ON wt.id=m.worktree_id
 LEFT JOIN task_delivery_current_operations c ON c.task_id=m.task_id AND c.repository_id=m.repository_id
 LEFT JOIN task_delivery_operation_outcomes r ON r.operation_id=c.operation_id
 WHERE m.task_id=? AND (wt.id IS NULL OR wt.path<>m.workspace_path OR wt.branch<>m.branch OR
 (wt.status<>'READY' AND (wt.status<>'MISSING' OR r.outcome IS NOT 'APPLIED'
 OR r.commit_oid IS NOT m.resulting_commit OR r.tree_oid IS NOT m.commit_tree
 OR (r.changed=1 AND m.state<>'COMPLETED') OR (r.changed=0 AND m.state<>'NO_CHANGE'))))`, current.ID).Scan(&stale); err != nil {
			return err
		}
		conflict = stale != 0
	}
	if !conflict {
		var currentWorkspace bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspaces w JOIN feature_areas a ON a.current_workspace_id=w.id AND a.project_id=w.project_id WHERE w.id=? AND w.project_id=? AND w.feature_area_id=a.id AND w.status='READY' AND w.runtime_quarantined=0 AND a.id=? AND a.status='ACTIVE' AND a.legacy_read_only=0)`, receiptWorkspace, current.ProjectID, current.FeatureAreaID).Scan(&currentWorkspace); err != nil {
			return err
		}
		conflict = !currentWorkspace
	}
	timestamp := formatTime(time.Now().UTC())
	if conflict {
		if err = markTaskDeliveryRecovery(ctx, tx, current.ProjectID, current.ID, time.Now().UTC()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE task_commit_receipts SET last_error=? WHERE task_id=? AND protocol_version=2 AND completed_at IS NULL`, git.ErrRecoveryRequired.Error(), current.ID); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return git.ErrRecoveryRequired
	}
	// Only exact saved outcomes can reach this point from recovery. Restore the
	// delivery phase inside the finalization transaction so existing acceptance
	// guards remain authoritative; rollback cannot expose new dispatch authority.
	if receiptOnly && phase == "RECOVERY_REQUIRED" {
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='DELIVERING',updated_at=? WHERE id=? AND status='ACTIVE' AND phase='RECOVERY_REQUIRED'`, timestamp, current.ID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_commit_receipts SET completed_at=?,last_error=NULL WHERE task_id=? AND protocol_version=2 AND completed_at IS NULL`, timestamp, current.ID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET status=CASE WHEN EXISTS(SELECT 1 FROM task_completion_intents c WHERE c.task_id=tasks.id AND c.completed_at IS NULL) THEN 'ACTIVE' ELSE 'SUCCEEDED' END,updated_at=? WHERE id=? AND ((status='ACTIVE' AND phase='DELIVERING') OR status='SUCCEEDED')`, timestamp, current.ID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return rollbackErr
		}
		if err = markTaskDeliveryRecovery(ctx, s.db.sql, current.ProjectID, current.ID, time.Now().UTC()); err != nil {
			return err
		}
		return git.ErrRecoveryRequired
	}
	return tx.Commit()
}

// taskCanReceiveDeliveryAdmission is the RetryTaskCommit entry contract: the
// exact admitted DELIVERING Task, or an already-accepted legacy row whose
// durable receipt still needs observation. It never accepts a Task that was
// not accepted at all.
func taskCanReceiveDeliveryAdmission(status task.Status, phase task.Phase) bool {
	return (status == task.StatusActive && phase == task.PhaseDelivering) || status == task.StatusSucceeded
}
