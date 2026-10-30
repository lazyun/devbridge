package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
)

type TaskPlanStore struct{ db *DB }

func NewTaskPlanStore(db *DB) *TaskPlanStore { return &TaskPlanStore{db: db} }

func (s *TaskPlanStore) Create(ctx context.Context, value taskplan.Plan) error {
	work, err := encodeWorkSpec(value.WorkSpec)
	if err != nil {
		return err
	}
	verification, err := encodeVerificationSpec(value.VerificationSpec)
	if err != nil {
		return err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	currentTask, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, value.TaskID))
	if err != nil {
		return err
	}
	if currentTask.ReviewApprovalID != "" {
		return taskplan.ErrConflict
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO task_plans(id,ai_session_id,project_id,task_id,title,work_spec_json,verification_spec_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, value.ID, value.AISessionID, value.ProjectID, value.TaskID, value.Title, work, verification, string(value.Status), formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return taskPlanConflict(err)
	}
	payload, _ := event.Payload(map[string]any{"ai_session_id": value.AISessionID, "task_id": value.TaskID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.TaskPlanCreated, ProjectID: value.ProjectID, TaskID: value.TaskID, EntityType: "task_plan", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *TaskPlanStore) Get(ctx context.Context, id string) (taskplan.Plan, error) {
	value, err := scanTaskPlan(s.db.sql.QueryRowContext(ctx, selectTaskPlan+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return taskplan.Plan{}, taskplan.ErrNotFound
	}
	if err != nil {
		return taskplan.Plan{}, fmt.Errorf("get task plan: %w", err)
	}
	return value, nil
}

func (s *TaskPlanStore) ListByProject(ctx context.Context, projectID string) ([]taskplan.Plan, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectTaskPlan+` WHERE project_id=? ORDER BY created_at DESC,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]taskplan.Plan, 0)
	for rows.Next() {
		value, err := scanTaskPlan(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *TaskPlanStore) LatestByTask(ctx context.Context, taskID string) (taskplan.Plan, error) {
	value, err := scanTaskPlan(s.db.sql.QueryRowContext(ctx, selectTaskPlan+` WHERE task_id=? ORDER BY created_at DESC,id LIMIT 1`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return taskplan.Plan{}, taskplan.ErrNotFound
	}
	if err != nil {
		return taskplan.Plan{}, fmt.Errorf("get latest task plan: %w", err)
	}
	return value, nil
}

func (s *TaskPlanStore) Confirm(ctx context.Context, id string, now time.Time) (taskplan.Plan, error) {
	return s.transition(ctx, id, taskplan.StatusPendingConfirmation, taskplan.StatusConfirmed, "confirmed_at", event.TaskPlanConfirmed, now)
}

func (s *TaskPlanStore) Reject(ctx context.Context, id string, now time.Time) (taskplan.Plan, error) {
	return s.transition(ctx, id, taskplan.StatusPendingConfirmation, taskplan.StatusRejected, "rejected_at", event.TaskPlanRejected, now)
}

func (s *TaskPlanStore) transition(ctx context.Context, id string, from, to taskplan.Status, timestampColumn, eventType string, now time.Time) (taskplan.Plan, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return taskplan.Plan{}, err
	}
	defer tx.Rollback()
	value, err := scanTaskPlan(tx.QueryRowContext(ctx, selectTaskPlan+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return taskplan.Plan{}, taskplan.ErrNotFound
	}
	if err != nil {
		return taskplan.Plan{}, err
	}
	var decisionRequest *decisionresolution.Request
	if attempt, ok := decisionresolution.FromContext(ctx); ok {
		request := attempt.Request
		decisionRequest = &request
		_, handled, preflightErr := preflightDecisionResolutionTx(ctx, tx, request, value.ProjectID)
		if preflightErr != nil {
			return value, preflightErr
		}
		if handled {
			return value, nil
		}
	}
	if value.Status != from {
		return value, &taskplan.TransitionError{From: value.Status, To: to}
	}
	query := fmt.Sprintf(`UPDATE task_plans SET status=?,updated_at=?,%s=? WHERE id=? AND status=? AND updated_at=?`, timestampColumn)
	result, err := tx.ExecContext(ctx, query, string(to), formatTime(now), formatTime(now), id, string(from), formatTime(value.UpdatedAt))
	if err != nil {
		return value, taskPlanConflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return value, taskplan.ErrConflict
	}
	value.Status, value.UpdatedAt = to, now.UTC()
	if to == taskplan.StatusConfirmed {
		value.ConfirmedAt = timePointer(now.UTC())
	} else {
		value.RejectedAt = timePointer(now.UTC())
	}
	payload, _ := event.Payload(map[string]any{"from": from, "to": to})
	if err := appendEvent(ctx, tx, event.Event{Type: eventType, ProjectID: value.ProjectID, TaskID: value.TaskID, EntityType: "task_plan", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
		return value, err
	}
	if decisionRequest != nil {
		if _, err := insertDecisionResolutionTx(ctx, tx, *decisionRequest, value.ProjectID, decisionresolution.ResultTaskPlan, value.ID, now); err != nil {
			return value, err
		}
	}
	if err := tx.Commit(); err != nil {
		return value, err
	}
	return value, nil
}

func (s *TaskPlanStore) Apply(ctx context.Context, id string, now time.Time) (taskplan.Plan, task.Task, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return taskplan.Plan{}, task.Task{}, err
	}
	defer tx.Rollback()
	value, err := scanTaskPlan(tx.QueryRowContext(ctx, selectTaskPlan+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, task.Task{}, taskplan.ErrNotFound
	}
	if err != nil {
		return value, task.Task{}, err
	}
	if value.Status != taskplan.StatusConfirmed {
		return value, task.Task{}, &taskplan.TransitionError{From: value.Status, To: taskplan.StatusApplied}
	}
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, value.TaskID))
	if err != nil {
		return value, current, err
	}
	if current.Type == task.TypeDevelopment || !task.CanAuthorPlan(current.Status) || current.LegacyReadOnly {
		return value, current, taskplan.ErrConflict
	}
	workJSON, _ := encodeWorkSpec(value.WorkSpec)
	verificationJSON, _ := encodeVerificationSpec(value.VerificationSpec)
	constraints, err := encodeStringItems(value.WorkSpec.Constraints)
	if err != nil {
		return value, current, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET goal=?,constraints_json=?,work_spec_json=?,verification_spec_json=?,confirmed_task_plan_id=?,status='READY',updated_at=? WHERE id=? AND status IN ('PLANNING','READY','SUCCEEDED','FAILED','CANCELED') AND legacy_read_only=0`, value.WorkSpec.Goal, constraints, workJSON, verificationJSON, value.ID, formatTime(now), value.TaskID)
	if err != nil {
		return value, current, taskPlanConflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return value, current, taskplan.ErrConflict
	}
	result, err = tx.ExecContext(ctx, `UPDATE task_plans SET status='APPLIED',applied_at=?,updated_at=? WHERE id=? AND status='CONFIRMED'`, formatTime(now), formatTime(now), value.ID)
	if err != nil {
		return value, current, taskPlanConflict(err)
	}
	affected, _ = result.RowsAffected()
	if affected != 1 {
		return value, current, taskplan.ErrConflict
	}
	payload, _ := event.Payload(map[string]any{"from": current.Status, "to": task.StatusReady, "task_plan_id": value.ID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: value.ProjectID, TaskID: value.TaskID, EntityType: "task", EntityID: value.TaskID, Payload: payload, CreatedAt: now}); err != nil {
		return value, current, err
	}
	if err := appendEvent(ctx, tx, event.Event{Type: event.TaskPlanApplied, ProjectID: value.ProjectID, TaskID: value.TaskID, EntityType: "task_plan", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
		return value, current, err
	}
	if value.VerificationSpec.Mode == task.VerificationModeNone && value.VerificationSpec.Waiver != nil {
		waiverPayload, _ := event.Payload(map[string]any{"source": value.VerificationSpec.Waiver.Source, "reason": value.VerificationSpec.Waiver.Reason, "task_plan_id": value.ID})
		if err := appendEvent(ctx, tx, event.Event{Type: event.VerificationWaived, ProjectID: value.ProjectID, TaskID: value.TaskID, EntityType: "verification", EntityID: value.ID, Payload: waiverPayload, CreatedAt: now}); err != nil {
			return value, current, err
		}
	}
	current.WorkSpec, current.VerificationSpec = task.CloneWorkSpec(value.WorkSpec), task.CloneVerificationSpec(value.VerificationSpec)
	current.Goal, current.Constraints = current.WorkSpec.Goal, append([]string(nil), current.WorkSpec.Constraints...)
	current.ConfirmedTaskPlanID, current.Status, current.UpdatedAt = value.ID, task.StatusReady, now.UTC()
	if _, err := ensureTaskDefinitionTx(ctx, tx, &current, now); err != nil {
		return value, current, err
	}
	if err := tx.Commit(); err != nil {
		return value, current, err
	}
	value.Status, value.UpdatedAt, value.AppliedAt = taskplan.StatusApplied, now.UTC(), timePointer(now.UTC())
	return value, current, nil
}

func encodeStringItems(values []string) (string, error) {
	encoded, err := json.Marshal(map[string]any{"version": 1, "items": append([]string(nil), values...)})
	return string(encoded), err
}

type taskPlanScanner interface{ Scan(...any) error }

func scanTaskPlan(scanner taskPlanScanner) (taskplan.Plan, error) {
	var value taskplan.Plan
	var work, verification, created, updated string
	var confirmed, rejected, applied sql.NullString
	if err := scanner.Scan(&value.ID, &value.AISessionID, &value.ProjectID, &value.TaskID, &value.Title, &work, &verification, &value.Status, &created, &updated, &confirmed, &rejected, &applied); err != nil {
		return value, err
	}
	var err error
	if value.WorkSpec, err = decodeWorkSpec(work); err != nil {
		return value, err
	}
	if value.VerificationSpec, err = decodeVerificationSpec(verification); err != nil {
		return value, err
	}
	if value.CreatedAt, err = parseTime(created); err != nil {
		return value, err
	}
	if value.UpdatedAt, err = parseTime(updated); err != nil {
		return value, err
	}
	if value.ConfirmedAt, err = parseNullableTime(confirmed); err != nil {
		return value, err
	}
	if value.RejectedAt, err = parseNullableTime(rejected); err != nil {
		return value, err
	}
	value.AppliedAt, err = parseNullableTime(applied)
	return value, err
}

func taskPlanConflict(err error) error {
	if errors.Is(conflict(err), ErrConflict) {
		return fmt.Errorf("%w: %v", taskplan.ErrConflict, err)
	}
	return err
}

const selectTaskPlan = `SELECT id,COALESCE(ai_session_id,''),project_id,task_id,title,work_spec_json,verification_spec_json,status,created_at,updated_at,confirmed_at,rejected_at,applied_at FROM task_plans`
