package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

type ApprovalStore struct{ db *DB }

func NewApprovalStore(db *DB) *ApprovalStore { return &ApprovalStore{db: db} }

func (s *ApprovalStore) HasPendingByRun(ctx context.Context, runID string) (bool, error) {
	var present int
	err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM approvals WHERE run_id=? AND status=? LIMIT 1)`, runID, string(approval.StatusPending)).Scan(&present)
	return present != 0, err
}

func (s *ApprovalStore) CreatePending(ctx context.Context, value approval.Approval, now time.Time) (approval.Approval, error) {
	if value.ID == "" || value.RunID == "" || value.Kind == "" || len(value.RequestPayload) == 0 || len(value.AllowedDecisions) == 0 {
		return approval.Approval{}, errors.New("pending approval is incomplete")
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return approval.Approval{}, err
	}
	defer tx.Rollback()
	if err := s.db.checkRunTx(ctx, tx, value.RunID); err != nil {
		return approval.Approval{}, err
	}
	var runStatus run.Status
	var taskID, projectID, runThreadID string
	err = tx.QueryRowContext(ctx, `SELECT r.status,COALESCE(r.task_id,''),r.project_id,COALESCE(r.thread_id,''),`+approvalWorkflowOwnedRunSQL+` FROM runs r WHERE r.id=?`, value.RunID).Scan(&runStatus, &taskID, &projectID, &runThreadID, &value.WorkflowOwnedRun)
	if errors.Is(err, sql.ErrNoRows) {
		return approval.Approval{}, run.ErrNotFound
	}
	if err != nil {
		return approval.Approval{}, err
	}
	if runStatus != run.StatusRunning && runStatus != run.StatusWaitingApproval {
		return approval.Approval{}, fmt.Errorf("cannot request approval while run is %s", runStatus)
	}
	if value.ThreadID != "" && runThreadID != "" && value.ThreadID != runThreadID {
		return approval.Approval{}, errors.New("approval thread does not belong to run")
	}
	allowed, err := json.Marshal(value.AllowedDecisions)
	if err != nil {
		return approval.Approval{}, err
	}
	value.Status = approval.StatusPending
	value.CreatedAt = now.UTC()
	value.Decision, value.DecidedAt, value.DeliveredAt = nil, nil, nil
	_, err = tx.ExecContext(ctx, `INSERT INTO approvals(id,run_id,thread_id,turn_id,item_id,kind,status,request_payload_json,allowed_decisions_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		value.ID, value.RunID, value.ThreadID, value.TurnID, nullString(value.ItemID), string(value.Kind), string(value.Status), string(value.RequestPayload), string(allowed), formatTime(now))
	if err != nil {
		return approval.Approval{}, conflict(err)
	}
	payload, _ := event.Payload(map[string]any{"kind": value.Kind, "status": value.Status, "thread_id": value.ThreadID, "turn_id": value.TurnID, "item_id": value.ItemID, "allowed_decisions": value.AllowedDecisions})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ApprovalRequested, ProjectID: projectID, TaskID: taskID, RunID: value.RunID, EntityType: "approval", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
		return approval.Approval{}, err
	}
	if runStatus == run.StatusRunning {
		result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,updated_at=? WHERE id=? AND status=?`, string(run.StatusWaitingApproval), formatTime(now), value.RunID, string(run.StatusRunning))
		if err != nil {
			return approval.Approval{}, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return approval.Approval{}, ErrConflict
		}
		payload, _ := event.Payload(map[string]any{"from": run.StatusRunning, "to": run.StatusWaitingApproval})
		if err := appendEvent(ctx, tx, event.Event{Type: event.RunStatusChanged, ProjectID: projectID, TaskID: taskID, RunID: value.RunID, EntityType: "run", EntityID: value.RunID, Payload: payload, CreatedAt: now}); err != nil {
			return approval.Approval{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return approval.Approval{}, err
	}
	return approval.Clone(value), nil
}

func (s *ApprovalStore) Get(ctx context.Context, id string) (approval.Approval, error) {
	value, err := scanApproval(s.db.sql.QueryRowContext(ctx, selectApproval+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return approval.Approval{}, approval.ErrNotFound
	}
	if err != nil {
		return approval.Approval{}, fmt.Errorf("get approval: %w", err)
	}
	return value, nil
}

func (s *ApprovalStore) ListByRun(ctx context.Context, runID string) ([]approval.Approval, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectApproval+` WHERE run_id=? ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]approval.Approval, 0)
	for rows.Next() {
		value, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *ApprovalStore) Decide(ctx context.Context, id string, decision approval.Decision, now time.Time) (approval.Approval, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return approval.Approval{}, err
	}
	defer tx.Rollback()
	value, err := scanApproval(tx.QueryRowContext(ctx, selectApproval+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return approval.Approval{}, approval.ErrNotFound
	}
	if err != nil {
		return approval.Approval{}, err
	}
	if err := s.db.checkRunTx(ctx, tx, value.RunID); err != nil {
		return approval.Approval{}, err
	}
	decision, err = approval.NormalizeDecision(value, decision)
	if err != nil {
		return approval.Approval{}, err
	}
	encoded, err := json.Marshal(decision)
	if err != nil {
		return approval.Approval{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE approvals SET status=?,decision_json=?,decided_at=? WHERE id=? AND status=?`, string(approval.StatusDecided), string(encoded), formatTime(now), id, string(approval.StatusPending))
	if err != nil {
		return approval.Approval{}, conflict(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return approval.Approval{}, approval.ErrAlreadyResolved
	}
	value.Status = approval.StatusDecided
	value.Decision = &decision
	decidedAt := now.UTC()
	value.DecidedAt = &decidedAt
	projectID, taskID, runStatus, taskStatus, err := approvalParents(ctx, tx, value.RunID)
	if err != nil {
		return approval.Approval{}, err
	}
	payload, _ := event.Payload(map[string]any{"from": approval.StatusPending, "to": approval.StatusDecided, "decision": decision})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ApprovalDecided, ProjectID: projectID, TaskID: taskID, RunID: value.RunID, EntityType: "approval", EntityID: id, Payload: payload, CreatedAt: now}); err != nil {
		return approval.Approval{}, err
	}
	if decision.Type == approval.DecisionCancel && !value.WorkflowOwnedRun {
		if err := cancelRunForApproval(ctx, tx, value, projectID, taskID, runStatus, taskStatus, now); err != nil {
			return approval.Approval{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return approval.Approval{}, err
	}
	return approval.Clone(value), nil
}

func (s *ApprovalStore) MarkDelivered(ctx context.Context, id string, now time.Time) (approval.Approval, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return approval.Approval{}, err
	}
	defer tx.Rollback()
	value, err := scanApproval(tx.QueryRowContext(ctx, selectApproval+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return approval.Approval{}, approval.ErrNotFound
	}
	if err != nil {
		return approval.Approval{}, err
	}
	if err := s.db.checkRunTx(ctx, tx, value.RunID); err != nil {
		return approval.Approval{}, err
	}
	if value.Status != approval.StatusDecided {
		return approval.Approval{}, approval.ErrAlreadyResolved
	}
	result, err := tx.ExecContext(ctx, `UPDATE approvals SET status=?,delivered_at=? WHERE id=? AND status=?`, string(approval.StatusDelivered), formatTime(now), id, string(approval.StatusDecided))
	if err != nil {
		return approval.Approval{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return approval.Approval{}, approval.ErrAlreadyResolved
	}
	value.Status = approval.StatusDelivered
	deliveredAt := now.UTC()
	value.DeliveredAt = &deliveredAt
	projectID, taskID, runStatus, _, err := approvalParents(ctx, tx, value.RunID)
	if err != nil {
		return approval.Approval{}, err
	}
	payload, _ := event.Payload(map[string]any{"from": approval.StatusDecided, "to": approval.StatusDelivered})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ApprovalDelivered, ProjectID: projectID, TaskID: taskID, RunID: value.RunID, EntityType: "approval", EntityID: id, Payload: payload, CreatedAt: now}); err != nil {
		return approval.Approval{}, err
	}
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE run_id=? AND status IN (?,?)`, value.RunID, string(approval.StatusPending), string(approval.StatusDecided)).Scan(&unresolved); err != nil {
		return approval.Approval{}, err
	}
	if unresolved == 0 && runStatus == run.StatusWaitingApproval {
		result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,updated_at=? WHERE id=? AND status=?`, string(run.StatusRunning), formatTime(now), value.RunID, string(run.StatusWaitingApproval))
		if err != nil {
			return approval.Approval{}, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return approval.Approval{}, ErrConflict
		}
		payload, _ := event.Payload(map[string]any{"from": run.StatusWaitingApproval, "to": run.StatusRunning})
		if err := appendEvent(ctx, tx, event.Event{Type: event.RunStatusChanged, ProjectID: projectID, TaskID: taskID, RunID: value.RunID, EntityType: "run", EntityID: value.RunID, Payload: payload, CreatedAt: now}); err != nil {
			return approval.Approval{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return approval.Approval{}, err
	}
	return approval.Clone(value), nil
}

func (s *ApprovalStore) FailRun(ctx context.Context, runID, code, message string, now time.Time) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.db.checkRunTx(ctx, tx, runID); err != nil {
		return err
	}
	// This writer is retained only for standalone compatibility. Workflow Runs
	// must consume their Coordinator's finalization facts in RunStore.Update.
	var workflowOwned bool
	if err := tx.QueryRowContext(ctx, `SELECT `+approvalWorkflowOwnedRunSQL+` FROM runs r WHERE r.id=?`, runID).Scan(&workflowOwned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return run.ErrNotFound
		}
		return err
	}
	if workflowOwned {
		return approval.ErrWorkflowFinalizationRequired
	}
	projectID, taskID, runStatus, taskStatus, err := approvalParents(ctx, tx, runID)
	if err != nil {
		return err
	}
	if runStatus == run.StatusSucceeded || runStatus == run.StatusFailed || runStatus == run.StatusInfrastructureFailed || runStatus == run.StatusCanceled {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,failure_class=?,failure_code=?,failure_stage=?,failure_message=?,finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND status=?`,
		string(run.StatusInfrastructureFailed), string(run.FailureInfrastructure), code, "approval", message, formatTime(now), formatTime(now), runID, string(runStatus))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_token_usage SET status=?,updated_at=? WHERE run_id=? AND status=?`, string(run.TokenUsageUnavailable), formatTime(now), runID, string(run.TokenUsagePending)); err != nil {
		return err
	}
	payload, _ := event.Payload(map[string]any{"from": runStatus, "to": run.StatusInfrastructureFailed, "reason_code": code})
	if err := appendEvent(ctx, tx, event.Event{Type: event.RunStatusChanged, ProjectID: projectID, TaskID: taskID, RunID: runID, EntityType: "run", EntityID: runID, Payload: payload, CreatedAt: now}); err != nil {
		return err
	}
	if err := cancelPendingApprovals(ctx, tx, runID, "run_failed", projectID, taskID, now); err != nil {
		return err
	}
	if taskStatus == task.StatusRunning {
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,updated_at=? WHERE id=? AND status=?`, string(task.StatusFailed), formatTime(now), taskID, string(task.StatusRunning)); err != nil {
			return err
		}
		payload, _ := event.Payload(map[string]any{"from": taskStatus, "to": task.StatusFailed, "reason_code": code})
		if err := appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: projectID, TaskID: taskID, RunID: runID, EntityType: "task", EntityID: taskID, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const approvalWorkflowOwnedRunSQL = `(COALESCE(r.execution_proposal_id,'')<>'' OR COALESCE(r.operation_kind,'')<>'' OR COALESCE(r.operation_id,'')<>'' OR COALESCE(r.review_id,'')<>'')`

const selectApproval = `SELECT id,run_id,thread_id,turn_id,COALESCE(item_id,''),kind,status,request_payload_json,allowed_decisions_json,decision_json,created_at,decided_at,delivered_at,(SELECT ` + approvalWorkflowOwnedRunSQL + ` FROM runs r WHERE r.id=approvals.run_id) FROM approvals`

type approvalScanner interface{ Scan(...any) error }

func scanApproval(scanner approvalScanner) (approval.Approval, error) {
	var value approval.Approval
	var kind, status, requestPayload, allowedJSON, created string
	var decisionJSON, decided, delivered sql.NullString
	if err := scanner.Scan(&value.ID, &value.RunID, &value.ThreadID, &value.TurnID, &value.ItemID, &kind, &status, &requestPayload, &allowedJSON, &decisionJSON, &created, &decided, &delivered, &value.WorkflowOwnedRun); err != nil {
		return approval.Approval{}, err
	}
	value.Kind, value.Status = approval.Kind(kind), approval.Status(status)
	value.RequestPayload = json.RawMessage(requestPayload)
	if err := json.Unmarshal([]byte(allowedJSON), &value.AllowedDecisions); err != nil {
		return approval.Approval{}, err
	}
	if decisionJSON.Valid {
		var decision approval.Decision
		if err := json.Unmarshal([]byte(decisionJSON.String), &decision); err != nil {
			return approval.Approval{}, err
		}
		value.Decision = &decision
	}
	var err error
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return approval.Approval{}, err
	}
	if decided.Valid {
		when, err := parseTime(decided.String)
		if err != nil {
			return approval.Approval{}, err
		}
		value.DecidedAt = &when
	}
	if delivered.Valid {
		when, err := parseTime(delivered.String)
		if err != nil {
			return approval.Approval{}, err
		}
		value.DeliveredAt = &when
	}
	return value, nil
}

