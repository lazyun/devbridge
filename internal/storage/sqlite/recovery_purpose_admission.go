package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
)

// insertRecoveryPurposeExecutionTx is private to the receipt-first Recovery
// consumer. An acceptance decision cannot use this authority, and the ordinary
// continuation writer continues to require a formal revision binding.
func insertRecoveryPurposeExecutionTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent, in rs.PurposeConsumeInput, current task.Task, predecessor executionproposal.Proposal, out rs.PurposeConsumption, authority runtimeselection.CurrentAuthority, now time.Time) error {
	p := intent.Content.RecoveryPurpose.Proposal
	if out.ExecutionID == "" || out.TaskDefinitionRevisionID == "" || out.TaskID != current.ID || current.ProjectID != intent.ProjectID || predecessor.ID != p.SourceExecutionID || predecessor.TaskID != current.ID || predecessor.TaskDefinitionRevisionID != p.TaskDefinitionRevisionID || predecessor.BackendSelection == nil || authority.Owner != (runtimeselection.Owner{ProjectID: intent.ProjectID, TaskID: current.ID, TaskDefinitionRevisionID: p.TaskDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"}) || authority.AuthoritySnapshotHash != runtimeselection.HashValue(authority.Values) {
		return rs.ErrPurposeReceiptMismatch
	}
	if err := requireRecoveryPurposeConsumptionTx(ctx, tx, intent, in, out); err != nil {
		return err
	}
	v := authority.Values
	backend := *predecessor.BackendSelection
	backend.ConfigID, backend.ConfigFingerprint, backend.Source = v.BackendIdentity, v.BackendConfigFingerprint, v.BackendSource
	if err := validateFeatureAreaBackendContinuityTx(ctx, tx, current.FeatureAreaID, &backend); err != nil {
		return err
	}
	candidate := executionproposal.Proposal{
		ID: out.ExecutionID, AISessionID: in.AISessionID, ProjectID: current.ProjectID, TaskID: current.ID,
		TaskPlanID: current.ConfirmedTaskPlanID, TaskDefinitionRevisionID: out.TaskDefinitionRevisionID,
		WorkSpec: current.WorkSpec, VerificationSpec: current.VerificationSpec,
		RuntimeConfig:    codexruntime.NextRunRuntimeConfig{Model: v.Model, ReasoningEffort: v.ReasoningEffort, CodexFastMode: v.CodexFastMode, ServiceTier: v.ServiceTier},
		BackendSelection: &backend, RuntimeAdapterIdentity: v.RuntimeAdapterIdentity, RuntimeProtocolIdentity: v.RuntimeProtocolIdentity,
	}
	if err := bindExecutionDefinitionTx(ctx, tx, &candidate); err != nil {
		return err
	}
	workJSON, err := encodeWorkSpec(candidate.WorkSpec)
	if err != nil {
		return err
	}
	verificationJSON, err := encodeVerificationSpec(candidate.VerificationSpec)
	if err != nil {
		return err
	}
	runtimeJSON, err := encodeExecutionRuntimeConfig(candidate.RuntimeConfig)
	if err != nil {
		return err
	}
	backendJSON, err := encodeBackendSelection(candidate.BackendSelection)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_proposals(id,ai_session_id,content_actor_kind,content_actor_id,project_id,task_id,task_plan_id,status,created_at,updated_at,confirmed_at,work_spec_json,verification_spec_json,outcome,task_definition_revision_id,runtime_config_json,backend_selection_json,primary_member_id,approved_workset_hash,runtime_adapter_identity,runtime_protocol_identity) VALUES(?,?,'AI_SESSION',?,?,?,?,'CONFIRMED',?,?,?,?,?,'PENDING',?,?,?,?,?,?,?)`, candidate.ID, in.AISessionID, in.AISessionID, candidate.ProjectID, candidate.TaskID, candidate.TaskPlanID, formatTime(now), formatTime(now), formatTime(now), workJSON, verificationJSON, candidate.TaskDefinitionRevisionID, runtimeJSON, backendJSON, candidate.PrimaryMemberID, candidate.ApprovedWorksetHash, candidate.RuntimeAdapterIdentity, candidate.RuntimeProtocolIdentity)
	if err != nil {
		return executionProposalConflict(err)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE tasks SET current_execution_id=? WHERE id=? AND project_id=? AND status='ACTIVE' AND phase='IMPLEMENTATION_READY' AND current_definition_revision_id=? AND current_execution_id=?`, candidate.ID, candidate.TaskID, candidate.ProjectID, candidate.TaskDefinitionRevisionID, p.SourceExecutionID)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return rs.ErrConflict
	}
	payload, err := event.Payload(map[string]any{"authorization_source": "RECOVERY_PURPOSE_CONFIRMATION", "confirmation_intent_id": intent.IntentID, "consumption_request_id": in.RequestID, "task_definition_revision_id": candidate.TaskDefinitionRevisionID})
	if err != nil {
		return err
	}
	return appendEvent(ctx, tx, event.Event{Type: event.ExecutionProposalCreated, ProjectID: candidate.ProjectID, TaskID: candidate.TaskID, EntityType: "execution_proposal", EntityID: candidate.ID, Payload: payload, CreatedAt: now})
}

