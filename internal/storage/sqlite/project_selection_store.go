package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/projectselection"
)

type ProjectSelectionStore struct{ db *DB }

func NewProjectSelectionStore(db *DB) *ProjectSelectionStore {
	return &ProjectSelectionStore{db: db}
}

func (s *ProjectSelectionStore) Get(ctx context.Context) (projectselection.Selection, error) {
	var value projectselection.Selection
	var updated string
	err := s.db.sql.QueryRowContext(ctx, `SELECT project_id,updated_at FROM project_selection WHERE singleton=1`).Scan(&value.CurrentSelectedProjectID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return projectselection.Selection{}, projectselection.ErrNotSelected
	}
	if err != nil {
		return projectselection.Selection{}, fmt.Errorf("get project selection: %w", err)
	}
	value.UpdatedAt, err = parseTime(updated)
	return value, err
}

func (s *ProjectSelectionStore) Set(ctx context.Context, value projectselection.Selection) error {
	_, err := s.db.execWrite(ctx, `INSERT INTO project_selection(singleton,project_id,updated_at) VALUES(1,?,?) ON CONFLICT(singleton) DO UPDATE SET project_id=excluded.project_id,updated_at=excluded.updated_at`, value.CurrentSelectedProjectID, formatTime(value.UpdatedAt))
	if err != nil {
		return conflict(fmt.Errorf("set project selection: %w", err))
	}
	return nil
}
