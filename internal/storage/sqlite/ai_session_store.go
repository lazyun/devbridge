package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/project"
)

type AISessionStore struct{ db *DB }

func NewAISessionStore(db *DB) *AISessionStore { return &AISessionStore{db: db} }

// Legacy project-first creation is disabled at the persistence boundary.
func (s *AISessionStore) Create(ctx context.Context, value aisession.Session) error {
	return aisession.ErrInitializationRequired
}
func (s *AISessionStore) CreateFromSelection(ctx context.Context, selectionID, sessionID string, at time.Time) (aisession.Session, error) {
	return aisession.Session{}, aisession.ErrInitializationRequired
}

func (s *AISessionStore) Get(ctx context.Context, id string) (aisession.Session, error) {
	var value aisession.Session
	var created, updated string
	var projectID, callerScope sql.NullString
	var projectExists bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT id,project_id,created_at,updated_at,caller_scope,EXISTS(SELECT 1 FROM projects p WHERE p.id=ai_sessions.project_id) FROM ai_sessions WHERE id=?`, id).Scan(&value.ID, &projectID, &created, &updated, &callerScope, &projectExists)
	if errors.Is(err, sql.ErrNoRows) {
		return aisession.Session{}, aisession.ErrNotFound
	}
	if err != nil {
		return aisession.Session{}, fmt.Errorf("get AI session: %w", err)
	}
	if scope, ok := aisession.CallerScopeFromContext(ctx); ok && callerScope.Valid && scope != callerScope.String {
		return aisession.Session{}, aisession.ErrScopeMismatch
	}
	if !projectID.Valid {
		return aisession.Session{}, aisession.ErrProjectUnbound
	}
	if !projectExists {
		return aisession.Session{}, project.ErrNotFound
	}
	value.ProjectID = projectID.String
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return aisession.Session{}, err
	}
	value.UpdatedAt, err = parseTime(updated)
	return value, err
}

func (s *AISessionStore) List(ctx context.Context) ([]aisession.Session, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT id,project_id,created_at,updated_at FROM ai_sessions WHERE project_id IS NOT NULL AND EXISTS(SELECT 1 FROM projects p WHERE p.id=ai_sessions.project_id) ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, fmt.Errorf("list AI sessions: %w", err)
	}
	defer rows.Close()
	values := make([]aisession.Session, 0)
	for rows.Next() {
		var value aisession.Session
		var created, updated string
		if err := rows.Scan(&value.ID, &value.ProjectID, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan AI session: %w", err)
		}
		value.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		value.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list AI sessions: %w", err)
	}
	return values, nil
}

// GetOwned is the authenticated API read. Background control-plane work keeps
// its existing Get contract; HTTP/MCP Session entry points must prove ownership.
func (s *AISessionStore) GetOwned(ctx context.Context, id string) (aisession.Session, error) {
	if err := instructionCaller(ctx, s.db.sql, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return aisession.Session{}, aisession.ErrNotFound
		}
		return aisession.Session{}, err
	}
	return s.Get(ctx, id)
}
