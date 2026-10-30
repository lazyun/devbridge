package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"reflect"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
)

// ReadExecutionAuthority adds the purpose-consumption path to execution launch,
// without turning a Recovery receipt into a formal runtime-selection producer.
// ReadCurrent retains its original, closed formal-source contract.
func (s *RuntimeSelectionStore) ReadExecutionAuthority(ctx context.Context, owner runtimeselection.Owner, executionID string) (runtimeselection.CurrentAuthority, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return runtimeselection.CurrentAuthority{}, err
	}
	defer tx.Rollback()
	v, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, executionID))
	if err != nil {
		return runtimeselection.CurrentAuthority{}, err
	}
	if executionAuthorityOwner(v) != owner {
		return runtimeselection.CurrentAuthority{}, runtimeselection.ErrCorrupt
	}
	return readCurrentExecutionAuthorityTx(ctx, tx, v)
}

func executionAuthorityOwner(v executionproposal.Proposal) runtimeselection.Owner {
	return runtimeselection.Owner{ProjectID: v.ProjectID, TaskID: v.TaskID, TaskDefinitionRevisionID: v.TaskDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"}
}

func executionAuthorityValues(v executionproposal.Proposal) (runtimeselection.AuthorityValues, error) {
	if v.BackendSelection == nil {
		return runtimeselection.AuthorityValues{}, runtimeselection.ErrBindingMissing
	}
	b, r := v.BackendSelection, v.RuntimeConfig
	values := runtimeselection.AuthorityValues{BackendIdentity: b.ConfigID, BackendConfigFingerprint: b.ConfigFingerprint, BackendSource: b.Source, BackendKind: "CODEX", Model: r.Model, ReasoningEffort: r.ReasoningEffort, CodexFastMode: r.CodexFastMode, ServiceTier: r.ServiceTier, AgentID: r.AgentID, AgentConfigFingerprint: r.AgentConfigFingerprint, ExecutionPolicy: r.ExecutionPolicy, RuntimeAdapterIdentity: v.RuntimeAdapterIdentity, RuntimeProtocolIdentity: v.RuntimeProtocolIdentity}
	if agentpolicy.ValidateFrozenSelection(values.AgentID, values.ExecutionPolicy, values.AgentConfigFingerprint) != nil || values.BackendIdentity == "" || len(values.BackendConfigFingerprint) != 64 || values.BackendSource == "" || values.Model == "" || values.ReasoningEffort == "" || values.RuntimeAdapterIdentity == "" || values.RuntimeProtocolIdentity == "" || (values.CodexFastMode && values.ServiceTier != "priority") || (!values.CodexFastMode && values.ServiceTier != "") {
		return values, runtimeselection.ErrCorrupt
	}
	if values.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 && (values.RuntimeAdapterIdentity != agentpolicy.CooperativeAdapterIdentity || values.RuntimeProtocolIdentity != agentpolicy.CooperativeProtocolIdentity) {
		return values, runtimeselection.ErrCorrupt
	}
	return values, nil
}

func readCurrentExecutionAuthorityTx(ctx context.Context, q definitionQuerier, v executionproposal.Proposal) (runtimeselection.CurrentAuthority, error) {
	owner := executionAuthorityOwner(v)
	if err := runtimeSelectionOwnerCurrentTx(ctx, q, owner); err != nil {
		return runtimeselection.CurrentAuthority{}, err
	}
	var current bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t WHERE t.id=? AND t.project_id=? AND t.current_execution_id=? AND t.status='ACTIVE' AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(t.id,?)))`, v.TaskID, v.ProjectID, v.ID, v.ID).Scan(&current)
	if err != nil {
		return runtimeselection.CurrentAuthority{}, err
	}
	if !current {
		return runtimeselection.CurrentAuthority{}, runtimeselection.ErrNotCurrentRevision
	}
	a, found, err := readRecoveryExecutionAuthorityTx(ctx, q, v, map[string]bool{})
	if err != nil {
		return a, err
	}
	if found {
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposal_admissions a JOIN runtime_preparation_authorities auth ON auth.plan_id=a.runtime_preparation_authority_id WHERE a.proposal_id=? AND auth.disposition='CURRENT')`, v.ID).Scan(&current); err != nil {
			return a, err
		}
		if !current {
			return runtimeselection.CurrentAuthority{}, runtimeselection.ErrNotCurrentRevision
		}
		return a, err
	}
	return readCurrentRuntimeSelectionTx(ctx, q, owner)
}

