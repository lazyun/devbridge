package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

// PrepareRecoveryChildRuntime is the production managed-worktree stage after
// canonical child Run creation. The source Workset supplies backend selection
// and immutable provenance only; every child root, HEAD and snapshot is read
// from a newly allocated child worktree.
func (c *Coordinator) PrepareRecoveryChildRuntime(ctx context.Context, recoveryID string, plan recoverystatus.ChildPreparation, child run.Run, source workset.RuntimeWorkset) (out PreparedMultiRuntime, err error) {
	if c == nil || c.Workspaces == nil || c.Worktrees == nil || c.Repositories == nil || c.Evidence == nil || c.Runs == nil || recoveryID == "" || child.ID == "" || plan.PlanID == "" || plan.SourceRunID == "" || child.ID == plan.SourceRunID || source.RunID != plan.SourceRunID || source.ContentHash != plan.SourceWorksetHash || source.WorkspaceID != plan.SourceWorkspaceID || source.BackendConfigID != plan.BackendConfigID || source.Model != plan.Model || source.ReasoningEffort != plan.ReasoningEffort || source.RuntimeAdapterIdentity != plan.AdapterIdentity {
		return PreparedMultiRuntime{}, recoverystatus.ErrConflict
	}
	preparer, ok := c.Runs.(canonicalRuntimePreparer)
	if !ok {
		return PreparedMultiRuntime{}, errors.New("canonical Recovery RuntimePreparation unavailable")
	}
	owner, ok := c.Workspaces.(interface {
		EnsureRecoveryMembers(context.Context, string, string, string, []workset.Target) (workspace.Workspace, error)
		AttachMemberWorktree(context.Context, string, string, string) (workspace.Workspace, error)
	})
	if !ok {
		return PreparedMultiRuntime{}, errors.New("managed Recovery Workspace unavailable")
	}
	workspaceRequest := "recovery_workspace_" + recoveryID
	workspaceSum := sha256.Sum256([]byte(plan.PlanID + "\x00" + child.ID + "\x00" + plan.PayloadSHA256 + "\x00RECOVERY_WORKSPACE"))
	workspacePayload := hex.EncodeToString(workspaceSum[:])
	replay, err := preparer.ClaimWorkspacePreparation(ctx, plan.PlanID, child.ID, workspaceRequest, workspacePayload, time.Now().UTC())
	if err != nil {
		return PreparedMultiRuntime{}, err
	}
	if replay {
		reader, ok := c.Runs.(interface {
			RuntimeWorkset(context.Context, string) (workset.RuntimeWorkset, error)
			ValidatePreparedStart(context.Context, string) error
		})
		if !ok {
			return PreparedMultiRuntime{}, errors.New("RECOVERY_WORKSPACE_PREPARATION_OUTCOME_UNKNOWN")
		}
		frozen, readErr := reader.RuntimeWorkset(ctx, child.ID)
		if errors.Is(readErr, run.ErrNotFound) {
			return c.bindObservedRecoveryChild(ctx, recoveryID, plan, child, source, workspaceRequest, workspacePayload, preparer)
		}
		if readErr != nil || frozen.Validate() != nil || frozen.RecoveryStatusID != recoveryID || frozen.RecoverySourceRunID != plan.SourceRunID || frozen.RunID != child.ID || frozen.WorkspaceID == source.WorkspaceID || frozen.ProtocolVersion != workset.RuntimeProtocolVersionRecoveryStatus {
			return PreparedMultiRuntime{}, errors.New("RECOVERY_WORKSPACE_PREPARATION_OUTCOME_UNKNOWN")
		}
		if readErr = reader.ValidatePreparedStart(ctx, child.ID); readErr != nil {
			return PreparedMultiRuntime{}, readErr
		}
		cwd, binding, readErr := multiRuntimeClientBinding(frozen)
		if readErr != nil {
			return PreparedMultiRuntime{}, readErr
		}
		binding.WritableRoots = nil
		return PreparedMultiRuntime{Workset: frozen, CWD: cwd, Binding: binding, ReadOnly: true}, nil
	}
	// Settle only observations after a successful, fully returned worktree
	// creation. A failed Create or canceled/unknown call supplies no such proof.
	var artifacts []runtimepreparation.FailureArtifact
	definiteObservationFailure := false
	defer func() {
		if err == nil || !definiteObservationFailure || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		recorder, ok := c.Runs.(interface {
			SettleRecoveryPreparationFailure(context.Context, string, int, runtimepreparation.FailureSettlement) error
		})
		if !ok {
			return
		}
		err = errors.Join(err, recorder.SettleRecoveryPreparationFailure(context.WithoutCancel(ctx), recoveryID, plan.Generation, runtimepreparation.FailureSettlement{PlanID: plan.PlanID, RunID: child.ID, RequestID: workspaceRequest, PayloadHash: workspacePayload, RuntimeEpoch: child.RuntimeEpoch, Stage: "MEMBER_OBSERVATION", Code: "RECOVERY_PREPARATION_FAILED", Message: err.Error(), Artifacts: artifacts, At: time.Now().UTC()}))
	}()
	value, err := owner.EnsureRecoveryMembers(ctx, plan.ProjectID, plan.FeatureAreaID, recoveryID, plan.Targets)
	if err != nil || value.ID == source.WorkspaceID {
		return PreparedMultiRuntime{}, errors.Join(err, recoverystatus.ErrConflict)
	}
	observations := make([]runtimepreparation.WorkspaceObservation, 0, len(plan.Targets))
	members := make([]workset.RuntimeMemberBinding, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		definiteObservationFailure = false
		member, memberErr := value.MemberForRepository(target.RepositoryID)
		if memberErr != nil || member.ID != target.MemberID || member.WorktreeID != "" {
			return PreparedMultiRuntime{}, errors.Join(memberErr, recoverystatus.ErrConflict)
		}
		repository, getErr := c.Repositories.Get(ctx, target.RepositoryID)
		if getErr != nil || repository.ProjectID != plan.ProjectID || repository.Path == "" {
			return PreparedMultiRuntime{}, errors.Join(getErr, recoverystatus.ErrConflict)
		}
		tree, createErr := c.Worktrees.Create(ctx, worktree.CreateInput{PreserveOnFailure: true, MemberID: member.ID, MemberVersion: member.Version, MemberAlias: member.Alias, RepositoryID: target.RepositoryID, RepositoryPath: repository.Path, FeatureAreaID: plan.FeatureAreaID, OwnerKind: string(workspace.OwnerRecoveryStatus), OwnerID: recoveryID, RunID: child.ID, WorkspaceID: value.ID, BaseCommit: target.OriginCommit})
		if createErr != nil || tree.RunID != child.ID || tree.RepositoryID != target.RepositoryID || tree.BaseCommit != target.OriginCommit || tree.Status != worktree.StatusReady {
			return PreparedMultiRuntime{}, errors.Join(createErr, recoverystatus.ErrConflict)
		}
		value, err = owner.AttachMemberWorktree(ctx, value.ID, member.ID, tree.ID)
		if err != nil {
			return PreparedMultiRuntime{}, err
		}
		artifacts = append(artifacts, runtimepreparation.FailureArtifact{MemberID: member.ID, WorktreeID: tree.ID, Root: tree.Path})
		definiteObservationFailure = true
		root, identity, rootErr := multiRuntimeRootIdentity(tree.Path)
		if rootErr != nil {
			return PreparedMultiRuntime{}, rootErr
		}
		snapshot, snapshotErr := c.Evidence.CaptureSnapshot(ctx, root)
		if snapshotErr != nil || !workset.ValidCommit(snapshot) {
			return PreparedMultiRuntime{}, errors.Join(snapshotErr, recoverystatus.ErrConflict)
		}
		start, evidenceErr := c.Evidence.CollectRun(ctx, root, target.OriginCommit, snapshot, snapshot)
		if evidenceErr != nil || !workset.ValidCommit(start.HeadCommit) || start.HeadCommit != plan.ExpectedHEADs[target.MemberID] {
			return PreparedMultiRuntime{}, errors.Join(evidenceErr, recoverystatus.ErrConflict)
		}
		for _, sourceMember := range source.Members {
			if sourceMember.MemberID == member.ID || sourceMember.WorktreeID == tree.ID || sourceMember.CanonicalRoot == root || sourceMember.RootFileIdentity == identity {
				return PreparedMultiRuntime{}, recoverystatus.ErrConflict
			}
		}
		metadata := sha256.Sum256([]byte(tree.ID + "\x00" + root + "\x00" + tree.Branch + "\x00" + target.OriginCommit + "\x00" + identity + "\x00" + start.HeadCommit + "\x00" + snapshot))
		observationID := sha256.Sum256([]byte(plan.PlanID + "\x00" + child.ID + "\x00" + member.ID))
		observations = append(observations, runtimepreparation.WorkspaceObservation{ID: "observation_" + hex.EncodeToString(observationID[:16]), PlanID: plan.PlanID, RunID: child.ID, WorkspaceID: value.ID, RepositoryID: target.RepositoryID, MemberID: member.ID, WorktreeID: tree.ID, CanonicalRoot: root, RootIdentity: identity, StartHEAD: start.HeadCommit, StartSnapshot: snapshot, Branch: tree.Branch, MetadataDigest: hex.EncodeToString(metadata[:]), Generation: plan.Generation, ObservedAt: time.Now().UTC()})
		members = append(members, workset.RuntimeMemberBinding{RepositoryID: target.RepositoryID, RepositoryAlias: member.Alias, MemberID: member.ID, MemberVersion: member.Version, WorktreeID: tree.ID, CanonicalRoot: root, RootFileIdentity: identity, OriginCommit: target.OriginCommit, StartHEAD: start.HeadCommit, StartSnapshot: snapshot, Read: true, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: identity})
	}
	definiteObservationFailure = false
	if _, err = preparer.RecordWorkspaceObservations(ctx, plan.PlanID, child.ID, workspaceRequest, workspacePayload, observations, time.Now().UTC()); err != nil {
		return PreparedMultiRuntime{}, err
	}
	return bindRecoveryChildWorkset(ctx, recoveryID, plan, child, source, value.ID, members, preparer)
}

