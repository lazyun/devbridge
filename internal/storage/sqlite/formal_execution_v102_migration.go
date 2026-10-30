package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// V102 adds ordered approval commits without rewriting historical receipts.
func migrateFormalExecutionV102(ctx context.Context, tx *sql.Tx) error {
	// A T2 admission intentionally precedes preparation authority. Its original
	// plan identity stays immutable; an exact T2 commit replaces that one FK.
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='execution_proposal_admissions'`).Scan(&ddl); err != nil {
		return err
	}
	old := "runtime_preparation_authority_id TEXT NOT NULL UNIQUE REFERENCES runtime_preparation_authorities(plan_id)"
	if !strings.Contains(ddl, old) {
		return fmt.Errorf("formal execution admission schema changed")
	}
	rebuilt := strings.Replace(ddl, old, "runtime_preparation_authority_id TEXT NOT NULL UNIQUE", 1)
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA legacy_alter_table=OFF`)
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name='execution_proposal_admissions' AND sql IS NOT NULL) ORDER BY type,name`)
	if err != nil {
		return err
	}
	var objects []schemaObject
	for rows.Next() {
		var o schemaObject
		if err = rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range objects {
		if _, err = tx.ExecContext(ctx, "DROP "+strings.ToUpper(o.kind)+" "+sqlIdentifier(o.name)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `ALTER TABLE execution_proposal_admissions RENAME TO _formal102_admissions`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, rebuilt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO execution_proposal_admissions SELECT * FROM _formal102_admissions`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE _formal102_admissions`); err != nil {
		return err
	}
	for _, o := range objects {
		if _, err = tx.ExecContext(ctx, o.sql); err != nil {
			return fmt.Errorf("restore %s: %w", o.name, err)
		}
	}
	if _, err = tx.ExecContext(ctx, formalExecutionSchema); err != nil {
		return err
	}
	formalAdmission := `NEW.runtime_scope_plan_id=NEW.runtime_preparation_authority_id AND EXISTS(SELECT 1 FROM formal_execution_t2 f JOIN formal_execution_t1 b ON b.ai_session_id=f.ai_session_id AND b.request_id=f.request_id JOIN execution_proposals e ON e.id=f.execution_id JOIN runtime_scope_plans p ON p.plan_id=f.plan_id JOIN tasks t ON t.id=b.task_id WHERE f.execution_id=NEW.proposal_id AND f.plan_id=NEW.runtime_scope_plan_id AND f.admission_request_id=NEW.admission_request_id AND f.admission_payload_hash=NEW.payload_sha256 AND b.task_id=NEW.task_id AND b.revision_id=NEW.task_definition_revision_id AND b.decision_id=NEW.source_decision_id AND t.current_execution_id=e.id AND t.current_definition_revision_id=b.revision_id AND t.phase='IMPLEMENTATION_PREPARING' AND t.status='ACTIVE' AND e.status='CONFIRMED' AND e.run_id IS NULL AND e.task_id=b.task_id AND e.task_definition_revision_id=b.revision_id AND p.owner_id=e.id AND p.task_id=b.task_id AND p.task_definition_revision_id=b.revision_id AND p.content_hash=f.plan_hash AND p.preparation_request_id=f.admission_request_id AND p.preparation_payload_hash=f.admission_payload_hash)`
	if err = addNoRunRevisionTriggerException(ctx, tx, "execution_proposal_admissions_owner", formalAdmission); err != nil {
		return err
	}
	admitted := `EXISTS(SELECT 1 FROM formal_execution_t2 f JOIN formal_execution_t1 b ON b.ai_session_id=f.ai_session_id AND b.request_id=f.request_id JOIN execution_proposal_admissions a ON a.proposal_id=f.execution_id AND a.runtime_scope_plan_id=f.plan_id JOIN tasks t ON t.id=b.task_id WHERE f.execution_id=NEW.id AND b.task_id=NEW.task_id AND b.revision_id=NEW.task_definition_revision_id AND t.current_execution_id=NEW.id AND t.current_definition_revision_id=b.revision_id AND t.phase='IMPLEMENTATION_PREPARING' AND t.status='ACTIVE' AND NEW.run_id IS NULL)`
	if err = addNoRunRevisionTriggerException(ctx, tx, "execution_proposals_admitted_receipt", admitted); err != nil {
		return err
	}
	// Retained canceled predecessors can be replaced only by this exact new T1.
	candidate := `NEW.status='CONFIRMED' AND NEW.outcome='PENDING' AND NEW.run_id IS NULL AND NEW.verification_run_id IS NULL AND EXISTS(SELECT 1 FROM formal_execution_t1 b JOIN tasks t ON t.id=b.task_id JOIN task_plans p ON p.id=NEW.task_plan_id AND p.task_id=t.id AND p.project_id=t.project_id AND p.status='APPLIED' WHERE b.task_id=NEW.task_id AND b.project_id=NEW.project_id AND b.revision_id=NEW.task_definition_revision_id AND t.current_definition_revision_id=b.revision_id AND t.review_approval_id=b.decision_id AND t.current_review_id=b.review_id AND t.current_execution_id IS json_extract(b.archive_json,'$.core.currentExecutionId') AND t.status='ACTIVE' AND t.phase='IMPLEMENTATION_PREPARING' AND t.confirmed_task_plan_id=p.id AND NEW.work_spec_json=t.work_spec_json AND NEW.verification_spec_json=t.verification_spec_json AND NOT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.task_id=t.id AND e.task_definition_revision_id=b.revision_id))`
	// NULL pointers are represented as an empty string in typed archives.
	candidate = strings.ReplaceAll(candidate, "t.current_execution_id IS json_extract", "COALESCE(t.current_execution_id,'') IS json_extract")
	if err = addNoRunRevisionTriggerException(ctx, tx, "approved_execution_binding", candidate); err != nil {
		return err
	}
	columns, err := schemaColumns(ctx, tx, "tasks")
	if err != nil {
		return err
	}
	var same []string
	for _, column := range columns {
		if column != "current_execution_id" {
			same = append(same, "NEW."+sqlIdentifier(column)+" IS OLD."+sqlIdentifier(column))
		}
	}
	pointer := strings.Join(same, " AND ") + ` AND OLD.status='ACTIVE' AND OLD.phase='IMPLEMENTATION_PREPARING' AND EXISTS(SELECT 1 FROM formal_execution_t1 b JOIN execution_proposals e ON e.id=NEW.current_execution_id WHERE b.task_id=OLD.id AND b.project_id=OLD.project_id AND b.revision_id=OLD.current_definition_revision_id AND b.decision_id=OLD.review_approval_id AND COALESCE(OLD.current_execution_id,'') IS json_extract(b.archive_json,'$.core.currentExecutionId') AND e.task_id=b.task_id AND e.task_definition_revision_id=b.revision_id AND e.status='CONFIRMED' AND e.outcome='PENDING' AND e.run_id IS NULL AND e.verification_run_id IS NULL AND e.task_plan_id=OLD.confirmed_task_plan_id)`
	if err = addNoRunRevisionTriggerException(ctx, tx, "task_current_execution_guard", pointer); err != nil {
		return err
	}
	for _, table := range []string{"formal_execution_fences", "formal_execution_t1", "formal_execution_t2", "formal_execution_receipts"} {
		for _, op := range []string{"UPDATE", "DELETE"} {
			if _, err = tx.ExecContext(ctx, "CREATE TRIGGER "+table+"_no_"+strings.ToLower(op)+" BEFORE "+op+" ON "+table+" BEGIN SELECT RAISE(ABORT,'formal execution immutable'); END"); err != nil {
				return err
			}
		}
	}
	return nil
}

const formalExecutionSchema = `
CREATE TABLE formal_execution_fences (
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),intent_id TEXT NOT NULL UNIQUE REFERENCES confirmation_intents(intent_id),intent_version INTEGER NOT NULL CHECK(intent_version>0),project_id TEXT NOT NULL REFERENCES projects(id),task_id TEXT NOT NULL REFERENCES tasks(id),review_id TEXT NOT NULL REFERENCES reviews(id),decision_id TEXT NOT NULL,parent_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),content_hash TEXT NOT NULL CHECK(length(content_hash)=64),fence_json TEXT NOT NULL CHECK(json_valid(fence_json)),fence_sha256 TEXT NOT NULL CHECK(length(fence_sha256)=64),PRIMARY KEY(ai_session_id,intent_id));
CREATE TABLE formal_execution_t1 (
 ai_session_id TEXT NOT NULL,request_id TEXT NOT NULL,intent_id TEXT NOT NULL,project_id TEXT NOT NULL REFERENCES projects(id),task_id TEXT NOT NULL REFERENCES tasks(id),review_id TEXT NOT NULL REFERENCES reviews(id),decision_id TEXT NOT NULL UNIQUE,approval_step_request_id TEXT NOT NULL UNIQUE,revision_id TEXT NOT NULL UNIQUE REFERENCES task_definition_revisions(id),operation_generation TEXT NOT NULL UNIQUE,source_origin_id TEXT NOT NULL UNIQUE,identity_hash TEXT NOT NULL CHECK(length(identity_hash)=64),t1_json TEXT NOT NULL CHECK(json_valid(t1_json)),t1_sha256 TEXT NOT NULL CHECK(length(t1_sha256)=64),archive_json TEXT NOT NULL CHECK(json_valid(archive_json)),archive_sha256 TEXT NOT NULL CHECK(length(archive_sha256)=64),created_at TEXT NOT NULL,PRIMARY KEY(ai_session_id,request_id),UNIQUE(ai_session_id,intent_id),FOREIGN KEY(ai_session_id,intent_id) REFERENCES formal_execution_fences(ai_session_id,intent_id));
