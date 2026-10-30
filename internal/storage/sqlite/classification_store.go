package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/classification"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/task"
)

type ClassificationStore struct{ db *DB }

func NewClassificationStore(db *DB) *ClassificationStore { return &ClassificationStore{db: db} }

func (s *ClassificationStore) Classify(ctx context.Context, value classification.Classification) (task.Task, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback()
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, value.TaskID))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, classification.ErrNotFound
	}
	if err != nil {
		return task.Task{}, err
	}
	if current.ProjectID != value.ProjectID || current.ModuleID != value.FromModuleID {
		return task.Task{}, classification.ErrConflict
	}
	var sourceSystem, targetSystem bool
	var sourceKey, targetProject string
	if err := tx.QueryRowContext(ctx, `SELECT system_owned,key FROM modules WHERE id=? AND project_id=?`, value.FromModuleID, value.ProjectID).Scan(&sourceSystem, &sourceKey); err != nil {
		return task.Task{}, classification.ErrConflict
	}
	if err := tx.QueryRowContext(ctx, `SELECT system_owned,project_id FROM modules WHERE id=?`, value.ToModuleID).Scan(&targetSystem, &targetProject); err != nil {
		return task.Task{}, classification.ErrConflict
	}
	if !sourceSystem || sourceKey != "_unclassified" || targetSystem || targetProject != value.ProjectID {
		return task.Task{}, classification.ErrConflict
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE task_id=? AND status IN ('PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING')`, value.TaskID).Scan(&active); err != nil {
		return task.Task{}, err
	}
	if active != 0 {
		return task.Task{}, classification.ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET module_id=?,updated_at=? WHERE id=? AND project_id=? AND module_id=? AND updated_at=?`, value.ToModuleID, formatTime(value.CreatedAt), value.TaskID, value.ProjectID, value.FromModuleID, formatTime(current.UpdatedAt))
	if err != nil {
		return task.Task{}, conflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return task.Task{}, classification.ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_classifications(id,ai_session_id,project_id,task_id,from_module_id,to_module_id,created_at) VALUES(?,?,?,?,?,?,?)`, value.ID, value.AISessionID, value.ProjectID, value.TaskID, value.FromModuleID, value.ToModuleID, formatTime(value.CreatedAt))
	if err != nil {
		return task.Task{}, conflict(fmt.Errorf("record task classification: %w", err))
	}
	payload, _ := event.Payload(map[string]any{"ai_session_id": value.AISessionID, "from_module_id": value.FromModuleID, "to_module_id": value.ToModuleID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.TaskClassified, ProjectID: value.ProjectID, TaskID: value.TaskID, EntityType: "task", EntityID: value.TaskID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return task.Task{}, err
	}
	current.ModuleID, current.UpdatedAt = value.ToModuleID, value.CreatedAt
	if _, err := ensureTaskDefinitionTx(ctx, tx, &current, value.CreatedAt); err != nil {
		return task.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return task.Task{}, err
	}
	return s.GetTask(ctx, value.TaskID)
}

func (s *ClassificationStore) GetTask(ctx context.Context, id string) (task.Task, error) {
	value, err := scanTask(s.db.sql.QueryRowContext(ctx, selectTask+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, task.ErrNotFound
	}
	return value, err
}