// An observation receipt proves all managed worktree effects completed. Resume
// only the remaining transactional binding; a claim alone never permits a
// second worktree creation or snapshot capture.
func (c *Coordinator) bindObservedRecoveryChild(ctx context.Context, recoveryID string, plan recoverystatus.ChildPreparation, child run.Run, source workset.RuntimeWorkset, request, payload string, preparer canonicalRuntimePreparer) (PreparedMultiRuntime, error) {
	gotPlan, gotRun, outcome, err := preparer.ReadPreparationReceipt(ctx, request, payload)
	if err != nil || gotPlan != plan.PlanID || gotRun != child.ID || outcome != "OBSERVATION_RECORDED" {
		return PreparedMultiRuntime{}, errors.Join(err, errors.New("RECOVERY_WORKSPACE_PREPARATION_OUTCOME_UNKNOWN"))
	}
	reader, ok := c.Runs.(interface {
		ReadWorkspaceObservations(context.Context, string, string) ([]runtimepreparation.WorkspaceObservation, error)
	})
	owner, ownerOK := c.Workspaces.(interface {
		GetByOwner(context.Context, workspace.OwnerKind, string) (workspace.Workspace, error)
	})
	if !ok || !ownerOK {
		return PreparedMultiRuntime{}, recoverystatus.ErrConflict
	}
	observations, err := reader.ReadWorkspaceObservations(ctx, plan.PlanID, child.ID)
	if err != nil || len(observations) != len(plan.Targets) {
		return PreparedMultiRuntime{}, errors.Join(err, recoverystatus.ErrConflict)
	}
	value, err := owner.GetByOwner(ctx, workspace.OwnerRecoveryStatus, recoveryID)
	if err != nil || value.ID == source.WorkspaceID || value.ProjectID != plan.ProjectID || value.FeatureAreaID != "" || value.OwnerKind != workspace.OwnerRecoveryStatus || value.OwnerID != recoveryID || value.RuntimeQuarantined || len(value.Members) != len(plan.Targets) {
		return PreparedMultiRuntime{}, errors.Join(err, recoverystatus.ErrConflict)
	}
	members := make([]workset.RuntimeMemberBinding, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		member, err := value.MemberForRepository(target.RepositoryID)
		if err != nil || member.ID != target.MemberID || member.Version != target.MemberVersion || member.OriginCommit != target.OriginCommit {
			return PreparedMultiRuntime{}, errors.Join(err, recoverystatus.ErrConflict)
		}
		var observed *runtimepreparation.WorkspaceObservation
		for i := range observations {
			if observations[i].MemberID == target.MemberID {
				if observed != nil {
					return PreparedMultiRuntime{}, recoverystatus.ErrConflict
				}
				observed = &observations[i]
			}
		}
		if observed == nil || observed.PlanID != plan.PlanID || observed.RunID != child.ID || observed.WorkspaceID != value.ID || observed.RepositoryID != target.RepositoryID || observed.WorktreeID != member.WorktreeID || observed.Generation != plan.Generation || observed.StartHEAD != plan.ExpectedHEADs[target.MemberID] {
			return PreparedMultiRuntime{}, recoverystatus.ErrConflict
		}
		root, identity, err := multiRuntimeRootIdentity(observed.CanonicalRoot)
		if err != nil || root != observed.CanonicalRoot || identity != observed.RootIdentity {
			return PreparedMultiRuntime{}, errors.Join(err, recoverystatus.ErrConflict)
		}
		current, err := c.Evidence.CollectRun(ctx, root, target.OriginCommit, observed.StartSnapshot, observed.StartSnapshot)
		if err != nil || current.HeadCommit != observed.StartHEAD || current.Dirty {
			return PreparedMultiRuntime{}, errors.Join(err, recoverystatus.ErrConflict)
		}
		members = append(members, workset.RuntimeMemberBinding{RepositoryID: target.RepositoryID, RepositoryAlias: member.Alias, MemberID: member.ID, MemberVersion: member.Version, WorktreeID: observed.WorktreeID, CanonicalRoot: root, RootFileIdentity: identity, OriginCommit: target.OriginCommit, StartHEAD: observed.StartHEAD, StartSnapshot: observed.StartSnapshot, Read: true, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: identity})
	}
	return bindRecoveryChildWorkset(ctx, recoveryID, plan, child, source, value.ID, members, preparer)
}

