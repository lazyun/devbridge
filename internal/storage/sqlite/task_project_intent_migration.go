package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Only unapproved planning intent may omit repository identity. Existing
// references and authorization triggers retain their exact legacy semantics.
func migrateTaskProjectIntent(ctx context.Context, tx *sql.Tx) error {
	var guard string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='tasks_current_insert'`).Scan(&guard); err != nil {
		return err
	}
	const old = " AND NEW.repository_id IS NOT NULL"
	if strings.Count(guard, old) != 1 {
		return errors.New("unexpected Task repository planning guard; migration blocked")
	}
	const replacement = " AND (NEW.repository_id IS NOT NULL OR (NEW.phase='REVIEW_REQUIRED' AND NEW.status='ACTIVE' AND NEW.base_commit='' AND NEW.review_approval_id IS NULL AND NEW.current_definition_revision_id IS NULL AND NEW.current_review_id IS NULL AND NEW.current_execution_id IS NULL AND NEW.work_spec_json='{}' AND NEW.verification_spec_json='{}'))"
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER tasks_current_insert`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, strings.Replace(guard, old, replacement, 1))
	return err
}
