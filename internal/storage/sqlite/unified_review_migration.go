package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func migrateUnifiedReviewV48(ctx context.Context, tx *sql.Tx) error {
	if err := widenReviewDispositionV48(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
ALTER TABLE reviews ADD COLUMN executor_kind TEXT NOT NULL DEFAULT 'CODING_AGENT' CHECK(executor_kind IN ('CODING_AGENT','UPPER_LLM'));
ALTER TABLE reviews ADD COLUMN executor_metadata_json TEXT CHECK(executor_metadata_json IS NULL OR json_valid(executor_metadata_json));
CREATE TRIGGER review_executor_immutable BEFORE UPDATE OF executor_kind,executor_metadata_json,model,reasoning_effort ON reviews BEGIN SELECT RAISE(ABORT,'Review executor is immutable'); END;
CREATE TRIGGER review_executor_config BEFORE INSERT ON reviews WHEN NEW.executor_kind='UPPER_LLM' AND (NEW.codex_config_id IS NOT NULL OR NEW.model IS NOT NULL OR NEW.reasoning_effort IS NOT NULL) BEGIN SELECT RAISE(ABORT,'Upper LLM cannot carry Coding Agent configuration'); END;
DROP TRIGGER review_grants_immutable;
CREATE TRIGGER review_grants_immutable BEFORE UPDATE OF review_id,feature_area_id,project_id,repository_id,mode,initial_scope_kind,initial_scope_paths_json,selected_modules_json,created_at ON review_grants BEGIN SELECT RAISE(ABORT,'Review grant scope is immutable'); END;
ALTER TABLE review_grants ADD COLUMN run_id TEXT REFERENCES runs(id);
ALTER TABLE review_grants ADD COLUMN runtime_epoch TEXT;
ALTER TABLE review_grants ADD COLUMN generation INTEGER NOT NULL DEFAULT 1 CHECK(generation>0);
ALTER TABLE review_grants ADD COLUMN state TEXT NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('PREPARING','ACTIVE','AWAITING_REPORT','FINALIZING','CLOSED','CANCELED','RECOVERY_REQUIRED'));
ALTER TABLE review_grants ADD COLUMN workspace_id TEXT REFERENCES workspaces(id);
ALTER TABLE review_grants ADD COLUMN worktree_id TEXT REFERENCES worktrees(id);
ALTER TABLE review_grants ADD COLUMN baseline TEXT;
ALTER TABLE review_grants ADD COLUMN forbidden_paths_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(forbidden_paths_json));
ALTER TABLE review_grants ADD COLUMN last_observed_at TEXT;
ALTER TABLE review_grants ADD COLUMN preparation_error TEXT;
CREATE TRIGGER review_grant_lifecycle_owner BEFORE UPDATE ON review_grants
WHEN NOT EXISTS(SELECT 1 FROM reviews v WHERE v.id=NEW.review_id AND v.executor_kind='UPPER_LLM')
BEGIN SELECT RAISE(ABORT,'Coding Agent initial Review grant is immutable'); END;
ALTER TABLE review_access_evidence ADD COLUMN run_id TEXT REFERENCES runs(id);
ALTER TABLE review_access_evidence ADD COLUMN repository_id TEXT;
ALTER TABLE review_access_evidence ADD COLUMN workspace_id TEXT;
ALTER TABLE review_access_evidence ADD COLUMN worktree_id TEXT;
ALTER TABLE review_access_evidence ADD COLUMN baseline TEXT;
ALTER TABLE review_access_evidence ADD COLUMN runtime_epoch TEXT;
ALTER TABLE review_access_evidence ADD COLUMN grant_generation INTEGER;
ALTER TABLE review_access_evidence ADD COLUMN path TEXT;
ALTER TABLE review_access_evidence ADD COLUMN digest TEXT;
ALTER TABLE review_access_evidence ADD COLUMN byte_count INTEGER;
ALTER TABLE review_access_evidence ADD COLUMN line_count INTEGER;
CREATE TABLE review_reports (
 id TEXT PRIMARY KEY, review_id TEXT NOT NULL UNIQUE REFERENCES reviews(id), run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id), baseline TEXT NOT NULL, payload_sha256 TEXT NOT NULL,
 report_json TEXT NOT NULL CHECK(json_valid(report_json) AND length(CAST(report_json AS BLOB))<=262144), created_at TEXT NOT NULL
);
CREATE TRIGGER review_report_immutable BEFORE UPDATE ON review_reports BEGIN SELECT RAISE(ABORT,'Review report is immutable'); END;
CREATE TRIGGER review_report_durable BEFORE DELETE ON review_reports BEGIN SELECT RAISE(ABORT,'Review report is durable'); END;
CREATE TABLE review_result_receipts (
 review_id TEXT NOT NULL REFERENCES reviews(id), request_id TEXT NOT NULL, ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 payload_sha256 TEXT NOT NULL, payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 state TEXT NOT NULL CHECK(state IN ('FINALIZING','COMPLETED')), receipt_json TEXT CHECK(receipt_json IS NULL OR json_valid(receipt_json)), created_at TEXT NOT NULL,
 PRIMARY KEY(review_id,request_id), UNIQUE(review_id)
);
`)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TRIGGER review_result_receipt_immutable BEFORE UPDATE ON review_result_receipts
 WHEN OLD.state='COMPLETED' OR NEW.receipt_json IS NULL OR json_type(NEW.receipt_json)<>'object' OR json_extract(NEW.receipt_json,'$.status') IS NOT 'COMPLETED' OR json_extract(NEW.receipt_json,'$.reviewId') IS NOT OLD.review_id OR json_extract(NEW.receipt_json,'$.requestId') IS NOT OLD.request_id OR json_extract(NEW.receipt_json,'$.payloadSha256') IS NOT OLD.payload_sha256 OR NOT EXISTS(SELECT 1 FROM review_reports WHERE id=json_extract(NEW.receipt_json,'$.reportId') AND review_id=OLD.review_id AND run_id=json_extract(NEW.receipt_json,'$.runId')) OR NEW.review_id IS NOT OLD.review_id OR NEW.request_id IS NOT OLD.request_id OR NEW.ai_session_id IS NOT OLD.ai_session_id OR NEW.payload_sha256 IS NOT OLD.payload_sha256 OR NEW.payload_json IS NOT OLD.payload_json OR NEW.created_at IS NOT OLD.created_at OR NEW.state<>'COMPLETED'
 BEGIN SELECT RAISE(ABORT,'Review result receipt is immutable'); END;
 CREATE TRIGGER review_result_receipt_durable BEFORE DELETE ON review_result_receipts BEGIN SELECT RAISE(ABORT,'Review result receipt is durable'); END;`); err != nil {
		return err
	}
	var guard string
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='reviews_current_owner'`).Scan(&guard); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DROP TRIGGER reviews_current_owner`); err != nil {
		return err
	}
	guard = strings.Replace(guard, "NEW.codex_config_id IS NULL", "(NEW.executor_kind='CODING_AGENT' AND NEW.codex_config_id IS NULL)", 1)
	_, err = tx.ExecContext(ctx, guard)
	if err != nil {
		return err
	}
	return extendUpperRecoveryV48(ctx, tx)
}
func widenReviewDispositionV48(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA legacy_alter_table=OFF`)

	tableSQL := make(map[string]string, 2)
	for _, table := range []string{"reviews"} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return err
		}
		old := "review_disposition IN ('APPROVAL_READY','FURTHER_REVIEW_REQUIRED')"
		updated := "review_disposition IN ('APPROVAL_READY','FURTHER_REVIEW_REQUIRED','BLOCKED')"
		if !strings.Contains(ddl, old) {
			return fmt.Errorf("%s disposition constraint is not the v47 contract", table)
		}
		tableSQL[table] = strings.Replace(ddl, old, updated, 1)
	}

	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name IN ('reviews') AND sql IS NOT NULL)`)
	if err != nil {
		return err
	}
	var retained []schemaObject
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.sql); err != nil {
			rows.Close()
			return err
		}
		retained = append(retained, object)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, object := range retained {
		if _, err := tx.ExecContext(ctx, "DROP "+strings.ToUpper(object.kind)+" "+sqlIdentifier(object.name)); err != nil {
			return err
		}
	}
	for _, table := range []string{"reviews"} {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+sqlIdentifier(table)+" RENAME TO "+sqlIdentifier("_source47_"+table)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, tableSQL[table]); err != nil {
			return err
		}
		columns, err := schemaColumns(ctx, tx, "_source47_"+table)
		if err != nil {
			return err
		}
		for i := range columns {
			columns[i] = sqlIdentifier(columns[i])
		}
		shared := strings.Join(columns, ",")
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+sqlIdentifier(table)+"("+shared+") SELECT "+shared+" FROM "+sqlIdentifier("_source47_"+table)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DROP TABLE "+sqlIdentifier("_source47_"+table)); err != nil {
			return err
		}
	}
	for _, object := range retained {
		if _, err := tx.ExecContext(ctx, object.sql); err != nil {
			return err
		}
	}
	return nil
}

