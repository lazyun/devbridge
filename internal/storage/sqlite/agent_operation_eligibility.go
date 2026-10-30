package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
)

// CancelEligibility is the read-only counterpart of the authorization portion
// of ExecutionCancellationStore.Begin. It intentionally does not use Begin:
// observing a Card must never create an intent, lock a writer, or dispatch an
// interrupt.
func (s *AgentConversationStore) CancelEligibility(ctx context.Context, featureAreaID string) (agentconversation.Eligibility, error) {
	result := agentconversation.Eligibility{Reason: "No exact active Execution Run is eligible for cancellation."}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	claim, err := scanAgentClaim(tx.QueryRowContext(ctx, `SELECT feature_area_id,operation_kind,operation_id,run_id,created_at,COALESCE(runtime_epoch,'') FROM feature_area_agent_claims WHERE feature_area_id=?`, featureAreaID))
	if errors.Is(err, agentconversation.ErrNotFound) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if claim.Kind != agentconversation.Execution {
		result.Reason = "The current FeatureArea operation is not an Execution."
		return result, nil
	}
	r, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, claim.RunID))
	if errors.Is(err, sql.ErrNoRows) {
		result.Reason = "The claimed Execution Run no longer exists."
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if r.FeatureAreaID != featureAreaID || r.OperationKind != run.OperationExecution || r.OperationID != claim.OperationID || r.ExecutionProposalID != claim.OperationID || r.RuntimeEpoch != claim.RuntimeEpoch {
		result.Reason = "The current Execution claim does not match its durable Run."
		return result, nil
	}
	e, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, claim.OperationID))
	if errors.Is(err, sql.ErrNoRows) {
		result.Reason = "The claimed Execution no longer exists."
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if e.ProjectID != r.ProjectID || e.Status != executionproposal.StatusConsumed || e.ImplementationRunID == "" || e.Outcome != executionproposal.OutcomeRunning {
		result.Reason = "The Execution is not durably running."
		return result, nil
	}
	latest := e.VerificationRunID
	if latest == "" {
		latest = e.ImplementationRunID
	}
	if latest != r.ID || (r.Purpose != run.PurposeImplementation && r.Purpose != run.PurposeVerification) || r.Status != run.StatusRunning {
		result.Reason = "The claimed Run is not the exact active Implementation or Verification Run."
		return result, nil
	}
	if claim.RuntimeEpoch != s.db.epoch {
		result.Reason = "The Execution belongs to an older runtime and requires recovery."
		return result, nil
	}
	var conversationID string
	err = tx.QueryRowContext(ctx, `SELECT a.id FROM agent_operation_boundaries b JOIN agent_conversations a ON a.id=b.agent_conversation_id WHERE b.run_id=? AND b.quiesced=0 AND b.advance_state='UNKNOWN' AND b.thread_start_pending=0 AND a.ended_at IS NULL AND a.host_tool_contract_version=? AND a.feature_area_id=? AND rtrim(COALESCE(a.backend_thread_id,''))=rtrim(COALESCE(? ,''))`, r.ID, agentconversation.HostToolContractVersion, featureAreaID, r.ThreadID).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) || r.ThreadID == "" || r.TurnID == "" {
		result.Reason = "The exact backend conversation boundary is not active."
		return result, nil
	}
	if err != nil {
		return result, err
	}
	var unsafe, pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_operation_recovery_states WHERE run_id=?) OR EXISTS(SELECT 1 FROM agent_finalization_intents WHERE run_id=? AND (advance_state='UNKNOWN' OR quiesced=0)), EXISTS(SELECT 1 FROM execution_cancellations WHERE run_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))`, r.ID, r.ID, r.ID).Scan(&unsafe, &pending); err != nil {
		return result, err
	}
	if unsafe {
		result.Reason = "The exact Execution Run requires recovery before cancellation."
		return result, nil
	}
	if pending {
		result.Reason = "Cancellation has already been requested for the exact Run."
		return result, nil
	}
	result.Available, result.Reason = true, ""
	result.Target = &agentconversation.ActionTarget{FeatureAreaID: featureAreaID, ExecutionID: e.ID, OperationKind: claim.Kind, OperationID: claim.OperationID, RunID: claim.RunID, AgentConversationID: conversationID}
	return result, nil
}

// RecoveryEligibility mirrors AgentConversationStore.Recover's immutable
// target checks without taking its writer lock or mutating any recovery state.
func (s *AgentConversationStore) RecoveryEligibility(ctx context.Context, featureAreaID string) (agentconversation.Eligibility, error) {
	result := agentconversation.Eligibility{Reason: "No exact stranded Agent operation is eligible for recovery."}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	claim, err := scanAgentClaim(tx.QueryRowContext(ctx, `SELECT feature_area_id,operation_kind,operation_id,run_id,created_at,COALESCE(runtime_epoch,'') FROM feature_area_agent_claims WHERE feature_area_id=?`, featureAreaID))
	if errors.Is(err, agentconversation.ErrNotFound) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if claim.Kind == agentconversation.Review {
		executor, e := reviewClaimExecutorTx(ctx, tx, claim)
		if errors.Is(e, agentconversation.ErrConflict) {
			result.Reason = "The claimed Review has a missing, unknown, or conflicting executor identity."
			return result, nil
		}
		if e != nil {
			return result, e
		}
		if executor == review.ExecutorUpperLLM {
			if s.db.writer == nil || !s.db.writer.Held() {
				result.Reason = "Upper Review recovery requires the current Core writer service."
				return result, nil
			}
			facts, e := upperReviewRecoveryFactsTx(ctx, tx, claim, s.db.epoch)
			if e != nil {
				return result, e
			}
			if facts.reason != "" {
				result.Reason = facts.reason
				if facts.workerEnrolled {
					result.ReasonCode = agentconversation.ReasonUpperWorkerContinuationUnsupported
					result.Reason = upperWorkerContinuationGuidance
				}
				if claim.RuntimeEpoch == s.db.epoch && !facts.workerEnrolled {
					result.Prerequisite = "CORE_RESTART"
				}
				return result, nil
			}
			result.Available, result.Reason = true, ""
			result.Target = &agentconversation.ActionTarget{FeatureAreaID: featureAreaID, OperationKind: claim.Kind, OperationID: claim.OperationID, RunID: claim.RunID, ExpectedUpperVersion: facts.observedVersion()}
			return result, nil
		}
	} else if claim.Kind != agentconversation.Execution {
		result.Reason = "The claimed operation kind is unsupported for recovery."
		return result, nil
	}
	if claim.RuntimeEpoch == s.db.epoch {
		result.Prerequisite = "CORE_RESTART"
		result.Reason = "The operation belongs to the current runtime and cannot be recovered."
		return result, nil
	}
	facts, err := classifyAgentClaimTx(ctx, tx, claim)
	if err != nil {
		return result, err
	}
	legacyUnbound := claim.RuntimeEpoch == "" && facts.boundary.Advance == agentconversation.NoAdvance && facts.boundary.Quiesced && facts.conversation.BackendThreadID == ""
	if facts.category != "UNKNOWN" || !terminalAgentRun(facts.run.Status) || facts.conversation.ID == "" || (facts.boundary.Advance != agentconversation.Unknown && !legacyUnbound) {
		result.Reason = "The durable operation state does not meet exact recovery requirements."
		return result, nil
	}
	result.Available, result.Reason = true, ""
	result.Target = &agentconversation.ActionTarget{FeatureAreaID: featureAreaID, ExecutionID: facts.run.ExecutionProposalID, OperationKind: claim.Kind, OperationID: claim.OperationID, RunID: claim.RunID, AgentConversationID: facts.conversation.ID}
	return result, nil
}
