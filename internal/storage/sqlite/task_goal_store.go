package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

// DefineGoals inserts the immutable ordered Task Goal definitions inside the
// caller's transaction boundary (the task.Service owns validation).
func (s *TaskStore) DefineGoals(ctx context.Context, taskID string, values []task.Goal) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	projectID, err := s.projectIDForGoal(ctx, tx, taskID)
	if err != nil {
		return err
	}
	for _, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_goals(id,task_id,project_id,position,title,created_at) VALUES(?,?,?,?,?,?)`, value.ID, value.TaskID, projectID, value.Position, value.Title, formatTime(value.CreatedAt)); err != nil {
			return conflict(fmt.Errorf("insert task goal: %w", err))
		}
	}
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, taskID))
	if err != nil {
		return err
	}
	now := current.UpdatedAt
	if len(values) > 0 {
		now = values[0].CreatedAt
	}
	if _, err := ensureTaskDefinitionTx(ctx, tx, &current, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *TaskStore) projectIDForGoal(ctx context.Context, tx *sql.Tx, taskID string) (string, error) {
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id=?`, taskID).Scan(&projectID); err != nil {
		return "", fmt.Errorf("load task project for goal: %w", err)
	}
	return projectID, nil
}

func (s *TaskStore) Goals(ctx context.Context, taskID string) ([]task.Goal, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT id,task_id,project_id,position,title,created_at FROM task_goals WHERE task_id=? ORDER BY position`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []task.Goal
	for rows.Next() {
		var value task.Goal
		var projectID string
		var created string
		if err := rows.Scan(&value.ID, &value.TaskID, &projectID, &value.Position, &value.Title, &created); err != nil {
			return nil, err
		}
		if value.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// InitializeGoalProgress creates PENDING progress rows for every Goal of the
// Task that owns the Execution. It is idempotent per Execution: a second call
// for an already-initialized Execution is a no-op.
func (s *TaskStore) InitializeGoalProgress(ctx context.Context, executionProposalID, taskID, projectID string, now time.Time) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revisionID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(task_definition_revision_id,'') FROM execution_proposals WHERE id=? AND task_id=? AND project_id=?`, executionProposalID, taskID, projectID).Scan(&revisionID); err != nil {
		return err
	}
	if revisionID == "" {
		return task.ErrLegacyReadOnly
	}
	revision, err := getDefinitionRevision(ctx, tx, revisionID)
	if err != nil {
		return err
	}
	if revision.TaskID != taskID {
		return ErrConflict
	}
	goals := revision.Definition.Goals
	for _, goal := range goals {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO execution_goal_progress(execution_proposal_id,goal_id,project_id,status,updated_at) VALUES(?,?,?,?,?)`, executionProposalID, goal.ID, projectID, string(taskgoal.StatusPending), formatTime(now)); err != nil {
			return conflict(fmt.Errorf("insert goal progress: %w", err))
		}
	}
	return tx.Commit()
}

func (s *TaskStore) GoalProgress(ctx context.Context, executionProposalID string) ([]taskgoal.Progress, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT p.execution_proposal_id,p.goal_id,p.project_id,p.status,p.updated_at,g.position,g.title FROM execution_goal_progress p JOIN task_goals g ON g.id=p.goal_id WHERE p.execution_proposal_id=? ORDER BY g.position`, executionProposalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []taskgoal.Progress
	for rows.Next() {
		var value taskgoal.Progress
		var updated string
		if err := rows.Scan(&value.ExecutionID, &value.GoalID, &value.ProjectID, &value.Status, &updated, &value.Position, &value.Title); err != nil {
			return nil, err
		}
		if value.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// UpdateGoalProgress applies a transition-guarded status update for one goal
// of one Execution. ErrGoalProgressConflict signals an invalid transition or
// missing row.
func (s *TaskStore) UpdateGoalProgress(ctx context.Context, executionProposalID, goalID string, status taskgoal.Status, now time.Time) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID string
	// Goals are an implementation-only capability. A verification run may be
	// attached later, but it must never become the authority for a Goal update.
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(run_id,'') FROM execution_proposals WHERE id=?`, executionProposalID).Scan(&runID); err != nil {
		return err
	}
	// Pre-reservation progress initialization has no runtime Run. Every
	// reserved execution, including a legacy Feature-Area-less one, is fenced.
	if runID != "" {
		if err := s.db.checkRunTx(ctx, tx, runID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_goal_progress SET status=?,updated_at=? WHERE execution_proposal_id=? AND goal_id=?
 AND EXISTS(SELECT 1 FROM execution_proposals e JOIN tasks t ON t.id=e.task_id
 WHERE e.id=execution_goal_progress.execution_proposal_id AND t.legacy_read_only=0
 AND t.feature_area_id IS NOT NULL AND e.task_definition_revision_id IS NOT NULL
 AND e.outcome='RUNNING' AND EXISTS(
 SELECT 1 FROM feature_area_agent_claims c JOIN runs r ON r.id=c.run_id
 JOIN agent_operation_boundaries b ON b.run_id=r.id JOIN agent_conversations a ON a.id=b.agent_conversation_id
 WHERE c.feature_area_id=t.feature_area_id AND c.operation_kind='EXECUTION' AND c.operation_id=e.id
 AND r.execution_proposal_id=e.id AND r.purpose='IMPLEMENTATION' AND r.status='RUNNING'
 AND b.quiesced=0 AND b.advance_state='UNKNOWN' AND a.ended_at IS NULL
 AND a.feature_area_id=c.feature_area_id AND a.backend_thread_id=r.thread_id))`, string(status), formatTime(now), executionProposalID, goalID)
	if err != nil {
		if wrapped := conflict(err); errors.Is(wrapped, ErrConflict) {
			return wrapped
		}
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return agentconversation.ErrConflict
	}
	return tx.Commit()
}
