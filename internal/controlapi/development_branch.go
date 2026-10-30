package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/developmentcycle"
)

type DevelopmentCycleService interface {
	Context(context.Context, string) (developmentcycle.Context, error)
	Select(context.Context, developmentcycle.SelectInput) (developmentcycle.Cycle, error)
}

// DevelopmentContext reads the common Project-owned context for trusted Console callers.
func (f *Facade) DevelopmentContext(ctx context.Context, projectID string) (developmentcycle.Context, error) {
	if f.deps.DevelopmentCycles == nil {
		return developmentcycle.Context{}, invalid("development branch service unavailable")
	}
	value, err := f.deps.DevelopmentCycles.Context(ctx, projectID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

// SelectDevelopmentBranch changes workflow metadata only; it never checks out Git.
func (f *Facade) SelectDevelopmentBranch(ctx context.Context, input developmentcycle.SelectInput) (developmentcycle.Cycle, error) {
	if f.deps.DevelopmentCycles == nil {
		return developmentcycle.Cycle{}, invalid("development branch service unavailable")
	}
	value, err := f.deps.DevelopmentCycles.Select(ctx, input)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) DevelopmentContextForSession(ctx context.Context, sessionID string) (developmentcycle.Context, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return developmentcycle.Context{}, err
	}
	return f.DevelopmentContext(ctx, projectID)
}

func (f *Facade) SelectDevelopmentBranchForSession(ctx context.Context, input developmentcycle.SelectInput) (developmentcycle.Cycle, error) {
	projectID, err := f.sessionProject(ctx, input.AISessionID)
	if err != nil {
		return developmentcycle.Cycle{}, err
	}
	// Caller-supplied Project identity cannot override the immutable session boundary.
	if input.ProjectID != "" && input.ProjectID != projectID {
		return developmentcycle.Cycle{}, MapError(developmentcycle.ErrRepositoryMismatch)
	}
	input.ProjectID = projectID
	return f.SelectDevelopmentBranch(ctx, input)
}
