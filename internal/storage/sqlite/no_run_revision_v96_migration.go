package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The canceled predecessor retains its original revision even while a later
// Review approval replaces the Task definition. This predicate deliberately
// binds the cancellation to the eligibility's immutable revision, not the
// Task's advancing revision. No successor effect may have consumed it.
var noRunRevisionPredecessorSQL = `EXISTS(SELECT 1 FROM no_run_successor_eligibilities eligibility
 WHERE eligibility.task_id=t.id AND eligibility.predecessor_execution_id=t.current_execution_id
 AND eligibility.effective_cancellation_project_id=t.project_id AND eligibility.consumed_successor_execution_id IS NULL
 AND EXISTS(SELECT 1 FROM execution_cancellations settled WHERE settled.project_id=eligibility.effective_cancellation_project_id AND settled.request_id=eligibility.effective_cancellation_request_id AND settled.owner_type='EXECUTION' AND settled.owner_id=t.current_execution_id AND settled.task_id=t.id AND settled.state='CANCELED')
 AND ` + strings.ReplaceAll(noRunSuccessorEligibleSQL, "t.current_definition_revision_id", "eligibility.task_definition_revision_id") + `)`

// Starting a new Review is the existing durable revision workflow. Its exact
// ownership and increasing ordinal distinguish it from the original approval;
// STOP itself adds no new receipt or authority table.
const noRunRevisionReviewSQL = `EXISTS(SELECT 1 FROM execution_proposals predecessor
 JOIN task_definition_revisions source ON source.id=predecessor.task_definition_revision_id AND source.task_id=t.id
 JOIN review_approval_decisions prior ON prior.decision_id=json_extract(source.payload_json,'$.reviewApprovalId') AND prior.task_id=t.id AND prior.project_id=t.project_id
 JOIN reviews previous ON previous.id=prior.review_id AND previous.task_id=t.id AND previous.project_id=t.project_id
 JOIN reviews current ON current.id=t.current_review_id AND current.task_id=t.id AND current.project_id=t.project_id
 WHERE predecessor.id=t.current_execution_id AND predecessor.task_id=t.id AND predecessor.project_id=t.project_id AND current.ordinal>previous.ordinal)`

func noRunRevisionReviewPendingTx(ctx context.Context, tx *sql.Tx, taskID string) (bool, error) {
	var eligible bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN execution_proposals predecessor ON predecessor.id=t.current_execution_id
 WHERE t.id=? AND t.phase IN ('REVIEWING','REVIEW_APPROVAL_PENDING') AND t.current_definition_revision_id=predecessor.task_definition_revision_id AND `+noRunRevisionPredecessorSQL+` AND `+noRunRevisionReviewSQL+`)`, taskID).Scan(&eligible)
	return eligible, err
}

// A completed new approval authorizes replacing only the retained canceled
// predecessor. It does not reuse or consume the no-Run successor identity.
const noRunRevisionApprovedSQL = `t.phase='IMPLEMENTATION_READY' AND EXISTS(
 SELECT 1 FROM execution_proposals predecessor
 JOIN task_definition_revisions revision ON revision.id=t.current_definition_revision_id AND revision.task_id=t.id
 JOIN review_approval_materializations materialized ON materialized.task_id=t.id AND materialized.decision_id=t.review_approval_id AND materialized.initial_revision_id=revision.id
 JOIN review_approval_decisions approved ON approved.decision_id=materialized.decision_id AND approved.task_id=t.id AND approved.project_id=t.project_id AND approved.review_id=t.current_review_id
 JOIN decision_resolution_receipts receipt ON receipt.decision_id=approved.decision_id AND receipt.project_id=t.project_id AND receipt.option='APPROVE' AND receipt.result_kind='DEVELOPMENT_TASK' AND receipt.result_id=t.id
 WHERE predecessor.id=t.current_execution_id AND predecessor.task_id=t.id AND predecessor.project_id=t.project_id AND predecessor.task_definition_revision_id<>revision.id
 AND json_extract(revision.payload_json,'$.reviewApprovalId')=t.review_approval_id
 AND json_extract(revision.payload_json,'$.workSpec')=json_remove(json(t.work_spec_json),'$.version')
 AND json_extract(revision.payload_json,'$.verificationSpec')=json_remove(json(t.verification_spec_json),'$.version'))`

func noRunRevisionApprovedTx(ctx context.Context, tx *sql.Tx, taskID string) (bool, error) {
	var eligible bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t WHERE t.id=? AND `+noRunRevisionPredecessorSQL+` AND `+noRunRevisionReviewSQL+` AND `+noRunRevisionApprovedSQL+`)`, taskID).Scan(&eligible)
	return eligible, err
}

