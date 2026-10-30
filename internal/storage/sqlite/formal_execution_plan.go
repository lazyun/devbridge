package sqlite

import (
	"context"
	"database/sql"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// T1 and T2 independently fence new writes against the original Prepare.
// Missing historical fields cannot be reconstructed from today's Project.
// T3 and final replay use their already committed Plan without this live check.
func validateFormalExecutionContextTx(ctx context.Context, tx *sql.Tx, fence fe.PrepareFence) (*confirmation.ExecutionBody, error) {
	var prepared confirmation.PrepareInput
	if fe.Decode(fence.FrozenPrepareJSON, fence.ContentHash, &prepared) != nil {
		return nil, fe.ErrCorrupt
	}
	if err := validateFormalConfirmationContextTx(ctx, tx, confirmation.Intent{ProjectID: fence.ProjectID, Content: prepared}); err != nil {
		return nil, err
	}
	return prepared.Execution, nil
}

func formalExecutionPlanTx(ctx context.Context, tx *sql.Tx, t task.Task, p fe.Progress, now time.Time) (runtimepreparation.Plan, error) {
	a := p.Archive
	adapter, protocol := formalDescriptorRuntimeIdentities(a.Descriptor)
	if a.Runtime.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 {
		adapter, protocol = agentpolicy.CooperativeAdapterIdentity, agentpolicy.CooperativeProtocolIdentity
	}
	frozen, err := validateFormalExecutionContextTx(ctx, tx, a.Fence)
	if err != nil {
		return runtimepreparation.Plan{}, err
	}
	if len(t.RepositoryTargets) == 0 || t.PrimaryMemberID == "" {
		return runtimepreparation.Plan{}, fe.ErrConflict
	}
	heads, refs, scopes := map[string]string{}, map[string]string{}, map[string]workset.WriteScope{}
	for _, target := range t.RepositoryTargets {
		heads[target.MemberID] = target.OriginCommit
		refs[target.MemberID] = target.OriginRef
		scopes[target.MemberID] = target.Write
	}
	request := "formal_execution_admission_" + p.T1.Identity.ImmutableBinding
	plan := runtimepreparation.Plan{ID: "runtime_scope_execution_" + workflowHash(t.CurrentExecutionID + "\x00" + request)[:32], Generation: 1, OwnerKind: runtimepreparation.OwnerImplementation, OwnerID: t.CurrentExecutionID, ProjectID: t.ProjectID, FeatureAreaID: t.FeatureAreaID, TaskID: t.ID, SourceCommand: "APPROVE_AND_START", SourceReceiptID: p.T1.Identity.SourceOriginID, SelectedVariantID: a.Commit.VariantID, ProposalHash: p.T1.Identity.VariantPayloadIdentity, TaskDefinitionRevisionID: a.RevisionID, SourceReviewID: a.Fence.ReviewID, RepositoryTargets: workset.CloneTargets(t.RepositoryTargets), PrimaryMemberID: t.PrimaryMemberID, BackendIdentity: a.Backend.ConfigID, BackendConfigFingerprint: a.Backend.ConfigFingerprint, BackendSource: a.Backend.Source, BackendKind: "CODEX", Model: a.Runtime.Model, ReasoningEffort: a.Runtime.ReasoningEffort, CodexFastMode: a.Runtime.CodexFastMode, ServiceTier: a.Runtime.ServiceTier, AgentID: a.Runtime.AgentID, AgentConfigFingerprint: a.Runtime.AgentConfigFingerprint, ExecutionPolicy: a.Runtime.ExecutionPolicy, RuntimeAdapterIdentity: adapter, RuntimeProtocolIdentity: protocol, TopologyRevision: frozen.PreparedTopologyRevision, DevelopmentContextHash: frozen.PreparedDevelopmentContextHash, DevelopmentRefs: refs, ExpectedSourceHEADs: heads, WriteScopes: scopes, PreparationRequestID: request, PreparationPayloadHash: p.T1.Identity.ImmutableBinding, CreatedAt: now.UTC()}
	plan.AuthoritySnapshotHash, err = plan.ComputeAuthoritySnapshotHash()
	if err != nil {
		return plan, err
	}
	plan.ContentHash, err = plan.ComputeContentHash()
	return plan, err
}