func extendUpperRecoveryV48(ctx context.Context, tx *sql.Tx) error {
	// Existing recovery receipts remain byte-for-byte durable; an upper recovery
	// has no old/new Coding Agent conversation and can occur once per epoch.
	_, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON;
DROP TRIGGER agent_operation_recovery_immutable;
DROP TRIGGER agent_operation_recovery_no_delete;
ALTER TABLE agent_operation_recoveries RENAME TO _upper_recovery_source;
CREATE TABLE agent_operation_recoveries (
 request_id TEXT NOT NULL,feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),run_id TEXT NOT NULL REFERENCES runs(id),operation_kind TEXT NOT NULL,operation_id TEXT NOT NULL,
 old_conversation_id TEXT REFERENCES agent_conversations(id),new_conversation_id TEXT REFERENCES agent_conversations(id),authorized_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),created_at TEXT NOT NULL,
 previous_epoch TEXT REFERENCES runtime_epochs(id),grant_generation INTEGER,
 PRIMARY KEY(feature_area_id,request_id),UNIQUE(run_id,authorized_epoch)
);
INSERT INTO agent_operation_recoveries(request_id,feature_area_id,run_id,operation_kind,operation_id,old_conversation_id,new_conversation_id,authorized_epoch,created_at)
 SELECT request_id,feature_area_id,run_id,operation_kind,operation_id,old_conversation_id,new_conversation_id,authorized_epoch,created_at FROM _upper_recovery_source;
