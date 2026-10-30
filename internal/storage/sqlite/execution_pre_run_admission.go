package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// ExecutionAdmission is the committed business authority before any Run or
// managed workspace exists. The request and plan are immutable replay keys.
type ExecutionAdmission struct {
	Proposal      executionproposal.Proposal
	PlanID        string
	RequestID     string
	PayloadSHA256 string
}

// AdmitExecution consumes an already confirmed, exact start decision. The
// transaction never allocates a Run, Workset, or runtime start authority.
func (s *ExecutionProposalStore) AdmitExecution(ctx context.Context, proposalID, requestID, payloadSHA256 string, now time.Time) (ExecutionAdmission, error) {
	if proposalID == "" || requestID == "" || len(payloadSHA256) != 64 {
		return ExecutionAdmission{}, executionproposal.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return ExecutionAdmission{}, err
	}
	defer tx.Rollback()
	admission, err := admitExecutionTx(ctx, tx, proposalID, requestID, payloadSHA256, now)
	if err != nil {
		return ExecutionAdmission{}, err
	}
	if err = tx.Commit(); err != nil {
		return ExecutionAdmission{}, err
	}
	return admission, nil
}

// admitExecutionTx lets a Task start record its exact decision effect and
// pre-Run authority in one business transaction. The caller must commit the
// transaction before it asks RuntimePreparation to allocate a Run.
func admitExecutionTx(ctx context.Context, tx *sql.Tx, proposalID, requestID, payloadSHA256 string, now time.Time) (ExecutionAdmission, error) {
	return admitExecutionWithPlatformTx(ctx, tx, proposalID, requestID, payloadSHA256, now, nil)
}

