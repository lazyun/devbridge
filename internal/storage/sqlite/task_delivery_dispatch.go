package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/git"
)

type taskDeliveryDispatchInput struct {
	TaskID, RepositoryID, RevisionID, WorkspacePath, Branch, ExpectedHead, Snapshot, Message string
	Paths                                                                                    []string
	Selective                                                                                bool
}
type taskDeliveryDispatchResult struct {
	Commit, Tree string
	Changed      bool
}

// Read exact Core evidence before consulting a mutable checkout. This helper
// never reserves authority, observes Git, or invents an outcome for UNKNOWN.
func readTaskDeliveryOutcome(ctx context.Context, q definitionQuerier, in taskDeliveryDispatchInput) (taskDeliveryDispatchResult, bool, error) {
	var result taskDeliveryDispatchResult
	raw, err := json.Marshal(in)
	if err != nil {
		return result, false, err
	}
	var payload, hash string
	var outcome, commit, tree, detail sql.NullString
	var changed sql.NullBool
	err = q.QueryRowContext(ctx, `SELECT o.payload_json,o.payload_sha256,r.outcome,r.commit_oid,r.tree_oid,r.changed,r.error
 FROM task_delivery_current_operations c JOIN task_delivery_operations o ON o.operation_id=c.operation_id
 LEFT JOIN task_delivery_operation_outcomes r ON r.operation_id=o.operation_id
 WHERE c.task_id=? AND c.repository_id=?`, in.TaskID, in.RepositoryID).Scan(&payload, &hash, &outcome, &commit, &tree, &changed, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if payload != string(raw) || hash != workflowHash(string(raw)) {
		return result, true, git.ErrRecoveryRequired
	}
	if !outcome.Valid {
		return result, false, nil
	}
	if outcome.String == "REJECTED_NOT_CONSUMED" {
		return result, true, fmt.Errorf("%w: %s", delivery.ErrRejected, detail.String)
	}
	if outcome.String != "APPLIED" {
		return result, true, git.ErrRecoveryRequired
	}
	return taskDeliveryDispatchResult{Commit: commit.String, Tree: tree.String, Changed: changed.Bool}, true, nil
}

func initialDeliveryOperation(taskID, repositoryID string) string {
	return "initial/" + workflowHash(taskID+"\x00"+repositoryID)
}

const unresolvedDeliverySQL = `EXISTS(SELECT 1 FROM task_delivery_operations o JOIN task_delivery_operation_dispatches d ON d.operation_id=o.operation_id LEFT JOIN task_delivery_operation_outcomes r ON r.operation_id=o.operation_id WHERE o.task_id=? AND r.operation_id IS NULL)`

// A reserved, undispatched retry can be abandoned. A dispatched operation can
// only be abandoned after the exact positive or negative outcome is durable.
func guardTaskDeliveryCancellationTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input) error {
	if in.OwnerType != "TASK" {
		return nil
	}
	var unresolved bool
	if err := tx.QueryRowContext(ctx, `SELECT `+unresolvedDeliverySQL, in.OwnerID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved {
		return executioncancel.ErrRecoveryRequired
	}
	return nil
}

// Each new operation owns one dispatch. Only the explicit reprepare command
// can append a successor to an exact rejected operation. Existing dispatches
// observe only; a lost response never authorizes another process.
func (s *WorkflowProgressionStore) dispatchTaskDeliveryEffect(ctx context.Context, in taskDeliveryDispatchInput, effect, observe func() (taskDeliveryDispatchResult, error)) (taskDeliveryDispatchResult, error) {
	empty := taskDeliveryDispatchResult{}
	raw, err := json.Marshal(in)
	if err != nil {
		return empty, err
	}
	hash := workflowHash(string(raw))
	if s.deliveryFaultHook != nil {
		if err = s.deliveryFaultHook("TASK_COMMIT_BEFORE_CLAIM", in.RepositoryID); err != nil {
			return empty, err
		}
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var operation, payload, storedHash string
	err = tx.QueryRowContext(ctx, `SELECT o.operation_id,o.payload_json,o.payload_sha256 FROM task_delivery_current_operations c JOIN task_delivery_operations o ON o.operation_id=c.operation_id WHERE c.task_id=? AND c.repository_id=?`, in.TaskID, in.RepositoryID).Scan(&operation, &payload, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		operation = initialDeliveryOperation(in.TaskID, in.RepositoryID)
		now := formatTime(time.Now().UTC())
		// Preserve the original v116 dispatch identity. It is immutable history;
		// retries use new operations, never replacement rows in this table.
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_delivery_dispatch_intents(task_id,repository_id,payload_json,payload_sha256,runtime_epoch,created_at) VALUES(?,?,?,?,?,?)`, in.TaskID, in.RepositoryID, string(raw), hash, s.db.epoch, now); err != nil {
			return empty, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_delivery_operations(operation_id,project_id,task_id,repository_id,request_id,payload_json,payload_sha256,member_id,execution_id,workset_id,development_context_id,created_at)
SELECT ?,t.project_id,t.id,?,?,?, ?,COALESCE(m.member_id,?),COALESCE(NULLIF(t.current_execution_id,''),'direct/'||q.result_id),q.workspace_id,COALESCE((SELECT c.id FROM development_cycles c WHERE c.project_id=t.project_id AND c.repository_id=? AND c.superseded_at IS NULL),'workspace/'||q.workspace_id),?
FROM tasks t JOIN task_commit_receipts q ON q.task_id=t.id LEFT JOIN task_commit_members m ON m.task_id=t.id AND m.repository_id=? WHERE t.id=?`, operation, in.RepositoryID, operation, string(raw), hash, in.RepositoryID, in.RepositoryID, now, in.RepositoryID, in.TaskID)
		if err != nil {
			return empty, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_delivery_current_operations(task_id,repository_id,operation_id) VALUES(?,?,?)`, in.TaskID, in.RepositoryID, operation); err != nil {
			return empty, err
		}
	} else if err != nil {
		return empty, err
	} else if payload != string(raw) || storedHash != hash {
		return empty, git.ErrRecoveryRequired
	}
	var result taskDeliveryDispatchResult
	var outcome, detail string
	err = tx.QueryRowContext(ctx, `SELECT outcome,commit_oid,tree_oid,changed,error FROM task_delivery_operation_outcomes WHERE operation_id=?`, operation).Scan(&outcome, &result.Commit, &result.Tree, &result.Changed, &detail)
	if err == nil {
		if outcome == "REJECTED_NOT_CONSUMED" {
			return empty, fmt.Errorf("%w: %s", delivery.ErrRejected, detail)
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	var dispatched bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_delivery_operation_dispatches WHERE operation_id=?)`, operation).Scan(&dispatched); err != nil {
		return empty, err
	}
	fresh := !dispatched
	if fresh {
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_delivery_operation_dispatches(operation_id,runtime_epoch,created_at) VALUES(?,?,?)`, operation, s.db.epoch, formatTime(time.Now().UTC())); err != nil {
			return empty, err
		}
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	if fresh && s.deliveryFaultHook != nil {
		if err = s.deliveryFaultHook("TASK_COMMIT_BEFORE_GIT", in.RepositoryID); err != nil {
			return empty, err
		}
	}
	if fresh {
		result, err = effect()
	} else {
		result, err = observe()
	}
	if err != nil {
		var rejected *git.BeforeEffectError
		if fresh && errors.As(err, &rejected) {
			if saveErr := s.recordDeliveryOutcome(context.WithoutCancel(ctx), in, operation, hash, empty, rejected.Error()); saveErr != nil {
				return empty, saveErr
			}
			return empty, fmt.Errorf("%w: %w", delivery.ErrRejected, rejected)
		}
		return empty, err
	}
	if s.deliveryFaultHook != nil {
		if err = s.deliveryFaultHook("TASK_COMMIT_BEFORE_RESULT", in.RepositoryID); err != nil {
			return empty, err
		}
	}
	err = s.recordDeliveryOutcome(context.WithoutCancel(ctx), in, operation, hash, result, "")
	return result, err
}

func (s *WorkflowProgressionStore) recordDeliveryOutcome(ctx context.Context, in taskDeliveryDispatchInput, operation, hash string, result taskDeliveryDispatchResult, rejection string) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	outcome, before := "APPLIED", 0
	if rejection != "" {
		outcome, before = "REJECTED_NOT_CONSUMED", 1
	}
	now := formatTime(time.Now().UTC())
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_delivery_operation_outcomes(operation_id,outcome,before_external_effect,commit_oid,tree_oid,changed,error,created_at) VALUES(?,?,?,?,?,?,?,?)`, operation, outcome, before, result.Commit, result.Tree, result.Changed, rejection, now); err != nil {
		return err
	}
	var initial bool
	if err = tx.QueryRowContext(ctx, `SELECT predecessor_operation_id IS NULL FROM task_delivery_operations WHERE operation_id=?`, operation).Scan(&initial); err != nil {
		return err
	}
	if rejection == "" && initial {
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_delivery_dispatch_results(task_id,repository_id,payload_sha256,commit_oid,tree_oid,changed,created_at) VALUES(?,?,?,?,?,?,?)`, in.TaskID, in.RepositoryID, hash, result.Commit, result.Tree, result.Changed, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
