package controlapi

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/developmentcycle"
)

type developmentPort struct {
	input     developmentcycle.SelectInput
	projectID string
	err       error
}

func (p *developmentPort) Context(_ context.Context, projectID string) (developmentcycle.Context, error) {
	p.projectID = projectID
	return developmentcycle.Context{ProjectID: projectID}, p.err
}
func (p *developmentPort) Select(_ context.Context, input developmentcycle.SelectInput) (developmentcycle.Cycle, error) {
	p.input = input
	return developmentcycle.Cycle{ProjectID: input.ProjectID, SelectedByAISessionID: input.AISessionID}, p.err
}

func TestDevelopmentBranchFacadeRespectsSessionProjectAndTrustedConsole(t *testing.T) {
	p := &developmentPort{}
	f := &Facade{deps: Dependencies{DevelopmentCycles: p, AISessions: scopeSessions{"session-a": {ID: "session-a", ProjectID: "project-a"}}}}
	ctx := context.Background()
	if got, err := f.DevelopmentContextForSession(ctx, "session-a"); err != nil || got.ProjectID != "project-a" {
		t.Fatalf("session read=%+v %v", got, err)
	}
	if got, err := f.DevelopmentContext(ctx, "project-console"); err != nil || got.ProjectID != "project-console" {
		t.Fatalf("trusted read=%+v %v", got, err)
	}
	input := developmentcycle.SelectInput{AISessionID: "session-a", RepositoryID: "repo", DevelopmentRef: "refs/heads/dev/test", RequestID: "request"}
	if _, err := f.SelectDevelopmentBranchForSession(ctx, input); err != nil || p.input.ProjectID != "project-a" || p.input.AISessionID != "session-a" {
		t.Fatalf("session select=%+v %v", p.input, err)
	}
	input.ProjectID = "project-other"
	if _, err := f.SelectDevelopmentBranchForSession(ctx, input); err == nil || MapError(err).Code != "REPOSITORY_PROJECT_MISMATCH" {
		t.Fatalf("project override=%v", err)
	}
	input.AISessionID = ""
	if _, err := f.SelectDevelopmentBranch(ctx, input); err != nil || p.input.ProjectID != "project-other" {
		t.Fatalf("trusted select=%+v %v", p.input, err)
	}
	for _, domainErr := range []error{developmentcycle.ErrFinalizationPending, developmentcycle.ErrNotSelected, developmentcycle.ErrBranchNotFound, developmentcycle.ErrRefMissing, developmentcycle.ErrRepositoryMismatch, developmentcycle.ErrRequestConflict} {
		p.err = domainErr
		_, err := f.SelectDevelopmentBranch(ctx, input)
		var apiErr *Error
		if !errors.As(err, &apiErr) || string(apiErr.Code) != domainErr.Error() {
			t.Fatalf("lost domain code %v: %v", domainErr, err)
		}
	}
}
