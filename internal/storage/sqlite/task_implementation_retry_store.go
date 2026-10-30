package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// The supplemental hash binds the exact submitted payload (including omitted
// instructions/configuration). The original Decision receipt remains authority.
func acceptanceContinuationHash(in wp.TaskAcceptanceInput) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return workflowHash(string(b)), nil
}
func checkAcceptanceContinuationReplay(ctx context.Context, tx *sql.Tx, in wp.TaskAcceptanceInput) error {
	hash, err := acceptanceContinuationHash(in)
	if err != nil {
		return err
	}
	var saved string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM task_acceptance_continuations WHERE decision_id=? AND request_id=? AND ((actor_kind=? AND actor_id=?) OR (actor_kind IS NULL AND actor_id IS NULL AND ai_session_id=?))`, in.DecisionID, in.RequestID, continuationActorKind(in), continuationActorID(in), in.AISessionID).Scan(&saved)
	if err != nil {
		return err
	}
	if saved != hash {
		return executionproposal.ErrConflict
	}
	return nil
}

func validateImplementationRetryTx(ctx context.Context, tx *sql.Tx, current task.Task, predecessor executionproposal.Proposal, recoveryRetry bool) error {
	if predecessor.Outcome != executionproposal.OutcomeFailed && predecessor.Outcome != executionproposal.OutcomeCanceled {
		return executionproposal.ErrConflict
	}
	if current.WorkSpec.ImplementationMode != task.ImplementationCodingAgent || predecessor.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return executionproposal.ErrConflict
	}
	if err := validateApprovedTaskTx(ctx, tx, current, recoveryRetry); err != nil {
		return err
	}
	// Runtime authority already fences unknown contexts, claims, cancellation,
	// and unsafe orphan dispositions. Require the exact terminal product Run too;
	// a terminal outcome or an agent's text cannot stand in for finalization.
	var closed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN feature_areas a ON a.id=r.feature_area_id
 WHERE r.id=? AND r.project_id=? AND r.task_id=? AND r.feature_area_id=? AND r.execution_proposal_id=?
 AND r.operation_kind='EXECUTION' AND r.operation_id=? AND r.purpose IN ('IMPLEMENTATION','VERIFICATION')
 AND r.status IN ('FAILED','INFRASTRUCTURE_FAILED','CANCELED') AND a.status='ACTIVE'
 AND (r.runtime_epoch IS NULL OR EXISTS(SELECT 1 FROM runtime_epochs epoch WHERE epoch.id=r.runtime_epoch))
 AND (r.workspace_id IS NULL OR EXISTS(SELECT 1 FROM workspaces w JOIN tasks t ON t.id=r.task_id
  WHERE w.id=r.workspace_id AND w.project_id=r.project_id AND w.feature_area_id=r.feature_area_id AND (w.repository_id=t.repository_id OR (w.repository_id IS NULL AND t.repository_id IS NULL AND json_extract(r.runtime_workset_json,'$.workspaceId')=w.id AND json_extract(r.runtime_workset_json,'$.taskDefinitionRevisionId')=?))))
 AND (EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)
  OR (r.thread_id IS NULL AND r.turn_id IS NULL AND r.prompt_sent_at IS NULL
   AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id)))
 AND EXISTS(SELECT 1 FROM tasks t JOIN review_approval_decisions d ON d.decision_id=t.review_approval_id AND d.review_id=t.current_review_id
 JOIN decision_resolution_receipts receipt ON receipt.decision_id=d.decision_id AND receipt.option='APPROVE'
 WHERE t.id=r.task_id AND d.task_id=t.id AND d.project_id=t.project_id AND receipt.project_id=t.project_id)
 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id AND i.advance_state<>'UNKNOWN' AND i.applied_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id AND b.advance_state<>'UNKNOWN'
  AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=b.run_id AND i.runtime_epoch=r.runtime_epoch AND i.applied_at IS NOT NULL)))`, terminalExecutionRunID(predecessor), current.ProjectID, current.ID, current.FeatureAreaID, predecessor.ID, predecessor.ID, predecessor.TaskDefinitionRevisionID).Scan(&closed)
	if err != nil {
		return err
	}
	if !closed {
		return executionproposal.ErrConflict
	}
	return nil
}

