package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const recoveryPurposeMigrationV89SQL = `
CREATE TABLE recovery_purpose_confirmation_links (
 recovery_id TEXT NOT NULL REFERENCES recovery_status_records(id),
 recovery_generation INTEGER NOT NULL CHECK(recovery_generation>0),
 intent_ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),
 intent_version INTEGER NOT NULL CHECK(intent_version>0),
 intent_content_hash TEXT NOT NULL CHECK(length(intent_content_hash)=64),
 proposal_hash TEXT NOT NULL CHECK(length(proposal_hash)=64),
 source_owner_kind TEXT NOT NULL CHECK(source_owner_kind IN ('IMPLEMENTATION','VERIFICATION','REVIEW')),
 source_owner_id TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 source_execution_id TEXT NOT NULL DEFAULT '',
 source_review_id TEXT NOT NULL DEFAULT '',
 task_definition_revision_id TEXT NOT NULL DEFAULT '',
 source_operation_id TEXT NOT NULL,
 source_run_id TEXT NOT NULL REFERENCES runs(id),
 source_turn_id TEXT NOT NULL,
 runtime_epoch TEXT NOT NULL,
 runtime_workset_hash TEXT NOT NULL,
 assessment_hash TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(recovery_id,recovery_generation),
 UNIQUE(intent_ai_session_id,intent_id,intent_version,intent_content_hash),
 CHECK((source_owner_kind='REVIEW' AND source_review_id=source_owner_id AND source_execution_id='') OR
       (source_owner_kind IN ('IMPLEMENTATION','VERIFICATION') AND source_execution_id=source_owner_id AND source_review_id=''))
);
CREATE TABLE recovery_purpose_confirmation_receipts (
 intent_ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),
 intent_version INTEGER NOT NULL,
 intent_content_hash TEXT NOT NULL,
 commit_request_id TEXT NOT NULL,
 selected_variant_id TEXT NOT NULL,
 recovery_id TEXT NOT NULL,
 recovery_generation INTEGER NOT NULL,
 source_owner_kind TEXT NOT NULL,
 source_owner_id TEXT NOT NULL,
 proposal_hash TEXT NOT NULL,
 submitted_hash TEXT NOT NULL,
 outcome TEXT NOT NULL CHECK(outcome IN ('COMMITTED','REJECTED_NOT_CONSUMED')),
 consumption_disposition TEXT NOT NULL CHECK(consumption_disposition IN ('UNCONSUMED','INELIGIBLE')),
 committed_at TEXT NOT NULL,
 PRIMARY KEY(intent_ai_session_id,intent_id,intent_version,intent_content_hash),
 UNIQUE(intent_ai_session_id,commit_request_id),
 FOREIGN KEY(recovery_id,recovery_generation) REFERENCES recovery_purpose_confirmation_links(recovery_id,recovery_generation)
);
CREATE TABLE recovery_purpose_rejection_receipts (
 intent_ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),
 intent_version INTEGER NOT NULL,
 intent_content_hash TEXT NOT NULL,
 commit_request_id TEXT NOT NULL,
 submitted_hash TEXT NOT NULL,
 recovery_id TEXT NOT NULL,
 recovery_generation INTEGER NOT NULL,
 source_owner_kind TEXT NOT NULL CHECK(source_owner_kind IN ('IMPLEMENTATION','VERIFICATION','REVIEW')),
 failure_code TEXT NOT NULL,
 outcome TEXT NOT NULL CHECK(outcome='REJECTED_NOT_CONSUMED'),
 rejected_at TEXT NOT NULL,
 PRIMARY KEY(intent_ai_session_id,intent_id,intent_version,intent_content_hash,commit_request_id),
 UNIQUE(intent_ai_session_id,commit_request_id),
 FOREIGN KEY(recovery_id,recovery_generation) REFERENCES recovery_purpose_confirmation_links(recovery_id,recovery_generation)
);
CREATE TABLE recovery_purpose_confirmation_consumptions (
 intent_ai_session_id TEXT NOT NULL,
 intent_id TEXT NOT NULL,
 intent_version INTEGER NOT NULL,
 intent_content_hash TEXT NOT NULL,
 consume_action TEXT NOT NULL CHECK(consume_action IN ('APPLY_CONFIRMED_IMPLEMENTATION_PURPOSE','APPLY_CONFIRMED_VERIFICATION_PURPOSE','APPLY_CONFIRMED_REVIEW_PURPOSE')),
 consume_request_id TEXT NOT NULL,
 consume_payload_hash TEXT NOT NULL CHECK(length(consume_payload_hash)=64),
 source_owner_kind TEXT NOT NULL CHECK(source_owner_kind IN ('IMPLEMENTATION','VERIFICATION','REVIEW')),
 selected_variant_id TEXT NOT NULL,
 proposal_hash TEXT NOT NULL,
 successor_task_definition_revision_id TEXT NOT NULL DEFAULT '',
 successor_execution_id TEXT NOT NULL DEFAULT '',
 successor_review_id TEXT NOT NULL DEFAULT '',
 successor_run_id TEXT NOT NULL DEFAULT '',
 consumed_at TEXT NOT NULL,
 PRIMARY KEY(intent_ai_session_id,intent_id,intent_version,intent_content_hash),
 UNIQUE(intent_ai_session_id,consume_request_id),
 FOREIGN KEY(intent_ai_session_id,intent_id,intent_version,intent_content_hash)
   REFERENCES recovery_purpose_confirmation_receipts(intent_ai_session_id,intent_id,intent_version,intent_content_hash),
 CHECK((source_owner_kind='REVIEW' AND consume_action='APPLY_CONFIRMED_REVIEW_PURPOSE' AND successor_review_id<>'' AND successor_run_id<>'' AND successor_execution_id='' AND successor_task_definition_revision_id='') OR
       (source_owner_kind='IMPLEMENTATION' AND consume_action='APPLY_CONFIRMED_IMPLEMENTATION_PURPOSE' AND successor_task_definition_revision_id<>'' AND successor_execution_id<>'' AND successor_review_id='' AND successor_run_id='') OR
       (source_owner_kind='VERIFICATION' AND consume_action='APPLY_CONFIRMED_VERIFICATION_PURPOSE' AND successor_task_definition_revision_id<>'' AND successor_execution_id<>'' AND successor_review_id='' AND successor_run_id=''))
);
CREATE TABLE recovery_purpose_repair_receipts (
 recovery_id TEXT NOT NULL,
 recovery_generation INTEGER NOT NULL,
 repair_request_id TEXT NOT NULL,
 repair_payload_hash TEXT NOT NULL CHECK(length(repair_payload_hash)=64),
 intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),
 repaired_at TEXT NOT NULL,
 PRIMARY KEY(recovery_id,recovery_generation),
 FOREIGN KEY(recovery_id,recovery_generation) REFERENCES recovery_purpose_confirmation_links(recovery_id,recovery_generation)
);
CREATE TRIGGER recovery_purpose_link_immutable BEFORE UPDATE ON recovery_purpose_confirmation_links BEGIN SELECT RAISE(ABORT,'Recovery purpose link immutable'); END;
CREATE TRIGGER recovery_purpose_link_no_delete BEFORE DELETE ON recovery_purpose_confirmation_links BEGIN SELECT RAISE(ABORT,'Recovery purpose link immutable'); END;
CREATE TRIGGER recovery_purpose_receipt_immutable BEFORE UPDATE ON recovery_purpose_confirmation_receipts BEGIN SELECT RAISE(ABORT,'Recovery purpose receipt immutable'); END;
CREATE TRIGGER recovery_purpose_receipt_no_delete BEFORE DELETE ON recovery_purpose_confirmation_receipts BEGIN SELECT RAISE(ABORT,'Recovery purpose receipt immutable'); END;
CREATE TRIGGER recovery_purpose_rejection_immutable BEFORE UPDATE ON recovery_purpose_rejection_receipts BEGIN SELECT RAISE(ABORT,'Recovery purpose rejection immutable'); END;
CREATE TRIGGER recovery_purpose_rejection_no_delete BEFORE DELETE ON recovery_purpose_rejection_receipts BEGIN SELECT RAISE(ABORT,'Recovery purpose rejection immutable'); END;
CREATE TRIGGER recovery_purpose_consumption_immutable BEFORE UPDATE ON recovery_purpose_confirmation_consumptions BEGIN SELECT RAISE(ABORT,'Recovery purpose consumption immutable'); END;
CREATE TRIGGER recovery_purpose_consumption_no_delete BEFORE DELETE ON recovery_purpose_confirmation_consumptions BEGIN SELECT RAISE(ABORT,'Recovery purpose consumption immutable'); END;
CREATE TRIGGER recovery_purpose_repair_immutable BEFORE UPDATE ON recovery_purpose_repair_receipts BEGIN SELECT RAISE(ABORT,'Recovery purpose repair immutable'); END;
CREATE TRIGGER recovery_purpose_repair_no_delete BEFORE DELETE ON recovery_purpose_repair_receipts BEGIN SELECT RAISE(ABORT,'Recovery purpose repair immutable'); END;
`

