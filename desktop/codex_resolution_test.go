package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	codexprobe "github.com/lazyun/devbridge/desktop/internal/codex"
	"github.com/lazyun/devbridge/desktop/internal/settings"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/test/real_e2e/harness"
)

// This helper is a separate app-server process. It exercises the production
// wire transport without contacting a model or modifying repository sources.
func TestCodexResolutionHelper(t *testing.T) {
	if os.Getenv("DEVBRIDGE_RESOLUTION_HELPER") != "1" {
		return
	}
	write := func(value any) { data, _ := json.Marshal(value); fmt.Println(string(data)) }
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Permissions string `json:"permissions"`
			} `json:"params"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &req)
		switch req.Method {
		case "initialize":
			write(map[string]any{"id": req.ID, "result": map[string]any{"userAgent": "resolution-test"}})
		case "initialized":
		case "config/read":
			write(map[string]any{"id": req.ID, "result": map[string]any{"config": map[string]any{}}})
		case "mcpServerStatus/list":
			write(map[string]any{"id": req.ID, "result": map[string]any{"data": []any{}}})
		case "thread/start":
			write(map[string]any{"id": req.ID, "result": map[string]any{"thread": map[string]any{"id": "resolution-thread"}, "activePermissionProfile": map[string]any{"id": req.Params.Permissions}}})
		case "thread/name/set":
			write(map[string]any{"id": req.ID, "result": map[string]any{}})
		case "turn/start":
			write(map[string]any{"id": req.ID, "result": map[string]any{"turn": map[string]any{"id": "resolution-turn", "status": "inProgress", "items": []any{}}}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "resolution-thread", "turn": map[string]any{"id": "resolution-turn", "status": "completed", "items": []any{}}}})
		default:
			if len(req.ID) != 0 {
				write(map[string]any{"id": req.ID, "error": map[string]any{"code": -32601, "message": "unsupported resolution fixture method: " + req.Method}})
			}
		}
	}
	os.Exit(0)
}

func TestDesktopProbeSelectionReachesProductionReviewWithMinimalPATH(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := harness.NewFixture(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	// The shared adversarial fixture includes an escaping symlink, which the
	// current review sandbox correctly rejects before launching any runtime.
	// This test concerns executable resolution; use a valid committed source
	// tree so that the launch path (including minimal PATH) is exercised.
	for _, args := range [][]string{{"rm", "--", "escape-link"}, {"commit", "-m", "runtime resolution fixture"}, {"rev-parse", "HEAD"}} {
		command := exec.CommandContext(ctx, "git", append([]string{"-C", fixture.SourceRepo}, args...)...)
		output, commandErr := command.CombinedOutput()
		if commandErr != nil {
			t.Fatalf("prepare resolution fixture: %v: %s", commandErr, output)
		}
		if args[0] == "rev-parse" {
			fixture.BaseCommit = strings.TrimSpace(string(output))
		}
	}
	launcher := filepath.Join(root, "node-bin", "codex")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "launches")
	// env resolves a sibling interpreter only if both probe and runtime share
	// the executable-directory PATH augmentation (the original second bug).
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env codex-node\n"), 0755); err != nil {
		t.Fatal(err)
	}
	helper := "#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"$2\" >> '" + calls + "'\nif [ \"$2\" = --version ]; then echo codex-cli-test; exit; fi\nexec '" + os.Args[0] + "' -test.run=^TestCodexResolutionHelper$\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(launcher), "codex-node"), []byte(helper), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("DEVBRIDGE_RESOLUTION_HELPER", "1")
	app := newApp(settings.PathsFor(root, root), nil)
	app.updateCodexProbe(launcher)
	if app.codex.Status != codexprobe.Available || app.codex.Executable != launcher {
		t.Fatalf("probe: %+v", app.codex)
	}
	t.Setenv(codexlaunch.SelectionEnv, app.codexSelectionFile())
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: fixture.DataRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	httpServer, err := bridgehttp.New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpServer.Handler())
	defer server.Close()
	if got := codexprobe.VerifyCore(server.URL, app.codexSelectionFile(), app.codex); got.Status != codexprobe.Available {
		t.Fatalf("guard: %+v", got)
	}
	project, err := plane.Projects.Create(ctx, "Resolution regression")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := plane.Repositories.RegisterInProject(ctx, project.ID, fixture.SourceRepo, "Source")
	if err != nil {
		t.Fatal(err)
	}
	development, err := plane.DevelopmentCycles.Context(ctx, project.ID)
	if err != nil || len(development.Repositories) != 1 || len(development.Repositories[0].Branches) == 0 {
		t.Fatalf("development context=%+v err=%v", development, err)
	}
	if _, err := plane.DevelopmentCycles.Select(ctx, developmentcycle.SelectInput{ProjectID: project.ID, RepositoryID: repo.ID, DevelopmentRef: development.Repositories[0].Branches[0].FullRef, RequestID: "desktop-resolution-baseline"}); err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "desktop-codex-resolution"}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, "desktop-resolution-session", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, project.ID, "desktop-resolution-bind"); err != nil {
		t.Fatal(err)
	}
	session, err := plane.AIContext.GetSession(ctx, initialized.AISessionID)
	if err != nil {
		t.Fatal(err)
	}
	area, err := plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: session.Session.ID, Title: "Resolve runtime", ProblemStatement: "Verify startup"})
	if err != nil {
		t.Fatal(err)
	}
	moduleValue, err := plane.Modules.EnsureUnclassified(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	configValue, err := plane.CodexConfigs.Get(ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	backendSelection := configValue.Selection("desktop-resolution-fixture")
	intent, err := plane.Tasks.Create(ctx, task.CreateInput{AISessionID: session.Session.ID, RequestID: "desktop-resolution-task", ProjectID: project.ID, FeatureAreaID: area.ID, ModuleID: moduleValue.ID, RepositoryID: repo.ID, CodexConfigID: codexconfig.DefaultConfigID, Goal: "Resolve runtime", ProblemStatement: area.ProblemStatement, BaseRevision: fixture.BaseCommit, Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := plane.Reviews.Create(ctx, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: intent.ID, ProjectID: project.ID, FeatureAreaID: area.ID, CreatedByAISessionID: session.Session.ID, RepositoryID: repo.ID, CodexConfigID: codexconfig.DefaultConfigID, BackendSelection: &backendSelection, BaseCommit: fixture.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Verify startup", Instruction: "Read only; do not modify files", Model: "gpt-5.6-terra", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := plane.ScopeReviews.Start(ctx, scopereview.StartInput{AISessionID: session.Session.ID, ReviewID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	for {
		current, err := plane.Runs.Get(ctx, started.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == run.StatusInfrastructureFailed {
			t.Fatalf("runtime startup failed: %+v", current.Failure)
		}
		if current.ThreadID != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Review never passed runtime startup: %+v", current)
		case <-time.After(10 * time.Millisecond):
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), launcher+" app-server") != 2 || !strings.Contains(string(data), launcher+" --version") {
		t.Fatalf("probe/runtime launch mismatch: %s", data)
	}
	// Deletion must report the selected path, not rediscover codex via PATH.
	if err := os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if got := codexprobe.VerifyCore(server.URL, app.codexSelectionFile(), app.codex); got.Status != codexprobe.Error || !strings.Contains(got.Error, launcher) {
		t.Fatalf("missing path guard: %+v", got)
	}
	// A fresh Review persists the exact executable failure. Re-probing never
	// edits that failed Run or retries it, and a new Review allocates a new Run.
	createReview := func() review.Review {
		t.Helper()
		newArea, e := plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: session.Session.ID, Title: "New startup attempt", ProblemStatement: "Verify startup"})
		if e != nil {
			t.Fatal(e)
		}
		newIntent, e := plane.Tasks.Create(ctx, task.CreateInput{AISessionID: session.Session.ID, RequestID: "desktop-resolution-task-" + newArea.ID, ProjectID: project.ID, FeatureAreaID: newArea.ID, ModuleID: moduleValue.ID, RepositoryID: repo.ID, CodexConfigID: codexconfig.DefaultConfigID, Goal: "New startup attempt", ProblemStatement: newArea.ProblemStatement, BaseRevision: fixture.BaseCommit, Planning: true})
		if e != nil {
			t.Fatal(e)
		}
		value, e := plane.Reviews.Create(ctx, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: newIntent.ID, ProjectID: project.ID, FeatureAreaID: newArea.ID, CreatedByAISessionID: session.Session.ID, RepositoryID: repo.ID, CodexConfigID: codexconfig.DefaultConfigID, BackendSelection: &backendSelection, BaseCommit: fixture.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Verify startup", Instruction: "Read only", Model: "gpt-5.6-terra", ReasoningEffort: "high"})
		if e != nil {
			t.Fatal(e)
		}
		return value
	}
	failedOwner := createReview()
	failedStart, err := plane.ScopeReviews.Start(ctx, scopereview.StartInput{AISessionID: session.Session.ID, ReviewID: failedOwner.ID})
	if err != nil {
		t.Fatal(err)
	}
	var failed run.Run
	for {
		failed, err = plane.Runs.Get(ctx, failedStart.ID)
		if err != nil {
			t.Fatal(err)
		}
		if failed.Failure != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("missing executable failure not persisted")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !strings.Contains(failed.Failure.Message, launcher) || !strings.Contains(failed.Failure.Message, "CODEX_EXECUTABLE_MISSING") {
		t.Fatalf("failure lost: %+v", failed.Failure)
	}
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env codex-node\n"), 0755); err != nil {
		t.Fatal(err)
	}
	app.updateCodexProbe(launcher)
	preserved, err := plane.Runs.Get(ctx, failed.ID)
	if err != nil || !reflect.DeepEqual(failed, preserved) {
		t.Fatalf("historical failed Run changed: %v", err)
	}
	newOwner := createReview()
	fresh, err := plane.ScopeReviews.Start(ctx, scopereview.StartInput{AISessionID: session.Session.ID, ReviewID: newOwner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == failed.ID || fresh.ReviewID == failed.ReviewID {
		t.Fatal("new Review reused historical Run")
	}

}

func TestCodexMismatchGuardRejectsOldAndDifferentCore(t *testing.T) {
	for _, body := range []string{"old-core", `{"selectionFile":"other","executable":"/other/codex"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		got := codexprobe.VerifyCore(server.URL, "/selected.json", codexprobe.Result{Status: codexprobe.Available, Executable: "/selected/codex"})
		server.Close()
		if got.Status != codexprobe.Error || !strings.Contains(got.Error, "CODEX_EXECUTABLE_MISMATCH") {
			t.Fatalf("guard: %+v", got)
		}
	}
}
