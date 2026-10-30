package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// TaskLifecycleV88SQL adds the durable REVIEW_MATERIALIZING claim authority.
// Exactly one claim row exists per Review for its whole lifecycle: the
// MATERIALIZING claim commits in its own transaction, the deterministic
// materialization runs outside any transaction, and the finalize transaction
// CASes that exact claim to FINALIZED together with the formal materialization
// and its Decision, or to VOIDED when the durable facts were canceled first.
const TaskLifecycleV88SQL = `
CREATE TABLE review_result_materialization_claims(
 review_id TEXT PRIMARY KEY REFERENCES reviews(id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 state TEXT NOT NULL CHECK(state IN ('MATERIALIZING','FINALIZED','VOIDED')),
 raw_result_sha256 TEXT NOT NULL CHECK(length(raw_result_sha256)=64 AND raw_result_sha256 GLOB '[0-9a-f]*'),
 runtime_workset_hash TEXT NOT NULL CHECK(length(runtime_workset_hash)=64 AND runtime_workset_hash GLOB '[0-9a-f]*'),
 provenance_sha256 TEXT NOT NULL CHECK(length(provenance_sha256)=64 AND provenance_sha256 GLOB '[0-9a-f]*'),
 generation INTEGER NOT NULL CHECK(generation>0),
 claim_token TEXT NOT NULL UNIQUE CHECK(length(claim_token)>0 AND length(claim_token)<=128),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 finalized_at TEXT,
 outcome TEXT CHECK(outcome IS NULL OR outcome IN ('VALID','INVALID','SCOPE_CONFLICT')),
 decision_id TEXT REFERENCES review_approval_decisions(decision_id),
 CHECK(
  (state='MATERIALIZING' AND finalized_at IS NULL AND outcome IS NULL AND decision_id IS NULL) OR
  (state='FINALIZED' AND finalized_at IS NOT NULL AND outcome IS NOT NULL) OR
  (state='VOIDED' AND finalized_at IS NOT NULL AND outcome IS NULL AND decision_id IS NULL)
 )
);
CREATE TRIGGER review_materialization_claim_identity_immutable BEFORE UPDATE ON review_result_materialization_claims
WHEN NEW.review_id IS NOT OLD.review_id OR NEW.project_id IS NOT OLD.project_id OR NEW.task_id IS NOT OLD.task_id OR NEW.run_id IS NOT OLD.run_id OR NEW.raw_result_sha256 IS NOT OLD.raw_result_sha256 OR NEW.runtime_workset_hash IS NOT OLD.runtime_workset_hash OR NEW.provenance_sha256 IS NOT OLD.provenance_sha256 OR NEW.generation IS NOT OLD.generation OR NEW.claim_token IS NOT OLD.claim_token OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT,'Review materialization claim identity is immutable'); END;
CREATE TRIGGER review_materialization_claim_state_transition BEFORE UPDATE OF state ON review_result_materialization_claims
WHEN NOT ((OLD.state='MATERIALIZING' AND NEW.state IN ('FINALIZED','VOIDED')) OR OLD.state=NEW.state)
BEGIN SELECT RAISE(ABORT,'invalid Review materialization claim transition'); END;
CREATE TRIGGER review_materialization_claim_no_delete BEFORE DELETE ON review_result_materialization_claims
BEGIN SELECT RAISE(ABORT,'Review materialization claim is durable'); END;
CREATE TRIGGER review_materialization_claim_finalized_pairing BEFORE UPDATE OF state, outcome, decision_id ON review_result_materialization_claims
WHEN NEW.state='FINALIZED' AND NOT EXISTS(SELECT 1 FROM review_result_materializations m WHERE m.review_id=NEW.review_id AND m.outcome=NEW.outcome)
BEGIN SELECT RAISE(ABORT,'FINALIZED claim requires its formal Review result materialization'); END;
CREATE TRIGGER review_materialization_claim_source_fence BEFORE INSERT ON review_result_materialization_claims
WHEN EXISTS(SELECT 1 FROM review_result_materializations m WHERE m.review_id=NEW.review_id)
   OR NOT EXISTS(SELECT 1 FROM reviews v JOIN tasks t ON t.id=v.task_id JOIN runs r ON r.id=NEW.run_id WHERE v.id=NEW.review_id AND t.id=NEW.task_id AND t.project_id=NEW.project_id AND r.task_id=NEW.task_id AND r.project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'Review materialization claim requires its exact Review, Task and Run'); END;
DROP TRIGGER file_result_task_accept_guard;
CREATE TRIGGER file_result_task_accept_guard BEFORE UPDATE OF status ON tasks
WHEN NEW.status='SUCCEEDED' AND OLD.status='ACTIVE' AND json_extract(OLD.work_spec_json,'$.implementationMode')='DIRECT_FILE'
AND (OLD.phase<>'DELIVERING' OR NOT EXISTS(SELECT 1 FROM direct_file_implementation_acceptances a JOIN direct_file_implementation_results r ON r.id=a.result_id JOIN direct_file_task_authorizations g ON g.workflow_id=r.workflow_id WHERE r.task_id=OLD.id AND r.revision_id=OLD.current_definition_revision_id AND r.project_id=OLD.project_id AND g.state='CLOSED' AND g.decision_id=OLD.review_approval_id AND g.task_id=OLD.id AND g.task_definition_revision_id=r.revision_id AND a.ai_session_id=r.ai_session_id AND json_extract(OLD.verification_spec_json,'$.mode')='NONE'))
BEGIN SELECT RAISE(ABORT,'file result acceptance required'); END;
`

