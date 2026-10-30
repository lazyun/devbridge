package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/worktree"
)

type WorktreeStore struct{ db *DB }

func NewWorktreeStore(db *DB) *WorktreeStore { return &WorktreeStore{db: db} }
func (s *WorktreeStore) Create(ctx context.Context, value worktree.Worktree) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.db.checkRunTx(ctx, tx, value.RunID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO worktrees(id,repository_id,run_id,path,branch,base_commit,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, value.ID, value.RepositoryID, value.RunID, value.Path, value.Branch, value.BaseCommit, string(value.Status), formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return conflict(fmt.Errorf("insert worktree: %w", err))
	}
	var projectID, taskID string
	if err := tx.QueryRowContext(ctx, `SELECT r.project_id,COALESCE(u.task_id,'') FROM repositories r JOIN runs u ON u.id=? WHERE r.id=?`, value.RunID, value.RepositoryID).Scan(&projectID, &taskID); err != nil {
		return err
	}
	payload, _ := event.Payload(map[string]any{"path": value.Path, "status": value.Status})
	if err := appendEvent(ctx, tx, event.Event{Type: event.WorktreeCreated, ProjectID: projectID, TaskID: taskID, RunID: value.RunID, EntityType: "worktree", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}

type worktreeScanner interface{ Scan(...any) error }

func scanWorktree(scanner worktreeScanner) (worktree.Worktree, error) {
	var v worktree.Worktree
	var created, updated string
	err := scanner.Scan(&v.ID, &v.RepositoryID, &v.RunID, &v.Path, &v.Branch, &v.BaseCommit, &v.Status, &created, &updated, &v.RepositoryPath)
	if err != nil {
		return v, err
	}
	v.CreatedAt, err = parseTime(created)
	if err != nil {
		return v, err
	}
	v.UpdatedAt, err = parseTime(updated)
	return v, err
}

const selectWorktree = `SELECT id,repository_id,run_id,path,branch,base_commit,status,created_at,updated_at,(SELECT path FROM repositories WHERE id=worktrees.repository_id) FROM worktrees`

func (s *WorktreeStore) Get(ctx context.Context, id string) (worktree.Worktree, error) {
	v, err := scanWorktree(s.db.sql.QueryRowContext(ctx, selectWorktree+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, worktree.ErrNotFound
	}
	return v, err
}
func (s *WorktreeStore) List(ctx context.Context) ([]worktree.Worktree, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectWorktree+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []worktree.Worktree
	for rows.Next() {
		v, err := scanWorktree(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
func (s *WorktreeStore) Update(ctx context.Context, id string, update func(*worktree.Worktree) error) (worktree.Worktree, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return worktree.Worktree{}, err
	}
	defer tx.Rollback()
	v, err := scanWorktree(tx.QueryRowContext(ctx, selectWorktree+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, worktree.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	previous := v.Status
	previousUpdatedAt := v.UpdatedAt
	if err := update(&v); err != nil {
		return v, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE worktrees SET status=?,updated_at=? WHERE id=? AND status=? AND updated_at=?`, string(v.Status), formatTime(v.UpdatedAt), id, string(previous), formatTime(previousUpdatedAt))
	if err != nil {
		return v, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return v, err
	}
	if affected != 1 {
		return v, ErrConflict
	}
	if previous != v.Status {
		var projectID, taskID string
		if err := tx.QueryRowContext(ctx, `SELECT u.project_id,COALESCE(u.task_id,'') FROM runs u WHERE u.id=?`, v.RunID).Scan(&projectID, &taskID); err != nil {
			return v, err
		}
		payload, _ := event.Payload(map[string]any{"from": previous, "to": v.Status})
		if err := appendEvent(ctx, tx, event.Event{Type: event.WorktreeStatusChanged, ProjectID: projectID, TaskID: taskID, RunID: v.RunID, EntityType: "worktree", EntityID: v.ID, Payload: payload, CreatedAt: v.UpdatedAt}); err != nil {
			return v, err
		}
	}
	if err := tx.Commit(); err != nil {
		return v, err
	}
	return v, nil
}
