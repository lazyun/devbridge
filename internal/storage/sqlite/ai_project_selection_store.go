package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
)

type AIProjectSelectionStore struct{ db *DB }

func NewAIProjectSelectionStore(db *DB) *AIProjectSelectionStore {
	return &AIProjectSelectionStore{db: db}
}

func (s *AIProjectSelectionStore) Create(ctx context.Context, value aiprojectselection.Selection) error {
	_, err := s.db.execWrite(ctx, `INSERT INTO ai_project_selections(id,project_id,status,created_at) VALUES(?,?,?,?)`, value.ID, value.ProjectID, value.Status, formatTime(value.CreatedAt))
	if err != nil {
		if errors.Is(conflict(err), ErrConflict) {
			return aiprojectselection.ErrConflict
		}
		return fmt.Errorf("insert AI Project selection: %w", err)
	}
	return nil
}

func (s *AIProjectSelectionStore) Get(ctx context.Context, id string) (aiprojectselection.Selection, error) {
	return scanAIProjectSelection(s.db.sql.QueryRowContext(ctx, `SELECT id,project_id,status,ai_session_id,created_at,consumed_at FROM ai_project_selections WHERE id=?`, id))
}

type rowScanner interface{ Scan(...any) error }

func scanAIProjectSelection(scanner rowScanner) (aiprojectselection.Selection, error) {
	var value aiprojectselection.Selection
	var aiSessionID, consumedAt sql.NullString
	var createdAt string
	err := scanner.Scan(&value.ID, &value.ProjectID, &value.Status, &aiSessionID, &createdAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return aiprojectselection.Selection{}, aiprojectselection.ErrNotFound
	}
	if err != nil {
		return aiprojectselection.Selection{}, fmt.Errorf("scan AI Project selection: %w", err)
	}
	value.AISessionID = aiSessionID.String
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return aiprojectselection.Selection{}, err
	}
	if consumedAt.Valid {
		parsed, parseErr := parseTime(consumedAt.String)
		if parseErr != nil {
			return aiprojectselection.Selection{}, parseErr
		}
		value.ConsumedAt = &parsed
	}
	return value, nil
}
