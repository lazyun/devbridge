package developmentcycle

import (
	"context"
	"errors"
	"testing"

	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
)

type createBranchProjects struct{ value project.Project }

func (s createBranchProjects) Get(context.Context, string) (project.Project, error) {
	return s.value, nil
}

type createBranchRepos struct{ value repository.Repository }

func (s createBranchRepos) Get(context.Context, string) (repository.Repository, error) {
	return s.value, nil
}
func (s createBranchRepos) List(context.Context) ([]repository.Repository, error) {
	return []repository.Repository{s.value}, nil
}

type createBranchGit struct {
	calls  int
	input  CreateBranchInput
	branch bridgegit.LocalBranch
}

func (s *createBranchGit) LocalBranches(context.Context, string) (bridgegit.BranchInventory, error) {
	return bridgegit.BranchInventory{}, nil
}
func (s *createBranchGit) CreateLocalBranch(_ context.Context, path, ref, base string) (bridgegit.LocalBranch, error) {
	s.calls++
	s.input = CreateBranchInput{RepositoryID: path, DevelopmentRef: ref, BaseCommit: base}
	return s.branch, nil
}

func TestCreateBranchDelegatesGitOnlyWithoutSelectionState(t *testing.T) {
	git := &createBranchGit{branch: bridgegit.LocalBranch{FullRef: "refs/heads/dev/card", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	service := NewService(nil, createBranchProjects{value: project.Project{ID: "project"}}, createBranchRepos{value: repository.Repository{ID: "repo", ProjectID: "project", Path: "/repo"}}, git)
	got, err := service.CreateBranch(context.Background(), CreateBranchInput{ProjectID: "project", RepositoryID: "repo", DevelopmentRef: "refs/heads/dev/card", BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil || got != git.branch || git.calls != 1 || git.input.RepositoryID != "/repo" || git.input.DevelopmentRef != got.FullRef || git.input.BaseCommit != got.HeadSHA {
		t.Fatalf("create result=%+v calls=%d input=%+v err=%v", got, git.calls, git.input, err)
	}
}

func TestCreateBranchRejectsRepositoryOutsideProjectBeforeGit(t *testing.T) {
	git := &createBranchGit{}
	service := NewService(nil, createBranchProjects{value: project.Project{ID: "project"}}, createBranchRepos{value: repository.Repository{ID: "repo", ProjectID: "other", Path: "/repo"}}, git)
	_, err := service.CreateBranch(context.Background(), CreateBranchInput{ProjectID: "project", RepositoryID: "repo", DevelopmentRef: "refs/heads/dev/card", BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if !errors.Is(err, ErrRepositoryMismatch) || git.calls != 0 {
		t.Fatalf("mismatch err=%v git calls=%d", err, git.calls)
	}
}
