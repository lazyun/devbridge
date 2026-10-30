package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// The column already permits NULL and retains its FK for explicit configs.
// Only the old Task-creation guard wrongly equated intent with Coding authority.
// Preserve the complete installed trigger and change this one known predicate.
func migrateTaskOptionalConfigV59(ctx context.Context, tx *sql.Tx) error {
	var guard string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='tasks_current_insert'`).Scan(&guard); err != nil {
		return err
	}
	const previous = " AND NEW.codex_config_id IS NOT NULL"
	if strings.Count(guard, previous) != 1 {
		return errors.New("Task creation guard does not match the expected optional-config migration contract")
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER tasks_current_insert`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, strings.Replace(guard, previous, "", 1))
	return err
}