// The source is historical after consumption. Re-read its immutable original
// binding (or earlier purpose chain), never the latest platform qualification.
func recoveryPurposeSourceAuthorityTx(ctx context.Context, q definitionQuerier, intent confirmation.Intent) (runtimeselection.CurrentAuthority, error) {
	return recoveryPurposeSourceAuthoritySeenTx(ctx, q, intent, map[string]bool{}, true)
}

func recoveryPurposeSourceAuthoritySeenTx(ctx context.Context, q definitionQuerier, intent confirmation.Intent, seen map[string]bool, verifyPlatform bool) (runtimeselection.CurrentAuthority, error) {
	var empty runtimeselection.CurrentAuthority
	if intent.Content.RecoveryPurpose == nil {
		return empty, rs.ErrPurposeReceiptMismatch
	}
	if intent.Commit != nil && (intent.Commit.Selection.RuntimeConfig != nil || intent.Commit.Selection.Backend != nil || intent.Commit.BackendSelection != nil) {
		return empty, rs.ErrPurposeReceiptMismatch
	}
	p := intent.Content.RecoveryPurpose.Proposal
	if p.OwnerKind != "IMPLEMENTATION" && p.OwnerKind != "VERIFICATION" {
		return empty, rs.ErrPurposeOwnerMismatch
	}
	v, err := scanExecutionProposal(q.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, p.SourceExecutionID))
	if err != nil {
		return empty, err
	}
	source, err := scanRun(q.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, p.SourceRunID))
	if err != nil {
		return empty, err
	}
	w := source.RuntimeWorkset
	if v.ProjectID != intent.ProjectID || v.TaskID != p.TaskID || v.TaskDefinitionRevisionID != p.TaskDefinitionRevisionID || v.Status != executionproposal.StatusConsumed || source.ProjectID != intent.ProjectID || source.TaskID != p.TaskID || source.FeatureAreaID != p.FeatureAreaID || source.OperationID != v.ID || string(source.Purpose) != p.OwnerKind || source.TurnID != p.SourceTurnID || source.RuntimeEpoch != p.RuntimeEpoch || w == nil || w.Validate() != nil || w.ContentHash != p.RuntimeWorksetHash || w.ProjectID != intent.ProjectID || w.TaskID != p.TaskID || w.ExecutionID != v.ID || w.TaskDefinitionRevisionID != v.TaskDefinitionRevisionID {
		return empty, rs.ErrPurposeSourceWorksetChanged
	}
	if p.OwnerID != v.ID || p.SourceOperationID != v.ID || string(source.OperationKind) != "EXECUTION" || w.RunID != source.ID || w.FeatureAreaID != p.FeatureAreaID || p.OwnerKind == "IMPLEMENTATION" && v.RunID != source.ID || p.OwnerKind == "VERIFICATION" && v.VerificationRunID != source.ID {
		return empty, rs.ErrPurposeSourceWorksetChanged
	}
	var raw, hash string
	if err = q.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, v.TaskDefinitionRevisionID, v.TaskID).Scan(&raw, &hash); err != nil {
		return empty, err
	}
	d, err := task.DecodeDefinition(raw, hash)
	if err != nil {
		return empty, err
	}
	// Legacy scalar executions predate adapter columns. Only their established
	// adapter family can fill those omissions; a Workset cannot choose a launcher.
	if d.Version == 1 {
		if v.RuntimeAdapterIdentity == "" {
			v.RuntimeAdapterIdentity = executionproposal.RuntimeAdapterCodexAppServerV1
		}
		if v.RuntimeProtocolIdentity == "" {
			v.RuntimeProtocolIdentity = executionproposal.RuntimeProtocolCodex0149
		}
	}
	values, err := executionAuthorityValues(v)
	if err != nil {
		return empty, err
	}
	if w.BackendConfigID != values.BackendIdentity || w.BackendConfigFingerprint != values.BackendConfigFingerprint || w.Model != values.Model || w.ReasoningEffort != values.ReasoningEffort || w.ServiceTier != values.ServiceTier || w.AgentID != values.AgentID || w.AgentConfigFingerprint != values.AgentConfigFingerprint || w.ExecutionPolicy != values.ExecutionPolicy || w.RuntimeAdapterIdentity != values.RuntimeAdapterIdentity || w.RuntimeProtocolIdentity != values.RuntimeProtocolIdentity {
		return empty, rs.ErrPurposeSourceWorksetChanged
	}
	// Recovery has no backend/runtime selection controls. A recommendation may
	// describe the locked values, but cannot authorize a different configuration.
	if r := intent.Content.RecommendedRuntimeConfig; r != nil && *r != v.RuntimeConfig {
		return empty, rs.ErrPurposeReceiptMismatch
	}
	if r := p.RecommendedRuntimeConfig; r != nil && *r != v.RuntimeConfig {
		return empty, rs.ErrPurposeReceiptMismatch
	}
	a, found, err := readRecoveryExecutionEvidenceTx(ctx, q, v, seen, verifyPlatform)
	if err != nil {
		return empty, err
	}
	if !found {
		owner := executionAuthorityOwner(v)
		b, readErr := readRuntimeSelectionBindingTx(ctx, q, "project_id=? AND task_id=? AND revision_id=? AND runtime_role=?", owner.ProjectID, owner.TaskID, owner.TaskDefinitionRevisionID, owner.RuntimeRole)
		if readErr == nil {
			if verifyPlatform {
				if _, err = validateRuntimeSelectionPlatformTx(ctx, q, b.Platform, owner.RuntimeRole, false); err != nil {
					return empty, err
				}
			}
			a = runtimeselection.CurrentAuthority{Owner: owner, BindingID: b.BindingID, Platform: b.Platform, Values: b.Values, AuthoritySnapshotHash: b.AuthoritySnapshotHash}
		} else if errors.Is(readErr, runtimeselection.ErrBindingMissing) && d.Version == 1 && v.RuntimeAdapterIdentity == executionproposal.RuntimeAdapterCodexAppServerV1 && v.RuntimeProtocolIdentity == executionproposal.RuntimeProtocolCodex0149 {
			// Preserve the historical scalar path; no qualification is invented.
			a = runtimeselection.CurrentAuthority{Owner: owner, Values: values, AuthoritySnapshotHash: runtimeselection.HashValue(values)}
		} else {
			return empty, readErr
		}
	}
	if a.Values != values {
		return empty, runtimeselection.ErrCorrupt
	}
	return a, nil
}