CREATE TABLE formal_execution_t2 (
 ai_session_id TEXT NOT NULL,request_id TEXT NOT NULL,execution_id TEXT NOT NULL UNIQUE REFERENCES execution_proposals(id),plan_id TEXT NOT NULL UNIQUE REFERENCES runtime_scope_plans(plan_id),plan_hash TEXT NOT NULL CHECK(length(plan_hash)=64),admission_request_id TEXT NOT NULL UNIQUE,admission_payload_hash TEXT NOT NULL CHECK(length(admission_payload_hash)=64),t2_json TEXT NOT NULL CHECK(json_valid(t2_json)),t2_sha256 TEXT NOT NULL CHECK(length(t2_sha256)=64),plan_json TEXT NOT NULL CHECK(json_valid(plan_json)),plan_sha256 TEXT NOT NULL CHECK(length(plan_sha256)=64),created_at TEXT NOT NULL,PRIMARY KEY(ai_session_id,request_id),FOREIGN KEY(ai_session_id,request_id) REFERENCES formal_execution_t1(ai_session_id,request_id),FOREIGN KEY(execution_id) REFERENCES execution_proposal_admissions(proposal_id) DEFERRABLE INITIALLY DEFERRED);
CREATE TABLE formal_execution_receipts (
 ai_session_id TEXT NOT NULL,request_id TEXT NOT NULL,receipt_identity TEXT NOT NULL UNIQUE,receipt_sha256 TEXT NOT NULL CHECK(length(receipt_sha256)=64),receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json)),preparation_authority_id TEXT NOT NULL UNIQUE REFERENCES runtime_preparation_authorities(plan_id),created_at TEXT NOT NULL,PRIMARY KEY(ai_session_id,request_id),FOREIGN KEY(ai_session_id,request_id) REFERENCES formal_execution_t2(ai_session_id,request_id));
