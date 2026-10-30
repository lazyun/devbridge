package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func migrateDirectFileVerificationV128(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE direct_file_verification_selections (
 task_id TEXT NOT NULL REFERENCES tasks(id),revision_id TEXT PRIMARY KEY REFERENCES task_definition_revisions(id),ai_session_id TEXT NOT NULL,intent_id TEXT NOT NULL,runtime_config_json TEXT NOT NULL CHECK(json_valid(runtime_config_json)),backend_selection_json TEXT NOT NULL CHECK(json_valid(backend_selection_json)),created_at TEXT NOT NULL,
 FOREIGN KEY(intent_id) REFERENCES confirmation_intents(intent_id)
);
CREATE TRIGGER direct_file_verification_selection_no_update BEFORE UPDATE ON direct_file_verification_selections BEGIN SELECT RAISE(ABORT,'Codex verification selection is immutable'); END;
CREATE TRIGGER direct_file_verification_selection_no_delete BEFORE DELETE ON direct_file_verification_selections BEGIN SELECT RAISE(ABORT,'Codex verification selection is durable'); END;
CREATE TRIGGER direct_file_verification_selection_source BEFORE INSERT ON direct_file_verification_selections
WHEN NOT EXISTS(SELECT 1 FROM confirmation_intents i JOIN tasks t ON t.id=NEW.task_id WHERE i.ai_session_id=NEW.ai_session_id AND i.intent_id=NEW.intent_id AND json_extract(i.intent_json,'$.state')='CONFIRMING' AND json_extract(i.intent_json,'$.action')='START_DIRECT_FILE' AND json_extract(i.intent_json,'$.content.execution.taskId')=NEW.task_id AND json_extract(i.intent_json,'$.content.execution.taskDefinitionRevisionId')=NEW.revision_id AND t.current_definition_revision_id=NEW.revision_id AND t.status='ACTIVE' AND json_extract(t.verification_spec_json,'$.mode')='RUN' AND json_extract(i.intent_json,'$.commit.selection.runtimeConfig')=json(NEW.runtime_config_json) AND json_extract(i.intent_json,'$.commit.backendSelection')=json(NEW.backend_selection_json))
BEGIN SELECT RAISE(ABORT,'exact consumed Codex verification selection required'); END;
CREATE VIEW direct_file_verification_runtime_sources AS
 SELECT task_id,revision_id,runtime_config_json,backend_selection_json FROM direct_file_verification_selections
 UNION ALL SELECT a.task_id,a.initial_revision_id,a.runtime_config_json,a.backend_selection_json FROM review_approval_materializations a JOIN task_definition_revisions d ON d.id=a.initial_revision_id WHERE json_extract(d.payload_json,'$.version')<>2;
CREATE TABLE direct_file_verifications (
 result_id TEXT PRIMARY KEY REFERENCES direct_file_implementation_results(id),
 execution_id TEXT NOT NULL UNIQUE REFERENCES execution_proposals(id) DEFERRABLE INITIALLY DEFERRED,
 task_id TEXT NOT NULL REFERENCES tasks(id), revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id), created_at TEXT NOT NULL
);
CREATE TRIGGER direct_file_verifications_no_update BEFORE UPDATE ON direct_file_verifications BEGIN SELECT RAISE(ABORT,'file verification source is immutable'); END;
CREATE TRIGGER direct_file_verifications_no_delete BEFORE DELETE ON direct_file_verifications BEGIN SELECT RAISE(ABORT,'file verification source is durable'); END;
CREATE TRIGGER direct_file_verifications_source BEFORE INSERT ON direct_file_verifications
WHEN NOT EXISTS(SELECT 1 FROM direct_file_implementation_results r JOIN direct_file_task_authorizations g ON g.workflow_id=r.workflow_id JOIN tasks t ON t.id=r.task_id JOIN direct_file_verification_runtime_sources a ON a.task_id=t.id AND a.revision_id=t.current_definition_revision_id
WHERE r.id=NEW.result_id AND r.task_id=NEW.task_id AND r.revision_id=NEW.revision_id AND t.current_definition_revision_id=r.revision_id AND t.status='ACTIVE' AND t.phase IN ('IMPLEMENTATION_READY','IMPLEMENTING') AND t.current_execution_id IS NULL AND g.state='CLOSED' AND g.task_definition_revision_id=r.revision_id AND g.decision_id=t.review_approval_id AND json_extract(t.work_spec_json,'$.implementationMode')='DIRECT_FILE' AND json_extract(t.verification_spec_json,'$.mode')='RUN' AND a.runtime_config_json IS NOT NULL AND a.backend_selection_json IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'file verification requires exact closed result and approved Codex selection'); END;
CREATE TRIGGER direct_file_verification_execution BEFORE INSERT ON execution_proposals
WHEN json_extract(NEW.work_spec_json,'$.implementationMode')='DIRECT_FILE' AND NOT EXISTS(
 SELECT 1 FROM direct_file_verifications v JOIN tasks t ON t.id=v.task_id JOIN direct_file_verification_runtime_sources a ON a.task_id=t.id AND a.revision_id=v.revision_id
 WHERE v.execution_id=NEW.id AND v.task_id=NEW.task_id AND v.revision_id=NEW.task_definition_revision_id AND NEW.status='CONFIRMED' AND NEW.outcome='PENDING' AND NEW.run_id IS NULL AND NEW.verification_run_id IS NULL AND NEW.work_spec_json=t.work_spec_json AND NEW.verification_spec_json=t.verification_spec_json AND NEW.runtime_config_json=a.runtime_config_json AND NEW.backend_selection_json=a.backend_selection_json)
