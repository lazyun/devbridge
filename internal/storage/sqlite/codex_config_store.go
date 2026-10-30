package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/event"
)

type CodexConfigStore struct{ db *DB }

func NewCodexConfigStore(db *DB) *CodexConfigStore { return &CodexConfigStore{db: db} }

func (s *CodexConfigStore) Create(ctx context.Context, value codexconfig.Config) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_configs(id,name,codex_path,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?)`, value.ID, value.Name, value.CodexPath, boolInt(value.Enabled), formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return conflict(fmt.Errorf("insert codex config: %w", err))
	}
	payload, _ := event.Payload(map[string]any{"name": value.Name, "enabled": value.Enabled})
	if err := appendEvent(ctx, tx, event.Event{Type: event.CodexConfigCreated, EntityType: "codex_config", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}

type codexConfigScanner interface{ Scan(...any) error }

func scanCodexConfig(scanner codexConfigScanner) (codexconfig.Config, error) {
	var value codexconfig.Config
	var enabled int
	var created, updated string
	if err := scanner.Scan(&value.ID, &value.Name, &value.CodexPath, &enabled, &created, &updated); err != nil {
		return codexconfig.Config{}, err
	}
	value.Enabled = enabled != 0
	var err error
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return codexconfig.Config{}, err
	}
	value.UpdatedAt, err = parseTime(updated)
	return value, err
}

const selectCodexConfig = `SELECT id,name,codex_path,enabled,created_at,updated_at FROM codex_configs`

func (s *CodexConfigStore) Get(ctx context.Context, id string) (codexconfig.Config, error) {
	value, err := scanCodexConfig(s.db.sql.QueryRowContext(ctx, selectCodexConfig+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return codexconfig.Config{}, codexconfig.ErrNotFound
	}
	if err != nil {
		return codexconfig.Config{}, fmt.Errorf("get codex config: %w", err)
	}
	return value, nil
}

func (s *CodexConfigStore) List(ctx context.Context) ([]codexconfig.Config, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectCodexConfig+` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list codex configs: %w", err)
	}
	defer rows.Close()
	var values []codexconfig.Config
	for rows.Next() {
		value, err := scanCodexConfig(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