// NoRunRevisionStartEligible observes an exact retained predecessor after a new
// approval. Callers authenticate the actor; this read grants no start effect.
func (s *WorkflowProgressionStore) NoRunRevisionStartEligible(ctx context.Context, projectID, taskID, predecessorID, revisionID string) (bool, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE project_id=? AND id=? AND current_execution_id=? AND current_definition_revision_id=?)`, projectID, taskID, predecessorID, revisionID).Scan(&exact)
	if err != nil || !exact {
		return false, err
	}
	return noRunRevisionApprovedTx(ctx, tx, taskID)
}

// Add an exception around the complete previous predicate, including its OR
// branches. All previous approval, continuation and successor guards survive.
func addNoRunRevisionTriggerException(ctx context.Context, tx *sql.Tx, name, exception string) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
		return err
	}
	when, body := strings.Index(ddl, "WHEN "), strings.Index(ddl, "BEGIN SELECT RAISE")
	if when < 0 || body <= when || strings.Count(ddl, "BEGIN SELECT RAISE") != 1 {
		return fmt.Errorf("%s trigger shape changed", name)
	}
	updated := ddl[:when] + "WHEN (" + strings.TrimSpace(ddl[when+5:body]) + ") AND NOT (" + exception + ")\n" + ddl[body:]
	if _, err := tx.ExecContext(ctx, "DROP TRIGGER "+sqlIdentifier(name)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, updated)
	return err
}

func migrateNoRunRevisionV96(ctx context.Context, tx *sql.Tx) error {
	// The STOP transition changes exactly one Task column. Building the equality
	// list from the schema preserves every preexisting and future Task field.
	columns, err := schemaColumns(ctx, tx, "tasks")
	if err != nil {
		return err
	}
	var unchanged, pointerUnchanged []string
	for _, column := range columns {
		if column != "current_execution_id" {
			pointerUnchanged = append(pointerUnchanged, "NEW."+sqlIdentifier(column)+" IS OLD."+sqlIdentifier(column))
		}
		if column != "phase" {
			unchanged = append(unchanged, "NEW."+sqlIdentifier(column)+" IS OLD."+sqlIdentifier(column))
		}
	}
	stop := `NEW.phase='REVIEW_REQUIRED' AND OLD.status='ACTIVE' AND ` + strings.Join(unchanged, " AND ") + ` AND EXISTS(SELECT 1 FROM tasks t WHERE t.id=OLD.id AND ` + noRunSuccessorEligibleSQL + ` AND ` + noRunRevisionPredecessorSQL + `)`
	if err := addNoRunRevisionTriggerException(ctx, tx, "no_run_successor_task_exit", stop); err != nil {
		return err
	}
	depth := `EXISTS(SELECT 1 FROM tasks t JOIN reviews previous ON previous.id=t.current_review_id AND previous.task_id=t.id AND previous.project_id=t.project_id WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.phase='REVIEW_REQUIRED' AND t.required_review_depth IS NULL AND (NEW.review_depth='DEEP' OR (NEW.review_depth='FAST' AND previous.review_depth='FAST')) AND ` + noRunSuccessorEligibleSQL + ` AND ` + noRunRevisionPredecessorSQL + `)`
	if err := addNoRunRevisionTriggerException(ctx, tx, "review_minimum_depth_guard", depth); err != nil {
		return err
	}
	stable := `NEW.id=OLD.id AND NEW.project_id IS OLD.project_id AND NEW.feature_area_id IS OLD.feature_area_id AND NEW.repository_id IS OLD.repository_id AND NEW.module_id IS OLD.module_id AND NEW.codex_config_id IS OLD.codex_config_id AND NEW.base_commit IS OLD.base_commit AND NEW.problem_statement IS OLD.problem_statement AND NEW.status=OLD.status AND NEW.phase=OLD.phase AND NEW.current_execution_id IS OLD.current_execution_id AND NEW.current_review_id IS OLD.current_review_id AND OLD.phase='REVIEW_APPROVAL_PENDING'`
	approval := `EXISTS(SELECT 1 FROM tasks t JOIN execution_proposals predecessor ON predecessor.id=t.current_execution_id
 JOIN review_approval_decisions approved ON approved.task_id=t.id AND approved.project_id=t.project_id AND approved.review_id=t.current_review_id AND approved.decision_id=NEW.review_approval_id
 JOIN decision_resolution_receipts receipt ON receipt.decision_id=approved.decision_id AND receipt.project_id=t.project_id AND receipt.option='APPROVE' AND receipt.result_kind='DEVELOPMENT_TASK' AND receipt.result_id=t.id
 WHERE t.id=OLD.id AND t.current_definition_revision_id=predecessor.task_definition_revision_id AND ` + noRunRevisionPredecessorSQL + ` AND ` + noRunRevisionReviewSQL + `)`
	for _, name := range []string{"approved_task_source_immutable", "approved_task_semantics_immutable"} {
		if err := addNoRunRevisionTriggerException(ctx, tx, name, stable+` AND NEW.current_definition_revision_id IS OLD.current_definition_revision_id AND NEW.review_approval_id IS NOT OLD.review_approval_id AND `+approval); err != nil {
			return err
		}
	}
	revision := stable + ` AND NEW.review_approval_id=OLD.review_approval_id AND ` + approval + ` AND EXISTS(SELECT 1 FROM task_definition_revisions revision WHERE revision.id=NEW.current_definition_revision_id AND revision.task_id=NEW.id
 AND json_extract(revision.payload_json,'$.reviewApprovalId')=NEW.review_approval_id
 AND json_extract(revision.payload_json,'$.workSpec')=json_remove(json(NEW.work_spec_json),'$.version')
 AND json_extract(revision.payload_json,'$.verificationSpec')=json_remove(json(NEW.verification_spec_json),'$.version')
 AND NEW.goal=json_extract(revision.payload_json,'$.workSpec.goal')
 AND json_extract(NEW.constraints_json,'$.items')=json_extract(revision.payload_json,'$.workSpec.constraints'))`
	if err := addNoRunRevisionTriggerException(ctx, tx, "approved_task_revision_immutable", revision); err != nil {
		return err
	}
	approved := noRunRevisionPredecessorSQL + ` AND ` + noRunRevisionReviewSQL + ` AND ` + noRunRevisionApprovedSQL
	insert := `NEW.status='CONFIRMED' AND NEW.outcome='PENDING' AND NEW.run_id IS NULL AND NEW.verification_run_id IS NULL AND EXISTS(SELECT 1 FROM tasks t JOIN task_plans plan ON plan.id=NEW.task_plan_id AND plan.task_id=t.id AND plan.project_id=t.project_id AND plan.status='APPLIED'
 WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.confirmed_task_plan_id=plan.id AND NEW.task_definition_revision_id=t.current_definition_revision_id AND NEW.work_spec_json=t.work_spec_json AND NEW.verification_spec_json=t.verification_spec_json
 AND NOT EXISTS(SELECT 1 FROM execution_proposals candidate WHERE candidate.task_id=t.id AND candidate.task_definition_revision_id=t.current_definition_revision_id) AND ` + approved + `)`
	if err := addNoRunRevisionTriggerException(ctx, tx, "approved_execution_binding", insert); err != nil {
		return err
	}
	pointer := strings.Join(pointerUnchanged, " AND ") + ` AND EXISTS(SELECT 1 FROM tasks t JOIN execution_proposals candidate ON candidate.id=NEW.current_execution_id AND candidate.task_id=t.id AND candidate.project_id=t.project_id AND candidate.task_definition_revision_id=t.current_definition_revision_id AND candidate.task_plan_id=t.confirmed_task_plan_id AND candidate.status='CONFIRMED' AND candidate.outcome='PENDING' AND candidate.run_id IS NULL AND candidate.verification_run_id IS NULL WHERE t.id=OLD.id AND ` + approved + `)`
	return addNoRunRevisionTriggerException(ctx, tx, "task_current_execution_guard", pointer)
}