func bindRecoveryChildWorkset(ctx context.Context, recoveryID string, plan recoverystatus.ChildPreparation, child run.Run, source workset.RuntimeWorkset, workspaceID string, members []workset.RuntimeMemberBinding, preparer canonicalRuntimePreparer) (PreparedMultiRuntime, error) {
	frozen := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersionRecoveryStatus, OwnerKind: workset.RuntimeOwnerRecoveryStatus, ProjectID: plan.ProjectID, FeatureAreaID: plan.FeatureAreaID, TaskID: child.TaskID, RecoveryStatusID: recoveryID, RecoverySourceRunID: plan.SourceRunID, RunID: child.ID, WorkspaceID: workspaceID, PrimaryMemberID: plan.PrimaryMemberID, Members: members, BackendConfigID: source.BackendConfigID, BackendConfigFingerprint: source.BackendConfigFingerprint, Model: plan.Model, ReasoningEffort: plan.ReasoningEffort, ServiceTier: source.ServiceTier, RuntimeAdapterIdentity: plan.AdapterIdentity, RuntimeProtocolIdentity: source.RuntimeProtocolIdentity, CreatedAt: child.CreatedAt}
	frozen, err := frozen.WithHash()
	if err != nil {
		return PreparedMultiRuntime{}, err
	}
	bindingRequest := "recovery_binding_" + recoveryID
	bindingSum := sha256.Sum256([]byte(plan.PlanID + "\x00" + child.ID + "\x00" + frozen.ContentHash))
	if _, err = preparer.BindPreparedRuntimeWorkset(ctx, plan.PlanID, bindingRequest, hex.EncodeToString(bindingSum[:]), "recovery_start_authority_"+recoveryID, frozen, time.Now().UTC()); err != nil {
		return PreparedMultiRuntime{}, err
	}
	cwd, binding, err := multiRuntimeClientBinding(frozen)
	if err != nil {
		return PreparedMultiRuntime{}, err
	}
	binding.WritableRoots = nil
	return PreparedMultiRuntime{Workset: frozen, CWD: cwd, Binding: binding, ReadOnly: true}, nil
}
