package developmentcycle

import (
	"context"
	"encoding/hex"
	"strings"

	bridgegit "github.com/lazyun/devbridge/internal/git"
)

type CreateBranchInput struct {
	ProjectID      string `json:"projectId,omitempty"`
	AISessionID    string `json:"aiSessionId"`
	RepositoryID   string `json:"repositoryId"`
	DevelopmentRef string `json:"developmentRef"`
	BaseCommit     string `json:"baseCommit"`
}

type branchCreator interface {
	CreateLocalBranch(context.Context, string, string, string) (bridgegit.LocalBranch, error)
}

// CreateBranch changes Git only. Selection remains a separate durable command.
func (s *Service) CreateBranch(ctx context.Context, in CreateBranchInput) (bridgegit.LocalBranch, error) {
	_, hashErr := hex.DecodeString(in.BaseCommit)
	if in.ProjectID == "" || in.RepositoryID == "" || len(in.DevelopmentRef) > 1024 || len(in.DevelopmentRef) <= len("refs/heads/") || !strings.HasPrefix(in.DevelopmentRef, "refs/heads/") || hashErr != nil || (len(in.BaseCommit) != 40 && len(in.BaseCommit) != 64) {
		return bridgegit.LocalBranch{}, ErrInvalidArgument
	}
	if _, err := s.projects.Get(ctx, in.ProjectID); err != nil {
		return bridgegit.LocalBranch{}, err
	}
	repo, err := s.repos.Get(ctx, in.RepositoryID)
	if err != nil {
		return bridgegit.LocalBranch{}, err
	}
	if repo.ProjectID != in.ProjectID {
		return bridgegit.LocalBranch{}, ErrRepositoryMismatch
	}
	creator, ok := s.git.(branchCreator)
	if !ok {
		return bridgegit.LocalBranch{}, ErrInvalidArgument
	}
	return creator.CreateLocalBranch(ctx, repo.Path, in.DevelopmentRef, in.BaseCommit)
}
