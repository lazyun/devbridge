package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
)

const v95OldPhaseCheck = "CHECK(phase IN ('REVIEW_REQUIRED','REVIEWING','REVIEW_APPROVAL_PENDING','IMPLEMENTATION_READY','IMPLEMENTING','VERIFICATION_READY','VERIFYING','ACCEPTANCE_PENDING','DELIVERING','RECOVERY_REQUIRED'))"
const v95NewPhaseCheck = "CHECK(phase IN ('REVIEW_REQUIRED','REVIEWING','REVIEW_APPROVAL_PENDING','IMPLEMENTATION_READY','IMPLEMENTING','VERIFICATION_READY','VERIFYING','ACCEPTANCE_PENDING','DELIVERING','RECOVERY_REQUIRED','NO_RUN_SUCCESSOR_REQUIRED','IMPLEMENTATION_PREPARING'))"

// v95 keeps every prior Task column, index, trigger and phase literal while
// adding a distinct no-Run successor authority. The two receipt tables have
// different cardinalities: one business effect per predecessor, one command
// acknowledgment per globally unique RequestID.
func migrateNoRunSuccessorV95(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'`).Scan(&ddl); err != nil {
		return err
	}
	if strings.Count(ddl, v95OldPhaseCheck) != 1 {
		return fmt.Errorf("tasks phase constraint is not the v94 contract")
	}
	rebuilt := strings.Replace(ddl, v95OldPhaseCheck, v95NewPhaseCheck, 1)
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA legacy_alter_table=OFF`)
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name='tasks' AND sql IS NOT NULL) ORDER BY type,name`)
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
	if _, err = tx.ExecContext(ctx, `ALTER TABLE tasks RENAME TO _source94_tasks`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, rebuilt); err != nil {
		return err
	}
	columns, err := schemaColumns(ctx, tx, "_source94_tasks")
	if err != nil {
		return err
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = sqlIdentifier(c)
	}
	names := strings.Join(quoted, ",")
	if _, err = tx.ExecContext(ctx, "INSERT INTO tasks("+names+") SELECT "+names+" FROM _source94_tasks"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE _source94_tasks`); err != nil {
		return err
	}
	if err = extendExecutionPlansV95(ctx, tx); err != nil {
		return err
	}
	for _, object := range objects {
		if _, err = tx.ExecContext(ctx, object.sql); err != nil {
			return fmt.Errorf("recreate %s: %w", object.name, err)
		}
	}
	if _, err = tx.ExecContext(ctx, noRunSuccessorV95SQL); err != nil {
		return err
	}
	if err = replaceNoRunSuccessorV95Triggers(ctx, tx); err != nil {
		return err
	}
	if err = upgradeCanceledNoRunExecutionPlansV95(ctx, tx); err != nil {
		return err
	}
	// Freeze exact eligibility for the deterministic backfill before changing
	// any Task phase. This is a separate durable authority from a successor.
	if _, err = tx.ExecContext(ctx, `INSERT INTO no_run_successor_eligibilities(predecessor_execution_id,task_id,task_definition_revision_id,effective_cancellation_project_id,effective_cancellation_request_id,created_at)
      SELECT t.current_execution_id,t.id,t.current_definition_revision_id,c.project_id,c.request_id,c.requested_at FROM tasks t JOIN execution_proposals e ON e.id=t.current_execution_id JOIN execution_cancellations c ON c.project_id=e.project_id AND c.owner_type='EXECUTION' AND c.owner_id=e.id AND c.state='CANCELED'
      WHERE t.phase IN ('IMPLEMENTATION_READY','RECOVERY_REQUIRED') AND `+noRunSuccessorEligibleSQL); err != nil {
		return err
	}
	// Reclassify only a fully proven canceled-before-Run current owner. The
	// backfill changes no pointer, revision, receipt or timestamp.
	_, err = tx.ExecContext(ctx, `UPDATE tasks AS t SET phase='NO_RUN_SUCCESSOR_REQUIRED' WHERE phase IN ('IMPLEMENTATION_READY','RECOVERY_REQUIRED') AND `+noRunSuccessorEligibleSQL)
	return err
}

// Earlier admission candidates froze an IMPLEMENTATION-owned Plan. An exact
// canceled-before-Run predecessor must acquire the EXECUTION owner and the
// frozen snapshot before its Task can enter the successor-required phase.
// This conversion runs only inside the v95 migration transaction; it never
// consults live backend settings or invents an authority for an ambiguous Run.
func upgradeCanceledNoRunExecutionPlansV95(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT e.id FROM tasks t JOIN execution_proposals e ON e.id=t.current_execution_id JOIN execution_proposal_admissions a ON a.proposal_id=e.id JOIN runtime_scope_plans p ON p.plan_id=a.runtime_scope_plan_id WHERE t.phase IN ('IMPLEMENTATION_READY','RECOVERY_REQUIRED') AND p.owner_kind='IMPLEMENTATION' AND `+noRunSuccessorEligibleSQL)
	if err != nil {
		return err
	}
	var predecessors []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		predecessors = append(predecessors, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(predecessors) == 0 {
		return err
	}
	triggers := map[string]string{}
	for _, name := range []string{"runtime_scope_plans_immutable_UPDATE", "runtime_preparation_authorities_immutable_UPDATE"} {
		var definition string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&definition); err != nil {
			return err
		}
		triggers[name] = definition
		if _, err = tx.ExecContext(ctx, `DROP TRIGGER `+sqlIdentifier(name)); err != nil {
			return err
		}
	}
	for _, predecessorID := range predecessors {
		plan, readErr := readSuccessorSourcePlanWithOwner(ctx, tx, predecessorID, runtimepreparation.OwnerImplementation)
		if readErr != nil {
			return fmt.Errorf("legacy no-Run predecessor %s Plan: %w", predecessorID, readErr)
		}
		value, readErr := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, predecessorID))
		if readErr != nil {
			return readErr
		}
		var repositoryID string
		if readErr = tx.QueryRowContext(ctx, `SELECT COALESCE(repository_id,'') FROM tasks WHERE id=?`, plan.TaskID).Scan(&repositoryID); readErr != nil {
			return readErr
		}
		identity, fingerprint, source, kind := "", "", "", ""
		if value.BackendSelection != nil {
			identity, fingerprint, source, kind = value.BackendSelection.ConfigID, value.BackendSelection.ConfigFingerprint, value.BackendSelection.Source, "CODEX_CONFIG"
			if source == "" {
				source = "CORE_SELECTION"
			}
		} else if repositoryID != "" {
			identity = "scalar:" + repositoryID
			fingerprint, source, kind = workflowHash(identity), "SCALAR", "SCALAR"
		}
		if identity == "" || fingerprint == "" || identity != plan.BackendIdentity {
			return fmt.Errorf("legacy no-Run predecessor %s backend cannot be reconstructed", predecessorID)
		}
		plan.OwnerKind = runtimepreparation.OwnerExecution
		plan.BackendConfigFingerprint, plan.BackendSource, plan.BackendKind = fingerprint, source, kind
		plan.CodexFastMode, plan.ServiceTier = value.RuntimeConfig.CodexFastMode, value.RuntimeConfig.ServiceTier
		plan.RuntimeProtocolIdentity = value.RuntimeProtocolIdentity
		if plan.RuntimeProtocolIdentity == "" && repositoryID != "" {
			plan.RuntimeProtocolIdentity = executionproposal.RuntimeProtocolCodex0149
		}
		if plan.RuntimeProtocolIdentity == "" {
			return fmt.Errorf("legacy no-Run predecessor %s protocol cannot be reconstructed", predecessorID)
		}
		plan.AuthoritySnapshotHash, readErr = plan.ComputeAuthoritySnapshotHash()
		if readErr != nil {
			return readErr
		}
		plan.ContentHash, readErr = plan.ComputeContentHash()
		if readErr != nil {
			return readErr
		}
		if readErr = plan.Validate(); readErr != nil {
			return fmt.Errorf("legacy no-Run predecessor %s conversion: %w", predecessorID, readErr)
		}
		result, writeErr := tx.ExecContext(ctx, `UPDATE runtime_scope_plans SET owner_kind='EXECUTION',backend_config_fingerprint=?,backend_source=?,backend_kind=?,codex_fast_mode=?,service_tier=?,runtime_protocol_identity=?,authority_snapshot_hash=?,content_hash=? WHERE plan_id=? AND owner_kind='IMPLEMENTATION' AND owner_id=?`, plan.BackendConfigFingerprint, plan.BackendSource, plan.BackendKind, boolInt(plan.CodexFastMode), plan.ServiceTier, plan.RuntimeProtocolIdentity, plan.AuthoritySnapshotHash, plan.ContentHash, plan.ID, predecessorID)
		if writeErr != nil {
			return writeErr
		}
		changed, writeErr := result.RowsAffected()
		if writeErr != nil || changed != 1 {
			return fmt.Errorf("legacy no-Run predecessor %s Plan conversion cardinality: %w", predecessorID, writeErr)
		}
		result, writeErr = tx.ExecContext(ctx, `UPDATE runtime_preparation_authorities SET owner_kind='EXECUTION' WHERE plan_id=? AND owner_kind='IMPLEMENTATION' AND owner_id=? AND disposition='CURRENT'`, plan.ID, predecessorID)
		if writeErr != nil {
			return writeErr
		}
		changed, writeErr = result.RowsAffected()
		if writeErr != nil || changed != 1 {
			return fmt.Errorf("legacy no-Run predecessor %s preparation authority conversion cardinality: %w", predecessorID, writeErr)
		}
	}
	for _, name := range []string{"runtime_scope_plans_immutable_UPDATE", "runtime_preparation_authorities_immutable_UPDATE"} {
		if _, err = tx.ExecContext(ctx, triggers[name]); err != nil {
			return err
		}
	}
	return nil
}

