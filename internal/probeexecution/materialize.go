package probeexecution

import (
	"context"
	"errors"
	"time"

	gitrepo "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

type InspectionWorkspaceService interface {
	PlanInspectionMembers(context.Context, string, string, string, []workset.Target) (workset.Set, error)
	EnsurePlannedInspectionMembers(context.Context, string, string, string, string, []workset.Target) (workspace.Workspace, error)
	AttachMemberWorktree(context.Context, string, string, string) (workspace.Workspace, error)
}
type InspectionWorktreeLookup interface {
	Get(context.Context, string) (worktree.Worktree, error)
	Create(context.Context, worktree.CreateInput) (worktree.Worktree, error)
}
type ManagedInspectionMaterializer struct {
	Workspaces   InspectionWorkspaceService
	Worktrees    InspectionWorktreeLookup
	Repositories RepositoryLookup
}

func (m ManagedInspectionMaterializer) MaterializeInspection(ctx context.Context, spec CanonicalSpec) (workset.RuntimeWorkset, []recoverystatus.WorktreeObservation, error) {
	w := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersionProjectInspection, OwnerKind: workset.RuntimeOwnerProjectInspection, ProjectID: spec.Probe.ProjectID, InspectionOwnerTaskID: spec.Probe.TaskID, InspectionOperationID: spec.Probe.ID, RunID: spec.Probe.RunID, PrimaryMemberID: spec.PrimaryMemberID, BackendConfigID: spec.BackendSelection.ConfigID, BackendConfigFingerprint: spec.BackendSelection.ConfigFingerprint, Model: spec.Model, ReasoningEffort: spec.ReasoningEffort, ServiceTier: spec.ServiceTier, RuntimeAdapterIdentity: "codex-app-server", RuntimeProtocolIdentity: "0.149.0-project-inspection-v1", CreatedAt: time.Now().UTC()}
	if w.RunID == "" || spec.Probe.Canonical == nil {
		return w, nil, errors.New("confirmed Inspection and reserved Run required")
	}
	if m.Workspaces == nil || m.Worktrees == nil || m.Repositories == nil {
		return w, nil, errors.New("Inspection managed workspace dependencies unavailable")
	}
	plan, err := m.Workspaces.PlanInspectionMembers(ctx, spec.Probe.ProjectID, spec.Probe.TaskID, spec.Probe.ID, spec.Targets)
	if err != nil {
		return w, nil, err
	}
	w.WorkspaceID = plan.WorkspaceID
	if spec.WorkspaceID != "" && spec.WorkspaceID != w.WorkspaceID {
		return w, nil, errors.New("Inspection workspace plan changed")
	}
	resource, err := m.Workspaces.EnsurePlannedInspectionMembers(ctx, spec.Probe.ProjectID, spec.Probe.TaskID, spec.Probe.ID, w.WorkspaceID, spec.Targets)
	if err != nil {
		return w, nil, err
	}
	if len(resource.Members) != len(spec.Targets) {
		return w, nil, errors.New("Inspection member set changed")
	}
	git := gitrepo.NewClient(gitrepo.Config{})
	for _, target := range spec.Targets {
		var member workspace.Member
		for _, candidate := range resource.Members {
			if candidate.ID == target.MemberID {
				member = candidate
			}
		}
		if member.ID == "" || member.RepositoryID != target.RepositoryID || member.Version != target.MemberVersion || member.OriginCommit != target.OriginCommit || target.Write.Kind != "NONE" || !target.Read {
			return w, nil, errors.New("Inspection member does not match confirmed target")
		}
		if member.WorktreeID == "" {
			repo, err := m.Repositories.Get(ctx, target.RepositoryID)
			if err != nil {
				return w, nil, err
			}
			if repo.ProjectID != spec.Probe.ProjectID {
				return w, nil, errors.New("Inspection repository owner changed")
			}
			tree, err := m.Worktrees.Create(ctx, worktree.CreateInput{PreserveOnFailure: true, MemberID: member.ID, MemberVersion: member.Version, MemberAlias: member.Alias, RepositoryID: repo.ID, RepositoryPath: repo.Path, OwnerKind: "PROJECT_INSPECTION", OwnerID: spec.Probe.ID, RunID: w.RunID, WorkspaceID: w.WorkspaceID, BaseCommit: member.OriginCommit})
			if err != nil {
				return w, nil, err
			}
			resource, err = m.Workspaces.AttachMemberWorktree(ctx, w.WorkspaceID, member.ID, tree.ID)
			if err != nil {
				return w, nil, err
			}
			member.WorktreeID = tree.ID
		}
		tree, err := m.Worktrees.Get(ctx, member.WorktreeID)
		if err != nil {
			return w, nil, err
		}
		root, identity, err := recoverystatus.RootIdentity(tree.Path)
		if err != nil {
			return w, nil, err
		}
		head, err := git.ResolveHEAD(ctx, root)
		if err != nil {
			return w, nil, err
		}
		snapshot, err := git.Snapshot(ctx, root)
		if err != nil {
			return w, nil, err
		}
		var readScope workset.ReadScope
		for _, confirmed := range spec.Probe.Canonical.Targets {
			if confirmed.RepositoryID == member.RepositoryID && confirmed.MemberID == member.ID {
				readScope = workset.ReadScope{Kind: string(confirmed.Scope.Kind), Paths: append([]string(nil), confirmed.Scope.Paths...)}
			}
		}
		if readScope.Kind == "" {
			return w, nil, errors.New("confirmed Inspection read scope missing")
		}
		w.Members = append(w.Members, workset.RuntimeMemberBinding{RepositoryID: member.RepositoryID, RepositoryAlias: member.Alias, MemberID: member.ID, MemberVersion: member.Version, WorktreeID: tree.ID, CanonicalRoot: root, RootFileIdentity: identity, OriginCommit: member.OriginCommit, StartHEAD: head, StartSnapshot: snapshot, Read: true, IntendedReadScope: readScope, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: identity})
	}
	w, err = w.WithHash()
	if err != nil {
		return w, nil, err
	}
	before, err := recoverystatus.ObserveWorkset(ctx, w)
	return w, before, err
}
