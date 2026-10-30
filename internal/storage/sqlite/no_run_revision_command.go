package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// StopAndReviseNoRunTask changes only phase. It deliberately writes no request
// receipt, revision, successor or runtime authority. A retry can read the same
// REVIEW_REQUIRED owner until a later Review progresses the Task.
func (s *WorkflowProgressionStore) StopAndReviseNoRunTask(ctx context.Context, in wp.StopAndReviseNoRunTask) (task.Task, error) {
	reject := func(code string) (task.Task, error) {
		return task.Task{}, wp.Reject(code, wp.NoRunSuccessorGuidance(in.ProjectID, in.TaskID, in.PredecessorExecutionID))
	}
	if in.ProjectID == "" || in.TaskID == "" || in.PredecessorExecutionID == "" || in.ExpectedDefinitionRevisionID == "" || in.ActorAuthority == "" {
		return reject("INVALID_ARGUMENT")
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback()
	if strings.HasPrefix(in.ActorAuthority, "operator:") {
		actor, ok := localoperator.FromContext(ctx)
		if !ok || actor.Check(in.ProjectID, in.TaskID, wp.NoRunStopAndReviseAction) != nil || in.ActorAuthority != "operator:"+actor.PrincipalID() {
			return reject("PROJECT_MISMATCH")
		}
	} else {
		if err = instructionCaller(ctx, tx, in.ActorAuthority); err != nil {
			return task.Task{}, err
		}
		var projectID string
		if err = tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, in.ActorAuthority).Scan(&projectID); err != nil || projectID != in.ProjectID {
			return reject("PROJECT_MISMATCH")
		}
	}
	owner, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, in.TaskID, in.ProjectID))
	if errors.Is(err, sql.ErrNoRows) {
		return reject("TASK_NOT_CURRENT")
	}
	if err != nil {
		return task.Task{}, err
	}
	if owner.Status != task.StatusActive || owner.Type != task.TypeDevelopment || owner.LegacyReadOnly || owner.CurrentExecutionID != in.PredecessorExecutionID || owner.CurrentDefinitionRevisionID != in.ExpectedDefinitionRevisionID {
		return reject("TASK_NOT_CURRENT")
	}
	if owner.Phase != task.PhaseNoRunSuccessorRequired && owner.Phase != task.PhaseReviewRequired {
		return reject("TASK_STATE_STALE")
	}
	facts, err := readNoRunSuccessorFacts(ctx, tx, owner.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return reject("NO_RUN_SUCCESSOR_AUTHORITY_UNAVAILABLE")
	}
	if err != nil {
		return task.Task{}, err
	}
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM no_run_successor_eligibilities WHERE task_id=? AND predecessor_execution_id=? AND task_definition_revision_id=? AND effective_cancellation_project_id=? AND effective_cancellation_request_id=? AND consumed_successor_execution_id IS NULL)`, owner.ID, facts.PredecessorExecutionID, facts.DefinitionRevisionID, facts.ProjectID, facts.EffectiveCancellationRequestID).Scan(&eligible)
	if err != nil {
		return task.Task{}, err
	}
	if !eligible {
		return reject("NO_RUN_SUCCESSOR_AUTHORITY_UNAVAILABLE")
	}
	if owner.Phase == task.PhaseReviewRequired {
		return owner, nil
	}
	parent, err := taskActionNoRunParentTx(ctx, tx, owner.ID)
	if err != nil {
		return task.Task{}, err
	}
	if parent != "" {
		if expected, ok := taskaction.Expected(ctx); ok {
			var generation int64
			if err = tx.QueryRowContext(ctx, `SELECT generation FROM task_action_heads WHERE task_id=?`, owner.ID).Scan(&generation); err != nil {
				return task.Task{}, err
			}
			if expected.TaskID != owner.ID || expected.Generation != generation {
				return task.Task{}, taskActionFailure("TASK_ACTION_STALE")
			}
		}
		ctx = taskaction.WithIntent(ctx, parent)
	}
	if err = taskActionAdmissionTx(ctx, tx, owner.ID); err != nil {
		return task.Task{}, err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED' WHERE id=? AND project_id=? AND status='ACTIVE' AND phase='NO_RUN_SUCCESSOR_REQUIRED' AND current_execution_id=? AND current_definition_revision_id=?`, owner.ID, owner.ProjectID, in.PredecessorExecutionID, in.ExpectedDefinitionRevisionID)
	if err != nil {
		return task.Task{}, err
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return task.Task{}, err
	}
	if count != 1 {
		return reject("TASK_NOT_CURRENT")
	}
	if parent != "" {
		if err = releaseTaskActionTx(ctx, tx, owner.ID, parent, "CANCELED_NO_RUN_REVISED"); err != nil {
			return task.Task{}, err
		}
	}
	// Re-read derived Task fields as well as the durable phase so the first
	// response and later readback describe the same post-transition state.
	owner, err = scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, owner.ID, owner.ProjectID))
	if err != nil {
		return task.Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return task.Task{}, err
	}
	return owner, nil
}