// Decision, optional instruction-only immutable revision, successor and current
// pointers commit in the existing acceptance transaction. No Run is dispatched
// here; execution_start retains its exact reservation and authority checks.
func continueTaskImplementationTx(ctx context.Context, tx *sql.Tx, current task.Task, predecessor executionproposal.Proposal, in wp.TaskAcceptanceInput, request decisionresolution.Request, payloadHash string, now time.Time) (string, decisionresolution.Receipt, error) {
	recovery, err := validateRecoveryContinuationTx(ctx, tx, current, predecessor, in)
	if err != nil {
		return "", decisionresolution.Receipt{}, err
	}
	source, err := getDefinitionRevision(ctx, tx, predecessor.TaskDefinitionRevisionID)
	if err != nil {
		return "", decisionresolution.Receipt{}, err
	}
	if source.TaskID != current.ID {
		return "", decisionresolution.Receipt{}, executionproposal.ErrConflict
	}
	work, verification, err := task.ApplyExecutionInstructions(source.Definition.WorkSpec, source.Definition.VerificationSpec, in.ExecutionInstructions)
	if err != nil {
		return "", decisionresolution.Receipt{}, err
	}
	definition := source.Definition
	definition.WorkSpec, definition.VerificationSpec = work, verification
	payload, hash, err := task.EncodeDefinition(definition)
	if err != nil {
		return "", decisionresolution.Receipt{}, err
	}
	revisionID := source.ID
	if hash != source.PayloadSHA256 {
		var ordinal int
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0)+1 FROM task_definition_revisions WHERE task_id=?`, current.ID).Scan(&ordinal); err != nil {
			return "", decisionresolution.Receipt{}, err
		}
		revisionID = fmt.Sprintf("task_definition_%s_%d_%s", current.ID, ordinal, hash[:16])
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_definition_revisions(id,task_id,ordinal,payload_json,payload_sha256,created_at) VALUES(?,?,?,?,?,?)`, revisionID, current.ID, ordinal, payload, hash, formatTime(now)); err != nil {
			return "", decisionresolution.Receipt{}, err
		}
	}
	_, successorID := continuationIDs(predecessor.ID, request.Option)
	receipt, err := insertDecisionResolutionTx(ctx, tx, request, current.ProjectID, decisionresolution.ResultExecution, successorID, now)
	if err != nil {
		return "", receipt, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_acceptance_continuations(decision_id,ai_session_id,actor_kind,actor_id,request_id,payload_sha256,source_revision_id,revision_id) VALUES(?,?,?,?,?,?,?,?)`, request.DecisionID, continuationSessionEvidence(predecessor, in), continuationActorKind(in), continuationActorID(in), request.RequestID, payloadHash, source.ID, revisionID); err != nil {
		return "", receipt, err
	}
	workJSON, err := encodeWorkSpec(work)
	if err != nil {
		return "", receipt, err
	}
	verificationJSON, err := encodeVerificationSpec(verification)
	if err != nil {
		return "", receipt, err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE tasks SET current_definition_revision_id=?,work_spec_json=?,verification_spec_json=?,phase='IMPLEMENTATION_READY',updated_at=? WHERE id=? AND status='ACTIVE' AND current_execution_id=? AND current_definition_revision_id=?`, revisionID, workJSON, verificationJSON, formatTime(now), current.ID, predecessor.ID, source.ID)
	if err != nil {
		return "", receipt, executionProposalConflict(err)
	}
	n, err := changed.RowsAffected()
	if err != nil {
		return "", receipt, err
	}
	if n != 1 {
		return "", receipt, executionproposal.ErrConflict
	}
	current.CurrentDefinitionRevisionID, current.Phase, current.WorkSpec, current.VerificationSpec = revisionID, task.PhaseImplementationReady, work, verification
	if err = insertAcceptanceSuccessorTx(ctx, tx, successorID, continuationSessionEvidence(predecessor, in), continuationActorKind(in), continuationActorID(in), current, predecessor, predecessor.TaskPlanID, revisionID, work, verification, in.NextRunRuntimeConfig, now); err != nil {
		return "", receipt, err
	}
	if err = persistRecoveryContinuationTx(ctx, tx, recovery, successorID, in, now); err != nil {
		return "", receipt, err
	}
	return successorID, receipt, nil
}