// The qualified entry is private to a formal business transaction which has
// independently authenticated its platform reference and complete source facts.
func admitQualifiedExecutionTx(ctx context.Context, tx *sql.Tx, proposalID, requestID, payloadSHA256 string, now time.Time, descriptor runtimeplatform.Descriptor) (ExecutionAdmission, error) {
	if descriptor.Validate() != nil || descriptor.RuntimeRole != "IMPLEMENTATION" {
		return ExecutionAdmission{}, runtimeplatform.ErrIdentityMismatch
	}
	return admitExecutionWithPlatformTx(ctx, tx, proposalID, requestID, payloadSHA256, now, &descriptor)
}
func admitExecutionWithPlatformTx(ctx context.Context, tx *sql.Tx, proposalID, requestID, payloadSHA256 string, now time.Time, descriptor *runtimeplatform.Descriptor) (ExecutionAdmission, error) {
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, proposalID))
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionAdmission{}, executionproposal.ErrNotFound
	}
	if err != nil {
		return ExecutionAdmission{}, err
	}
	var savedRequest, savedPayload, savedPlan string
	err = tx.QueryRowContext(ctx, `SELECT admission_request_id,payload_sha256,runtime_scope_plan_id FROM execution_proposal_admissions WHERE proposal_id=?`, proposalID).Scan(&savedRequest, &savedPayload, &savedPlan)
	if err == nil {
		if savedRequest != requestID || savedPayload != payloadSHA256 || value.Status != executionproposal.StatusAdmitted && value.Status != executionproposal.StatusConsumed {
			return ExecutionAdmission{}, executionproposal.ErrConflict
		}
		if descriptor != nil {
			adapter, protocol := qualifiedRuntimeIdentities(*descriptor)
			if value.RuntimeConfig.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 {
				adapter, protocol = agentpolicy.CooperativeAdapterIdentity, agentpolicy.CooperativeProtocolIdentity
			}
			var exact bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_scope_plans WHERE plan_id=? AND runtime_adapter_identity=? AND runtime_protocol_identity=? AND backend_kind='CODEX')`, savedPlan, adapter, protocol).Scan(&exact); err != nil {
				return ExecutionAdmission{}, err
			}
			if !exact {
				return ExecutionAdmission{}, executionproposal.ErrConflict
			}
		}
		return ExecutionAdmission{Proposal: value, PlanID: savedPlan, RequestID: requestID, PayloadSHA256: payloadSHA256}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ExecutionAdmission{}, err
	}
	if value.Status != executionproposal.StatusConfirmed || value.Outcome != executionproposal.OutcomePending || value.RunID != "" || value.TaskDefinitionRevisionID == "" {
		return ExecutionAdmission{}, executionproposal.ErrConflict
	}
	if err = requireExecutionStartAuthorizationTx(ctx, tx, value); err != nil {
		return ExecutionAdmission{}, err
	}
	var sourceDecisionID string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(decision_id,'') FROM execution_start_effects WHERE execution_id=? AND project_id=? AND task_id=? AND task_definition_revision_id=? AND request_id=? AND payload_sha256=? AND effect='START_AUTHORIZED' AND expected_phase='IMPLEMENTATION_READY'`, value.ID, value.ProjectID, value.TaskID, value.TaskDefinitionRevisionID, requestID, payloadSHA256).Scan(&sourceDecisionID)
	if err != nil {
		return ExecutionAdmission{}, executionproposal.ErrStartAuthorizationUnavailable
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, value.TaskID, value.ProjectID))
	if err != nil {
		return ExecutionAdmission{}, executionProposalConflict(err)
	}
	if err = validatePiResumeRequirementTx(ctx, tx, t.FeatureAreaID, value.RuntimeConfig, value.VerificationSpec.Mode); err != nil {
		return ExecutionAdmission{}, err
	}
	scalar := len(t.RepositoryTargets) == 0 && t.RepositoryID != ""
	multiRepo := len(t.RepositoryTargets) > 0
	if !scalar && !multiRepo || t.CurrentExecutionID != value.ID || t.CurrentDefinitionRevisionID != value.TaskDefinitionRevisionID || t.Status != task.StatusActive || t.Phase != task.PhaseImplementationReady {
		return ExecutionAdmission{}, executionproposal.ErrConflict
	}
	if multiRepo && (t.PrimaryMemberID != value.PrimaryMemberID || value.BackendSelection == nil) {
		return ExecutionAdmission{}, executionproposal.ErrConflict
	}
	if scalar && value.RunID != "" {
		return ExecutionAdmission{}, executionproposal.ErrConflict
	}
	topology, contextHash, err := purposeProjectContextTx(ctx, tx, value.ProjectID)
	if err != nil {
		return ExecutionAdmission{}, err
	}
	heads := map[string]string{}
	refs := map[string]string{}
	scopes := map[string]workset.WriteScope{}
	var targets []workset.Target
	var primaryMemberID, backendIdentity, model, reasoningEffort, adapterIdentity string
	if multiRepo {
		for _, target := range t.RepositoryTargets {
			heads[target.MemberID] = target.OriginCommit
			refs[target.MemberID] = target.OriginRef
			scopes[target.MemberID] = target.Write
		}
		targets = workset.CloneTargets(t.RepositoryTargets)
		primaryMemberID = t.PrimaryMemberID
		backendIdentity = value.BackendSelection.ConfigID
		model = value.RuntimeConfig.Model
		reasoningEffort = value.RuntimeConfig.ReasoningEffort
		adapterIdentity = value.RuntimeAdapterIdentity
	} else {
		memberID := t.RepositoryID
		heads[memberID] = t.BaseCommit
		scopes[memberID] = workset.WriteScope{Kind: "REPOSITORY"}
		targets = []workset.Target{{RepositoryID: t.RepositoryID, MemberID: memberID, MemberVersion: 1, OriginCommit: t.BaseCommit, Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}}}
		primaryMemberID = memberID
		if value.BackendSelection != nil {
			backendIdentity = value.BackendSelection.ConfigID
		} else {
			backendIdentity = "scalar:" + t.RepositoryID
		}
		model = value.RuntimeConfig.Model
		if model == "" {
			model = "scalar-default"
		}
		reasoningEffort = value.RuntimeConfig.ReasoningEffort
		if reasoningEffort == "" {
			reasoningEffort = "medium"
		}
		adapterIdentity = value.RuntimeAdapterIdentity
		if adapterIdentity == "" {
			adapterIdentity = "codex-app-server-v1"
		}
	}
	backendFingerprint, backendSource, backendKind := workflowHash(backendIdentity), "SCALAR", "SCALAR"
	if value.BackendSelection != nil {
		backendFingerprint, backendSource, backendKind = value.BackendSelection.ConfigFingerprint, value.BackendSelection.Source, "CODEX_CONFIG"
		if backendSource == "" {
			backendSource = "CORE_SELECTION"
		}
	}
	protocolIdentity := value.RuntimeProtocolIdentity
	if protocolIdentity == "" && scalar {
		protocolIdentity = executionproposal.RuntimeProtocolCodex0149
	}
	if descriptor != nil {
		if !multiRepo || value.BackendSelection == nil {
			return ExecutionAdmission{}, executionproposal.ErrConflict
		}
		adapterIdentity, protocolIdentity = qualifiedRuntimeIdentities(*descriptor)
		if value.RuntimeConfig.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 {
			adapterIdentity, protocolIdentity = agentpolicy.CooperativeAdapterIdentity, agentpolicy.CooperativeProtocolIdentity
		}
		if value.RuntimeAdapterIdentity != adapterIdentity || value.RuntimeProtocolIdentity != protocolIdentity {
			return ExecutionAdmission{}, runtimeplatform.ErrIdentityMismatch
		}
		backendKind = "CODEX"
	}
	if value.RuntimeConfig.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 && (adapterIdentity != agentpolicy.CooperativeAdapterIdentity || protocolIdentity != agentpolicy.CooperativeProtocolIdentity) {
		return ExecutionAdmission{}, runtimeplatform.ErrIdentityMismatch
	}
	plan := runtimepreparation.Plan{ID: "runtime_scope_execution_" + workflowHash(proposalID + "\x00" + requestID)[:32], Generation: 1, OwnerKind: runtimepreparation.OwnerExecution, OwnerID: value.ID, ProjectID: value.ProjectID, FeatureAreaID: t.FeatureAreaID, TaskID: value.TaskID, SourceCommand: "EXECUTION_ADMISSION", SourceReceiptID: requestID, TaskDefinitionRevisionID: value.TaskDefinitionRevisionID, RepositoryTargets: targets, PrimaryMemberID: primaryMemberID, BackendIdentity: backendIdentity, BackendConfigFingerprint: backendFingerprint, BackendSource: backendSource, BackendKind: backendKind, Model: model, ReasoningEffort: reasoningEffort, CodexFastMode: value.RuntimeConfig.CodexFastMode, ServiceTier: value.RuntimeConfig.ServiceTier, AgentID: value.RuntimeConfig.AgentID, AgentConfigFingerprint: value.RuntimeConfig.AgentConfigFingerprint, ExecutionPolicy: value.RuntimeConfig.ExecutionPolicy, RuntimeAdapterIdentity: adapterIdentity, RuntimeProtocolIdentity: protocolIdentity, TopologyRevision: topology, DevelopmentContextHash: contextHash, DevelopmentRefs: refs, ExpectedSourceHEADs: heads, WriteScopes: scopes, PreparationRequestID: requestID, PreparationPayloadHash: payloadSHA256, CreatedAt: now.UTC()}
	plan.AuthoritySnapshotHash, err = plan.ComputeAuthoritySnapshotHash()
	if err != nil {
		return ExecutionAdmission{}, err
	}
	plan.ContentHash, err = plan.ComputeContentHash()
	if err != nil {
		return ExecutionAdmission{}, err
	}
	if err = freezeRuntimeScopePlanTx(ctx, tx, plan); err != nil {
		return ExecutionAdmission{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_proposal_admissions(proposal_id,admission_request_id,payload_sha256,source_decision_id,task_id,task_definition_revision_id,runtime_scope_plan_id,runtime_preparation_authority_id,admitted_at) VALUES(?,?,?,?,?,?,?,?,?)`, value.ID, requestID, payloadSHA256, sourceDecisionID, value.TaskID, value.TaskDefinitionRevisionID, plan.ID, plan.ID, formatTime(now))
	if err != nil {
		return ExecutionAdmission{}, executionProposalConflict(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET status='ADMITTED',updated_at=? WHERE id=? AND status='CONFIRMED' AND run_id IS NULL AND outcome='PENDING'`, formatTime(now), value.ID)
	if err != nil {
		return ExecutionAdmission{}, executionProposalConflict(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ExecutionAdmission{}, executionproposal.ErrConflict
	}
	value.Status = executionproposal.StatusAdmitted
	value.UpdatedAt = now.UTC()
	return ExecutionAdmission{Proposal: value, PlanID: plan.ID, RequestID: requestID, PayloadSHA256: payloadSHA256}, nil
}

func qualifiedRuntimeIdentities(d runtimeplatform.Descriptor) (string, string) {
	a, _ := json.Marshal(map[string]string{"adapterId": d.AdapterID, "adapterImplementationDigest": d.AdapterImplementationDigest})
	p, _ := json.Marshal(map[string]string{"protocolId": d.ProtocolID, "protocolSchemaDigest": d.ProtocolSchemaDigest})
	return string(a), string(p)
}

// CanonicalExecutionPreparation proves that a committed admission was
// consumed by the exact Core Run attempt. It does not imply start authority;
// workspace observation and binding are still required before dispatch.
func (s *ExecutionProposalStore) CanonicalExecutionPreparation(ctx context.Context, proposalID, runID string) (bool, error) {
	if proposalID == "" || runID == "" {
		return false, executionproposal.ErrInvalidArgument
	}
	var canonical bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposal_admissions a JOIN runtime_preparation_attempts attempt ON attempt.plan_id=a.runtime_scope_plan_id AND attempt.request_id=a.admission_request_id AND attempt.payload_hash=a.payload_sha256 JOIN execution_proposals e ON e.id=a.proposal_id JOIN runs r ON r.id=attempt.run_id WHERE a.proposal_id=? AND attempt.run_id=? AND r.execution_proposal_id=e.id AND e.status='CONSUMED' AND e.run_id=r.id)`, proposalID, runID).Scan(&canonical)
	return canonical, err
}
