package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/event"
)

type DocumentStore struct{ db *DB }

func NewDocumentStore(db *DB) *DocumentStore { return &DocumentStore{db: db} }

func (s *DocumentStore) Create(ctx context.Context, value document.Document) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO documents(id,project_id,feature_area_id,created_by_ai_session_id,title,content,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, value.ID, value.ProjectID, nullString(value.FeatureAreaID), value.CreatedByAISessionID, value.Title, value.Content, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		if errors.Is(conflict(err), ErrConflict) {
			return document.ErrConflict
		}
		return fmt.Errorf("insert document: %w", err)
	}
	payload, _ := event.Payload(map[string]any{"feature_area_id": value.FeatureAreaID, "title_bytes": len([]byte(value.Title)), "content_bytes": len([]byte(value.Content))})
	if err := appendEvent(ctx, tx, event.Event{Type: event.DocumentCreated, ProjectID: value.ProjectID, EntityType: "document", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}

type documentScanner interface{ Scan(...any) error }

func scanDocument(scanner documentScanner) (document.Document, error) {
	var value document.Document
	var featureAreaID sql.NullString
	var created, updated string
	if err := scanner.Scan(&value.ID, &value.ProjectID, &featureAreaID, &value.CreatedByAISessionID, &value.Title, &value.Content, &created, &updated); err != nil {
		return value, err
	}
	value.FeatureAreaID = featureAreaID.String
	var err error
	if value.CreatedAt, err = parseTime(created); err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updated)
	return value, err
}

func (s *DocumentStore) Get(ctx context.Context, id string) (document.Document, error) {
	value, err := scanDocument(s.db.sql.QueryRowContext(ctx, selectDocument+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, document.ErrNotFound
	}
	if err != nil {
		return value, fmt.Errorf("get document: %w", err)
	}
	return value, nil
}

func (s *DocumentStore) ListByProject(ctx context.Context, projectID string, limit int) ([]document.Document, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectDocument+` WHERE project_id=? ORDER BY created_at DESC,id LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()
	values := make([]document.Document, 0)
	for rows.Next() {
		value, scanErr := scanDocument(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

const selectDocument = `SELECT id,project_id,feature_area_id,created_by_ai_session_id,title,content,created_at,updated_at FROM documents`
