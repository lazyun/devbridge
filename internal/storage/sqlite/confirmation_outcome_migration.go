package sqlite

import (
	"context"
	"database/sql"
)

// Nullable historical evidence is deliberately not backfilled from derived content.
func migrateConfirmationOutcomeV58(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
ALTER TABLE confirmation_intents ADD COLUMN submitted_sha256 TEXT;
ALTER TABLE confirmation_commit_requests ADD COLUMN submitted_sha256 TEXT;
ALTER TABLE confirmation_commit_requests ADD COLUMN request_json TEXT CHECK(request_json IS NULL OR json_valid(request_json));
ALTER TABLE confirmation_commit_requests ADD COLUMN outcome TEXT CHECK(outcome IS NULL OR outcome IN ('CONSUMED','REJECTED_NOT_CONSUMED'));
ALTER TABLE confirmation_commit_requests ADD COLUMN failure_json TEXT CHECK(failure_json IS NULL OR json_valid(failure_json));
CREATE TRIGGER confirmation_submitted_identity_immutable BEFORE UPDATE OF submitted_sha256 ON confirmation_intents
WHEN NEW.submitted_sha256 IS NOT OLD.submitted_sha256
BEGIN SELECT RAISE(ABORT,'confirmation original input is immutable'); END;
CREATE TRIGGER confirmation_outcome_complete BEFORE INSERT ON confirmation_commit_requests
WHEN NEW.submitted_sha256 IS NULL OR length(NEW.submitted_sha256)<>64 OR NEW.request_json IS NULL OR NEW.outcome IS NULL
 OR (NEW.outcome='REJECTED_NOT_CONSUMED' AND NEW.failure_json IS NULL)
 OR (NEW.outcome='CONSUMED' AND NEW.failure_json IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'confirmation request outcome evidence required'); END;
`)
	return err
}
