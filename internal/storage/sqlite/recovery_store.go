package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/recovery"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/worktree"
)

type RecoveryStore struct{ db *DB }

func NewRecoveryStore(db *DB) *RecoveryStore { return &RecoveryStore{db: db} }
func (s *RecoveryStore) ListRuns(ctx context.Context) ([]run.Run, error) {
	return NewRunStore(s.db).List(ctx)
}
func (s *RecoveryStore) ListWorktrees(ctx context.Context) ([]worktree.Worktree, error) {
	return NewWorktreeStore(s.db).List(ctx)
}

func (s *RecoveryStore) InterruptRun(ctx context.Context, id string, previous run.Status, now time.Time) (bool, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var taskID, projectID, executionProposalID string
	var current run.Status
	var parentStatus task.Status
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(u.task_id,''),u.status,u.project_id,COALESCE(t.status,''),COALESCE(u.execution_proposal_id,'') FROM runs u LEFT JOIN tasks t ON t.id=u.task_id WHERE u.id=? AND EXISTS(SELECT 1 FROM agent_executions ae WHERE ae.id=u.agent_execution_id AND ae.provenance_kind<>'LEGACY_UNKNOWN') AND COALESCE(u.runtime_epoch,'')<>? AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=u.id) AND NOT EXISTS(SELECT 1 FROM inspection_runtime_records i WHERE i.run_id=u.id) AND NOT EXISTS(SELECT 1 FROM app_server_run_bindings b WHERE b.run_id=u.id)`, id, s.db.epoch).Scan(&taskID, &current, &projectID, &parentStatus, &executionProposalID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current != previous || !(current == run.StatusPreparing || current == run.StatusRunning || current == run.StatusWaitingApproval || current == run.StatusCollecting) {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,failure_class=?,failure_code=?,failure_stage=?,failure_message=?,finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND status=?`, string(run.StatusInfrastructureFailed), string(run.FailureInfrastructure), recovery.InterruptedCode, "recovery", "Dev Bridge restarted during owned execution", formatTime(now), formatTime(now), id, string(previous))
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_token_usage SET status=?,updated_at=? WHERE run_id=? AND status=?`, string(run.TokenUsageUnavailable), formatTime(now), id, string(run.TokenUsagePending)); err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,status FROM approvals WHERE run_id=? AND status IN (?,?) ORDER BY created_at,id`, id, string(approval.StatusPending), string(approval.StatusDecided))
	if err != nil {
		return false, err
	}
	type interruptedApproval struct {
		id     string
		status approval.Status
	}
	var interrupted []interruptedApproval
	for rows.Next() {
		var value interruptedApproval
		if err := rows.Scan(&value.id, &value.status); err != nil {
			rows.Close()
			return false, err
		}
		interrupted = append(interrupted, value)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, value := range interrupted {
		if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status=? WHERE id=? AND status=?`, string(approval.StatusExpired), value.id, string(value.status)); err != nil {
			return false, err
		}
		payload, _ := event.Payload(map[string]any{"from": value.status, "to": approval.StatusExpired, "reason_code": recovery.InterruptedCode})
		if err := appendEvent(ctx, tx, event.Event{Type: event.ApprovalExpired, ProjectID: projectID, TaskID: taskID, RunID: id, EntityType: "approval", EntityID: value.id, Payload: payload, CreatedAt: now}); err != nil {
			return false, err
		}
	}
	payload, _ := event.Payload(map[string]any{"previous_status": previous, "new_status": run.StatusInfrastructureFailed, "reason_code": recovery.InterruptedCode})
	if err := appendEvent(ctx, tx, event.Event{Type: event.RecoveryRunInterrupted, ProjectID: projectID, TaskID: taskID, RunID: id, EntityType: "run", EntityID: id, Payload: payload, CreatedAt: now}); err != nil {
		return false, err
	}
	if parentStatus == task.StatusReady || parentStatus == task.StatusRunning || parentStatus == task.StatusCanceled {
		result, err = tx.ExecContext(ctx, `UPDATE tasks SET status=?,updated_at=? WHERE id=? AND status=?`, string(task.StatusFailed), formatTime(now), taskID, string(parentStatus))
		if err != nil {
			return false, err
		}
		taskAffected, _ := result.RowsAffected()
		if taskAffected == 0 {
			return false, errors.New("task changed during recovery transaction")
		}
		payload, _ := event.Payload(map[string]any{"from": parentStatus, "to": task.StatusFailed, "reason_code": recovery.InterruptedCode})
		if err := appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: projectID, TaskID: taskID, RunID: id, EntityType: "task", EntityID: taskID, Payload: payload, CreatedAt: now}); err != nil {
			return false, err
		}
	}
	if executionProposalID != "" {
		result, err = tx.ExecContext(ctx, `UPDATE execution_proposals SET outcome=?,failure_code=?,failure_message=?,updated_at=? WHERE id=? AND outcome=?`, string(executionproposal.OutcomeFailed), recovery.InterruptedCode, "Dev Bridge restarted during owned execution", formatTime(now), executionProposalID, string(executionproposal.OutcomeRunning))
		if err != nil {
			return false, err
		}
		executionAffected, _ := result.RowsAffected()
		if executionAffected == 1 {
			executionPayload, _ := event.Payload(map[string]any{"outcome": executionproposal.OutcomeFailed, "failure_code": recovery.InterruptedCode, "interrupted_run_id": id})
			if err := appendEvent(ctx, tx, event.Event{Type: event.ExecutionCompleted, ProjectID: projectID, TaskID: taskID, RunID: id, EntityType: "execution_proposal", EntityID: executionProposalID, Payload: executionPayload, CreatedAt: now}); err != nil {
				return false, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// InterruptOrphanedExecutionPlans closes the narrow restart gap between two
// serial Runs (or between reservation and preparation). At startup no live
// supervisor owns a RUNNING plan, even when its latest Run is already terminal
// or was only durably CREATED.
func (s *RecoveryStore) InterruptOrphanedExecutionPlans(ctx context.Context, now time.Time) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.project_id,e.task_id,COALESCE(e.verification_run_id,e.run_id,''),t.status FROM execution_proposals e JOIN tasks t ON t.id=e.task_id WHERE e.outcome=? AND EXISTS(SELECT 1 FROM agent_executions ae WHERE ae.id=e.id AND ae.provenance_kind<>'LEGACY_UNKNOWN') AND NOT EXISTS(SELECT 1 FROM runs r JOIN agent_executions ae ON ae.id=r.agent_execution_id WHERE r.id=COALESCE(e.verification_run_id,e.run_id) AND ae.provenance_kind='LEGACY_UNKNOWN') AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.operation_kind='EXECUTION' AND c.operation_id=e.id) AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.id=COALESCE(e.verification_run_id,e.run_id) AND r.runtime_epoch=?) ORDER BY e.created_at,e.id`, string(executionproposal.OutcomeRunning), s.db.epoch)
	if err != nil {
		return err
	}
	type orphan struct {
		id, projectID, taskID, runID string
		taskStatus                   task.Status
	}
	var values []orphan
	for rows.Next() {
		var value orphan
		if err := rows.Scan(&value.id, &value.projectID, &value.taskID, &value.runID, &value.taskStatus); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		result, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET outcome=?,failure_code=?,failure_message=?,updated_at=? WHERE id=? AND outcome=?`, string(executionproposal.OutcomeFailed), recovery.InterruptedCode, "Dev Bridge restarted during an owned execution plan", formatTime(now), value.id, string(executionproposal.OutcomeRunning))
		if err != nil {
			return err
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			continue
		}
		if value.taskStatus == task.StatusReady || value.taskStatus == task.StatusRunning || value.taskStatus == task.StatusCanceled {
			taskResult, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,updated_at=? WHERE id=? AND status=?`, string(task.StatusFailed), formatTime(now), value.taskID, string(value.taskStatus))
			if err != nil {
				return err
			}
			taskAffected, _ := taskResult.RowsAffected()
			if taskAffected == 1 {
				taskPayload, _ := event.Payload(map[string]any{"from": value.taskStatus, "to": task.StatusFailed, "reason_code": recovery.InterruptedCode, "execution_proposal_id": value.id})
				if err := appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: value.projectID, TaskID: value.taskID, RunID: value.runID, EntityType: "task", EntityID: value.taskID, Payload: taskPayload, CreatedAt: now}); err != nil {
					return err
				}
			}
		}
		payload, _ := event.Payload(map[string]any{"outcome": executionproposal.OutcomeFailed, "failure_code": recovery.InterruptedCode, "interrupted_run_id": value.runID})
		if err := appendEvent(ctx, tx, event.Event{Type: event.ExecutionCompleted, ProjectID: value.projectID, TaskID: value.taskID, RunID: value.runID, EntityType: "execution_proposal", EntityID: value.id, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *RecoveryStore) MarkWorktreeMissing(ctx context.Context, id string, now time.Time) (bool, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var runID, taskID, projectID string
	var status worktree.Status
	err = tx.QueryRowContext(ctx, `SELECT w.run_id,w.status,COALESCE(u.task_id,''),u.project_id FROM worktrees w JOIN runs u ON u.id=w.run_id WHERE w.id=?`, id).Scan(&runID, &status, &taskID, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, worktree.ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if status == worktree.StatusMissing || status == worktree.StatusRemoved {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE worktrees SET status=?,updated_at=? WHERE id=? AND status=?`, string(worktree.StatusMissing), formatTime(now), id, string(status))
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return false, nil
	}
	payload, _ := event.Payload(map[string]any{"previous_status": status, "new_status": worktree.StatusMissing, "path_state": "missing"})
	if err := appendEvent(ctx, tx, event.Event{Type: event.RecoveryWorktreeMissing, ProjectID: projectID, TaskID: taskID, RunID: runID, EntityType: "worktree", EntityID: id, Payload: payload, CreatedAt: now}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
