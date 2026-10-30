package sqlite

// Preserve the historical Session foreign key as evidence but leave the new
// union source NULL for historical rows. In particular, no old decision is
// relabeled as a local operator.
const reviewDecisionActorSourceMigrationSQL = `
DROP TRIGGER review_receipt_context_immutable;
DROP TRIGGER review_receipt_context_no_delete;
ALTER TABLE review_approval_receipt_context RENAME TO _source62_review_receipt_context;
CREATE TABLE review_approval_receipt_context (
 decision_id TEXT PRIMARY KEY REFERENCES decision_resolution_receipts(decision_id),
 ai_session_id TEXT REFERENCES ai_sessions(id),
 actor_kind TEXT,
 actor_id TEXT,
 created_at TEXT NOT NULL,
 CHECK((actor_kind IS NULL) = (actor_id IS NULL))
);
INSERT INTO review_approval_receipt_context(decision_id,ai_session_id,created_at)
 SELECT decision_id,ai_session_id,created_at FROM _source62_review_receipt_context;
DROP TABLE _source62_review_receipt_context;
CREATE TRIGGER review_receipt_context_immutable BEFORE UPDATE ON review_approval_receipt_context BEGIN SELECT RAISE(ABORT,'Review receipt context is immutable'); END;
CREATE TRIGGER review_receipt_context_no_delete BEFORE DELETE ON review_approval_receipt_context BEGIN SELECT RAISE(ABORT,'Review receipt context is durable'); END;
`
