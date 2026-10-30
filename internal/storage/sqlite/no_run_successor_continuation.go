package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/executionproposal"
)

// Aliases e and t are the current Execution and Task. The successor command
// grants preparation authority without manufacturing an ordinary start effect.
// Reuse that exact immutable lineage for both queue selection and dispatch.
const noRunSuccessorContinuationSQL = `EXISTS(
 SELECT 1 FROM no_run_successor_effect_authorities effect
 JOIN no_run_successor_command_receipts receipt ON receipt.successor_effect_authority_id=effect.authority_id
 JOIN execution_proposal_admissions admission ON admission.proposal_id=effect.successor_execution_id
 JOIN runtime_scope_plans plan ON plan.plan_id=effect.runtime_scope_plan_id
 JOIN runtime_preparation_authorities authority ON authority.plan_id=effect.runtime_preparation_authority_id
 JOIN execution_cancellations c ON c.project_id=effect.effective_cancellation_project_id AND c.request_id=effect.effective_cancellation_request_id
 WHERE effect.successor_execution_id=e.id AND effect.task_id=t.id AND effect.task_definition_revision_id=e.task_definition_revision_id
 AND receipt.outcome='SUCCESSOR_CREATED' AND receipt.task_id=t.id AND receipt.predecessor_execution_id=effect.predecessor_execution_id AND receipt.successor_execution_id=e.id
 AND admission.task_id=t.id AND admission.task_definition_revision_id=e.task_definition_revision_id AND admission.runtime_scope_plan_id=plan.plan_id AND admission.runtime_preparation_authority_id=authority.plan_id
 AND plan.owner_kind='EXECUTION' AND plan.owner_id=e.id AND plan.project_id=e.project_id AND plan.task_id=t.id
 AND plan.source_command='PREPARE_SUCCESSOR_EXECUTION' AND plan.source_receipt_id=receipt.request_id AND plan.source_execution_id=effect.predecessor_execution_id AND plan.successor_execution_id=e.id
 AND plan.preparation_request_id=admission.admission_request_id AND plan.preparation_payload_hash=admission.payload_sha256 AND receipt.payload_sha256=admission.payload_sha256
 AND plan.authority_snapshot_hash=effect.verified_current_authority_snapshot_hash
 AND authority.plan_id=plan.plan_id AND authority.generation=plan.generation
 AND authority.owner_kind='EXECUTION' AND authority.owner_id=e.id AND authority.disposition='CURRENT'
 AND c.owner_id=effect.predecessor_execution_id AND ` + settledNoRunExecutionCancellationSQL + `)`

func noRunSuccessorRunStartAuthorizedTx(ctx context.Context, tx *sql.Tx, v executionproposal.Proposal, runID string) (bool, error) {
	var authorized bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM execution_proposals e JOIN tasks t ON t.id=e.task_id JOIN feature_areas f ON f.id=t.feature_area_id
 JOIN runs r ON r.id=? AND r.execution_proposal_id=e.id AND r.project_id=e.project_id AND r.task_id=t.id
 JOIN runtime_handoffs h ON h.run_id=r.id
 WHERE e.id=? AND e.project_id=? AND e.task_id=? AND e.task_definition_revision_id=?
 AND t.project_id=e.project_id AND t.current_execution_id=e.id AND t.current_definition_revision_id=e.task_definition_revision_id
 AND t.status='ACTIVE' AND t.legacy_read_only=0 AND f.status='ACTIVE'
 AND e.status='CONSUMED' AND e.outcome='RUNNING' AND r.status='CREATED' AND h.state='REGISTERED'
 AND h.runtime_epoch=r.runtime_epoch AND h.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND ((r.id=e.run_id AND r.purpose='IMPLEMENTATION' AND t.phase IN ('IMPLEMENTATION_PREPARING','IMPLEMENTING'))
 OR (r.id=e.verification_run_id AND r.purpose='VERIFICATION' AND t.phase IN ('IMPLEMENTING','VERIFICATION_READY','VERIFYING') AND EXISTS(SELECT 1 FROM runs implementation WHERE implementation.id=e.run_id AND implementation.execution_proposal_id=e.id AND implementation.purpose='IMPLEMENTATION' AND implementation.status='SUCCEEDED')))
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(e.id,t.id))
 AND `+noRunSuccessorContinuationSQL+`)`, runID, v.ID, v.ProjectID, v.TaskID, v.TaskDefinitionRevisionID).Scan(&authorized)
	return authorized, err
}
