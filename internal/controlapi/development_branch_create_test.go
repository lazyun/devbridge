package controlapi_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
)

func TestCreateDevelopmentBranchForSessionIsGitOnlyAndEntityReplayable(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	ctx := f.ctx
	initializePresentationRepository(t, f)
	base := repositoryHead(t, f, "HEAD")
	in := developmentcycle.CreateBranchInput{AISessionID: f.sessionID, RepositoryID: f.repositoryID, DevelopmentRef: "refs/heads/dev/card", BaseCommit: base}
	created, err := f.facade.CreateDevelopmentBranchForSession(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.FullRef != in.DevelopmentRef || created.HeadSHA != base || created.RepositoryCheckout {
		t.Fatalf("created branch=%+v", created)
	}
	contextBeforeSelect, err := f.facade.DevelopmentContextForSession(ctx, f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !contextBeforeSelect.DevelopmentBranchSelectionRequired || len(contextBeforeSelect.Repositories) != 1 || contextBeforeSelect.Repositories[0].DevelopmentRefStatus != "NOT_SELECTED" {
		t.Fatalf("creation selected or checked out the branch: %+v", contextBeforeSelect)
	}
	replay, err := f.facade.CreateDevelopmentBranchForSession(ctx, in)
	if err != nil || replay != created {
		t.Fatalf("same entity replay=%+v err=%v", replay, err)
	}

	// Advance the checkout to a distinct immutable commit. Reusing the ref with
	// this opposite base must conflict and must not alter branch selection.
	repositoryGit(t, f, "commit", "--allow-empty", "-m", "second base")
	opposite := repositoryHead(t, f, "HEAD")
	if opposite == base {
		t.Fatal("fixture did not advance base")
	}
	_, err = f.facade.CreateDevelopmentBranchForSession(ctx, developmentcycle.CreateBranchInput{AISessionID: f.sessionID, RepositoryID: f.repositoryID, DevelopmentRef: in.DevelopmentRef, BaseCommit: opposite})
	var conflict *controlapi.Error
	if !errors.As(err, &conflict) || conflict.Code != controlapi.CodeConflict {
		t.Fatalf("opposite base err=%v", err)
	}
	cycle, err := f.facade.SelectDevelopmentBranchForSession(ctx, developmentcycle.SelectInput{AISessionID: f.sessionID, RepositoryID: f.repositoryID, DevelopmentRef: in.DevelopmentRef, RequestID: "select-created"})
	if err != nil || cycle.DevelopmentRef != in.DevelopmentRef || cycle.SelectedHeadSHA != base {
		t.Fatalf("separate selection=%+v err=%v", cycle, err)
	}
	if head := repositoryHead(t, f, "HEAD"); head != opposite {
		t.Fatalf("selection checked out branch: head=%s want=%s", head, opposite)
	}
}

func TestCreateDevelopmentBranchForSessionRejectsForeignSessionRepository(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	ctx := f.ctx
	initializePresentationRepository(t, f)
	foreign, err := f.plane.Projects.Create(ctx, "foreign")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := f.bindProject(t, foreign.ID, "foreign-branch")
	_, err = f.facade.CreateDevelopmentBranchForSession(ctx, developmentcycle.CreateBranchInput{AISessionID: sessionID, RepositoryID: f.repositoryID, DevelopmentRef: "refs/heads/foreign", BaseCommit: repositoryHead(t, f, "HEAD")})
	var apiErr *controlapi.Error
	if !errors.As(err, &apiErr) || apiErr.Code != controlapi.ErrorCode("REPOSITORY_PROJECT_MISMATCH") {
		t.Fatalf("foreign repository err=%v", err)
	}
}

func repositoryGit(t *testing.T, f ownershipWorkflowFixture, args ...string) {
	t.Helper()
	repo, err := f.plane.Repositories.Get(context.Background(), f.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", args...)
	command.Dir = repo.Path
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func repositoryHead(t *testing.T, f ownershipWorkflowFixture, ref string) string {
	t.Helper()
	repo, err := f.plane.Repositories.Get(context.Background(), f.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "rev-parse", ref)
	command.Dir = repo.Path
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}
