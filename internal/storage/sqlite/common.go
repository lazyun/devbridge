package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/event"
)

const timestampLayout = time.RFC3339Nano

func formatTime(value time.Time) string         { return value.UTC().Format(timestampLayout) }
func parseTime(value string) (time.Time, error) { return time.Parse(timestampLayout, value) }

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func eventID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "event_" + hex.EncodeToString(raw[:]), nil
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendEvent(ctx context.Context, tx execer, value event.Event) error {
	id, err := eventID()
	if err != nil {
		return fmt.Errorf("allocate event id: %w", err)
	}
	payload := value.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{"version":1}`)
	}
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("validate event payload: %w", err)
	}
	if version, ok := envelope["version"].(float64); !ok || int(version) != event.PayloadVersion {
		return errors.New("event payload version 1 is required")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(id,type,project_id,task_id,run_id,entity_type,entity_id,payload_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, id, value.Type, nullString(value.ProjectID), nullString(value.TaskID), nullString(value.RunID), value.EntityType, value.EntityID, string(payload), formatTime(value.CreatedAt))
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func conflict(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	if stringContains(text, "FEATURE_AREA_BUSY") {
		return fmt.Errorf("%w: %v", agentconversation.ErrBusy, err)
	}
	if containsConstraint(text) {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

func containsConstraint(value string) bool {
	for _, marker := range []string{"constraint failed", "UNIQUE constraint", "FOREIGN KEY constraint"} {
		if len(value) >= len(marker) && stringContains(value, marker) {
			return true
		}
	}
	return false
}

func stringContains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