DROP TABLE _upper_recovery_source;
CREATE TRIGGER agent_operation_recovery_immutable BEFORE UPDATE ON agent_operation_recoveries BEGIN SELECT RAISE(ABORT,'Agent recovery is immutable'); END;
CREATE TRIGGER agent_operation_recovery_no_delete BEFORE DELETE ON agent_operation_recoveries BEGIN SELECT RAISE(ABORT,'Agent recovery is durable'); END;
DROP TRIGGER runs_runtime_epoch_immutable;
CREATE TRIGGER runs_runtime_epoch_immutable BEFORE UPDATE OF runtime_epoch ON runs
WHEN NEW.runtime_epoch IS NOT OLD.runtime_epoch AND NOT EXISTS(
 SELECT 1 FROM reviews v JOIN review_grants g ON g.review_id=v.id JOIN agent_operation_recoveries a ON a.run_id=OLD.id
 WHERE v.id=OLD.review_id AND v.executor_kind='UPPER_LLM' AND g.run_id=OLD.id AND g.state='RECOVERY_REQUIRED'
 AND a.operation_kind='REVIEW' AND a.operation_id=v.id AND a.feature_area_id=v.feature_area_id
 AND a.old_conversation_id IS NULL AND a.new_conversation_id IS NULL AND a.previous_epoch=OLD.runtime_epoch AND a.authorized_epoch=NEW.runtime_epoch
 AND a.grant_generation=g.generation+1 AND NEW.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND OLD.thread_id IS NULL AND OLD.turn_id IS NULL AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=OLD.id))