CREATE TRIGGER formal_execution_t1_source BEFORE INSERT ON formal_execution_t1
WHEN NOT EXISTS(SELECT 1 FROM formal_execution_fences f JOIN task_definition_revisions r ON r.id=NEW.revision_id JOIN decision_resolution_receipts d ON d.decision_id=NEW.decision_id JOIN review_approval_materializations m ON m.decision_id=d.decision_id
 WHERE f.ai_session_id=NEW.ai_session_id AND f.intent_id=NEW.intent_id AND f.project_id=NEW.project_id AND f.task_id=NEW.task_id AND f.review_id=NEW.review_id AND f.decision_id=NEW.decision_id
 AND d.actor_kind='AI_SESSION' AND d.actor_id=NEW.ai_session_id AND d.request_id=NEW.approval_step_request_id AND d.project_id=NEW.project_id AND d.option='APPROVE' AND d.result_kind='DEVELOPMENT_TASK' AND d.result_id=NEW.task_id
 AND m.task_id=NEW.task_id AND m.initial_revision_id=NEW.revision_id AND r.task_id=NEW.task_id
 AND json_extract(NEW.archive_json,'$.fence') IS f.fence_json AND json_extract(NEW.archive_json,'$.revisionId') IS r.id AND json_extract(NEW.archive_json,'$.revisionHash') IS r.payload_sha256 AND json_extract(NEW.archive_json,'$.definitionJson') IS r.payload_json
 AND json_extract(NEW.t1_json,'$.identity.operationGeneration') IS NEW.operation_generation AND json_extract(NEW.t1_json,'$.identity.immutableBinding') IS NEW.identity_hash AND json_extract(NEW.t1_json,'$.identity.sourceOriginId') IS NEW.source_origin_id AND json_extract(NEW.t1_json,'$.commitState') IS 'T1_COMMITTED')
BEGIN SELECT RAISE(ABORT,'formal execution T1 source mismatch'); END;
CREATE TRIGGER formal_execution_t2_source BEFORE INSERT ON formal_execution_t2
WHEN NOT EXISTS(SELECT 1 FROM formal_execution_t1 b JOIN tasks t ON t.id=b.task_id JOIN execution_proposals e ON e.id=NEW.execution_id JOIN runtime_scope_plans p ON p.plan_id=NEW.plan_id
 WHERE b.ai_session_id=NEW.ai_session_id AND b.request_id=NEW.request_id AND e.task_id=b.task_id AND e.task_definition_revision_id=b.revision_id AND e.status='CONFIRMED' AND e.run_id IS NULL AND t.current_execution_id=e.id AND t.current_definition_revision_id=b.revision_id AND t.status='ACTIVE' AND t.phase='IMPLEMENTATION_PREPARING'
 AND p.owner_id=e.id AND p.task_definition_revision_id=b.revision_id AND p.content_hash=NEW.plan_hash AND p.preparation_request_id=NEW.admission_request_id AND p.preparation_payload_hash=NEW.admission_payload_hash
 AND json_extract(NEW.t2_json,'$.operationIdentityBinding') IS b.identity_hash AND json_extract(NEW.t2_json,'$.operationGeneration') IS b.operation_generation AND json_extract(NEW.t2_json,'$.executionId') IS NEW.execution_id AND json_extract(NEW.t2_json,'$.scopePlanId') IS NEW.plan_id AND json_extract(NEW.t2_json,'$.scopePlanBinding') IS NEW.plan_hash AND json_extract(NEW.t2_json,'$.commitState') IS 'T2_COMMITTED')
