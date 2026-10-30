package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/task"
)

func (s *TaskStore) CreateIntent(ctx context.Context, value task.Task, session, request, hash string) (task.Task, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback()
	var project string
	if err = tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, session).Scan(&project); err != nil {
		return task.Task{}, err
	}
	if project != value.ProjectID {
		return task.Task{}, ErrConflict
	}
	var id, storedHash string
	err = tx.QueryRowContext(ctx, `SELECT task_id,payload_sha256 FROM task_create_requests WHERE project_id=? AND request_id=?`, project, request).Scan(&id, &storedHash)
	if err == nil {
		if storedHash != hash {
			return task.Task{}, ErrConflict
		}
		return scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, id))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, err
	}
	skipReview := value.Phase == task.PhaseImplementationReady && value.RequiredReviewDepth == nil
	initial := value
	if skipReview {
		initial.Phase = task.PhaseReviewRequired
	}
	if err = insertTask(ctx, tx, initial); err != nil {
		return task.Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_create_requests(project_id,request_id,ai_session_id,payload_sha256,task_id,created_at) VALUES(?,?,?,?,?,?)`, project, request, session, hash, value.ID, formatTime(value.CreatedAt)); err != nil {
		return task.Task{}, err
	}

	if skipReview {
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_review_skips(task_id,project_id,ai_session_id,request_id,payload_sha256,created_at) VALUES(?,?,?,?,?,?)`, value.ID, project, session, request, hash, formatTime(value.CreatedAt)); err != nil {
			return task.Task{}, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='IMPLEMENTATION_READY',required_review_depth=NULL WHERE id=?`, value.ID); err != nil {
			return task.Task{}, err
		}
	}
	value, err = scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, value.ID))
	if err != nil {
		return value, err
	}
	return value, tx.Commit()
}

func (s *TaskStore) GetIntent(ctx context.Context, project, request, hash string) (task.Task, error) {
	var id, storedHash string
	err := s.db.sql.QueryRowContext(ctx, `SELECT task_id,payload_sha256 FROM task_create_requests WHERE project_id=? AND request_id=?`, project, request).Scan(&id, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, task.ErrNotFound
	}
	if err != nil {
		return task.Task{}, err
	}
	if hash != storedHash {
		return task.Task{}, ErrConflict
	}
	return s.Get(ctx, id)
}
