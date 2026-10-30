package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
)

type scalarRuntimePreparer interface {
	canonicalRuntimePreparer
	ReadExecutionPreparationPlan(context.Context, string) (runtimepreparation.Plan, error)
	ReadWorkspaceObservations(context.Context, string, string) ([]runtimepreparation.WorkspaceObservation, error)
	ValidatePreparedStart(context.Context, string) error
}

func scalarDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// prepareScalarRuntime completes the post-Run stages of the frozen scalar
// admission. The legacy workspace's member ID is not an admission target ID:
// its exact repository/worktree is observed under the Plan's logical member.
func (c *Coordinator) prepareScalarRuntime(ctx context.Context, prepared *PreparedRun) error {
	store, ok := c.Runs.(scalarRuntimePreparer)
	if !ok {
		return nil
	} // in-memory/legacy Run adapters have no v90 admission
	p, err := store.ReadExecutionPreparationPlan(ctx, prepared.Run.ID)
	if errors.Is(err, run.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(p.RepositoryTargets) != 1 || p.TaskID != prepared.task.ID || p.OwnerID != prepared.Run.ExecutionProposalID || p.ProjectID != prepared.Run.ProjectID || p.FeatureAreaID != prepared.Run.FeatureAreaID || prepared.backendSelection == nil || p.BackendIdentity != prepared.backendSelection.ConfigID {
		return errors.New("scalar preparation owner conflicts with frozen Plan")
	}
	target := p.RepositoryTargets[0]
	if target.RepositoryID != prepared.repository.ID || target.MemberID != p.PrimaryMemberID || target.OriginCommit != prepared.task.BaseCommit {
		return errors.New("scalar repository conflicts with frozen Plan")
	}
	request := "workspace_" + prepared.Run.ID
	payload := scalarDigest(p.ID + "\x00" + prepared.Run.ID + "\x00WORKSPACE_PREPARATION")
	replay, err := store.ClaimWorkspacePreparation(ctx, p.ID, prepared.Run.ID, request, payload, time.Now().UTC())
	if err != nil {
		return err
	}
	var observations []runtimepreparation.WorkspaceObservation
	if replay {
		planID, runID, outcome, readErr := store.ReadPreparationReceipt(ctx, request, payload)
		if readErr != nil || planID != p.ID || runID != prepared.Run.ID || outcome != "OBSERVATION_RECORDED" {
			return errors.Join(readErr, errors.New("RUNTIME_WORKSPACE_PREPARATION_OUTCOME_UNKNOWN"))
		}
		observations, err = store.ReadWorkspaceObservations(ctx, p.ID, prepared.Run.ID)
		if err != nil {
			return err
		}
	} else {
		ws, wsErr := c.Workspaces.EnsureForFeatureArea(ctx, p.ProjectID, target.RepositoryID, p.FeatureAreaID, target.OriginCommit)
		if wsErr != nil {
			return wsErr
		}
		if len(ws.Members) != 1 || ws.RuntimeQuarantined {
			return errors.New("scalar workspace is quarantined or contains an unadmitted member")
		}
		member, memberErr := ws.MemberForRepository(target.RepositoryID)
		if memberErr != nil || member.OriginCommit != target.OriginCommit {
			return errors.Join(memberErr, errors.New("scalar workspace baseline conflicts with Plan"))
		}
		tree, _, treeErr := c.workspaceWorktree(ctx, ws, target.OriginCommit, prepared.repository, prepared.Run)
		if treeErr != nil {
			return treeErr
		}
		root, identity, rootErr := multiRuntimeRootIdentity(tree.Path)
		if rootErr != nil {
			return rootErr
		}
		source, sourceErr := filepath.Abs(prepared.repository.Path)
		if sourceErr != nil {
			return sourceErr
		}
		if resolved, e := filepath.EvalSymlinks(source); e == nil {
			source = resolved
		}
		if root == source {
			return errors.New("managed worktree equals source repository")
		}
		snapshot, snapshotErr := c.Evidence.CaptureSnapshot(ctx, root)
		if snapshotErr != nil {
			return snapshotErr
		}
		start, startErr := c.Evidence.CollectRun(ctx, root, target.OriginCommit, snapshot, snapshot)
		if startErr != nil {
			return startErr
		}
		if !workset.ValidCommit(snapshot) || start.HeadCommit != p.ExpectedSourceHEADs[target.MemberID] {
			return errors.New("scalar workspace fresh HEAD conflicts with Plan")
		}
		o := runtimepreparation.WorkspaceObservation{ID: "observation_" + scalarDigest(p.ID + "\x00" + prepared.Run.ID + "\x00" + target.MemberID)[:32], PlanID: p.ID, RunID: prepared.Run.ID, WorkspaceID: ws.ID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, WorktreeID: tree.ID, CanonicalRoot: root, RootIdentity: identity, StartHEAD: start.HeadCommit, StartSnapshot: snapshot, Branch: tree.Branch, MetadataDigest: scalarDigest(tree.ID + "\x00" + root + "\x00" + tree.Branch + "\x00" + tree.BaseCommit + "\x00" + identity + "\x00" + start.HeadCommit + "\x00" + snapshot), Generation: 1, ObservedAt: time.Now().UTC()}
		observations = []runtimepreparation.WorkspaceObservation{o}
		if _, err = store.RecordWorkspaceObservations(ctx, p.ID, prepared.Run.ID, request, payload, observations, time.Now().UTC()); err != nil {
			return err
		}
	}
	if len(observations) != 1 {
		return errors.New("scalar observation receipt incomplete")
	}
	o := observations[0]
	if o.MemberID != target.MemberID || o.RepositoryID != target.RepositoryID || o.PlanID != p.ID || o.RunID != prepared.Run.ID {
		return errors.New("scalar observation receipt conflicts with Plan")
	}
	// Revalidate the physical root on readback; receipt replay never recreates it.
	root, identity, err := multiRuntimeRootIdentity(o.CanonicalRoot)
	if err != nil {
		return err
	}
	if root != o.CanonicalRoot || identity != o.RootIdentity {
		return errors.New("scalar observed root identity changed")
	}
	frozen := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersion, ProjectID: p.ProjectID, FeatureAreaID: p.FeatureAreaID, TaskID: p.TaskID, TaskDefinitionRevisionID: p.TaskDefinitionRevisionID, ExecutionID: p.OwnerID, RunID: prepared.Run.ID, WorkspaceID: o.WorkspaceID, PrimaryMemberID: p.PrimaryMemberID, Members: []workset.RuntimeMemberBinding{{RepositoryID: target.RepositoryID, RepositoryAlias: target.MemberID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, WorktreeID: o.WorktreeID, CanonicalRoot: o.CanonicalRoot, RootFileIdentity: o.RootIdentity, OriginCommit: target.OriginCommit, StartHEAD: o.StartHEAD, StartSnapshot: o.StartSnapshot, Read: target.Read, Write: target.Write, IntendedReadScope: target.ReadScope, RuntimeRootIdentity: o.RootIdentity}}, BackendConfigID: p.BackendIdentity, BackendConfigFingerprint: prepared.backendSelection.ConfigFingerprint, Model: p.Model, ReasoningEffort: p.ReasoningEffort, RuntimeAdapterIdentity: p.RuntimeAdapterIdentity, CreatedAt: prepared.Run.CreatedAt}
	// Protocol and service tier are frozen on the execution, not inferred defaults.
	execution, err := c.Executions.Get(ctx, p.OwnerID)
	if err != nil {
		return err
	}
	frozen.ServiceTier = execution.RuntimeConfig.ServiceTier
	frozen.RuntimeProtocolIdentity = execution.RuntimeProtocolIdentity
	// Existing scalar admissions freeze the pre-workset adapter identifier.
	// That adapter implements this one protocol; unknown adapters fail closed.
	if frozen.RuntimeProtocolIdentity == "" && p.RuntimeAdapterIdentity == "codex-app-server-v1" {
		frozen.RuntimeProtocolIdentity = executionproposal.RuntimeProtocolCodex0149
	}
	if execution.RuntimeConfig.Model != p.Model || execution.RuntimeConfig.ReasoningEffort != p.ReasoningEffort || !(frozen.RuntimeProtocolIdentity == executionproposal.RuntimeProtocolCodex0149 || runtimeplatform.QualifiedExecutorIdentity(p.RuntimeAdapterIdentity, frozen.RuntimeProtocolIdentity)) {
		return errors.New("scalar runtime configuration conflicts with Plan")
	}
	frozen, err = frozen.WithHash()
	if err != nil {
		return err
	}
	frozen, err = c.prepareTaskPermissions(ctx, frozen, prepared.purpose == run.PurposeVerification)
	if err != nil {
		return err
	}
	bindingRequest := "binding_" + prepared.Run.ID
	bindingPayload := scalarDigest(p.ID + "\x00" + prepared.Run.ID + "\x00" + frozen.ContentHash)
	stored, err := store.BindPreparedRuntimeWorkset(ctx, p.ID, bindingRequest, bindingPayload, "start_authority_"+prepared.Run.ID, frozen, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("bind scalar RuntimeWorkset: %w", err)
	}
	if err = store.ValidatePreparedStart(ctx, stored.ID); err != nil {
		return err
	}
	cwd, binding, err := multiRuntimeClientBinding(frozen)
	if err != nil {
		return err
	}
	prepared.Run = stored
	prepared.scalarRuntime = &PreparedMultiRuntime{Workset: frozen, CWD: cwd, Binding: binding}
	return nil
}
