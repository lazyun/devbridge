package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/task"
)

// noRunSuccessorEligibleSQL is the sole canceled-before-Run authority predicate.
// The same exact durable facts govern v95 backfill, post-cancel projection,
// restart reconciliation and successor eligibility. The CANCELED receipt is
// selected by state and exact owner; ALREADY_TERMINAL is never an authority.
const noRunSuccessorEligibleSQL = `t.task_type='DEVELOPMENT' AND t.legacy_read_only=0 AND t.status='ACTIVE' AND t.current_execution_id IS NOT NULL AND EXISTS(
 SELECT 1 FROM execution_proposals e JOIN execution_cancellations c ON c.project_id=e.project_id AND c.owner_type='EXECUTION' AND c.owner_id=e.id AND c.task_id=t.id AND c.state='CANCELED' AND c.backend_disposition='NO_BACKEND' AND c.interrupt_state='NOT_SENT' AND c.run_id IS NULL AND c.resolved_at IS NOT NULL
 WHERE e.id=t.current_execution_id AND e.task_id=t.id AND e.project_id=t.project_id AND e.task_definition_revision_id=t.current_definition_revision_id AND e.status='ADMITTED' AND e.outcome='CANCELED' AND e.run_id IS NULL AND e.verification_run_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations other WHERE other.project_id=e.project_id AND other.owner_type='EXECUTION' AND other.owner_id=e.id AND other.state='CANCELED' AND other.request_id<>c.request_id)
 AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.execution_proposal_id=e.id OR (r.operation_kind='EXECUTION' AND r.operation_id=e.id))
 AND NOT EXISTS(SELECT 1 FROM runtime_preparation_attempts attempt JOIN runtime_scope_plans p ON p.plan_id=attempt.plan_id WHERE p.owner_kind IN ('IMPLEMENTATION','EXECUTION') AND p.owner_id=e.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_workset_bindings b JOIN runtime_scope_plans p ON p.plan_id=b.plan_id WHERE p.owner_kind IN ('IMPLEMENTATION','EXECUTION') AND p.owner_id=e.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_start_authorities a JOIN runtime_scope_plans p ON p.plan_id=a.plan_id WHERE p.owner_kind IN ('IMPLEMENTATION','EXECUTION') AND p.owner_id=e.id)
 AND NOT EXISTS(SELECT 1 FROM recovery_status_records s JOIN runs r ON r.id=s.source_run_id WHERE r.execution_proposal_id=e.id)
 AND NOT EXISTS(SELECT 1 FROM no_run_successor_effect_authorities authority WHERE authority.predecessor_execution_id=e.id)
)`

type noRunSuccessorFacts struct {
	TaskID                         string
	ProjectID                      string
	PredecessorExecutionID         string
	DefinitionRevisionID           string
	EffectiveCancellationRequestID string
}

type noRunFactsReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readNoRunSuccessorFacts(ctx context.Context, q noRunFactsReader, taskID string) (noRunSuccessorFacts, error) {
	var f noRunSuccessorFacts
	err := q.QueryRowContext(ctx, `SELECT t.id,t.project_id,t.current_execution_id,t.current_definition_revision_id,c.request_id FROM tasks t JOIN execution_proposals e ON e.id=t.current_execution_id JOIN execution_cancellations c ON c.project_id=e.project_id AND c.owner_type='EXECUTION' AND c.owner_id=e.id AND c.task_id=t.id AND c.state='CANCELED' WHERE t.id=? AND `+noRunSuccessorEligibleSQL, taskID).Scan(&f.TaskID, &f.ProjectID, &f.PredecessorExecutionID, &f.DefinitionRevisionID, &f.EffectiveCancellationRequestID)
	return f, err
}