const (
	v88PhaseCheckOld = "CHECK(phase IN ('REVIEW_REQUIRED','REVIEWING','REVIEW_APPROVAL_PENDING','IMPLEMENTATION_READY','IMPLEMENTING','VERIFICATION_READY','VERIFYING','ACCEPTANCE_PENDING'))"
	v88PhaseCheckNew = "CHECK(phase IN ('REVIEW_REQUIRED','REVIEWING','REVIEW_APPROVAL_PENDING','IMPLEMENTATION_READY','IMPLEMENTING','VERIFICATION_READY','VERIFYING','ACCEPTANCE_PENDING','DELIVERING','RECOVERY_REQUIRED'))"
)

// v88TaskNormalizedStatus is the deterministic lifecycle normalizer applied to
// every non-legacy DEVELOPMENT row during the tasks rebuild. Terminal facts
// already proven by durable receipts are preserved; unsafe in-flight or
// contradictory facts land in RECOVERY_REQUIRED instead of a guessed
// completion; nothing else may remain outside the ACTIVE dual-axis contract.
const v88TaskNormalizedStatus = `CASE
  WHEN t.status IN ('SUCCEEDED','CANCELED') THEN t.status
  WHEN EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.task_id=t.id AND c.state='CANCELED' AND c.terminal_observed=1 AND c.resolved_at IS NOT NULL) THEN 'CANCELED'
  WHEN EXISTS(SELECT 1 FROM decision_resolution_receipts r WHERE r.decision_kind='TASK_ACCEPTANCE' AND r.result_kind='DEVELOPMENT_TASK' AND r.result_id=t.id AND r.option='ACCEPT') AND ` + v88DeliveryCompleteSQL + ` THEN 'SUCCEEDED'
  ELSE 'ACTIVE'
END`

const v88TaskNormalizedPhase = `CASE
  WHEN t.status IN ('SUCCEEDED','CANCELED') THEN t.phase
  WHEN EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.task_id=t.id AND c.state='CANCELED' AND c.terminal_observed=1 AND c.resolved_at IS NOT NULL) THEN t.phase
  WHEN EXISTS(SELECT 1 FROM decision_resolution_receipts r WHERE r.decision_kind='TASK_ACCEPTANCE' AND r.result_kind='DEVELOPMENT_TASK' AND r.result_id=t.id AND r.option='ACCEPT') AND ` + v88DeliveryCompleteSQL + ` THEN t.phase
  WHEN ` + v88UncertainFactsSQL + ` THEN 'RECOVERY_REQUIRED'
  WHEN EXISTS(SELECT 1 FROM decision_resolution_receipts r WHERE r.decision_kind='TASK_ACCEPTANCE' AND r.result_kind='DEVELOPMENT_TASK' AND r.result_id=t.id AND r.option='ACCEPT') AND t.phase='ACCEPTANCE_PENDING' THEN 'DELIVERING'
  ELSE t.phase
END`