// Every edge is checked independently. A command name, content hash, or copied
// ten-field snapshot alone grants no purpose authority. The visited set rejects
// corrupt cycles while permitting multiple legitimate Recovery revisions.
func readRecoveryExecutionAuthorityTx(ctx context.Context, q definitionQuerier, v executionproposal.Proposal, seen map[string]bool) (runtimeselection.CurrentAuthority, bool, error) {
	return readRecoveryExecutionEvidenceTx(ctx, q, v, seen, true)
}

// Completed source resolution remains a historical fact after a platform is
// revoked. Only launch and new admission apply its current lifecycle gate.
func readRecoveryExecutionEvidenceTx(ctx context.Context, q definitionQuerier, v executionproposal.Proposal, seen map[string]bool, verifyPlatform bool) (runtimeselection.CurrentAuthority, bool, error) {
	var empty runtimeselection.CurrentAuthority
	var in rs.PurposeConsumeInput
	var owner, variantID, proposalHash, revision, reviewID, runID, linkID string
	var ordinal int
	err := q.QueryRowContext(ctx, `SELECT intent_ai_session_id,intent_id,intent_version,intent_content_hash,consume_action,consume_request_id,consume_payload_hash,source_owner_kind,selected_variant_id,proposal_hash,successor_task_definition_revision_id,successor_review_id,successor_run_id,purpose_link_id,purpose_link_ordinal FROM recovery_purpose_confirmation_consumptions WHERE successor_execution_id=?`, v.ID).Scan(&in.AISessionID, &in.IntentID, &in.Version, &in.ContentHash, &in.Action, &in.RequestID, &in.PayloadHash, &owner, &variantID, &proposalHash, &revision, &reviewID, &runID, &linkID, &ordinal)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, nil
	}
	if err != nil {
		return empty, true, err
	}
	fail := func() (runtimeselection.CurrentAuthority, bool, error) {
		return empty, true, rs.ErrPurposeReceiptMismatch
	}
	if seen[v.ID] {
		return fail()
	}
	seen[v.ID] = true
	defer delete(seen, v.ID)
	action := rs.ApplyConfirmedImplementationPurpose
	if owner == "VERIFICATION" {
		action = rs.ApplyConfirmedVerificationPurpose
	} else if owner != "IMPLEMENTATION" {
		return fail()
	}
	if in.Action != action || in.PayloadHash != in.ComputePayloadHash() || revision != v.TaskDefinitionRevisionID || reviewID != "" || runID != "" || linkID == "" || ordinal < 1 {
		return fail()
	}
	identity := workflowHash(in.IntentID + "\x00" + in.RequestID)[:32]
	if v.ID != "execution_recovery_purpose_"+identity || v.TaskPlanID != "task_plan_recovery_purpose_"+identity || v.AISessionID != in.AISessionID {
		return fail()
	}
	var raw, prepareHash, project string
	err = q.QueryRowContext(ctx, `SELECT intent_json,prepare_hash,project_id FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, in.IntentID, in.AISessionID).Scan(&raw, &prepareHash, &project)
	if err != nil {
		return empty, true, err
	}
	var intent confirmation.Intent
	if json.Unmarshal([]byte(raw), &intent) != nil {
		return fail()
	}
	content, _ := json.Marshal(intent.Content)
	if intent.IntentID != in.IntentID || intent.AISessionID != in.AISessionID || intent.Version != in.Version || intent.ContentHash != in.ContentHash || prepareHash != in.ContentHash || workflowHash(string(content)) != in.ContentHash || intent.ProjectID != project || project != v.ProjectID || intent.Purpose != confirmation.RecoveryPurpose || intent.Action != confirmation.ConfirmRecoveryPurpose || intent.State != "COMPLETED" || intent.Commit == nil || intent.Commit.VariantID != variantID || intent.Content.RecoveryPurpose == nil {
		return fail()
	}
	p := intent.Content.RecoveryPurpose.Proposal
	if p.OwnerKind != owner || p.OwnerID != p.SourceExecutionID || p.SourceAISessionID != intent.AISessionID || p.TaskID != v.TaskID || p.SubmittedHash != proposalHash || p.ComputeSubmittedHash() != proposalHash {
		return fail()
	}
	var exact bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_receipts r JOIN recovery_purpose_link_history l ON l.link_id=r.purpose_link_id WHERE r.intent_ai_session_id=? AND r.intent_id=? AND r.intent_version=? AND r.intent_content_hash=? AND r.commit_request_id=? AND r.selected_variant_id=? AND r.outcome='COMMITTED' AND r.source_owner_kind=? AND r.source_owner_id=? AND r.proposal_hash=? AND r.purpose_link_id=? AND r.purpose_link_ordinal=? AND r.recovery_id=? AND r.recovery_generation=? AND l.link_ordinal=r.purpose_link_ordinal AND l.intent_ai_session_id=r.intent_ai_session_id AND l.intent_id=r.intent_id AND l.intent_version=r.intent_version AND l.intent_content_hash=r.intent_content_hash AND l.proposal_hash=r.proposal_hash AND l.recovery_id=r.recovery_id AND l.recovery_generation=r.recovery_generation AND l.source_owner_kind=r.source_owner_kind AND l.source_owner_id=r.source_owner_id AND l.task_id=? AND l.feature_area_id=? AND l.source_execution_id=? AND l.source_review_id='' AND l.task_definition_revision_id=? AND l.source_operation_id=? AND l.source_run_id=? AND l.source_turn_id=? AND l.runtime_epoch=? AND l.runtime_workset_hash=? AND l.assessment_hash=?)`, in.AISessionID, in.IntentID, in.Version, in.ContentHash, intent.Commit.RequestID, variantID, owner, p.OwnerID, proposalHash, linkID, ordinal, p.RecoveryID, p.RecoveryGeneration, p.TaskID, p.FeatureAreaID, p.SourceExecutionID, p.TaskDefinitionRevisionID, p.SourceOperationID, p.SourceRunID, p.SourceTurnID, p.RuntimeEpoch, p.RuntimeWorksetHash, p.AssessmentHash).Scan(&exact)
	if err != nil {
		return empty, true, err
	}
	if !exact {
		return fail()
	}
	sub, err := confirmationSubmissionTx(ctx, q, intent, intent.Commit.RequestID)
	if err != nil {
		return empty, true, err
	}
	var typedHash string
	if err = q.QueryRowContext(ctx, `SELECT payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=?`, in.AISessionID, intent.Commit.RequestID, in.IntentID).Scan(&typedHash); err != nil {
		return empty, true, err
	}
	commitJSON, _ := json.Marshal(intent.Commit)
	if !confirmationCommitIdentity(intent, *intent.Commit) || workflowHash(string(commitJSON)) != typedHash || sub.Request.Selection.RuntimeConfig != nil || sub.Request.Selection.Backend != nil || sub.Request.BackendSelection != nil {
		return fail()
	}
	var submitted string
	if err = q.QueryRowContext(ctx, `SELECT submitted_hash FROM recovery_purpose_confirmation_receipts WHERE intent_ai_session_id=? AND intent_id=?`, in.AISessionID, in.IntentID).Scan(&submitted); err != nil {
		return empty, true, err
	}
	if !sub.Recorded || sub.Outcome != confirmation.Consumed || sub.Request.VariantID != variantID || sub.PayloadSHA256 != submitted {
		return fail()
	}
	var variant *rs.PurposeVariant
	for i := range p.Variants {
		if p.Variants[i].ID == variantID {
			variant = &p.Variants[i]
			break
		}
	}
	if variant == nil {
		return fail()
	}
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, q, "runtime_scope_recovery_purpose_"+identity)
	if err != nil {
		return empty, true, err
	}
	if plan.OwnerKind != runtimepreparation.OwnerExecution || plan.OwnerID != v.ID || plan.Generation != 1 || plan.ProjectID != v.ProjectID || plan.TaskID != v.TaskID || plan.FeatureAreaID != p.FeatureAreaID || plan.SourceCommand != in.Action || plan.SourceReceiptID != in.IntentID || plan.SelectedVariantID != variantID || plan.ProposalHash != proposalHash || plan.TaskDefinitionRevisionID != revision || plan.SourceExecutionID != p.SourceExecutionID || plan.SourceReviewID != "" || plan.SuccessorExecutionID != v.ID || plan.SuccessorReviewID != "" || plan.PreparationRequestID != "runtime_prepare_recovery_purpose_"+identity || plan.PreparationPayloadHash != workflowHash(in.Action+"\x00"+in.PayloadHash+"\x00"+variantID) || plan.TopologyRevision != intent.Content.RecoveryPurpose.PreparedTopologyRevision || plan.DevelopmentContextHash != intent.Content.RecoveryPurpose.PreparedDevelopmentContextHash {
		return fail()
	}
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposal_admissions a JOIN runtime_preparation_authorities auth ON auth.plan_id=a.runtime_preparation_authority_id WHERE a.proposal_id=? AND a.task_id=? AND a.task_definition_revision_id=? AND a.runtime_scope_plan_id=? AND a.runtime_preparation_authority_id=a.runtime_scope_plan_id AND a.source_confirmation_intent_id=? AND a.source_decision_id='' AND a.admission_request_id=? AND a.payload_sha256=? AND auth.owner_kind='EXECUTION' AND auth.owner_id=a.proposal_id AND auth.generation=1 AND auth.request_id=a.admission_request_id AND auth.payload_hash=a.payload_sha256)`, v.ID, v.TaskID, revision, plan.ID, in.IntentID, plan.PreparationRequestID, plan.PreparationPayloadHash).Scan(&exact)
	if err != nil {
		return empty, true, err
	}
	if !exact {
		return fail()
	}
	// Reconstruct the new definition from the exact selected authored variant.
	var sourceRaw, sourceHash, newRaw, newHash string
	if err = q.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, p.TaskDefinitionRevisionID, v.TaskID).Scan(&sourceRaw, &sourceHash); err != nil {
		return empty, true, err
	}
	d, err := task.DecodeDefinition(sourceRaw, sourceHash)
	if err != nil {
		return empty, true, err
	}
	if owner == "IMPLEMENTATION" {
		b := variant.Implementation
		if b == nil {
			return fail()
		}
		d.WorkSpec, d.VerificationSpec, d.RepositoryTargets, d.PrimaryMemberID = b.WorkSpec, b.VerificationSpec, b.RepositoryTargets, b.PrimaryMemberID
	} else {
		b := variant.Verification
		if b == nil {
			return fail()
		}
		if b.WorkSpec != nil {
			d.WorkSpec = *b.WorkSpec
		}
		d.VerificationSpec, d.RepositoryTargets, d.PrimaryMemberID = b.VerificationSpec, b.RepositoryTargets, b.PrimaryMemberID
	}
	d.Version, d.RepositoryID, d.BaseCommit = 2, "", ""
	_, expectedHash, err := task.EncodeDefinition(d)
	if err != nil {
		return empty, true, err
	}
	if err = q.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, revision, v.TaskID).Scan(&newRaw, &newHash); err != nil {
		return empty, true, err
	}
	if _, err = task.DecodeDefinition(newRaw, newHash); err != nil {
		return empty, true, err
	}
	targets, _ := json.Marshal(d.RepositoryTargets)
	if newHash != expectedHash || v.PrimaryMemberID != d.PrimaryMemberID || v.ApprovedWorksetHash != workflowHash(string(targets)) || !reflect.DeepEqual(task.CloneWorkSpec(v.WorkSpec), task.CloneWorkSpec(d.WorkSpec)) || !reflect.DeepEqual(task.CloneVerificationSpec(v.VerificationSpec), task.CloneVerificationSpec(d.VerificationSpec)) || !reflect.DeepEqual(plan.RepositoryTargets, d.RepositoryTargets) || plan.PrimaryMemberID != d.PrimaryMemberID {
		return fail()
	}
	if len(plan.DevelopmentRefs) != len(d.RepositoryTargets) || len(plan.ExpectedSourceHEADs) != len(d.RepositoryTargets) || len(plan.WriteScopes) != len(d.RepositoryTargets) {
		return fail()
	}
	for _, t := range d.RepositoryTargets {
		if plan.DevelopmentRefs[t.MemberID] != t.OriginRef || plan.ExpectedSourceHEADs[t.MemberID] != t.OriginCommit || !reflect.DeepEqual(plan.WriteScopes[t.MemberID], t.Write) {
			return fail()
		}
	}
	a, err := recoveryPurposeSourceAuthoritySeenTx(ctx, q, intent, seen, verifyPlatform)
	if err != nil {
		return empty, true, err
	}
	values, err := executionAuthorityValues(v)
	if err != nil {
		return empty, true, err
	}
	if values != a.Values || plan.AuthoritySnapshotHash != runtimeselection.HashValue(values) {
		return fail()
	}
	// The hash is independently recomputed by Plan.Validate, tying all ten
	// fields to the predecessor without copying its revision binding.
	a.Owner = executionAuthorityOwner(v)
	a.BindingID = ""
	a.PurposeConsumptionID = runtimeselection.HashValue([]string{in.AISessionID, in.IntentID, in.RequestID, in.PayloadHash})
	a.AuthoritySnapshotHash = plan.AuthoritySnapshotHash
	return a, true, nil
}
