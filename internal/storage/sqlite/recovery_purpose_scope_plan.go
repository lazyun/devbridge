package sqlite

import (
	"context"
	"database/sql"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/workset"
)

// freezeRecoveryPurposeScopeTx uses only the selected typed proposal and
// immutable source configuration. It does not copy source Run observations.
func freezeRecoveryPurposeScopeTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent, variant rs.PurposeVariant, in rs.PurposeConsumeInput, out rs.PurposeConsumption, authority *runtimeselection.CurrentAuthority, now time.Time) error {
	proposal := intent.Content.RecoveryPurpose.Proposal
	source, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, proposal.SourceRunID))
	if err != nil {
		return err
	}
	if source.RuntimeWorkset == nil || source.RuntimeWorkset.ContentHash != proposal.RuntimeWorksetHash {
		return rs.ErrPurposeSourceWorksetChanged
	}
	prior := source.RuntimeWorkset
	var targets []workset.Target
	primary := prior.PrimaryMemberID
	owner := runtimepreparation.OwnerKind(out.OwnerKind)
	switch owner {
	case runtimepreparation.OwnerImplementation:
		if variant.Implementation == nil {
			return rs.ErrIneligible
		}
		targets = variant.Implementation.RepositoryTargets
		primary = variant.Implementation.PrimaryMemberID
	case runtimepreparation.OwnerVerification:
		if variant.Verification == nil {
			return rs.ErrIneligible
		}
		targets = variant.Verification.RepositoryTargets
		primary = variant.Verification.PrimaryMemberID
	case runtimepreparation.OwnerReview:
		if variant.Review == nil {
			return rs.ErrIneligible
		}
		targets = variant.Review.RepositoryTargets
	default:
		return rs.ErrPurposeOwnerMismatch
	}
	if len(targets) == 0 {
		return rs.ErrIneligible
	}
	heads := map[string]string{}
	refs := map[string]string{}
	scopes := map[string]workset.WriteScope{}
	primaryFound := false
	for _, target := range targets {
		if target.ObservedCommit != "" {
			return rs.ErrIneligible
		}
		heads[target.MemberID] = target.OriginCommit
		refs[target.MemberID] = target.OriginRef
		scopes[target.MemberID] = target.Write
		if target.MemberID == primary {
			primaryFound = true
		}
	}
	if !primaryFound {
		return rs.ErrIneligible
	}
	model, effort := prior.Model, prior.ReasoningEffort
	if c := intent.Content.RecommendedRuntimeConfig; c != nil {
		model, effort = c.Model, c.ReasoningEffort
	}
	ownerID := out.ExecutionID
	if owner == runtimepreparation.OwnerReview {
		ownerID = out.ReviewID
	} else {
		// A Verification revision also starts a new Implementation. EXECUTION
		// is the admitted owner; the failed source Run's purpose is provenance.
		owner = runtimepreparation.OwnerExecution
		if authority == nil {
			return rs.ErrPurposeReceiptMismatch
		}
	}
	identity := workflowHash(in.IntentID + "\x00" + in.RequestID)[:32]
	plan := runtimepreparation.Plan{
		ID: "runtime_scope_recovery_purpose_" + identity, Generation: 1, OwnerKind: owner, OwnerID: ownerID,
		ProjectID: intent.ProjectID, FeatureAreaID: proposal.FeatureAreaID, TaskID: proposal.TaskID,
		SourceCommand: in.Action, SourceReceiptID: in.IntentID, SelectedVariantID: variant.ID, ProposalHash: proposal.SubmittedHash,
		TaskDefinitionRevisionID: out.TaskDefinitionRevisionID, SourceExecutionID: proposal.SourceExecutionID, SourceReviewID: proposal.SourceReviewID,
		SuccessorExecutionID: out.ExecutionID, SuccessorReviewID: out.ReviewID, RepositoryTargets: workset.CloneTargets(targets), PrimaryMemberID: primary,
		AgentID: prior.AgentID, AgentConfigFingerprint: prior.AgentConfigFingerprint, ExecutionPolicy: prior.ExecutionPolicy, BackendIdentity: prior.BackendConfigID, Model: model, ReasoningEffort: effort, RuntimeAdapterIdentity: prior.RuntimeAdapterIdentity,
		TopologyRevision: intent.Content.RecoveryPurpose.PreparedTopologyRevision, DevelopmentContextHash: intent.Content.RecoveryPurpose.PreparedDevelopmentContextHash,
		DevelopmentRefs: refs, ExpectedSourceHEADs: heads, WriteScopes: scopes,
		PreparationRequestID: "runtime_prepare_recovery_purpose_" + identity, PreparationPayloadHash: workflowHash(in.Action + "\x00" + in.PayloadHash + "\x00" + variant.ID), CreatedAt: now.UTC(),
	}
	if plan.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 {
		plan.RuntimeProtocolIdentity = prior.RuntimeProtocolIdentity
	}
	if owner == runtimepreparation.OwnerExecution {
		v := authority.Values
		plan.BackendIdentity, plan.BackendConfigFingerprint, plan.BackendSource, plan.BackendKind = v.BackendIdentity, v.BackendConfigFingerprint, v.BackendSource, v.BackendKind
		plan.Model, plan.ReasoningEffort, plan.CodexFastMode, plan.ServiceTier = v.Model, v.ReasoningEffort, v.CodexFastMode, v.ServiceTier
		plan.AgentID, plan.ExecutionPolicy, plan.AgentConfigFingerprint = v.AgentID, v.ExecutionPolicy, v.AgentConfigFingerprint
		plan.RuntimeAdapterIdentity, plan.RuntimeProtocolIdentity = v.RuntimeAdapterIdentity, v.RuntimeProtocolIdentity
		plan.AuthoritySnapshotHash, err = plan.ComputeAuthoritySnapshotHash()
		if err != nil {
			return err
		}
		if plan.AuthoritySnapshotHash != authority.AuthoritySnapshotHash {
			return rs.ErrPurposeReceiptMismatch
		}
	}
	plan.ContentHash, err = plan.ComputeContentHash()
	if err != nil {
		return err
	}
	if err = freezeRuntimeScopePlanTx(ctx, tx, plan); err != nil {
		return err
	}
	if owner == runtimepreparation.OwnerExecution {
		return admitRecoveryPurposeExecutionTx(ctx, tx, intent, in, out, plan, now)
	}
	return nil
}
