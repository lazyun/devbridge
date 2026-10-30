package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Keep the v89 initial-link table as the immutable FK anchor for existing
// receipts. The append-only history is the authority for every new link; the
// selectors are separate mutable Core records, never mutations of that history.
func migrateRecoveryPurposeV107(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, recoveryPurposeV107SQL+confirmationReplacementV107SQL); err != nil {
		return err
	}
	// Existing Task guards must recognize a replacement's exact link, too.
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND sql IS NOT NULL AND sql LIKE '%recovery_purpose_confirmation_links%' AND tbl_name<>'recovery_purpose_confirmation_links'`)
	if err != nil {
		return err
	}
	type trigger struct{ name, sql string }
	var triggers []trigger
	for rows.Next() {
		var t trigger
		if err = rows.Scan(&t.name, &t.sql); err != nil {
			rows.Close()
			return err
		}
		triggers = append(triggers, t)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, t := range triggers {
		if _, err = tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(t.name, `"`, `""`)+`"`); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, strings.ReplaceAll(t.sql, "recovery_purpose_confirmation_links", "recovery_purpose_link_history")); err != nil {
			return fmt.Errorf("update Recovery link guard %s: %w", t.name, err)
		}
	}
	for _, table := range []string{"recovery_purpose_confirmation_receipts", "recovery_purpose_confirmation_consumptions"} {
		name := "recovery_purpose_receipt_immutable"
		if strings.HasSuffix(table, "consumptions") {
			name = "recovery_purpose_consumption_immutable"
		}
		var ddl string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DROP TRIGGER `+name); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN purpose_link_id TEXT NOT NULL DEFAULT ''; ALTER TABLE `+table+` ADD COLUMN purpose_link_ordinal INTEGER NOT NULL DEFAULT 1; UPDATE `+table+` SET purpose_link_id=(SELECT link_id FROM recovery_purpose_link_history h WHERE h.intent_ai_session_id=`+table+`.intent_ai_session_id AND h.intent_id=`+table+`.intent_id AND h.intent_version=`+table+`.intent_version AND h.intent_content_hash=`+table+`.intent_content_hash)`); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

const recoveryPurposeV107SQL = `
ALTER TABLE recovery_status_records ADD COLUMN submitted_payload_hash TEXT NOT NULL DEFAULT '';
UPDATE recovery_status_records SET submitted_payload_hash=COALESCE(json_extract(spec_json,'$.payloadHash'),'');
CREATE TABLE recovery_current_generations (
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL,
 recovery_id TEXT NOT NULL UNIQUE REFERENCES recovery_status_records(id),
 generation INTEGER NOT NULL CHECK(generation>0), selection_version INTEGER NOT NULL CHECK(selection_version>0),
 PRIMARY KEY(owner_kind,owner_id)
);
INSERT INTO recovery_current_generations(owner_kind,owner_id,recovery_id,generation,selection_version)
SELECT owner_kind,owner_id,id,generation,1 FROM (
 SELECT CASE r.purpose WHEN 'SCOPE_REVIEW' THEN 'REVIEW' WHEN 'INSPECTION' THEN 'PROJECT_INSPECTION' ELSE r.purpose END owner_kind,
 r.operation_id owner_id,x.id,x.generation,
 ROW_NUMBER() OVER(PARTITION BY r.purpose,r.operation_id ORDER BY x.generation DESC,x.rowid DESC) ordinal
 FROM recovery_status_records x JOIN runs r ON r.id=x.source_run_id
) WHERE ordinal=1;
CREATE TABLE recovery_purpose_link_history (
 link_id TEXT PRIMARY KEY, link_ordinal INTEGER NOT NULL CHECK(link_ordinal>0), predecessor_link_id TEXT NOT NULL DEFAULT '',
 recovery_id TEXT NOT NULL REFERENCES recovery_status_records(id), recovery_generation INTEGER NOT NULL CHECK(recovery_generation>0),
 intent_ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id), intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),
 intent_version INTEGER NOT NULL CHECK(intent_version>0),intent_content_hash TEXT NOT NULL CHECK(length(intent_content_hash)=64),
 proposal_hash TEXT NOT NULL,source_owner_kind TEXT NOT NULL,source_owner_id TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(id),feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 source_execution_id TEXT NOT NULL DEFAULT '',source_review_id TEXT NOT NULL DEFAULT '',task_definition_revision_id TEXT NOT NULL DEFAULT '',
 source_operation_id TEXT NOT NULL,source_run_id TEXT NOT NULL REFERENCES runs(id),source_turn_id TEXT NOT NULL,
 runtime_epoch TEXT NOT NULL,runtime_workset_hash TEXT NOT NULL,assessment_hash TEXT NOT NULL,created_at TEXT NOT NULL,
 UNIQUE(recovery_id,recovery_generation,link_ordinal), UNIQUE(intent_ai_session_id,intent_id,intent_version,intent_content_hash),
 CHECK((source_owner_kind='REVIEW' AND source_review_id=source_owner_id AND source_execution_id='') OR
 (source_owner_kind IN ('IMPLEMENTATION','VERIFICATION') AND source_execution_id=source_owner_id AND source_review_id=''))
);
INSERT INTO recovery_purpose_link_history SELECT 'purpose-link:'||intent_id,1,'',l.* FROM recovery_purpose_confirmation_links l;
CREATE TABLE recovery_purpose_current_links (
 recovery_id TEXT NOT NULL REFERENCES recovery_status_records(id), recovery_generation INTEGER NOT NULL,
 link_id TEXT NOT NULL UNIQUE REFERENCES recovery_purpose_link_history(link_id),link_ordinal INTEGER NOT NULL CHECK(link_ordinal>0),
 selection_version INTEGER NOT NULL CHECK(selection_version>0),
 PRIMARY KEY(recovery_id,recovery_generation)
);
INSERT INTO recovery_purpose_current_links SELECT recovery_id,recovery_generation,link_id,link_ordinal,1 FROM recovery_purpose_link_history;
CREATE TRIGGER recovery_purpose_history_immutable BEFORE UPDATE ON recovery_purpose_link_history BEGIN SELECT RAISE(ABORT,'Recovery purpose link history immutable'); END;
CREATE TRIGGER recovery_purpose_history_no_delete BEFORE DELETE ON recovery_purpose_link_history BEGIN SELECT RAISE(ABORT,'Recovery purpose link history immutable'); END;
`