// ProjectCanceledAdmittedExecutionToSuccessorRequired is a separate Task
// transaction, never part of the cancellation receipt commit. Replay does not
// touch updated_at, and the predecessor pointer/revision remain unchanged.
func (s *ExecutionCancellationStore) ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx context.Context, taskID, predecessorID string) (task.Phase, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var phase task.Phase
	var current, status string
	if err = tx.QueryRowContext(ctx, `SELECT phase,COALESCE(current_execution_id,''),status FROM tasks WHERE id=?`, taskID).Scan(&phase, &current, &status); err != nil {
		return "", err
	}
	// The original cancellation remains readable after Task cancellation or a
	// later owner replaces the predecessor. Neither needs lifecycle repair.
	if current != predecessorID || status != string(task.StatusActive) {
		return phase, nil
	}
	facts, err := readNoRunSuccessorFacts(ctx, tx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		// Definition-only approval can retain the canceled predecessor until
		// the explicit start admits the replacement. Its exact new approval
		// already supersedes the old revision; cancellation replay stays read-only.
		if phase == task.PhaseImplementationReady {
			advanced, readErr := noRunRevisionApprovedTx(ctx, tx, taskID)
			if readErr != nil {
				return "", readErr
			}
			if advanced {
				return phase, nil
			}
		}
		return "", ErrConflict // cancellation is durable, lifecycle still needs repair
	}
	if err != nil {
		return "", err
	}
	if facts.PredecessorExecutionID != predecessorID {
		return "", ErrConflict
	}
	if phase == task.PhaseRecoveryRequired {
		// A later Review can own recovery while the canceled Execution is
		// retained for history. Its recovery must not be replaced by replaying
		// that earlier Execution cancellation (including during boot).
		var newerReview bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t WHERE t.id=? AND `+noRunRevisionPredecessorSQL+` AND `+noRunRevisionReviewSQL+`)`, taskID).Scan(&newerReview); err != nil {
			return "", err
		}
		if newerReview {
			return phase, nil
		}
	}
	// STOP_AND_REVISE preserves the canceled predecessor while a new Review
	// progresses. Its existing eligibility proves that projection already ran;
	// replay must not roll the Task back or manufacture a second authority.
	if phase == task.PhaseReviewRequired || phase == task.PhaseReviewing || phase == task.PhaseReviewApprovalPending {
		var revised bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM no_run_successor_eligibilities WHERE task_id=? AND predecessor_execution_id=? AND task_definition_revision_id=? AND effective_cancellation_project_id=? AND effective_cancellation_request_id=? AND consumed_successor_execution_id IS NULL)`, taskID, predecessorID, facts.DefinitionRevisionID, facts.ProjectID, facts.EffectiveCancellationRequestID).Scan(&revised); err != nil {
			return "", err
		}
		if revised {
			return phase, nil
		}
		return "", ErrConflict
	}
	// Eligibility is a predecessor-bound business authority. Insert it only
	// after reading the exact effective CANCELED receipt under this writer.
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO no_run_successor_eligibilities(predecessor_execution_id,task_id,task_definition_revision_id,effective_cancellation_project_id,effective_cancellation_request_id,created_at) SELECT ?,?,?,?,?,requested_at FROM execution_cancellations WHERE project_id=? AND request_id=?`, facts.PredecessorExecutionID, facts.TaskID, facts.DefinitionRevisionID, facts.ProjectID, facts.EffectiveCancellationRequestID, facts.ProjectID, facts.EffectiveCancellationRequestID)
	if err != nil {
		return "", err
	}
	var storedTask, storedRevision, storedProject, storedRequest string
	err = tx.QueryRowContext(ctx, `SELECT task_id,task_definition_revision_id,effective_cancellation_project_id,effective_cancellation_request_id FROM no_run_successor_eligibilities WHERE predecessor_execution_id=?`, facts.PredecessorExecutionID).Scan(&storedTask, &storedRevision, &storedProject, &storedRequest)
	if err != nil {
		return "", err
	}
	if storedTask != facts.TaskID || storedRevision != facts.DefinitionRevisionID || storedProject != facts.ProjectID || storedRequest != facts.EffectiveCancellationRequestID {
		return "", ErrConflict
	}
	if phase == task.PhaseNoRunSuccessorRequired {
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return phase, nil
	}
	if phase != task.PhaseImplementationReady && phase != task.PhaseImplementationPreparing && phase != task.PhaseRecoveryRequired {
		return "", ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='NO_RUN_SUCCESSOR_REQUIRED' WHERE id=? AND status='ACTIVE' AND phase=? AND current_execution_id=? AND current_definition_revision_id=?`, taskID, phase, predecessorID, facts.DefinitionRevisionID)
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return task.PhaseNoRunSuccessorRequired, nil
}

// ReconcileCanceledNoRunSuccessors repairs an interrupted post-commit Task
// projection before generic boot recovery can interpret terminal_observed=0.
func (s *RecoveryStore) ReconcileCanceledNoRunSuccessors(ctx context.Context) error {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT t.id,t.current_execution_id FROM tasks t WHERE t.phase IN ('IMPLEMENTATION_READY','IMPLEMENTATION_PREPARING','RECOVERY_REQUIRED') AND `+noRunSuccessorEligibleSQL)
	if err != nil {
		return err
	}
	type pair struct{ taskID, predecessorID string }
	var pending []pair
	for rows.Next() {
		var p pair
		if err = rows.Scan(&p.taskID, &p.predecessorID); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	projector := NewExecutionCancellationStore(s.db)
	for _, p := range pending {
		if _, err = projector.ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, p.taskID, p.predecessorID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}