BEGIN SELECT RAISE(ABORT,'Run runtime epoch is immutable'); END;
DROP TRIGGER claims_runtime_epoch_immutable;
CREATE TRIGGER claims_runtime_epoch_immutable BEFORE UPDATE OF runtime_epoch ON feature_area_agent_claims
WHEN NEW.runtime_epoch IS NOT OLD.runtime_epoch AND NOT EXISTS(
 SELECT 1 FROM reviews v JOIN review_grants g ON g.review_id=v.id JOIN agent_operation_recoveries a ON a.run_id=OLD.run_id JOIN runs r ON r.id=OLD.run_id
 WHERE v.id=OLD.operation_id AND OLD.operation_kind='REVIEW' AND v.executor_kind='UPPER_LLM' AND g.run_id=r.id AND g.state='RECOVERY_REQUIRED'
 AND a.operation_kind='REVIEW' AND a.operation_id=v.id AND a.feature_area_id=OLD.feature_area_id
 AND a.old_conversation_id IS NULL AND a.new_conversation_id IS NULL AND a.previous_epoch=OLD.runtime_epoch AND a.authorized_epoch=NEW.runtime_epoch
 AND a.grant_generation=g.generation+1 AND NEW.runtime_epoch=r.runtime_epoch AND NEW.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND r.thread_id IS NULL AND r.turn_id IS NULL AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id))
BEGIN SELECT RAISE(ABORT,'claim runtime epoch is immutable'); END;
CREATE TRIGGER upper_review_grant_binding BEFORE UPDATE ON review_grants
WHEN (OLD.run_id IS NOT NULL AND NEW.run_id IS NOT OLD.run_id)
 OR (OLD.workspace_id IS NOT NULL AND NEW.workspace_id IS NOT OLD.workspace_id)
 OR (OLD.worktree_id IS NOT NULL AND NEW.worktree_id IS NOT OLD.worktree_id)
 OR (OLD.baseline IS NOT NULL AND NEW.baseline IS NOT OLD.baseline)
 OR NEW.forbidden_paths_json IS NOT OLD.forbidden_paths_json
 OR (OLD.runtime_epoch IS NOT NULL AND (NEW.runtime_epoch IS NOT OLD.runtime_epoch OR NEW.generation<>OLD.generation)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_recoveries a WHERE a.run_id=OLD.run_id AND a.operation_id=OLD.review_id AND a.previous_epoch=OLD.runtime_epoch AND a.authorized_epoch=NEW.runtime_epoch AND a.grant_generation=NEW.generation AND NEW.generation=OLD.generation+1 AND OLD.state='RECOVERY_REQUIRED'))
 OR (OLD.state IN ('CLOSED','CANCELED') AND NEW.state IS NOT OLD.state)
BEGIN SELECT RAISE(ABORT,'Upper Review grant binding is frozen'); END;


CREATE TRIGGER upper_review_snapshot_immutable BEFORE UPDATE OF start_snapshot ON runs
WHEN OLD.start_snapshot IS NOT NULL AND NEW.start_snapshot IS NOT OLD.start_snapshot AND EXISTS(SELECT 1 FROM reviews WHERE id=OLD.review_id AND executor_kind='UPPER_LLM')
BEGIN SELECT RAISE(ABORT,'Upper Review start snapshot is immutable'); END;
CREATE TRIGGER upper_review_grant_transition BEFORE UPDATE OF state ON review_grants
WHEN NEW.state<>OLD.state AND NOT (
 (OLD.run_id IS NULL AND OLD.state='ACTIVE' AND NEW.state IN ('PREPARING','AWAITING_REPORT')) OR
 (OLD.state='PREPARING' AND NEW.state IN ('ACTIVE','CANCELED','RECOVERY_REQUIRED')) OR
 (OLD.state='ACTIVE' AND NEW.state IN ('AWAITING_REPORT','FINALIZING','CANCELED','RECOVERY_REQUIRED')) OR
 (OLD.state='AWAITING_REPORT' AND NEW.state IN ('FINALIZING','CANCELED','RECOVERY_REQUIRED')) OR
 (OLD.state='FINALIZING' AND NEW.state='CLOSED' AND EXISTS(SELECT 1 FROM review_reports WHERE review_id=OLD.review_id AND run_id=OLD.run_id)) OR
 (OLD.state='RECOVERY_REQUIRED' AND NEW.state='PREPARING' AND NEW.generation=OLD.generation+1))
BEGIN SELECT RAISE(ABORT,'Invalid Upper Review access transition'); END;
PRAGMA legacy_alter_table=OFF;`)
	return err
}
