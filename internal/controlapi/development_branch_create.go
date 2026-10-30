package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/developmentcycle"
	bridgegit "github.com/lazyun/devbridge/internal/git"
)

type developmentBranchCreator interface {
	CreateBranch(context.Context, developmentcycle.CreateBranchInput) (bridgegit.LocalBranch, error)
}

func (f *Facade) CreateDevelopmentBranchForSession(ctx context.Context, in developmentcycle.CreateBranchInput) (bridgegit.LocalBranch, error) {
	projectID, err := f.sessionProject(ctx, in.AISessionID)
	if err != nil {
		return bridgegit.LocalBranch{}, err
	}
	if in.ProjectID != "" && in.ProjectID != projectID {
		return bridgegit.LocalBranch{}, MapError(developmentcycle.ErrRepositoryMismatch)
	}
	in.ProjectID = projectID
	creator, ok := f.deps.DevelopmentCycles.(developmentBranchCreator)
	if !ok {
		return bridgegit.LocalBranch{}, invalid("development branch creation unavailable")
	}
	value, err := creator.CreateBranch(ctx, in)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
