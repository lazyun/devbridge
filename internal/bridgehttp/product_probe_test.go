package bridgehttp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

// TestLocalProductProbe exercises the synthetic DataRoot and Console read
// surface with multiple Projects, Reviews with and without reliable usage,
// 64 Modules, Documents, and a READY Project Context. It never invokes
// ChatGPT or a real Codex process.
func TestProductConsoleSyntheticDensityProbe(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := fixture.ctx
	projectB, err := fixture.plane.Projects.Create(ctx, "Beta Workspace")
	if err != nil {
		t.Fatal(err)
	}
	projectC, err := fixture.plane.Projects.Create(ctx, "Gamma Lab")
	if err != nil {
		t.Fatal(err)
	}
	fixture.bindSession(t, projectC.ID, "project-c")

	// Seed 60 synthetic modules
	inputs := make([]module.ContextModuleInput, 0, 64)
	for i := 0; i < 64; i++ {
		inputs = append(inputs, module.ContextModuleInput{
			Key:         fmt.Sprintf("module_%02d", i),
			Name:        fmt.Sprintf("Module %02d", i),
			Description: "",
			ProductionFacts: []module.ProductionFacts{{
				RepositoryID: fixture.repositoryID, RepositoryName: "console-repository",
				Responsibility:       fmt.Sprintf("Owns synthetic area %02d", i),
				ResponsibilitySource: module.ResponsibilityDerived,
				ProductionRoots:      []string{fmt.Sprintf("internal/area%02d", i)},
				Entrypoints:          []string{fmt.Sprintf("internal/area%02d/service.go", i)},
				RelatedModules:       []string{"module_00"},
			}},
		})
	}
	if _, err := fixture.plane.Modules.ReplaceContextMap(ctx, fixture.projectID, inputs); err != nil {
		t.Fatal(err)
	}

	// FeatureAreas and reviews
	featureAreaA, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Alpha Topic", ProblemStatement: "Alpha durable goal"})
	if err != nil {
		t.Fatal(err)
	}
	var taskIDs []string
	for _, setup := range []struct {
		featureArea string
		reliable    bool
	}{{featureAreaA.ID, true}, {featureAreaA.ID, false}} {
		ensureConsoleFeatureActive(t, fixture, setup.featureArea)
		taskValue, createErr := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: setup.featureArea, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, ProblemStatement: "Probe goal", Planning: true})
		if createErr != nil {
			t.Fatal(createErr)
		}
		taskIDs = append(taskIDs, taskValue.ID)
		reviewValue, createErr := fixture.plane.Reviews.Create(ctx, fixture.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: taskValue.ID, Instruction: "Inspect prod", ProjectID: fixture.projectID, FeatureAreaID: setup.featureArea, CreatedByAISessionID: fixture.sessionID, RepositoryID: taskValue.RepositoryID, CodexConfigID: taskValue.CodexConfigID, BaseCommit: taskValue.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Probe review"}))
		if createErr != nil {
			t.Fatal(createErr)
		}
		runValue, runErr := fixture.plane.Runs.CreateFocusedScopeReviewForReview(ctx, reviewValue.ID, "Inspect prod", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
		if runErr != nil {
			t.Fatal(runErr)
		}
		if err := fixture.plane.Reviews.LinkRun(ctx, reviewValue.ID, runValue.ID); err != nil {
			t.Fatal(err)
		}
		for _, status := range []run.Status{run.StatusPreparing, run.StatusRunning, run.StatusCollecting} {
			if runValue, runErr = fixture.plane.Runs.Transition(ctx, runValue.ID, status); runErr != nil {
				t.Fatal(runErr)
			}
		}
		// First review has a reliable delta; second has no usage.
		if setup.reliable {
			_, runErr = fixture.plane.Runs.SucceedWithUsage(ctx, runValue.ID, run.Result{}, &run.TokenUsageObservation{
				Reliable:        true,
				StartCumulative: run.TokenCounts{InputTokens: 20000, CachedInputTokens: 100, OutputTokens: 500, TotalTokens: 20500},
				EndCumulative:   run.TokenCounts{InputTokens: 29842, CachedInputTokens: 30102, OutputTokens: 2317, TotalTokens: 32659},
			})
		} else {
			_, runErr = fixture.plane.Runs.SucceedWithUsage(ctx, runValue.ID, run.Result{}, nil)
		}
		if runErr != nil {
			t.Fatal(runErr)
		}
	}
	documentValue, err := fixture.plane.Documents.Create(ctx, document.CreateInput{AISessionID: fixture.sessionID, FeatureAreaID: featureAreaA.ID, Title: "Probe document", Content: "Exact durable authored content."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.ProjectContexts.EnsureRequired(ctx, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.ProjectContexts.MarkBootstrapped(ctx, fixture.projectID, projectcontext.StatusReady, "probe-fingerprint", "guidance-identity", "", nil); err != nil {
		t.Fatal(err)
	}

	// Exercise the read surface
	status, _, body := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != 200 {
		t.Fatalf("workspace status=%d body=%s", status, body)
	}
	for _, marker := range []string{`"latest_review"`, `"state":"COMPLETE"`, `"input_tokens":9842`, `"cached_input_tokens":30002`, `"output_tokens":1817`, `"total_tokens":12159`, `"title":"Alpha Topic"`, `"title":"Probe document"`, `"content_preview":"Exact durable authored content."`} {
		if !strings.Contains(body, marker) {
			t.Fatalf("workspace probe missing %q", marker)
		}
	}
	status, _, modulesBody := getRaw(t, fixture.handler, "/ui/api/modules?project_id="+fixture.projectID)
	if status != 200 || !strings.Contains(modulesBody, "Module 63") || !strings.Contains(modulesBody, "internal/area63") {
		t.Fatalf("module probe status=%d body=%s", status, modulesBody)
	}
	status, _, projectsBody := getRaw(t, fixture.handler, "/v1/projects?limit=100")
	if status != 200 || !strings.Contains(projectsBody, "Console fixture") || !strings.Contains(projectsBody, "Beta Workspace") || !strings.Contains(projectsBody, "Gamma Lab") {
		t.Fatalf("project selector probe status=%d body=%s", status, projectsBody)
	}
	if !strings.Contains(modulesBody, `"repository_name":"console-repository"`) || !strings.Contains(modulesBody, `"production_roots":["internal/area63"]`) {
		t.Fatalf("module projection missing facts: %s", modulesBody)
	}
	// Module Map must be bounded at 64 and expose all synthetic modules for the search index.
	if strings.Count(modulesBody, `"name":"Module`) < 60 {
		t.Fatalf("module probe did not expose dense synthetic set: %s", modulesBody)
	}
	// Task-owned Review usage remains visible under its exact semantic Task.
	status, _, taskBody := getRaw(t, fixture.handler, "/v1/tasks/"+taskIDs[0]+"?project_id="+fixture.projectID)
	if status != 200 || !strings.Contains(taskBody, `"total_tokens":12159`) {
		t.Fatalf("Task lost its owned Review usage status=%d body=%s", status, taskBody)
	}
	// Document detail round-trips exact authored content.
	status, _, documentBody := getRaw(t, fixture.handler, "/ui/api/documents/"+documentValue.ID+"?project_id="+fixture.projectID)
	if status != 200 || !strings.Contains(documentBody, "Exact durable authored content.") {
		t.Fatalf("document detail status=%d body=%s", status, documentBody)
	}
	// Project switching crosses isolation boundaries through the Console endpoint.
	status, _, projectBWorkspace := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+projectB.ID)
	if status != 200 || strings.Contains(projectBWorkspace, "Alpha Topic") || !strings.Contains(projectBWorkspace, `"module_count":0`) {
		t.Fatalf("cross-Project isolation probe status=%d body=%s", status, projectBWorkspace)
	}
	if _, err := fixture.plane.ProjectSelection.Select(ctx, projectB.ID); err != nil {
		t.Fatal(err)
	}
	status, _, selection := getRaw(t, fixture.handler, "/v1/project-selection")
	if status != 200 || !strings.Contains(selection, projectB.ID) {
		t.Fatalf("selection projection status=%d body=%s", status, selection)
	}
}
