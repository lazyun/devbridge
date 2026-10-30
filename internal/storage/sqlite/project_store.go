package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/project"
)

type ProjectStore struct{ db *DB }

func NewProjectStore(db *DB) *ProjectStore { return &ProjectStore{db: db} }

func (s *ProjectStore) Create(ctx context.Context, value project.Project) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return fmt.Errorf("begin project creation: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO projects(id,name,created_at,updated_at) VALUES(?,?,?,?)`, value.ID, value.Name, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return conflict(fmt.Errorf("insert project: %w", err))
	}
	payload, _ := event.Payload(map[string]any{"name": value.Name})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ProjectCreated, ProjectID: value.ID, EntityType: "project", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ProjectStore) Get(ctx context.Context, id string) (project.Project, error) {
	var value project.Project
	var created, updated string
	err := s.db.sql.QueryRowContext(ctx, `SELECT id,name,created_at,updated_at FROM projects WHERE id=?`, id).Scan(&value.ID, &value.Name, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return project.Project{}, project.ErrNotFound
	}
	if err != nil {
		return project.Project{}, fmt.Errorf("get project: %w", err)
	}
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return project.Project{}, err
	}
	value.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return project.Project{}, err
	}
	return value, nil
}

func (s *ProjectStore) List(ctx context.Context) ([]project.Project, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT id,name,created_at,updated_at FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var values []project.Project
	for rows.Next() {
		var value project.Project
		var created, updated string
		if err := rows.Scan(&value.ID, &value.Name, &created, &updated); err != nil {
			return nil, err
		}
		value.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, fmt.Errorf("parse project created_at: %w", err)
		}
		value.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return nil, fmt.Errorf("parse project updated_at: %w", err)
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, rows.Err()
}
