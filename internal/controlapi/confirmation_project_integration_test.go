package controlapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProjectOnlyConfirmationBindsOriginalInitializedSessionWithoutRepository(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), "confirmation-project-integration")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	project, err := plane.Projects.Create(ctx, "Empty logical project")
	if err != nil {
		t.Fatal(err)
	}
	session, err := plane.AIContext.InitializeSession(ctx, aiprojectselection.ClientBindingReference{CallerScope: "confirmation-project-integration"}, "initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := facade.PrepareConfirmation(ctx, c.PrepareInput{AISessionID: session.AISessionID, RequestID: "prepare-project", Purpose: c.ProjectSetup, Action: "SELECT_PROJECT", ProjectSetup: &c.ProjectBody{Summary: "Enter project"}, Variants: []c.Variant{{ID: "enter", Label: "Enter", Summary: "Bind this Session to the logical project"}}})
	if err != nil {
		t.Fatal(err)
	}
	options, err := facade.ConfirmationOptions(ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: session.AISessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: c.ProjectSetup, ContentHash: prepared.ContentHash}, ProjectID: project.ID})
	if err != nil || len(options.Repositories) != 0 {
		t.Fatalf("options=%+v err=%v", options, err)
	}
	unbound, err := plane.AIContext.InitializeSession(ctx, aiprojectselection.ClientBindingReference{CallerScope: "confirmation-project-integration"}, "", session.AISessionID)
	if err != nil || unbound.BoundProjectID != nil {
		t.Fatalf("preview bound Session: %+v %v", unbound, err)
	}
	input := c.CommitInput{AISessionID: session.AISessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: c.ProjectSetup, Action: prepared.Action, RequestID: "enter-project", ContentHash: prepared.ContentHash, VariantID: "enter", Selection: c.Selection{ProjectID: project.ID}}
	got, err := facade.CommitConfirmation(ctx, input)
	if err != nil || got.State != "COMPLETED" || len(got.Steps) != 1 {
		t.Fatalf("commit=%+v err=%v", got, err)
	}
	replay, err := facade.CommitConfirmation(ctx, input)
	if err != nil || replay.State != "COMPLETED" || replay.Steps[0].RequestID != got.Steps[0].RequestID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	for _, table := range []string{"development_cycles", "tasks", "reviews", "runs", "workspaces"} {
		var count int
		if err := plane.DB.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}

