package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// v91 adds a durable pre-Run Execution state without changing the meaning of
// historical CONSUMED rows. The table rebuild retains every existing column,
// index, trigger and foreign key by replaying its live schema objects.
func migrateExecutionAdmissionV91(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='execution_proposals'`).Scan(&ddl); err != nil {
		return err
	}
	oldStatus := "status IN ('PENDING_CONFIRMATION','CONFIRMED','REJECTED','CONSUMED')"
	oldConsumed := "(status='CONSUMED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND consumed_at IS NOT NULL AND run_id IS NOT NULL)"
	if strings.Count(ddl, oldStatus) != 1 || strings.Count(ddl, oldConsumed) != 1 {
		return fmt.Errorf("execution proposal schema is not the v90 contract")
	}
	rebuilt := strings.Replace(ddl, oldStatus, "status IN ('PENDING_CONFIRMATION','CONFIRMED','ADMITTED','REJECTED','CONSUMED')", 1)
	rebuilt = strings.Replace(rebuilt, oldConsumed, "(status='ADMITTED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND consumed_at IS NULL AND run_id IS NULL) OR\n       "+oldConsumed, 1)
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA legacy_alter_table=OFF`)
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name='execution_proposals' AND sql IS NOT NULL) ORDER BY type,name`)
	if err != nil {
		return err
	}
	var objects []schemaObject
	for rows.Next() {
		var object schemaObject
		if err = rows.Scan(&object.kind, &object.name, &object.sql); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, object)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, object := range objects {
		if _, err = tx.ExecContext(ctx, "DROP "+strings.ToUpper(object.kind)+" "+sqlIdentifier(object.name)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `ALTER TABLE execution_proposals RENAME TO _source90_execution_proposals`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, rebuilt); err != nil {
		return err
	}
	columns, err := schemaColumns(ctx, tx, "_source90_execution_proposals")
	if err != nil {
		return err
	}
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = sqlIdentifier(column)
	}
	columnList := strings.Join(quoted, ",")
	if _, err = tx.ExecContext(ctx, "INSERT INTO execution_proposals("+columnList+") SELECT "+columnList+" FROM _source90_execution_proposals"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE _source90_execution_proposals`); err != nil {
		return err
	}
	for _, object := range objects {
		statement := object.sql
		statement = strings.ReplaceAll(statement, "'PENDING_CONFIRMATION','CONFIRMED','CONSUMED'", "'PENDING_CONFIRMATION','CONFIRMED','ADMITTED','CONSUMED'")
		statement = strings.ReplaceAll(statement, "NEW.status IN ('CONFIRMED','CONSUMED')", "NEW.status IN ('CONFIRMED','ADMITTED','CONSUMED')")
		switch object.name {
		case "execution_proposals_status_transition":
			statement = strings.Replace(statement, "(OLD.status='CONFIRMED' AND NEW.status='CONSUMED')", "(OLD.status='CONFIRMED' AND NEW.status='ADMITTED') OR (OLD.status='ADMITTED' AND NEW.status='CONSUMED') OR (OLD.status='CONFIRMED' AND NEW.status='CONSUMED')", 1)
		case "execution_proposals_outcome_transition":
			statement = strings.Replace(statement, "OLD.status='CONFIRMED' AND NEW.status='CONSUMED'", "OLD.status IN ('CONFIRMED','ADMITTED') AND NEW.status='CONSUMED'", 1)
		case "execution_proposals_one_active_per_task_plan":
			statement = strings.Replace(statement, "'PENDING_CONFIRMATION','CONFIRMED','CONSUMED'", "'PENDING_CONFIRMATION','CONFIRMED','ADMITTED','CONSUMED'", 1)
		}
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("recreate %s: %w", object.name, err)
		}
	}
	_, err = tx.ExecContext(ctx, `
CREATE TABLE execution_proposal_admissions (
 proposal_id TEXT PRIMARY KEY REFERENCES execution_proposals(id),
 admission_request_id TEXT NOT NULL UNIQUE,
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 source_decision_id TEXT NOT NULL DEFAULT '',
 source_confirmation_intent_id TEXT NOT NULL DEFAULT '',
 task_id TEXT NOT NULL REFERENCES tasks(id),
 task_definition_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 runtime_scope_plan_id TEXT NOT NULL UNIQUE REFERENCES runtime_scope_plans(plan_id),
 runtime_preparation_authority_id TEXT NOT NULL UNIQUE REFERENCES runtime_preparation_authorities(plan_id),
 admitted_at TEXT NOT NULL,
 CHECK(runtime_scope_plan_id=runtime_preparation_authority_id)
);
CREATE TRIGGER execution_proposal_admissions_no_update BEFORE UPDATE ON execution_proposal_admissions BEGIN SELECT RAISE(ABORT,'Execution admission immutable'); END;
CREATE TRIGGER execution_proposal_admissions_no_delete BEFORE DELETE ON execution_proposal_admissions BEGIN SELECT RAISE(ABORT,'Execution admission immutable'); END;
CREATE TRIGGER execution_proposal_admissions_owner BEFORE INSERT ON execution_proposal_admissions
WHEN NOT EXISTS(SELECT 1 FROM execution_proposals e JOIN runtime_scope_plans p ON p.plan_id=NEW.runtime_scope_plan_id JOIN runtime_preparation_authorities a ON a.plan_id=p.plan_id
 WHERE e.id=NEW.proposal_id AND e.task_id=NEW.task_id AND e.task_definition_revision_id=NEW.task_definition_revision_id AND e.status='CONFIRMED' AND e.run_id IS NULL
 AND p.owner_kind='IMPLEMENTATION' AND p.owner_id=e.id AND p.task_id=e.task_id AND p.task_definition_revision_id=e.task_definition_revision_id AND p.preparation_request_id=NEW.admission_request_id AND p.preparation_payload_hash=NEW.payload_sha256
 AND a.owner_kind=p.owner_kind AND a.owner_id=p.owner_id AND a.disposition='CURRENT')
BEGIN SELECT RAISE(ABORT,'Execution admission owner mismatch'); END;
CREATE TRIGGER execution_proposals_admitted_receipt BEFORE UPDATE OF status ON execution_proposals
WHEN NEW.status='ADMITTED' AND NOT EXISTS(SELECT 1 FROM execution_proposal_admissions receipt
 JOIN runtime_scope_plans p ON p.plan_id=receipt.runtime_scope_plan_id
 JOIN runtime_preparation_authorities authority ON authority.plan_id=p.plan_id
 JOIN tasks t ON t.id=NEW.task_id
 WHERE receipt.proposal_id=NEW.id AND receipt.task_id=NEW.task_id AND receipt.task_definition_revision_id=NEW.task_definition_revision_id
 AND p.owner_kind='IMPLEMENTATION' AND p.owner_id=NEW.id AND authority.owner_id=NEW.id AND authority.disposition='CURRENT'
 AND t.current_execution_id=NEW.id AND t.current_definition_revision_id=NEW.task_definition_revision_id)
BEGIN SELECT RAISE(ABORT,'Execution admission receipt required'); END;
CREATE TRIGGER execution_proposals_preparation_run_receipt BEFORE UPDATE OF status ON execution_proposals
WHEN OLD.status='ADMITTED' AND NEW.status='CONSUMED' AND NOT EXISTS(SELECT 1 FROM execution_proposal_admissions receipt
 JOIN runtime_preparation_attempts attempt ON attempt.plan_id=receipt.runtime_scope_plan_id
 WHERE receipt.proposal_id=NEW.id AND attempt.run_id=NEW.run_id AND attempt.request_id=receipt.admission_request_id AND attempt.payload_hash=receipt.payload_sha256)
BEGIN SELECT RAISE(ABORT,'Execution preparation Run receipt required'); END;
`)
	return err
}