func approvalParents(ctx context.Context, tx *sql.Tx, runID string) (string, string, run.Status, task.Status, error) {
	var projectID, taskID string
	var runStatus run.Status
	var taskStatus task.Status
	err := tx.QueryRowContext(ctx, `SELECT r.project_id,COALESCE(r.task_id,''),r.status,COALESCE(t.status,'') FROM runs r LEFT JOIN tasks t ON t.id=r.task_id WHERE r.id=?`, runID).Scan(&projectID, &taskID, &runStatus, &taskStatus)
	if errors.Is(err, sql.ErrNoRows) {
		err = run.ErrNotFound
	}
	return projectID, taskID, runStatus, taskStatus, err
}

func cancelRunForApproval(ctx context.Context, tx *sql.Tx, value approval.Approval, projectID, taskID string, runStatus run.Status, taskStatus task.Status, now time.Time) error {
	if value.WorkflowOwnedRun {
		return errors.New("workflow Approval cancellation only declines the requested action")
	}
	if runStatus != run.StatusRunning && runStatus != run.StatusWaitingApproval {
		return fmt.Errorf("cannot cancel run from %s", runStatus)
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,failure_class=?,failure_code=?,failure_stage=?,failure_message=?,finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND status=?`,
		string(run.StatusCanceled), string(run.FailureCanceled), "human_approval_canceled", "approval", "Run canceled by human approval decision", formatTime(now), formatTime(now), value.RunID, string(runStatus))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_token_usage SET status=?,updated_at=? WHERE run_id=? AND status=?`, string(run.TokenUsageUnavailable), formatTime(now), value.RunID, string(run.TokenUsagePending)); err != nil {
		return err
	}
	payload, _ := event.Payload(map[string]any{"from": runStatus, "to": run.StatusCanceled, "reason_code": "human_approval_canceled"})
	if err := appendEvent(ctx, tx, event.Event{Type: event.RunStatusChanged, ProjectID: projectID, TaskID: taskID, RunID: value.RunID, EntityType: "run", EntityID: value.RunID, Payload: payload, CreatedAt: now}); err != nil {
		return err
	}
	if err := cancelOpenApprovalsExcept(ctx, tx, value.RunID, value.ID, "run_canceled", projectID, taskID, now); err != nil {
		return err
	}
	if taskStatus == task.StatusRunning {
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,updated_at=? WHERE id=? AND status=?`, string(task.StatusCanceled), formatTime(now), taskID, string(task.StatusRunning)); err != nil {
			return err
		}
		payload, _ := event.Payload(map[string]any{"from": taskStatus, "to": task.StatusCanceled, "reason_code": "human_approval_canceled"})
		if err := appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: projectID, TaskID: taskID, RunID: value.RunID, EntityType: "task", EntityID: taskID, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
	}
	return nil
}

func cancelOpenApprovalsExcept(ctx context.Context, tx *sql.Tx, runID, exceptID, reason, projectID, taskID string, now time.Time) error {
	query := `SELECT id,status FROM approvals WHERE run_id=? AND status IN (?,?)`
	rows, err := tx.QueryContext(ctx, query, runID, string(approval.StatusPending), string(approval.StatusDecided))
	if err != nil {
		return err
	}
	type open struct {
		id     string
		status approval.Status
	}
	var values []open
	for rows.Next() {
		var value open
		if err := rows.Scan(&value.id, &value.status); err != nil {
			rows.Close()
			return err
		}
		if value.id != exceptID {
			values = append(values, value)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status=? WHERE id=? AND status=?`, string(approval.StatusCanceled), value.id, string(value.status)); err != nil {
			return err
		}
		payload, _ := event.Payload(map[string]any{"from": value.status, "to": approval.StatusCanceled, "reason": reason})
		if err := appendEvent(ctx, tx, event.Event{Type: event.ApprovalCanceled, ProjectID: projectID, TaskID: taskID, RunID: runID, EntityType: "approval", EntityID: value.id, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
	}
	return nil
}

func cancelPendingApprovals(ctx context.Context, tx *sql.Tx, runID, reason, projectID, taskID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM approvals WHERE run_id=? AND status=? ORDER BY created_at,id`, runID, string(approval.StatusPending))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status=? WHERE id=? AND status=?`, string(approval.StatusCanceled), id, string(approval.StatusPending)); err != nil {
			return err
		}
		payload, _ := event.Payload(map[string]any{"from": approval.StatusPending, "to": approval.StatusCanceled, "reason": reason})
		if err := appendEvent(ctx, tx, event.Event{Type: event.ApprovalCanceled, ProjectID: projectID, TaskID: taskID, RunID: runID, EntityType: "approval", EntityID: id, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
	}
	return nil
}
