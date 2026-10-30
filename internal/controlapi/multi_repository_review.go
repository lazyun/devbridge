package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
)

// Preparation freezes prospective member identities using reads only. The
// confirmation content, rather than a later branch lookup, owns this target set.
func (f *Facade) prepareReviewTargets(ctx context.Context, in *confirmation.PrepareInput, project string) error {
	b := in.Review
	if b.RepositoryID != "" || len(b.RepositoryTargets) == 0 || !b.ReviewDepth.Valid() {
		return invalid("explicit read-only repository targets and Review depth required")
	}
	for _, v := range in.Variants {
		kind := review.ExecutorKind(v.ExecutorKind)
		if kind != review.ExecutorUpperLLM && kind != review.ExecutorCodingAgent {
			return invalid("multi-repository Review requires a supported executor")
		}
		if kind == review.ExecutorCodingAgent && v.PrimaryMemberID == "" {
			return invalid("multi-repository Coding Review requires an explicit primary member")
		}
	}
	if b.ReviewPlan.Mode != run.ReviewModeRepository || len(b.ReviewPlan.SelectedModules) != 0 || len(b.ReviewPlan.InitialScope.Paths) != 0 {
		return invalid("multi-repository Review requires an explicit repository plan")
	}
	b.ReviewPlan.InitialScope = run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
	planner, ok := f.deps.Workspaces.(interface {
		PlanMembers(context.Context, string, string, []workset.Target) (workset.Set, error)
	})
	if !ok {
		return errors.New("Workspace target planner unavailable")
	}
	aliases := map[string]bool{}
	for _, target := range b.RepositoryTargets {
		if !target.Read || target.Write.Kind != "NONE" || !workset.ValidCommit(target.OriginCommit) {
			return invalid("Review requires explicit read-only targets and immutable baselines")
		}

		repo, e := f.deps.Repositories.Get(ctx, target.RepositoryID)
		if e != nil {
			return e
		}
		if repo.ProjectID != project || aliases[repo.Name] {
			return invalid("target repository ownership or alias conflict")
		}
		aliases[repo.Name] = true
		resolved, readErr := git.NewClient(git.Config{}).ResolveCommit(ctx, repo.Path, target.OriginCommit)
		if readErr != nil || resolved != target.OriginCommit {
			return invalid("Review target baseline is unavailable in its repository")
		}
	}
	planned, e := planner.PlanMembers(ctx, project, b.FeatureAreaID, b.RepositoryTargets)
	if e != nil {
		return e
	}
	if b.WorkspaceID != "" && b.WorkspaceID != planned.WorkspaceID {
		return invalid("frozen Workspace changed")
	}
	b.WorkspaceID, b.RepositoryTargets = planned.WorkspaceID, planned.Targets
	for _, v := range in.Variants {
		if review.ExecutorKind(v.ExecutorKind) != review.ExecutorCodingAgent {
			continue
		}
		found := false
		for _, target := range planned.Targets {
			found = found || target.MemberID == v.PrimaryMemberID
		}
		if !found {
			return invalid("Coding Review primary member is outside the frozen target set")
		}
	}
	return nil
}
