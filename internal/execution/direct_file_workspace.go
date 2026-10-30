package execution

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workspace"
)

// PrepareApprovedFileWorkspace is an internal continuation of a durable
// PREPARING approval intent. It uses the existing workspace manager, never
// starts an agent, and does not grant file access. The Authority revalidates
// the original approval in its transaction after this external preparation.
func (c *Coordinator) PrepareApprovedFileWorkspace(ctx context.Context, grant directfile.State) error {
	fail := func(message string) error { return directfile.Failure(directfile.WorktreeUnavailable, message) }
	if c == nil || c.Tasks == nil || c.Reviews == nil || c.Runs == nil || c.Workspaces == nil || c.Worktrees == nil || c.Repositories == nil || grant.State != "PREPARING" || grant.WorkflowID == "" {
		return fail("approved workspace preparation is unavailable")
	}
	t, err := c.Tasks.Get(ctx, grant.TaskID)
	if err != nil {
		return err
	}
	if len(t.RepositoryTargets) > 0 {
		return c.prepareApprovedMultiFileWorkspace(ctx, grant, t)
	}
	if t.Status != task.StatusActive || t.Phase != task.PhaseImplementationReady || t.ProjectID != grant.ProjectID || t.FeatureAreaID != grant.FeatureAreaID || t.RepositoryID != grant.RepositoryID || t.CurrentDefinitionRevisionID != grant.TaskDefinitionRevisionID || t.ReviewApprovalID != grant.DecisionID || t.CurrentReviewID == "" {
		return fail("approved workspace preparation target changed")
	}
	source, err := c.Reviews.Get(ctx, t.CurrentReviewID)
	if err != nil {
		return err
	}
	if source.TaskID != t.ID || source.ProjectID != t.ProjectID || source.FeatureAreaID != t.FeatureAreaID || source.RepositoryID != t.RepositoryID || source.BaseCommit != t.BaseCommit || source.RunID == "" {
		return fail("approved Review workspace provenance is unavailable")
	}
	sourceRun, err := c.Runs.Get(ctx, source.RunID)
	if err != nil {
		return err
	}
	if sourceRun.ReviewID != source.ID || sourceRun.TaskID != t.ID || sourceRun.ProjectID != t.ProjectID || sourceRun.FeatureAreaID != t.FeatureAreaID || sourceRun.Status != run.StatusSucceeded {
		return fail("approved Review Run provenance is unavailable")
	}
	reader, ok := c.Workspaces.(interface {
		GetByFeatureArea(context.Context, string) (workspace.Workspace, error)
	})
	if !ok {
		return fail("workspace continuity lookup is unavailable")
	}
	w, err := reader.GetByFeatureArea(ctx, t.FeatureAreaID)
	if errors.Is(err, workspace.ErrNotFound) {
		// An old reviewed workspace must never be silently replaced with a clean one.
		if sourceRun.WorkspaceID != "" || sourceRun.WorktreeID != "" {
			return fail("the reviewed workspace is missing; continuity requires repair")
		}
		w, err = c.Workspaces.EnsureForFeatureArea(ctx, t.ProjectID, t.RepositoryID, t.FeatureAreaID, t.BaseCommit)
	}
	if err != nil {
		return err
	}
	member, memberErr := w.MemberForRepository(t.RepositoryID)
	if memberErr != nil {
		return fail("reviewed workspace repository member is unavailable")
	}
	if w.ProjectID != t.ProjectID || w.FeatureAreaID != t.FeatureAreaID || w.Status != workspace.StatusReady || w.RuntimeQuarantined || sourceRun.WorkspaceID != "" && sourceRun.WorkspaceID != w.ID || sourceRun.WorktreeID != "" && sourceRun.WorktreeID != member.WorktreeID {
		return fail("reviewed workspace identity or continuity changed")
	}
	repositoryValue, err := c.Repositories.Get(ctx, t.RepositoryID)
	if err != nil {
		return err
	}
	// The original, terminal Review Run is only existing worktree provenance;
	// it is neither resumed nor changed and its read grant is never upgraded.
	_, _, err = c.workspaceWorktree(ctx, w, t.BaseCommit, repositoryValue, sourceRun)
	return err
}

// prepareApprovedMultiFileWorkspace only materializes the exact frozen target
// set.  It never consults current project inventory and never invents a Run or
// coding execution. Worktrees are created through the existing workspace
// mechanism when a member has no worktree; this coordinator deliberately does
// not activate the grant -- DirectFileAuthority records every member and does
// that after all roots are proven.
func (c *Coordinator) prepareApprovedMultiFileWorkspace(ctx context.Context, grant directfile.State, t task.Task) error {
	fail := func(message string) error { return directfile.Failure(directfile.WorktreeUnavailable, message) }
	if grant.ProjectID != t.ProjectID || grant.FeatureAreaID != t.FeatureAreaID || grant.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID || grant.DecisionID != t.ReviewApprovalID || grant.WorkspaceID == "" || len(grant.RepositoryTargets) != len(t.RepositoryTargets) {
		return fail("approved multi-repository workspace target changed")
	}
	planner, ok := c.Workspaces.(plannedWorkspaceService)
	if !ok {
		return fail("exact workspace member preparation is unavailable")
	}
	w, err := planner.EnsurePlannedMembers(ctx, t.ProjectID, t.FeatureAreaID, grant.WorkspaceID, t.RepositoryTargets)
	if err != nil {
		return err
	}
	if w.ID != grant.WorkspaceID || w.ProjectID != t.ProjectID || w.FeatureAreaID != t.FeatureAreaID || w.Status != workspace.StatusReady || w.RuntimeQuarantined {
		return fail("approved multi-repository workspace is unavailable")
	}
	for _, target := range t.RepositoryTargets {
		member, memberErr := w.MemberForRepository(target.RepositoryID)
		if memberErr != nil || member.ID != target.MemberID || member.Version != target.MemberVersion || member.OriginCommit != target.OriginCommit || member.WorktreeID == "" {
			return fail("approved workspace member has not completed preparation")
		}
		wt, ok := c.Worktrees.(worktreeGetter)
		if !ok {
			return fail("workspace worktree lookup is unavailable")
		}
		value, e := wt.Get(ctx, member.WorktreeID)
		if e != nil || value.RepositoryID != target.RepositoryID || value.BaseCommit != target.OriginCommit || value.Status != "READY" {
			return fail("approved workspace member worktree is unavailable")
		}
	}
	return nil
}