BEGIN SELECT RAISE(ABORT,'Direct File Execution is verification only and requires exact source'); END;
CREATE TRIGGER direct_file_verification_run_purpose BEFORE INSERT ON runs
WHEN EXISTS(SELECT 1 FROM direct_file_verifications WHERE execution_id=NEW.execution_proposal_id) AND NEW.purpose<>'VERIFICATION'
BEGIN SELECT RAISE(ABORT,'file verification cannot create implementation Run'); END;
`)
	if err != nil {
		return err
	}
	// Preserve every existing authorization branch; add only the exact immutable
	// Direct File source to the verification-only reservation guard.
	for _, change := range []struct{ name, old, new string }{
		{"execution_proposals_verification_run_guard", "EXISTS(SELECT 1 FROM decision_resolution_receipts a WHERE a.decision_kind='TASK_ACCEPTANCE' AND a.subject_kind='EXECUTION_PROPOSAL' AND a.option='REQUEST_REVERIFICATION' AND a.result_kind='EXECUTION_PROPOSAL' AND a.result_id=OLD.id)", "(EXISTS(SELECT 1 FROM decision_resolution_receipts a WHERE a.decision_kind='TASK_ACCEPTANCE' AND a.subject_kind='EXECUTION_PROPOSAL' AND a.option='REQUEST_REVERIFICATION' AND a.result_kind='EXECUTION_PROPOSAL' AND a.result_id=OLD.id) OR EXISTS(SELECT 1 FROM direct_file_verifications v WHERE v.execution_id=OLD.id AND v.task_id=OLD.task_id AND v.revision_id=OLD.task_definition_revision_id))"},
		{"file_result_task_accept_guard", "json_extract(OLD.verification_spec_json,'$.mode')='NONE'", "(json_extract(OLD.verification_spec_json,'$.mode')='NONE' OR EXISTS(SELECT 1 FROM direct_file_verifications v JOIN execution_proposals e ON e.id=v.execution_id JOIN runs run ON run.id=e.verification_run_id AND run.execution_proposal_id=e.id AND run.purpose='VERIFICATION' WHERE v.result_id=r.id AND v.task_id=OLD.id AND v.revision_id=OLD.current_definition_revision_id AND e.id=OLD.current_execution_id AND e.outcome='SUCCEEDED' AND run.status='SUCCEEDED' AND run.verification_result_json IS NOT NULL AND COALESCE(run.failure_code,'')<>'VERIFICATION_MUTATED_WORKTREE'))"},
	} {
		var source string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, change.name).Scan(&source); err != nil {
			return err
		}
		if strings.Count(source, change.old) != 1 {
			return fmt.Errorf("v128 trigger %s source mismatch", change.name)
		}
		if _, err = tx.ExecContext(ctx, `DROP TRIGGER `+change.name); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, strings.Replace(source, change.old, change.new, 1)); err != nil {
			return err
		}
	}
	return nil
}
