package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/module"
)

// confirmationCreationPrerequisites is the shared read-only qualification for
// the options projection and the original CREATE_TASK step. It neither creates
// a Task nor prepares a workspace. The canonical creation and Review admission
// services still validate these facts immediately before their writes.
func (f *Facade) confirmationCreationPrerequisites(ctx context.Context, session, repository string, _ bool) (out CreatePlanningTaskInput, err error) {
	out.AISessionID, out.RepositoryID = session, repository
	project, err := f.sessionProject(ctx, session)
	if err != nil {
		return out, err
	}
	if f.deps.StartupContext == nil || f.deps.Repositories == nil {
		return out, &Error{Code: CodeInfrastructure, Message: "Task prerequisite readers unavailable"}
	}
	if repository != "" {
		repo, err := f.deps.Repositories.Get(ctx, repository)
		if err != nil {
			return out, err
		}
		if repo.ProjectID != project {
			return out, notFound()
		}
	}
	// Task creation never guesses a backend from inventory order. A Coding
	// Review or first Coding Implementation carries its explicit backend in the
	// same confirmation that authorizes that operation.
	modules, err := f.deps.StartupContext.ListModules(ctx, session, aicontext.ListOptions{Limit: 100})
	if err != nil {
		return out, err
	}
	for _, candidate := range modules {
		if candidate.ProjectID == project && candidate.Key == module.UnclassifiedKey {
			out.ModuleID = candidate.ID
			break
		}
	}
	if out.ModuleID == "" {
		return out, &Error{Code: CodeInvalidState, Message: "Project unclassified module is unavailable"}
	}
	if repository == "" {
		return out, nil
	}
	development, err := f.DevelopmentContextForSession(ctx, session)
	if err != nil {
		return out, err
	}
	for _, candidate := range development.Repositories {
		if candidate.RepositoryID != repository {
			continue
		}
		if candidate.DevelopmentBranchSelectionRequired {
			return out, developmentcycle.ErrNotSelected
		}
		if candidate.CurrentHeadSHA == "" {
			return out, developmentcycle.ErrRefMissing
		}
		out.BaseRevision = candidate.CurrentHeadSHA
		return out, nil
	}
	return out, developmentcycle.ErrNotSelected
}
