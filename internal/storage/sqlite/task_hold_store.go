package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/task"
)

func (s *WorkflowProgressionStore) ReadTaskHold(ctx context.Context, project, id string) (*task.Hold, error) {
	var out task.Hold
	var at string
	err := s.db.sql.QueryRowContext(ctx, `SELECT h.task_id,h.execution_id,h.revision_id,h.note,h.held_at FROM task_holds h JOIN tasks t ON t.id=h.task_id WHERE h.task_id=? AND t.project_id=? AND t.status='ACTIVE' AND t.current_execution_id=h.execution_id AND t.current_definition_revision_id=h.revision_id`, id, project).Scan(&out.TaskID, &out.ExecutionID, &out.RevisionID, &out.Note, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out.HeldAt, err = parseTime(at)
	return &out, err
}

func verificationChoicePhase(t task.Task) bool {
	if t.Phase == task.PhaseAcceptancePending || t.Phase == task.PhaseVerifying {
		return true
	}
	return t.Phase == task.PhaseRecoveryRequired && t.RecoveryOrigin != nil && t.RecoveryOrigin.Phase == task.PhaseVerifying
}

func (s *WorkflowProgressionStore) ChangeTaskHold(ctx context.Context, in task.HoldInput) (task.Task, error) {
	if in.Actor == "" || in.RequestID == "" || len(in.RequestID) > 128 || len(in.Note) > 8192 || !utf8.ValidString(in.Note) || (in.Action != "DEFER_TASK" && in.Action != "CONTINUE_TASK") {
		return task.Task{}, errors.New("invalid task hold input")
	}
	payload := in
	payload.Actor = ""
	raw, _ := json.Marshal(payload)
	hash := workflowHash(string(raw))
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback()
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, in.TaskID, in.ProjectID))
	if err != nil {
		return current, err
	}
	if strings.HasPrefix(in.Actor, "session:") {
		var project string
		if err = instructionCaller(ctx, tx, strings.TrimPrefix(in.Actor, "session:")); err != nil {
			return current, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, strings.TrimPrefix(in.Actor, "session:")).Scan(&project); err != nil || project != in.ProjectID {
			return current, task.ErrNotFound
		}
	}
	if strings.HasPrefix(in.Actor, "operator:") {
		actor, ok := localoperator.FromContext(ctx)
		if !ok || actor.PrincipalID() != strings.TrimPrefix(in.Actor, "operator:") || actor.ProjectID() != in.ProjectID || actor.Check(in.ProjectID, in.TaskID, in.Action) != nil {
			return current, task.ErrNotFound
		}
	}
	if !strings.HasPrefix(in.Actor, "session:") && !strings.HasPrefix(in.Actor, "operator:") {
		return current, task.ErrNotFound
	}
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM task_hold_receipts WHERE actor=? AND request_id=?`, in.Actor, in.RequestID).Scan(&prior)
	if err == nil {
		if prior != hash {
			return current, task.ErrConflict
		}
		return current, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return current, err
	}
	if err = taskActionAdmissionTx(ctx, tx, current.ID); err != nil {
		return current, err
	}
	if current.FeatureAreaID != in.FeatureAreaID || current.CurrentExecutionID != in.ExecutionID || current.CurrentDefinitionRevisionID != in.RevisionID || in.ExecutionID == "" || in.RevisionID == "" || current.Status != task.StatusActive || current.LegacyReadOnly || !verificationChoicePhase(current) {
		return current, task.ErrConflict
	}
	if err = commitBusy(ctx, tx, current.FeatureAreaID); err != nil {
		return current, err
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND status NOT IN ('SUCCEEDED','FAILED','CANCELED','INFRASTRUCTURE_FAILED')) OR EXISTS(SELECT 1 FROM task_completion_intents WHERE task_id=?)`, current.ID, current.ID).Scan(&active); err != nil {
		return current, err
	}
	if active {
		return current, task.ErrConflict
	}
	now := time.Now().UTC()
	at := formatTime(now)
	if in.Action == "DEFER_TASK" {
		if strings.TrimSpace(in.Note) == "" {
			return current, errors.New("invalid task hold input")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_holds(task_id,execution_id,revision_id,note,held_at) VALUES(?,?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET note=excluded.note,held_at=excluded.held_at WHERE task_holds.execution_id=excluded.execution_id AND task_holds.revision_id=excluded.revision_id`, current.ID, in.ExecutionID, in.RevisionID, in.Note, at)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM task_holds WHERE task_id=? AND execution_id=? AND revision_id=?`, current.ID, in.ExecutionID, in.RevisionID)
	}
	if err != nil {
		return current, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_hold_receipts(actor,request_id,task_id,execution_id,revision_id,payload_sha256,action,created_at) VALUES(?,?,?,?,?,?,?,?)`, in.Actor, in.RequestID, current.ID, in.ExecutionID, in.RevisionID, hash, in.Action, at); err != nil {
		return current, err
	}
	eventPayload, _ := event.Payload(map[string]any{"action": in.Action, "execution_id": in.ExecutionID, "revision_id": in.RevisionID, "note": in.Note, "request_id": in.RequestID})
	if err = appendEvent(ctx, tx, event.Event{Type: "task.verification_choice", ProjectID: current.ProjectID, TaskID: current.ID, EntityType: "task", EntityID: current.ID, Payload: eventPayload, CreatedAt: now}); err != nil {
		return current, err
	}
	if err = tx.Commit(); err != nil {
		return current, err
	}
	return current, nil
}

func (s *WorkflowProgressionStore) ReadTaskContinuation(ctx context.Context, project, id string) (bool, error) {
	var confirmed bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN task_hold_receipts c ON c.task_id=t.id AND c.execution_id=t.current_execution_id AND c.revision_id=t.current_definition_revision_id WHERE t.id=? AND t.project_id=? AND t.status='ACTIVE' AND c.action='CONTINUE_TASK') AND NOT EXISTS(SELECT 1 FROM task_holds WHERE task_id=?)`, id, project, id).Scan(&confirmed)
	return confirmed, err
}