// v95 adds the canonical EXECUTION owner kind without changing the published
// v90 migration. Existing Plan rows retain their historical owner kind.
func extendExecutionPlansV95(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='runtime_scope_plans'`).Scan(&ddl); err != nil {
		return err
	}
	oldKinds := "owner_kind IN ('IMPLEMENTATION','VERIFICATION','REVIEW','INSPECTION','RECOVERY_STATUS')"
	oldExecution := "owner_kind IN ('IMPLEMENTATION','VERIFICATION')"
	if strings.Count(ddl, oldKinds) != 1 || strings.Count(ddl, oldExecution) != 1 {
		return fmt.Errorf("runtime Plan owner contract is not v94")
	}
	ddl = strings.Replace(ddl, oldKinds, "owner_kind IN ('IMPLEMENTATION','EXECUTION','VERIFICATION','REVIEW','INSPECTION','RECOVERY_STATUS')", 1)
	ddl = strings.Replace(ddl, oldExecution, "owner_kind IN ('IMPLEMENTATION','EXECUTION','VERIFICATION')", 1)
	if _, err := tx.ExecContext(ctx, `ALTER TABLE runtime_scope_plans RENAME TO _source94_runtime_scope_plans`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	columns, err := schemaColumns(ctx, tx, "_source94_runtime_scope_plans")
	if err != nil {
		return err
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = sqlIdentifier(c)
	}
	names := strings.Join(quoted, ",")
	if _, err = tx.ExecContext(ctx, "INSERT INTO runtime_scope_plans("+names+") SELECT "+names+" FROM _source94_runtime_scope_plans"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE _source94_runtime_scope_plans`); err != nil {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE runtime_scope_plans ADD COLUMN backend_config_fingerprint TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runtime_scope_plans ADD COLUMN backend_source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runtime_scope_plans ADD COLUMN backend_kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runtime_scope_plans ADD COLUMN codex_fast_mode INTEGER NOT NULL DEFAULT 0 CHECK(codex_fast_mode IN (0,1))`,
		`ALTER TABLE runtime_scope_plans ADD COLUMN service_tier TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runtime_scope_plans ADD COLUMN runtime_protocol_identity TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runtime_scope_plans ADD COLUMN authority_snapshot_hash TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

const noRunSuccessorV95SQL = `
CREATE TABLE no_run_successor_eligibilities (
 predecessor_execution_id TEXT PRIMARY KEY REFERENCES execution_proposals(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 task_definition_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 effective_cancellation_project_id TEXT NOT NULL,
 effective_cancellation_request_id TEXT NOT NULL,
 consumed_successor_execution_id TEXT UNIQUE,
 created_at TEXT NOT NULL,
 FOREIGN KEY(effective_cancellation_project_id,effective_cancellation_request_id) REFERENCES execution_cancellations(project_id,request_id)
);
CREATE TRIGGER no_run_successor_eligibility_no_delete BEFORE DELETE ON no_run_successor_eligibilities BEGIN SELECT RAISE(ABORT,'successor eligibility durable'); END;
CREATE TRIGGER no_run_successor_eligibility_update BEFORE UPDATE ON no_run_successor_eligibilities
WHEN NEW.predecessor_execution_id IS NOT OLD.predecessor_execution_id OR NEW.task_id IS NOT OLD.task_id OR NEW.task_definition_revision_id IS NOT OLD.task_definition_revision_id OR NEW.effective_cancellation_project_id IS NOT OLD.effective_cancellation_project_id OR NEW.effective_cancellation_request_id IS NOT OLD.effective_cancellation_request_id OR NEW.created_at IS NOT OLD.created_at OR OLD.consumed_successor_execution_id IS NOT NULL OR NEW.consumed_successor_execution_id IS NULL
BEGIN SELECT RAISE(ABORT,'successor eligibility immutable except one consumption'); END;
CREATE TRIGGER no_run_successor_task_entry BEFORE UPDATE OF phase ON tasks
WHEN NEW.phase='NO_RUN_SUCCESSOR_REQUIRED' AND OLD.phase<>NEW.phase AND NOT EXISTS(SELECT 1 FROM no_run_successor_eligibilities a WHERE a.task_id=NEW.id AND a.predecessor_execution_id=NEW.current_execution_id AND a.task_definition_revision_id=NEW.current_definition_revision_id AND a.consumed_successor_execution_id IS NULL)
BEGIN SELECT RAISE(ABORT,'successor eligibility required'); END;
CREATE TRIGGER no_run_successor_task_exit BEFORE UPDATE OF phase,current_execution_id ON tasks
WHEN OLD.phase='NO_RUN_SUCCESSOR_REQUIRED' AND (NEW.phase<>OLD.phase OR NEW.current_execution_id IS NOT OLD.current_execution_id) AND NOT EXISTS(SELECT 1 FROM no_run_successor_effect_authorities a WHERE a.task_id=OLD.id AND a.predecessor_execution_id=OLD.current_execution_id AND a.successor_execution_id=NEW.current_execution_id AND NEW.phase='IMPLEMENTATION_PREPARING')
BEGIN SELECT RAISE(ABORT,'successor effect authority required'); END;
CREATE TABLE no_run_successor_effect_authorities (
 authority_id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES tasks(id),
 task_definition_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 predecessor_execution_id TEXT NOT NULL UNIQUE REFERENCES execution_proposals(id),
 effective_cancellation_project_id TEXT NOT NULL,
 effective_cancellation_request_id TEXT NOT NULL,
 successor_execution_id TEXT NOT NULL UNIQUE REFERENCES execution_proposals(id),
 runtime_scope_plan_id TEXT NOT NULL UNIQUE REFERENCES runtime_scope_plans(plan_id),
 runtime_preparation_authority_id TEXT NOT NULL UNIQUE REFERENCES runtime_preparation_authorities(plan_id),
 predecessor_runtime_scope_plan_id TEXT NOT NULL REFERENCES runtime_scope_plans(plan_id),
 predecessor_plan_content_hash TEXT NOT NULL CHECK(length(predecessor_plan_content_hash)=64 AND predecessor_plan_content_hash NOT GLOB '*[^0-9a-f]*'),
 predecessor_authority_snapshot_hash TEXT NOT NULL CHECK(length(predecessor_authority_snapshot_hash)=64 AND predecessor_authority_snapshot_hash NOT GLOB '*[^0-9a-f]*'),
 verified_current_authority_snapshot_hash TEXT NOT NULL CHECK(length(verified_current_authority_snapshot_hash)=64 AND verified_current_authority_snapshot_hash NOT GLOB '*[^0-9a-f]*'),
 created_at TEXT NOT NULL,
 FOREIGN KEY(effective_cancellation_project_id,effective_cancellation_request_id) REFERENCES execution_cancellations(project_id,request_id),
 CHECK(runtime_scope_plan_id=runtime_preparation_authority_id)
);
CREATE TABLE no_run_successor_command_receipts (
 request_id TEXT PRIMARY KEY CHECK(length(request_id)>0 AND length(request_id)<=128),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 predecessor_execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 successor_effect_authority_id TEXT NOT NULL REFERENCES no_run_successor_effect_authorities(authority_id),
 successor_execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 outcome TEXT NOT NULL CHECK(outcome IN ('SUCCESSOR_CREATED','ALREADY_HAS_SUCCESSOR')),
 created_at TEXT NOT NULL
);
CREATE TRIGGER no_run_successor_effect_no_update BEFORE UPDATE ON no_run_successor_effect_authorities BEGIN SELECT RAISE(ABORT,'successor effect authority immutable'); END;
CREATE TRIGGER no_run_successor_effect_no_delete BEFORE DELETE ON no_run_successor_effect_authorities BEGIN SELECT RAISE(ABORT,'successor effect authority durable'); END;
CREATE TRIGGER no_run_successor_receipt_no_update BEFORE UPDATE ON no_run_successor_command_receipts BEGIN SELECT RAISE(ABORT,'successor command receipt immutable'); END;
CREATE TRIGGER no_run_successor_receipt_no_delete BEFORE DELETE ON no_run_successor_command_receipts BEGIN SELECT RAISE(ABORT,'successor command receipt durable'); END;
CREATE TRIGGER no_run_successor_execution_plan_snapshot BEFORE INSERT ON runtime_scope_plans
WHEN NEW.owner_kind='EXECUTION' AND (length(NEW.authority_snapshot_hash)<>64 OR NEW.authority_snapshot_hash GLOB '*[^0-9a-f]*' OR length(NEW.backend_config_fingerprint)<>64 OR NEW.backend_config_fingerprint GLOB '*[^0-9a-f]*' OR NEW.runtime_protocol_identity='')
BEGIN SELECT RAISE(ABORT,'execution Plan authority snapshot invalid'); END;
CREATE TRIGGER no_run_successor_receipt_owner BEFORE INSERT ON no_run_successor_command_receipts
WHEN NOT EXISTS(SELECT 1 FROM no_run_successor_effect_authorities a WHERE a.authority_id=NEW.successor_effect_authority_id AND a.task_id=NEW.task_id AND a.predecessor_execution_id=NEW.predecessor_execution_id AND a.successor_execution_id=NEW.successor_execution_id)
BEGIN SELECT RAISE(ABORT,'successor command receipt owner mismatch'); END;
CREATE TRIGGER no_run_successor_effect_owner BEFORE INSERT ON no_run_successor_effect_authorities
WHEN NOT EXISTS(SELECT 1 FROM tasks t JOIN execution_proposals predecessor ON predecessor.id=NEW.predecessor_execution_id JOIN execution_proposals successor ON successor.id=NEW.successor_execution_id JOIN execution_cancellations c ON c.project_id=NEW.effective_cancellation_project_id AND c.request_id=NEW.effective_cancellation_request_id JOIN runtime_scope_plans p ON p.plan_id=NEW.runtime_scope_plan_id JOIN runtime_preparation_authorities a ON a.plan_id=p.plan_id JOIN execution_proposal_admissions source_admission ON source_admission.proposal_id=predecessor.id AND source_admission.runtime_scope_plan_id=NEW.predecessor_runtime_scope_plan_id AND source_admission.runtime_preparation_authority_id=NEW.predecessor_runtime_scope_plan_id JOIN runtime_scope_plans source_plan ON source_plan.plan_id=source_admission.runtime_scope_plan_id JOIN runtime_preparation_authorities source_authority ON source_authority.plan_id=source_admission.runtime_preparation_authority_id
 WHERE t.id=NEW.task_id AND t.current_definition_revision_id=NEW.task_definition_revision_id AND predecessor.task_id=t.id AND predecessor.task_definition_revision_id=NEW.task_definition_revision_id AND predecessor.status='ADMITTED' AND predecessor.outcome='CANCELED' AND predecessor.run_id IS NULL AND successor.task_id=t.id AND successor.task_definition_revision_id=NEW.task_definition_revision_id AND successor.status='ADMITTED' AND successor.outcome='PENDING' AND successor.run_id IS NULL AND c.owner_type='EXECUTION' AND c.owner_id=predecessor.id AND c.state='CANCELED' AND c.backend_disposition='NO_BACKEND' AND c.interrupt_state='NOT_SENT' AND c.run_id IS NULL AND c.resolved_at IS NOT NULL AND source_plan.owner_kind='EXECUTION' AND source_plan.owner_id=predecessor.id AND source_authority.owner_kind='EXECUTION' AND source_authority.owner_id=predecessor.id AND source_authority.disposition='CURRENT' AND source_plan.content_hash=NEW.predecessor_plan_content_hash AND source_plan.authority_snapshot_hash=NEW.predecessor_authority_snapshot_hash AND p.owner_kind='EXECUTION' AND p.owner_id=successor.id AND p.task_id=t.id AND p.authority_snapshot_hash=NEW.verified_current_authority_snapshot_hash AND a.owner_kind='EXECUTION' AND a.owner_id=successor.id AND a.disposition='CURRENT' AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.execution_proposal_id=predecessor.id OR r.execution_proposal_id=successor.id) AND NOT EXISTS(SELECT 1 FROM runtime_preparation_attempts attempt WHERE attempt.plan_id=p.plan_id))
BEGIN SELECT RAISE(ABORT,'successor effect authority owner mismatch'); END;
`

// The legacy approval/admission triggers must recognize one exact no-Run
// successor permit, while retaining their existing authorization arms.
func replaceNoRunSuccessorV95Triggers(ctx context.Context, tx *sql.Tx) error {
	for _, name := range []string{"execution_proposal_admissions_owner", "execution_proposals_admitted_receipt"} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			return err
		}
		old := "p.owner_kind='IMPLEMENTATION'"
		if strings.Count(ddl, old) != 1 {
			return fmt.Errorf("%s owner contract changed", name)
		}
		ddl = strings.Replace(ddl, old, "p.owner_kind='EXECUTION'", 1)
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER "+sqlIdentifier(name)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	for name, condition := range map[string]string{
		"approved_execution_binding":           `AND NOT EXISTS(SELECT 1 FROM no_run_successor_eligibilities a JOIN tasks t ON t.id=a.task_id WHERE a.consumed_successor_execution_id IS NULL AND a.task_id=NEW.task_id AND a.task_definition_revision_id=NEW.task_definition_revision_id AND a.predecessor_execution_id=t.current_execution_id AND NEW.id LIKE 'execution_successor_%' AND t.phase='NO_RUN_SUCCESSOR_REQUIRED' AND t.status='ACTIVE')`,
		"execution_proposals_admitted_receipt": `AND NOT EXISTS(SELECT 1 FROM no_run_successor_eligibilities a JOIN tasks t ON t.id=a.task_id WHERE a.consumed_successor_execution_id=NEW.id AND a.task_id=NEW.task_id AND a.task_definition_revision_id=NEW.task_definition_revision_id AND a.predecessor_execution_id=t.current_execution_id AND t.phase='NO_RUN_SUCCESSOR_REQUIRED' AND t.status='ACTIVE')`,
	} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			return err
		}
		marker := "BEGIN SELECT RAISE"
		if strings.Count(ddl, marker) != 1 {
			return fmt.Errorf("%s trigger shape changed", name)
		}
		updated := strings.Replace(ddl, marker, condition+"\n"+marker, 1)
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER "+sqlIdentifier(name)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, updated); err != nil {
			return fmt.Errorf("recreate %s: %w", name, err)
		}
	}
	var pointerDDL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='task_current_execution_guard'`).Scan(&pointerDDL); err != nil {
		return err
	}
	old := `AND r.option IN ('REQUEST_CHANGES','REQUEST_REVERIFICATION','RETRY_IMPLEMENTATION')))`
	replacement := `AND r.option IN ('REQUEST_CHANGES','REQUEST_REVERIFICATION','RETRY_IMPLEMENTATION')) AND NOT EXISTS(SELECT 1 FROM no_run_successor_effect_authorities a WHERE a.task_id=OLD.id AND a.predecessor_execution_id=OLD.current_execution_id AND a.successor_execution_id=NEW.current_execution_id AND a.task_definition_revision_id=NEW.current_definition_revision_id AND OLD.phase='NO_RUN_SUCCESSOR_REQUIRED' AND NEW.phase='IMPLEMENTATION_PREPARING'))`
	if strings.Count(pointerDDL, old) != 1 {
		return fmt.Errorf("Task pointer guard shape changed")
	}
	pointerDDL = strings.Replace(pointerDDL, old, replacement, 1)
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER task_current_execution_guard`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, pointerDDL); err != nil {
		return err
	}
	return nil
}