BEGIN SELECT RAISE(ABORT,'formal execution T2 source mismatch'); END;
CREATE TRIGGER formal_execution_receipt_source BEFORE INSERT ON formal_execution_receipts
WHEN NOT EXISTS(SELECT 1 FROM formal_execution_t1 b JOIN formal_execution_t2 c ON c.ai_session_id=b.ai_session_id AND c.request_id=b.request_id JOIN runtime_preparation_authorities a ON a.plan_id=c.plan_id JOIN tasks t ON t.id=b.task_id
 WHERE b.ai_session_id=NEW.ai_session_id AND b.request_id=NEW.request_id AND a.plan_id=NEW.preparation_authority_id AND a.owner_id=c.execution_id AND a.request_id=c.admission_request_id AND a.payload_hash=c.admission_payload_hash AND a.disposition='CURRENT'
 AND t.current_execution_id=c.execution_id AND t.current_definition_revision_id=b.revision_id AND t.status='ACTIVE' AND t.phase='IMPLEMENTATION_PREPARING'
 AND json_extract(NEW.receipt_json,'$.t1') IS b.t1_json AND json_extract(NEW.receipt_json,'$.t2') IS c.t2_json AND json_extract(NEW.receipt_json,'$.archive') IS b.archive_json AND json_extract(NEW.receipt_json,'$.plan') IS c.plan_json AND json_extract(NEW.receipt_json,'$.receiptIdentity') IS NEW.receipt_identity AND json_extract(NEW.receipt_json,'$.role') IS 'CURRENT_FORMAL_COMMIT_RECEIPT' AND json_extract(NEW.receipt_json,'$.businessOutcome') IS 'COMMITTED')
BEGIN SELECT RAISE(ABORT,'formal execution final source mismatch'); END;
CREATE TRIGGER formal_execution_ready_guard BEFORE UPDATE OF phase ON tasks
WHEN NEW.phase='IMPLEMENTATION_READY' AND EXISTS(SELECT 1 FROM formal_execution_t1 b WHERE b.task_id=NEW.id AND b.revision_id=NEW.current_definition_revision_id)
 AND NOT EXISTS(SELECT 1 FROM formal_execution_t1 b JOIN formal_execution_t2 c ON c.ai_session_id=b.ai_session_id AND c.request_id=b.request_id JOIN formal_execution_receipts r ON r.ai_session_id=b.ai_session_id AND r.request_id=b.request_id WHERE b.task_id=NEW.id AND b.revision_id=NEW.current_definition_revision_id AND c.execution_id=NEW.current_execution_id AND r.preparation_authority_id=c.plan_id)
BEGIN SELECT RAISE(ABORT,'formal execution READY requires T3'); END;
CREATE TRIGGER formal_execution_confirmation_completion BEFORE UPDATE OF intent_json ON confirmation_intents
WHEN EXISTS(SELECT 1 FROM formal_execution_fences f WHERE f.intent_id=NEW.intent_id)
 AND EXISTS(SELECT 1 FROM json_each(NEW.intent_json,'$.steps') step WHERE json_extract(step.value,'$.kind')='APPROVE' AND json_extract(step.value,'$.state')='COMPLETED' AND NOT EXISTS(SELECT 1 FROM formal_execution_t1 b JOIN formal_execution_t2 c ON c.ai_session_id=b.ai_session_id AND c.request_id=b.request_id JOIN formal_execution_receipts r ON r.ai_session_id=b.ai_session_id AND r.request_id=b.request_id WHERE b.ai_session_id=NEW.ai_session_id AND b.intent_id=NEW.intent_id AND b.request_id=json_extract(NEW.intent_json,'$.commit.requestId') AND b.approval_step_request_id=json_extract(step.value,'$.requestId') AND b.task_id=json_extract(step.value,'$.taskId') AND b.revision_id=json_extract(step.value,'$.revisionId') AND c.execution_id=json_extract(step.value,'$.executionId') AND COALESCE(json_extract(step.value,'$.runId'),'')=''))
BEGIN SELECT RAISE(ABORT,'formal execution completion requires exact T3'); END;
`