// ImplementationRetryEligibility is the pure read counterpart of a NEW retry.
// It never consumes a receipt, creates a successor, prepares a workspace or
// dispatches. Exact projected identities fence changes between separate reads.
func (s *WorkflowProgressionStore) ImplementationRetryEligibility(ctx context.Context, sessionID, taskID, areaID, revisionID, executionID string) (bool, string, error) {
	return s.implementationRetryEligibility(ctx, sessionID, "", taskID, areaID, revisionID, executionID)
}
func (s *WorkflowProgressionStore) ImplementationRetryEligibilityForOperator(ctx context.Context, project, taskID, areaID, revisionID, executionID string) (bool, string, error) {
	if err := localoperator.RequireProject(ctx, project); err != nil {
		return false, "", err
	}
	return s.implementationRetryEligibility(ctx, "", project, taskID, areaID, revisionID, executionID)
}
func (s *WorkflowProgressionStore) implementationRetryEligibility(ctx context.Context, sessionID, project, taskID, areaID, revisionID, executionID string) (bool, string, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()
	if project == "" {
		if err = instructionCaller(ctx, tx, sessionID); err != nil {
			if errors.Is(err, aisession.ErrScopeMismatch) {
				return false, "SESSION_SCOPE_MISMATCH", nil
			}
			return false, "", err
		}
		if err = tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, sessionID).Scan(&project); err != nil {
			return false, "", err
		}
	}
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, taskID))
	if err != nil {
		return false, "", err
	}
	predecessor, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, executionID))
	if err != nil {
		return false, "", err
	}
	if taskID == "" || areaID == "" || revisionID == "" || executionID == "" || current.ProjectID != project || predecessor.ProjectID != project || predecessor.TaskID != taskID || current.FeatureAreaID != areaID || current.CurrentDefinitionRevisionID != revisionID || predecessor.TaskDefinitionRevisionID != revisionID {
		return false, "TARGET_MISMATCH", nil
	}
	var handled bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM decision_resolution_receipts WHERE decision_id=?)`, decisionresolution.TaskAcceptanceDecisionID(executionID)).Scan(&handled); err != nil {
		return false, "", err
	}
	if handled {
		return false, "DECISION_ALREADY_RESOLVED", nil
	}
	if current.WorkSpec.ImplementationMode != task.ImplementationCodingAgent || predecessor.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return false, "CODING_AGENT_REQUIRED", nil
	}
	if available, reason, err := executionRuntimeEligibilityTx(ctx, tx, predecessor, s.runtimeAllowlist()); err != nil || !available {
		return available, reason, err
	}
	// A caller may supply a complete explicit configuration. Missing or retired
	// inherited configuration is an input requirement, never a safety exemption.
	requiresExplicitConfig := predecessor.RuntimeConfig.Model == "" || predecessor.RuntimeConfig.ReasoningEffort == ""
	if _, configErr := predecessor.RuntimeConfig.Normalize(s.runtimeAllowlist()); configErr != nil {
		requiresExplicitConfig = true
	}
	if err = validateAcceptanceEligibilityTx(ctx, tx, current, predecessor, false); err != nil {
		if errors.Is(err, executionproposal.ErrConflict) {
			return false, "EXECUTION_NOT_TERMINAL_OR_AUTHORITY_BUSY", nil
		}
		return false, "", err
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, current.FeatureAreaID); err != nil {
		return retryEligibilityFailure(err)
	}
	if err = validateImplementationRetryTx(ctx, tx, current, predecessor, false); err != nil {
		if errors.Is(err, executionproposal.ErrConflict) {
			return false, "IMPLEMENTATION_RETRY_NOT_ELIGIBLE", nil
		}
		return retryEligibilityFailure(err)
	}
	if requiresExplicitConfig {
		return true, "EXPLICIT_RUNTIME_CONFIG_REQUIRED", nil
	}
	return true, "", nil
}

func retryEligibilityFailure(err error) (bool, string, error) {
	var workflow *wp.Error
	if errors.As(err, &workflow) {
		return false, workflow.Code, nil
	}
	var file *directfile.Error
	if errors.As(err, &file) {
		return false, file.Code, nil
	}
	return false, "", err
}
