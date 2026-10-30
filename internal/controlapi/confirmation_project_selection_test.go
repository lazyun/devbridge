package controlapi

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"testing"
)

func TestProjectSelectionDoesNotRequireRepositoryAndFreezesRows(t *testing.T) {
	opts := c.Options{Projects: []c.Choice{{ID: "p", Available: true}}, RepositoryInventoryComplete: true, DevelopmentContexts: []c.DevelopmentContextOption{{RepositoryID: "app", Available: true, Branches: []c.Choice{{ID: "refs/heads/dev", Available: true}}}, {RepositoryID: "sdk", Available: true, Branches: []c.Choice{{ID: "refs/heads/dev", Available: true}}}, {RepositoryID: "backend", Available: false}}}
	project := c.Intent{IntentID: "intent", ProjectID: "p", Action: "SELECT_PROJECT"}
	if err := validateProjectSetupSelection(project, c.Selection{ProjectID: "p"}, opts); err != nil {
		t.Fatal(err)
	}
	if steps := confirmationSteps(project); len(steps) != 1 || steps[0].Kind != "BIND_PROJECT" {
		t.Fatalf("steps=%+v", steps)
	}
	selection := c.Selection{ProjectID: "p", DevelopmentContexts: []c.RepositoryDevelopmentSelection{{RepositoryID: "app", DevelopmentRef: "refs/heads/dev"}, {RepositoryID: "sdk", DevelopmentRef: "refs/heads/dev"}}}
	multi := c.Intent{IntentID: "intent", ProjectID: "p", Action: "CONFIGURE_DEVELOPMENT_CONTEXT", Commit: &c.CommitInput{Selection: selection}}
	if err := validateProjectSetupSelection(multi, selection, opts); err != nil {
		t.Fatal(err)
	}
	steps := confirmationSteps(multi)
	if len(steps) != 2 || steps[0].RequestID == steps[1].RequestID || steps[0].RepositoryID != "app" || steps[1].RepositoryID != "sdk" {
		t.Fatalf("steps=%+v", steps)
	}
	selection.DevelopmentContexts = append(selection.DevelopmentContexts, c.RepositoryDevelopmentSelection{RepositoryID: "backend", DevelopmentRef: "refs/heads/dev"})
	if err := validateProjectSetupSelection(multi, selection, opts); err == nil {
		t.Fatal("unavailable target accepted")
	}
	selection.DevelopmentContexts = selection.DevelopmentContexts[:2]
	selection.DevelopmentContexts[1] = selection.DevelopmentContexts[0]
	if err := validateProjectSetupSelection(multi, selection, opts); err == nil {
		t.Fatal("duplicate target accepted")
	}
}