// v88DeliveryCompleteSQL proves every frozen delivery receipt is present and
// terminal with a concrete commit; partial or errored delivery is never read
// as completion.
const v88DeliveryCompleteSQL = `(EXISTS(SELECT 1 FROM task_commit_receipts q WHERE q.task_id=t.id AND q.completed_at IS NOT NULL AND COALESCE(q.resulting_commit,'')<>'' AND COALESCE(q.last_error,'')='' AND NOT EXISTS(SELECT 1 FROM task_commit_members m WHERE m.task_id=q.task_id AND m.state NOT IN ('COMPLETED','NO_CHANGE')))
 OR (COALESCE(t.commit_sha,'')<>'' AND COALESCE(t.merged_commit,'')<>''))`

// v88UncertainFactsSQL enumerates runtime, cancellation, delivery and recovery
// facts that cannot be classified safely at rest.
const v88UncertainFactsSQL = `(t.status='FAILED'
 OR t.phase IN ('IMPLEMENTING','VERIFYING')
 OR EXISTS(SELECT 1 FROM recovery_status_records s JOIN runs sr ON sr.id=s.source_run_id WHERE sr.task_id=t.id AND s.state<>'RESOLVED')
 OR EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.task_id=t.id AND (c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') OR c.interrupt_state='OUTCOME_UNKNOWN' OR c.backend_disposition='UNKNOWN_ORPHANED' OR (c.state='CANCELED' AND (c.terminal_observed<>1 OR c.resolved_at IS NULL))))
 OR EXISTS(SELECT 1 FROM task_commit_receipts q WHERE q.task_id=t.id AND (COALESCE(q.last_error,'')<>'' OR EXISTS(SELECT 1 FROM task_commit_members m WHERE m.task_id=q.task_id AND m.state='UNKNOWN'))))`

// migrateTaskLifecycleV88 extends the durable Phase vocabulary with
// DELIVERING/RECOVERY_REQUIRED, adds the REVIEW_MATERIALIZING claim authority,
// and deterministically normalizes legacy DEVELOPMENT lifecycle rows while
// preserving every receipt and provenance byte.
func migrateTaskLifecycleV88(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'`).Scan(&ddl); err != nil {
		return err
	}
	if !strings.Contains(ddl, v88PhaseCheckOld) {
		return fmt.Errorf("tasks phase constraint is not the v45-v87 contract")
	}
	rebuilt := strings.Replace(ddl, v88PhaseCheckOld, v88PhaseCheckNew, 1)

	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA legacy_alter_table=OFF`)

	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name='tasks' AND sql IS NOT NULL)`)
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
	if _, err := tx.ExecContext(ctx, `ALTER TABLE tasks RENAME TO _source87_tasks`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, rebuilt); err != nil {
		return err
	}
	columns, err := schemaColumns(ctx, tx, "_source87_tasks")
	if err != nil {
		return err
	}
	projections := make([]string, 0, len(columns))
	quotedColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		quotedColumns = append(quotedColumns, sqlIdentifier(column))
		switch column {
		case "status":
			projections = append(projections, "(CASE WHEN t.task_type<>'DEVELOPMENT' OR t.legacy_read_only<>0 THEN t.status ELSE "+v88TaskNormalizedStatus+" END) AS "+sqlIdentifier(column))
		case "phase":
			projections = append(projections, "(CASE WHEN t.task_type<>'DEVELOPMENT' OR t.legacy_read_only<>0 THEN t.phase ELSE "+v88TaskNormalizedPhase+" END) AS "+sqlIdentifier(column))
		default:
			projections = append(projections, "t."+sqlIdentifier(column))
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO tasks("+strings.Join(quotedColumns, ",")+") SELECT "+strings.Join(projections, ",")+" FROM _source87_tasks AS t"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE _source87_tasks`); err != nil {
		return err
	}
	for _, object := range retained {
		if _, err := tx.ExecContext(ctx, object.sql); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, TaskLifecycleV88SQL); err != nil {
		return err
	}
	return nil
}