// The consume receipt, exact successor and immutable Plan are one transaction.
// Preparation later uses this admission directly and never creates another Plan.
func admitRecoveryPurposeExecutionTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent, in rs.PurposeConsumeInput, out rs.PurposeConsumption, plan runtimepreparation.Plan, now time.Time) error {
	if plan.OwnerKind != runtimepreparation.OwnerExecution || plan.OwnerID != out.ExecutionID || plan.TaskID != out.TaskID || plan.TaskDefinitionRevisionID != out.TaskDefinitionRevisionID || plan.SourceCommand != in.Action || plan.SourceReceiptID != intent.IntentID || plan.SelectedVariantID != intent.Commit.VariantID || plan.ProposalHash != intent.Content.RecoveryPurpose.Proposal.SubmittedHash {
		return rs.ErrPurposeReceiptMismatch
	}
	if err := requireRecoveryPurposeConsumptionTx(ctx, tx, intent, in, out); err != nil {
		return err
	}
	candidate, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, out.ExecutionID))
	if err != nil {
		return err
	}
	b, r := candidate.BackendSelection, candidate.RuntimeConfig
	if candidate.Status != executionproposal.StatusConfirmed || candidate.Outcome != executionproposal.OutcomePending || candidate.RunID != "" || candidate.ProjectID != intent.ProjectID || candidate.TaskID != out.TaskID || candidate.TaskDefinitionRevisionID != out.TaskDefinitionRevisionID || b == nil || b.ConfigID != plan.BackendIdentity || b.ConfigFingerprint != plan.BackendConfigFingerprint || b.Source != plan.BackendSource || plan.BackendKind != "CODEX" || r.Model != plan.Model || r.ReasoningEffort != plan.ReasoningEffort || r.CodexFastMode != plan.CodexFastMode || r.ServiceTier != plan.ServiceTier || candidate.RuntimeAdapterIdentity != plan.RuntimeAdapterIdentity || candidate.RuntimeProtocolIdentity != plan.RuntimeProtocolIdentity {
		return rs.ErrPurposeReceiptMismatch
	}
	if err = requireExecutionStartAuthorizationTx(ctx, tx, candidate); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_proposal_admissions(proposal_id,admission_request_id,payload_sha256,source_confirmation_intent_id,task_id,task_definition_revision_id,runtime_scope_plan_id,runtime_preparation_authority_id,admitted_at) VALUES(?,?,?,?,?,?,?,?,?)`, candidate.ID, plan.PreparationRequestID, plan.PreparationPayloadHash, intent.IntentID, candidate.TaskID, candidate.TaskDefinitionRevisionID, plan.ID, plan.ID, formatTime(now))
	if err != nil {
		return executionProposalConflict(err)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET status='ADMITTED',updated_at=? WHERE id=? AND status='CONFIRMED' AND run_id IS NULL AND outcome='PENDING'`, formatTime(now), candidate.ID)
	if err != nil {
		return executionProposalConflict(err)
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return rs.ErrConflict
	}
	return nil
}

func requireRecoveryPurposeConsumptionTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent, in rs.PurposeConsumeInput, out rs.PurposeConsumption) error {
	var exact bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_consumptions c JOIN recovery_purpose_confirmation_receipts r ON r.intent_ai_session_id=c.intent_ai_session_id AND r.intent_id=c.intent_id AND r.intent_version=c.intent_version AND r.intent_content_hash=c.intent_content_hash AND r.purpose_link_id=c.purpose_link_id AND r.purpose_link_ordinal=c.purpose_link_ordinal WHERE c.intent_ai_session_id=? AND c.intent_id=? AND c.intent_version=? AND c.intent_content_hash=? AND c.consume_action=? AND c.consume_request_id=? AND c.consume_payload_hash=? AND c.successor_execution_id=? AND c.successor_task_definition_revision_id=? AND c.successor_run_id='' AND c.source_owner_kind IN ('IMPLEMENTATION','VERIFICATION') AND c.selected_variant_id=? AND c.proposal_hash=? AND r.outcome='COMMITTED' AND r.commit_request_id=? AND r.selected_variant_id=c.selected_variant_id AND r.proposal_hash=c.proposal_hash AND r.source_owner_kind=c.source_owner_kind)`, in.AISessionID, in.IntentID, in.Version, in.ContentHash, in.Action, in.RequestID, in.PayloadHash, out.ExecutionID, out.TaskDefinitionRevisionID, intent.Commit.VariantID, intent.Content.RecoveryPurpose.Proposal.SubmittedHash, intent.Commit.RequestID).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return rs.ErrPurposeReceiptMismatch
	}
	return nil
}
