package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/run"
	"sort"
)

type ReviewPlanContext interface {
	ListRepositories(context.Context, string, aicontext.ListOptions) ([]aicontext.RepositoryContext, error)
	ListProjectModuleMap(context.Context, string) ([]module.Module, error)
}
type ReviewPlanResolution struct {
	Plan                          run.ReviewPlan
	Scope                         run.ReviewScope
	Blockers                      []string
	RepositoryID, PrimaryModuleID string
}

// ResolveInitialReviewPlan is shared by standalone and composed Task Review starts.
func ResolveInitialReviewPlan(ctx context.Context, service ReviewPlanContext, sessionID, repositoryID string, input InitialReviewPlan) (ReviewPlanResolution, error) {
	result := ReviewPlanResolution{Plan: run.ReviewPlan{Mode: run.ReviewMode(input.Mode)}}
	if result.Plan.Mode == run.ReviewModeRepository {
		if len(input.SelectedModules) > 0 {
			result.Blockers = append(result.Blockers, "REPOSITORY plan cannot include selectedModules")
			return result, nil
		}
		repositories, err := service.ListRepositories(ctx, sessionID, aicontext.ListOptions{Limit: 100})
		if err != nil {
			return result, err
		}
		for _, value := range repositories {
			if value.ID == repositoryID {
				result.RepositoryID = value.ID
			}
		}
		if repositoryID == "" && len(repositories) == 1 {
			result.RepositoryID = repositories[0].ID
		}
		if result.RepositoryID == "" {
			result.Blockers = append(result.Blockers, "Repository-wide Feature Area Review requires one unambiguous Project Repository.")
			return result, nil
		}
		result.Scope = run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
		result.Plan.InitialScope = result.Scope
		return result, nil
	}
	if result.Plan.Mode != run.ReviewModeFocused {
		result.Blockers = append(result.Blockers, "Feature Area Review requires a FOCUSED module plan or an explicit REPOSITORY-wide plan.")
		return result, nil
	}
	modules, err := service.ListProjectModuleMap(ctx, sessionID)
	if err != nil {
		return result, err
	}
	byKey := make(map[string]module.Module, len(modules))
	for _, value := range modules {
		if len(value.ProductionFacts) != 0 {
			byKey[value.Key] = value
		}
	}
	seen, roots := map[string]struct{}{}, map[string]struct{}{}
	primary := false
	for _, selected := range input.SelectedModules {
		if _, duplicate := seen[selected.Key]; duplicate {
			result.Blockers = append(result.Blockers, "The Feature Area relevance plan contains a duplicate module key.")
			continue
		}
		seen[selected.Key] = struct{}{}
		role := run.ReviewModuleRole(selected.Role)
		if role != run.ReviewModulePrimary && role != run.ReviewModuleSupporting {
			result.Blockers = append(result.Blockers, "The Feature Area relevance plan contains an invalid module role.")
			continue
		}
		value, ok := byKey[selected.Key]
		if !ok {
			result.Blockers = append(result.Blockers, "Selected module \""+selected.Key+"\" is not available in the current Project Module Map.")
			continue
		}
		result.Plan.SelectedModules = append(result.Plan.SelectedModules, run.ReviewModuleSelection{Key: value.Key, Role: role})
		if role == run.ReviewModulePrimary {
			primary = true
			if result.PrimaryModuleID == "" {
				result.PrimaryModuleID = value.ID
			}
		}
		for _, facts := range value.ProductionFacts {
			if result.RepositoryID == "" {
				result.RepositoryID = facts.RepositoryID
			}
			if facts.RepositoryID != result.RepositoryID {
				result.Blockers = append(result.Blockers, "Selected modules span multiple Repositories; one focused Review currently requires one Repository.")
				continue
			}
			for _, root := range facts.ProductionRoots {
				roots[root] = struct{}{}
			}
		}
	}
	if !primary {
		result.Blockers = append(result.Blockers, "Focused Feature Area Review requires at least one PRIMARY module.")
	}
	paths := make([]string, 0, len(roots))
	for path := range roots {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		result.Blockers = append(result.Blockers, "The selected modules resolve to no valid production Review scope.")
	}
	result.Scope = run.ReviewScope{Kind: run.ReviewScopePaths, Paths: paths}
	result.Plan.InitialScope = result.Scope
	if repositoryID != "" && result.RepositoryID != repositoryID {
		result.Blockers = append(result.Blockers, "Selected modules must belong to the requested Repository")
	}
	return result, nil
}
