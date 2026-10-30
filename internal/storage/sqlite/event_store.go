package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/event"
)

type EventStore struct{ db *DB }

func NewEventStore(db *DB) *EventStore { return &EventStore{db: db} }

func (s *EventStore) RecordThreadNameFailure(ctx context.Context, taskID, threadID, message string, now time.Time) error {
	if len(message) > 2048 {
		message = message[:2048]
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id=?`, taskID).Scan(&projectID); err != nil {
		return fmt.Errorf("resolve thread naming diagnostic task: %w", err)
	}
	payload, err := event.Payload(map[string]any{"thread_id": threadID, "message": message})
	if err != nil {
		return err
	}
	if err = appendEvent(ctx, tx, event.Event{Type: event.CodexThreadNameFailed, ProjectID: projectID, TaskID: taskID, EntityType: "codex_thread", EntityID: threadID, Payload: payload, CreatedAt: now}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *EventStore) List(ctx context.Context, filter event.Filter) ([]event.Event, error) {
	query := `SELECT id,sequence,type,COALESCE(project_id,''),COALESCE(task_id,''),COALESCE(run_id,''),entity_type,entity_id,payload_json,created_at FROM events`
	var where []string
	var args []any
	if filter.ProjectID != "" {
		where = append(where, "project_id=?")
		args = append(args, filter.ProjectID)
	}
	if filter.TaskID != "" {
		where = append(where, "task_id=?")
		args = append(args, filter.TaskID)
	}
	if filter.RunID != "" {
		where = append(where, "run_id=?")
		args = append(args, filter.RunID)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY sequence"
	rows, err := s.db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var values []event.Event
	for rows.Next() {
		var v event.Event
		var payload, created string
		if err := rows.Scan(&v.ID, &v.Sequence, &v.Type, &v.ProjectID, &v.TaskID, &v.RunID, &v.EntityType, &v.EntityID, &payload, &created); err != nil {
			return nil, err
		}
		v.Payload = []byte(payload)
		v.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