func TestConfigureDevelopmentContextThreeRepositoryReceiptsAndUnavailableUnrelated(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), "confirmation-development-integration")
	root := t.TempDir()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	repositories := []projectregistration.RepositoryInput{}
	paths := []string{}
	for _, alias := range []string{"app", "sdk", "backend"} {
		path := filepath.Join(root, alias)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-b", "dev"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = path
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git fixture: %s %v", out, err)
			}
		}
		repositories = append(repositories, projectregistration.RepositoryInput{Path: path, Alias: alias})
		paths = append(paths, path)
	}
	registered, err := plane.ProjectRegistration.Register(ctx, projectregistration.Input{RequestID: "register-three", Name: "Three", Repositories: repositories})
	if err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "confirmation-development-integration"}
	session, err := plane.AIContext.InitializeSession(ctx, ref, "initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plane.AIContext.BindProject(ctx, ref, session.AISessionID, registered.Project.ID, "bind"); err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	// Make an unrelated registered member unreachable after registration; app/sdk remain eligible.
	if err := os.Rename(paths[2], paths[2]+"-offline"); err != nil {
		t.Fatal(err)
	}
	prepared, err := facade.PrepareConfirmation(ctx, c.PrepareInput{AISessionID: session.AISessionID, RequestID: "prepare-branches", Purpose: c.ProjectSetup, Action: "CONFIGURE_DEVELOPMENT_CONTEXT", ProjectSetup: &c.ProjectBody{Summary: "Configure app and sdk"}, Variants: []c.Variant{{ID: "configure", Label: "Configure", Summary: "Select each repository's development branch"}}})
	if err != nil {
		t.Fatal(err)
	}
	read := c.ReadInput{AISessionID: session.AISessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: c.ProjectSetup, ContentHash: prepared.ContentHash}
	options, err := facade.ConfirmationOptions(ctx, c.OptionsInput{ReadInput: read, ProjectID: registered.Project.ID})
	if err != nil || !options.RepositoryInventoryComplete || len(options.DevelopmentContexts) != 3 {
		t.Fatalf("options=%+v err=%v", options, err)
	}
	selection := c.Selection{ProjectID: registered.Project.ID}
	unavailable := 0
	for _, row := range options.DevelopmentContexts {
		if !row.Available {
			unavailable++
			continue
		}
		selection.DevelopmentContexts = append(selection.DevelopmentContexts, c.RepositoryDevelopmentSelection{RepositoryID: row.RepositoryID, DevelopmentRef: "refs/heads/dev"})
	}
	if unavailable != 1 || len(selection.DevelopmentContexts) != 2 {
		t.Fatalf("options=%+v", options)
	}
	var before int
	if err := plane.DB.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM development_cycles").Scan(&before); err != nil || before != 0 {
		t.Fatalf("read selected branches: %d %v", before, err)
	}
	input := c.CommitInput{AISessionID: session.AISessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: c.ProjectSetup, Action: prepared.Action, RequestID: "configure-two", ContentHash: prepared.ContentHash, VariantID: "configure", Selection: selection}
	// Fail only the final confirmation receipt after both canonical branch
	// commands committed. A completed intent is never reopened by the fixture.
	if _, err := plane.DB.SQL().ExecContext(ctx, `CREATE TRIGGER configure_receipt_fault BEFORE UPDATE OF intent_json ON confirmation_intents WHEN json_extract(NEW.intent_json,'$.action')='CONFIGURE_DEVELOPMENT_CONTEXT' AND json_extract(NEW.intent_json,'$.state')='COMPLETED' BEGIN SELECT RAISE(ABORT,'injected final branch receipt loss'); END`); err != nil {
		t.Fatal(err)
	}
	got, err := facade.CommitConfirmation(ctx, input)
	if err == nil || got.State != "CONFIRMING" || len(got.Steps) != 2 || got.Steps[0].State != "COMPLETED" || got.Steps[1].State != "UNKNOWN" || got.Steps[0].RequestID == got.Steps[1].RequestID {
		t.Fatalf("receipt fault commit=%+v err=%v", got, err)
	}
	if _, err := plane.DB.SQL().ExecContext(ctx, `DROP TRIGGER configure_receipt_fault`); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := plane.DB.SQL().QueryRowContext(ctx, `SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, prepared.IntentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var lost c.Intent
	if err := json.Unmarshal([]byte(raw), &lost); err != nil || lost.State != "CONFIRMING" || len(lost.Steps) != 2 || lost.Steps[1].State != "UNKNOWN" {
		t.Fatalf("durable interrupted receipt=%+v err=%v", lost, err)
	}
	branchReceipts := make(map[string]string)
	for _, step := range lost.Steps {
		var receipt string
		if err := plane.DB.SQL().QueryRowContext(ctx, `SELECT result_json FROM development_branch_requests WHERE request_id=?`, step.RequestID).Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		branchReceipts[step.RequestID] = receipt
	}
	recovered, err := facade.ReadConfirmation(ctx, read)
	if err != nil || recovered.State != "COMPLETED" || len(recovered.Steps) != 2 {
		t.Fatalf("recovery=%+v err=%v", recovered, err)
	}
	for i, step := range recovered.Steps {
		if step.State != "COMPLETED" || step.RequestID != lost.Steps[i].RequestID || step.RepositoryID != lost.Steps[i].RepositoryID || step.DevelopmentRef != lost.Steps[i].DevelopmentRef {
			t.Fatalf("recovery changed exact branch identity: %+v", recovered)
		}
	}
	var afterRead string
	if err := plane.DB.SQL().QueryRowContext(ctx, `SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, prepared.IntentID).Scan(&afterRead); err != nil || afterRead != raw {
		t.Fatalf("read recovery wrote confirmation: %v", err)
	}
	// Receipt replay must survive both selected branches disappearing.
	for _, path := range paths[:2] {
		cmd := exec.Command("git", "update-ref", "-d", "refs/heads/dev")
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %s %v", out, err)
		}
	}
	replay, err := facade.CommitConfirmation(ctx, input)
	if err != nil || replay.State != "COMPLETED" {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	for requestID, before := range branchReceipts {
		var after string
		if err := plane.DB.SQL().QueryRowContext(ctx, `SELECT result_json FROM development_branch_requests WHERE request_id=?`, requestID).Scan(&after); err != nil || after != before {
			t.Fatalf("branch receipt changed on recovery/replay: %s %v", requestID, err)
		}
	}
	var cycles int
	if err := plane.DB.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM development_cycles`).Scan(&cycles); err != nil || cycles != 2 {
		t.Fatalf("recovery/replay created development cycles: %d %v", cycles, err)
	}
	for _, table := range []string{"tasks", "reviews", "runs", "workspaces"} {
		var count int
		if err := plane.DB.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}

func TestProjectSelectionInventoryIsCompleteBeyondDefaultPageOrExplicitlyUnavailable(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), "project-inventory-test")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	for i := 0; i < 55; i++ {
		if _, err := plane.Projects.Create(ctx, fmt.Sprintf("Project %03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "project-inventory-test"}
	current, err := plane.AIContext.InitializeSession(ctx, ref, "initialize", "")
	if err != nil || !current.AvailableProjectsComplete || len(current.AvailableProjects) != 55 {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	for i := 55; i < 101; i++ {
		if _, err := plane.Projects.Create(ctx, fmt.Sprintf("Project %03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	current, err = plane.AIContext.InitializeSession(ctx, ref, "", current.AISessionID)
	if err != nil || current.AvailableProjectsComplete || current.AvailableProjectsStatus != "UNAVAILABLE" || current.AvailableProjectsReadError != "PROJECT_INVENTORY_INCOMPLETE" {
		t.Fatalf("truncated as complete=%+v err=%v", current, err)
	}
}