// Existing approved-Task guards remain strict. Their only new exception is an
// exact, already inserted purpose consumption in the same transaction. A
// failed consumer rolls back both the exception fact and every successor.
func migrateRecoveryPurposeV89(ctx context.Context, tx *sql.Tx) error {
	guards := map[string]string{
		"approved_task_revision_immutable": ` AND NOT EXISTS(
 SELECT 1 FROM recovery_purpose_confirmation_consumptions c JOIN recovery_purpose_confirmation_links l
 ON l.intent_ai_session_id=c.intent_ai_session_id AND l.intent_id=c.intent_id AND l.intent_version=c.intent_version AND l.intent_content_hash=c.intent_content_hash
 WHERE l.task_id=OLD.id AND l.task_id=NEW.id AND l.task_definition_revision_id=OLD.current_definition_revision_id
 AND l.source_execution_id=OLD.current_execution_id AND c.successor_task_definition_revision_id=NEW.current_definition_revision_id
 AND c.source_owner_kind IN ('IMPLEMENTATION','VERIFICATION') AND NEW.phase='IMPLEMENTATION_READY')`,
		"approved_task_semantics_immutable": ` AND NOT (OLD.phase='RECOVERY_REQUIRED' AND NEW.phase='IMPLEMENTATION_READY' AND EXISTS(
 SELECT 1 FROM recovery_purpose_confirmation_consumptions c JOIN recovery_purpose_confirmation_links l
 ON l.intent_ai_session_id=c.intent_ai_session_id AND l.intent_id=c.intent_id AND l.intent_version=c.intent_version AND l.intent_content_hash=c.intent_content_hash
 WHERE l.task_id=OLD.id AND l.task_id=NEW.id AND l.task_definition_revision_id=OLD.current_definition_revision_id
 AND l.source_execution_id=OLD.current_execution_id AND c.successor_execution_id<>''
 AND c.source_owner_kind IN ('IMPLEMENTATION','VERIFICATION')))`,
		"approved_execution_binding": ` AND NOT EXISTS(
 SELECT 1 FROM recovery_purpose_confirmation_consumptions c JOIN recovery_purpose_confirmation_links l
 ON l.intent_ai_session_id=c.intent_ai_session_id AND l.intent_id=c.intent_id AND l.intent_version=c.intent_version AND l.intent_content_hash=c.intent_content_hash
 WHERE l.task_id=NEW.task_id AND c.successor_execution_id=NEW.id AND c.successor_task_definition_revision_id=NEW.task_definition_revision_id
 AND c.source_owner_kind IN ('IMPLEMENTATION','VERIFICATION'))`,
		"task_current_execution_guard": ` AND NOT EXISTS(
 SELECT 1 FROM recovery_purpose_confirmation_consumptions c JOIN recovery_purpose_confirmation_links l
 ON l.intent_ai_session_id=c.intent_ai_session_id AND l.intent_id=c.intent_id AND l.intent_version=c.intent_version AND l.intent_content_hash=c.intent_content_hash
 WHERE l.task_id=OLD.id AND l.task_id=NEW.id AND l.source_execution_id=OLD.current_execution_id
 AND c.successor_execution_id=NEW.current_execution_id AND c.successor_task_definition_revision_id=NEW.current_definition_revision_id
 AND c.source_owner_kind IN ('IMPLEMENTATION','VERIFICATION') AND OLD.phase='IMPLEMENTATION_READY' AND NEW.phase='IMPLEMENTATION_READY')`,
	}
	for name, guard := range guards {
		var source string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&source); err != nil {
			return err
		}
		at := strings.LastIndex(strings.ToUpper(source), "BEGIN")
		if at < 0 || !strings.Contains(strings.ToUpper(source[:at]), "WHEN") {
			return fmt.Errorf("%s has no expected WHEN clause", name)
		}
		updated := source[:at] + guard + "\n" + source[at:]
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, updated); err != nil {
			return fmt.Errorf("extend %s: %w", name, err)
		}
	}
	return nil
}
