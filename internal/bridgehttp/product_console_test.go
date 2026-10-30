package bridgehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func TestProductConsoleUIFoundationIsEmbeddedAndLocal(t *testing.T) {
	plane, err := openTestPlane(t)
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}

	status, _, html := getRaw(t, server.Handler(), "/ui/")
	if status != http.StatusOK {
		t.Fatalf("GET /ui/: status=%d", status)
	}
	for _, marker := range []string{"data-product-nav=\"overview\"", "data-product-nav=\"tasks\"", "data-product-nav=\"featureAreas\"", "data-product-nav=\"resources\"", "data-product-nav=\"diagnostics\"", "language-select", "product-i18n.js", "product-status.js", "product-components.js", "product-development.js", "product-workbench.js", "product-task-workbench.js", "product-app.js"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("product shell is missing %q", marker)
		}
	}
	if strings.Contains(html, "<script src=\"app.js\"") {
		t.Fatal("parallel-owned legacy app.js must not drive the product shell")
	}
	for _, asset := range []string{"product.css", "product-i18n.js", "product-status.js", "product-components.js", "product-development.js", "product-workbench.js", "product-task-workbench.js", "product-app.js"} {
		status, _, body := getRaw(t, server.Handler(), "/ui/"+asset)
		if status != http.StatusOK || body == "" {
			t.Fatalf("GET %s: status=%d bytes=%d", asset, status, len(body))
		}
		if strings.Contains(body, "https://") || strings.Contains(body, "http://") || strings.Contains(body, "//cdn") {
			t.Fatalf("external dependency found in %s", asset)
		}
	}
	_, _, app := getRaw(t, server.Handler(), "/ui/product-app.js")
	_, _, messages := getRaw(t, server.Handler(), "/ui/product-i18n.js")
	_, _, components := getRaw(t, server.Handler(), "/ui/product-components.js")
	if strings.Contains(app, "setInterval") || strings.Contains(app, "setTimeout(loadLogs") {
		t.Fatal("product Console must use manual refresh rather than busy polling")
	}
	for _, endpoint := range []string{"/v1/projects", "/v1/project-overview", "/ui/api/project-workspace", "/ui/api/featureAreas", "/ui/api/modules", "/v1/logs", "/v1/tasks/", "/v1/reviews/"} {
		if !strings.Contains(app, endpoint) {
			t.Fatalf("product app is missing local read surface %q", endpoint)
		}
	}
	for _, marker := range []string{"renderProjectSelector", "renderProjectDetail", "renderTopicRows", "renderTaskRows", "renderExecutionRows", "renderContextSummary", "data-project-trigger", "data-project-search", "module-search", "data-module-toggle", "modules.searchPlaceholder"} {
		if !strings.Contains(app, marker) {
			t.Fatalf("product workspace UI is missing %q", marker)
		}
	}
	if !strings.Contains(components, "agentOperationStateBadge") || !strings.Contains(messages, "agentOperation.recoveryRequired.label") {
		t.Fatal("product Console is missing the Agent operation state projection")
	}
	projectsStart := strings.Index(app, "function renderProjectSelector()")
	projectsEnd := strings.Index(app[projectsStart:], "function renderProjectDetail()") + projectsStart
	if projectsStart < 0 || projectsEnd < projectsStart {
		t.Fatal("could not locate Projects renderer")
	}
	projectsRenderer := app[projectsStart:projectsEnd]
	for _, removed := range []string{"project-rail", "project-row", "project-chooser", "project-workspace-layout", "project-grid"} {
		if strings.Contains(projectsRenderer, removed) {
			t.Fatalf("Projects renderer retained low-density structure %q", removed)
		}
	}
	for _, capability := range []string{"data-review-prompts", "data-view-review-prompt", "data-copy-review-prompt"} {
		if !strings.Contains(app, capability) {
			t.Fatalf("product app is missing Review prompt diagnostic capability %q", capability)
		}
	}
	for _, forbidden := range []string{"featureArea_review_start", "review_task_id", "ui_decision_get", "ui_task_decide", "ui_execution_decide", "work_review_", "task_plan_status", "TaskPlan", "ExecutionProposal"} {
		if strings.Contains(app, forbidden) {
			t.Fatalf("product app reimplemented deferred product object %q", forbidden)
		}
	}
	if !strings.Contains(components, "module-card") || !strings.Contains(components, "module-details") {
		t.Fatal("product Module view no longer renders the bounded Module Map")
	}
	if !strings.Contains(app, "usageFactsBlock") || !strings.Contains(components, "buildUsageFacts") || !strings.Contains(messages, "usage.total") {
		t.Fatal("product Console is missing Review usage presentation")
	}
	if strings.Contains(app, `"/v1/project-selection", {method: "POST"`) || strings.Contains(app, "project_open") {
		t.Fatal("product Console selection must remain local UI state without project_open or backend binding")
	}
	if strings.Contains(app+messages, "Review creates Task") || strings.Contains(app+messages, "Review pseudo Task") {
		t.Fatal("product Console exposes obsolete Review ownership")
	}
}

func TestProductConsoleDocumentsAreProjectScopedAndTopicAware(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := fixture.ctx
	featureAreaValue, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Document Topic", ProblemStatement: "Keep durable decisions visible."})
	if err != nil {
		t.Fatal(err)
	}
	projectDocument, err := fixture.plane.Documents.Create(ctx, document.CreateInput{AISessionID: fixture.sessionID, Title: "Project decision", Content: "Project-level durable content."})
	if err != nil {
		t.Fatal(err)
	}
	featureAreaDocument, err := fixture.plane.Documents.Create(ctx, document.CreateInput{AISessionID: fixture.sessionID, FeatureAreaID: featureAreaValue.ID, Title: "Topic decision", Content: "Topic-associated durable content."})
	if err != nil {
		t.Fatal(err)
	}
	status, _, body := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(body, projectDocument.ID) || !strings.Contains(body, featureAreaDocument.ID) || !strings.Contains(body, `"feature_area_title":"Document Topic"`) {
		t.Fatalf("Document workspace status=%d body=%s", status, body)
	}
	status, _, body = getRaw(t, fixture.handler, "/ui/api/documents/"+featureAreaDocument.ID+"?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(body, `"content":"Topic-associated durable content."`) || !strings.Contains(body, `"feature_area_title":"Document Topic"`) {
		t.Fatalf("Document detail status=%d body=%s", status, body)
	}
	other, err := fixture.plane.Projects.Create(ctx, "Other Document project")
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = getRaw(t, fixture.handler, "/ui/api/documents/"+featureAreaDocument.ID+"?project_id="+other.ID)
	if status != http.StatusNotFound {
		t.Fatalf("cross-Project Document detail status=%d, want 404", status)
	}
}

func TestProductTopicAndModuleReadProjectionsHideInternalProvenance(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()
	created, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Make startup reliable", ProblemStatement: "Keep the local runtime available after restart."})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.plane.Modules.ReplaceContextMap(ctx, fixture.projectID, []module.ContextModuleInput{{
		Key: "console", Name: "Console", Description: "Owns the product interface.",
		ProductionFacts: []module.ProductionFacts{{RepositoryID: fixture.repositoryID, RepositoryName: "console-repository", Responsibility: "Presents Bridge product state.", ResponsibilitySource: module.ResponsibilityGuidance, ProductionRoots: []string{"internal/bridgehttp/ui"}, Entrypoints: []string{"internal/bridgehttp/ui/index.html"}, RelatedModules: []string{"bridgehttp"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	status, _, featureAreasBody := getRaw(t, fixture.handler, "/ui/api/featureAreas?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(featureAreasBody, created.Title) || !strings.Contains(featureAreasBody, created.ProblemStatement) {
		t.Fatalf("Topic projection status=%d body=%s", status, featureAreasBody)
	}
	for _, forbidden := range []string{fixture.sessionID, "created_by_ai_session_id", "creatingAISession", "planning_task_id"} {
		if strings.Contains(featureAreasBody, forbidden) {
			t.Fatalf("Topic projection leaked %q: %s", forbidden, featureAreasBody)
		}
	}

	status, _, modulesBody := getRaw(t, fixture.handler, "/ui/api/modules?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("Module projection status=%d body=%s", status, modulesBody)
	}
	for _, expected := range []string{"Console", "Presents Bridge product state.", "console-repository", "internal/bridgehttp/ui", "internal/bridgehttp/ui/index.html", "bridgehttp"} {
		if !strings.Contains(modulesBody, expected) {
			t.Fatalf("Module projection missing %q: %s", expected, modulesBody)
		}
	}
	for _, forbidden := range []string{fixture.moduleID, fixture.repositoryID, "GUIDANCE", "responsibility_source", "system_owned", "unclassified"} {
		if strings.Contains(modulesBody, forbidden) {
			t.Fatalf("Module projection leaked %q: %s", forbidden, modulesBody)
		}
	}

	if _, err := fixture.plane.FeatureAreas.Activate(ctx, fixture.sessionID, created.ID, "lifecycle-request"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.FeatureAreas.Retire(ctx, fixture.sessionID, created.ID, "retire-request"); err != nil {
		t.Fatal(err)
	}
	waitConsoleRetirement(t, fixture.plane, created.ID)
	status, _, featureAreasBody = getRaw(t, fixture.handler, "/ui/api/featureAreas?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("Topic projection after cancel status=%d", status)
	}
	var page struct {
		Items []struct {
			State               string `json:"status"`
			AgentOperationState string `json:"agent_operation_state"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(featureAreasBody), &page); err != nil || len(page.Items) != 1 || page.Items[0].State != "RETIRED" || page.Items[0].AgentOperationState != "UNAVAILABLE" {
		t.Fatalf("retired FeatureArea history is missing or actionable: body=%s err=%v", featureAreasBody, err)
	}
}

func TestProductReadProjectionsRequireKnownProject(t *testing.T) {
	plane, err := openTestPlane(t)
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/ui/api/project-workspace?project_id=missing", "/ui/api/featureAreas?project_id=missing", "/ui/api/modules?project_id=missing"} {
		status, _, _ := getRaw(t, server.Handler(), path)
		if status != http.StatusNotFound {
			t.Fatalf("GET %s status=%d, want 404", path, status)
		}
	}
}

func TestProductProjectWorkspaceProjectsFeatureAreasTasksExecutionsAndWorkspaces(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()
	featureAreaValue, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Workspace redesign", ProblemStatement: "Show current work without aggregate prose."})
	if err != nil {
		t.Fatal(err)
	}
	ensureConsoleFeatureActive(t, fixture, featureAreaValue.ID)
	planning, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, ProblemStatement: featureAreaValue.ProblemStatement, Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	createCompletedProductReview(t, fixture, featureAreaValue, planning)
	if _, err := fixture.plane.FeatureAreas.Activate(ctx, fixture.sessionID, featureAreaValue.ID, "lifecycle-request"); err != nil {
		t.Fatal(err)
	}
	executing := createConsoleTask(t, fixture, fixture.moduleID, task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Synthetic probe"}}, "Run the product probe")
	startTestRun(t, fixture, executing, false)
	probe, err := fixture.plane.ContextProbes.Create(ctx, contextprobe.CreateInput{AISessionID: fixture.sessionID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}, CodexInstruction: "Synthetic refresh probe."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.ProjectContexts.MarkBootstrapped(ctx, fixture.projectID, projectcontext.StatusReady, "synthetic-fingerprint", "", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.ContextProbes.Start(ctx, probe.ID); err != nil {
		t.Fatal(err)
	}

	status, _, body := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("workspace projection status=%d body=%s", status, body)
	}
	assertUniversalExecutionInventory(t, fixture, body)
	for _, expected := range []string{"Workspace redesign", `"phase":"IMPLEMENTING"`, `"latest_review"`, `"state":"COMPLETE"`, "Run the product probe", `"state":"EXECUTING"`, `"workspaces"`, `"project_context"`, `"state":"READY"`, `"refreshing":true`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("workspace projection missing %q: %s", expected, body)
		}
	}
	status, _, projectsBody := getRaw(t, fixture.handler, "/v1/projects?limit=100")
	if status != http.StatusOK || !strings.Contains(projectsBody, `"project_context_status":"READY"`) || !strings.Contains(projectsBody, `"project_context_refreshing":true`) {
		t.Fatalf("Project list did not preserve usable refresh semantics: status=%d body=%s", status, projectsBody)
	}
	for _, forbidden := range []string{"execution_proposal_id", "thread_id", "created_by_ai_session_id"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("workspace projection leaked %q: %s", forbidden, body)
		}
	}
	status, _, _ = getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID+"&limit=999")
	if status != http.StatusBadRequest {
		t.Fatalf("workspace projection accepted an unsupported query: status=%d", status)
	}
}

// TestProductAgentOperationStateProjectsRecoveryRequired drives the production
// Agent reservation path (Run create → claim → BeginRun → BindThread →
// MarkTurnPending) into a stranded UNKNOWN operation, then requires the Console
// read projections to distinguish RECOVERY_REQUIRED from BUSY and READY without
// exposing runtime internals. Recovery itself stays with the upper-AI MCP flow.
func TestProductAgentOperationStateProjectsRecoveryRequired(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := fixture.ctx
	featureAreaValue, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Recovery projection", ProblemStatement: "Exercise the Agent operation state projection."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.FeatureAreas.Activate(ctx, fixture.sessionID, featureAreaValue.ID, "lifecycle-request"); err != nil {
		t.Fatal(err)
	}
	ensureConsoleFeatureActive(t, fixture, featureAreaValue.ID)
	taskValue, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, ProblemStatement: featureAreaValue.ProblemStatement, Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	taskValue.WorkSpec = task.WorkSpec{Goal: "Exercise recovery", CodexInstruction: "Implement recovery fixture", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	taskValue.VerificationSpec = task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "fixture"}}
	reserved := startTestRun(t, fixture, taskValue, false)
	runID := reserved.ID
	stranded := func() string {
		t.Helper()
		status, _, body := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
		if status != http.StatusOK {
			t.Fatalf("workspace status=%d body=%s", status, body)
		}
		var workspace struct {
			FeatureAreas []struct {
				AgentOperationState   string `json:"agent_operation_state"`
				AgentOperationMessage string `json:"agent_operation_message"`
			} `json:"featureAreas"`
			ActiveAgentOperations []struct {
				FeatureAreaID       string    `json:"feature_area_id"`
				Kind                string    `json:"kind"`
				OperationID         string    `json:"operation_id"`
				RunID               string    `json:"run_id"`
				AgentConversationID string    `json:"agent_conversation_id"`
				State               string    `json:"state"`
				StartedAt           time.Time `json:"started_at"`
			} `json:"active_agent_operations"`
		}
		if err := json.Unmarshal([]byte(body), &workspace); err != nil {
			t.Fatal(err)
		}
		if len(workspace.FeatureAreas) != 1 {
			t.Fatalf("feature areas = %+v", workspace.FeatureAreas)
		}
		if len(workspace.ActiveAgentOperations) != 1 || workspace.ActiveAgentOperations[0].FeatureAreaID != featureAreaValue.ID || workspace.ActiveAgentOperations[0].Kind != "EXECUTION" || workspace.ActiveAgentOperations[0].OperationID != reserved.ExecutionProposalID || workspace.ActiveAgentOperations[0].RunID != runID || workspace.ActiveAgentOperations[0].AgentConversationID == "" || workspace.ActiveAgentOperations[0].State != "BUSY" || workspace.ActiveAgentOperations[0].StartedAt.IsZero() {
			t.Fatalf("active Agent operations = %+v", workspace.ActiveAgentOperations)
		}
		return workspace.FeatureAreas[0].AgentOperationState
	}
	if state := stranded(); state != "BUSY" {
		t.Fatalf("active UNKNOWN operation state=%q, want BUSY", state)
	}
	// Busy close acceptance is covered by the retirement integration matrix;
	// this test keeps its synthetic operation active to inspect recovery facts.
	if err := fixture.plane.AgentConversations.RequireRecovery(ctx, runID, "synthetic runtime interruption"); err != nil {
		t.Fatal(err)
	}

	status, _, body := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("recovery workspace status=%d body=%s", status, body)
	}
	status, _, areasBody := getRaw(t, fixture.handler, "/ui/api/featureAreas?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("featureAreas status=%d body=%s", status, areasBody)
	}
	for _, source := range []string{body, areasBody} {
		for _, expected := range []string{`"agent_operation_state":"RECOVERY_REQUIRED"`, "agent_operation_recover"} {
			if !strings.Contains(source, expected) {
				t.Fatalf("projection missing %q: %s", expected, source)
			}
		}
	}
	for _, forbidden := range []string{"runtime_epoch", "runtimeEpoch", "epoch_id"} {
		for _, source := range []string{body, areasBody} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("recovery projection leaked %q: %s", forbidden, source)
			}
		}
	}
}

func createCompletedProductReview(t *testing.T, fixture consoleFixture, featureAreaValue featurearea.FeatureArea, taskValue task.Task) review.Review {
	t.Helper()
	ctx := context.Background()
	configValue, err := fixture.plane.CodexConfigs.Get(ctx, taskValue.CodexConfigID)
	if err != nil {
		t.Fatal(err)
	}
	selection := configValue.Selection("bridgehttp-product-review-fixture")
	reviewValue, err := fixture.plane.Reviews.Create(ctx, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: taskValue.ID, ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, CreatedByAISessionID: fixture.sessionID, RepositoryID: taskValue.RepositoryID, CodexConfigID: taskValue.CodexConfigID, BackendSelection: &selection, BaseCommit: taskValue.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Inspect production read-only.", EngineeringGoal: featureAreaValue.ProblemStatement, Model: "gpt-5.6-terra", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	reviewRun, err := fixture.plane.Runs.CreateFocusedScopeReviewForReview(ctx, reviewValue.ID, reviewValue.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.plane.Reviews.LinkRun(ctx, reviewValue.ID, reviewRun.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.plane.Reviews.RecordAccess(ctx, reviewValue.ID, "repository_read", review.AccessSuccess, ""); err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.Status{run.StatusPreparing, run.StatusRunning} {
		if reviewRun, err = fixture.plane.Runs.Transition(ctx, reviewRun.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	reviewRun, err = fixture.plane.Runs.AttachResult(ctx, reviewRun.ID, run.Result{AgentClaim: evidence.AgentClaim{Message: "The Topic Review established a bounded production change."}, GitEvidence: evidence.GitEvidence{RunID: reviewRun.ID, BaseCommit: fixture.baseCommit, HeadCommit: fixture.baseCommit, StartSnapshot: fixture.baseCommit, EndSnapshot: fixture.baseCommit}})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.Status{run.StatusCollecting, run.StatusSucceeded} {
		if reviewRun, err = fixture.plane.Runs.Transition(ctx, reviewRun.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	return reviewValue
}

func TestProductWorkspaceMultiProjectSwitchProbe(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()

	featureAreaA, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Project A current Topic", ProblemStatement: "Keep Project A isolated."})
	if err != nil {
		t.Fatal(err)
	}
	ensureConsoleFeatureActive(t, fixture, featureAreaA.ID)
	if _, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: featureAreaA.ID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, ProblemStatement: featureAreaA.ProblemStatement, Planning: true}); err != nil {
		t.Fatal(err)
	}

	projectB, err := fixture.plane.Projects.Create(ctx, "Synthetic Project B")
	if err != nil {
		t.Fatal(err)
	}
	repositoryPathB := filepath.Join(t.TempDir(), "source-b")
	if err := os.MkdirAll(repositoryPathB, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, repositoryPathB, "init", "-b", "main")
	git(t, repositoryPathB, "config", "user.name", "Workspace probe")
	git(t, repositoryPathB, "config", "user.email", "workspace-probe@example.invalid")
	if err := os.WriteFile(filepath.Join(repositoryPathB, "tracked.txt"), []byte("project b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repositoryPathB, "add", "tracked.txt")
	git(t, repositoryPathB, "commit", "-m", "project b base")
	baseB := git(t, repositoryPathB, "rev-parse", "HEAD")
	repositoryB, err := fixture.plane.Repositories.RegisterInProject(ctx, projectB.ID, repositoryPathB, "project-b-repository")
	if err != nil {
		t.Fatal(err)
	}
	moduleB, err := fixture.plane.Modules.Create(ctx, module.CreateInput{ProjectID: projectB.ID, Key: "project-b", Name: "Project B Module"})
	if err != nil {
		t.Fatal(err)
	}
	configB, err := fixture.plane.CodexConfigs.Create(ctx, codexconfig.CreateInput{Name: "Project B synthetic", CodexPath: "/synthetic/codex", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	sessionB := fixture.bindSession(t, projectB.ID, "project-b")
	selectConsoleDevelopmentBaseline(t, fixture.plane, projectB.ID, repositoryB.ID)
	featureAreaB, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: sessionB.ID, Title: "Project B active Topic", ProblemStatement: "Exercise dense Project switching."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.FeatureAreas.Activate(ctx, sessionB.ID, featureAreaB.ID, "lifecycle-request"); err != nil {
		t.Fatal(err)
	}
	fixtureB := fixture
	fixtureB.projectID, fixtureB.repositoryID, fixtureB.moduleID, fixtureB.configID, fixtureB.sessionID, fixtureB.baseCommit = projectB.ID, repositoryB.ID, moduleB.ID, configB.ID, sessionB.ID, baseB
	ensureConsoleFeatureActive(t, fixtureB, featureAreaB.ID)
	taskB, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: sessionB.ID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: projectB.ID, FeatureAreaID: featureAreaB.ID, ModuleID: moduleB.ID, RepositoryID: repositoryB.ID, CodexConfigID: configB.ID, BaseRevision: baseB, WorkSpec: task.WorkSpec{Goal: "Project B execution", CodexInstruction: "Synthetic only.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Synthetic probe"}}})
	if err != nil {
		t.Fatal(err)
	}
	taskB.WorkSpec = task.WorkSpec{Goal: "Project B execution", CodexInstruction: "Synthetic only.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	taskB.VerificationSpec = task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Synthetic probe"}}
	startTestRun(t, fixtureB, taskB, false)

	var selection map[string]any
	postJSON(t, fixture.handler, "/v1/project-selection", projectSelectionAction{ProjectID: projectB.ID}, http.StatusOK, &selection)
	status, _, bodyB := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+projectB.ID)
	if status != http.StatusOK || !strings.Contains(bodyB, "Project B active Topic") || !strings.Contains(bodyB, "Project B execution") || strings.Contains(bodyB, "Project A current Topic") {
		t.Fatalf("Project B product probe status=%d body=%s", status, bodyB)
	}
	postJSON(t, fixture.handler, "/v1/project-selection", projectSelectionAction{ProjectID: fixture.projectID}, http.StatusOK, &selection)
	status, _, bodyA := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(bodyA, "Project A current Topic") || strings.Contains(bodyA, "Project B active Topic") || strings.Contains(bodyA, "Project B execution") {
		t.Fatalf("Project A product probe status=%d body=%s", status, bodyA)
	}
}

func TestProductReviewBlockedShowsRunFailureMessageVerbatim(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()
	featureAreaValue, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Blocked review reason", ProblemStatement: "Show the durable Run failure message verbatim."})
	if err != nil {
		t.Fatal(err)
	}
	ensureConsoleFeatureActive(t, fixture, featureAreaValue.ID)
	taskValue, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, ProblemStatement: featureAreaValue.ProblemStatement, Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	reviewValue, err := fixture.plane.Reviews.Create(ctx, fixture.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: taskValue.ID, ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, CreatedByAISessionID: fixture.sessionID, RepositoryID: taskValue.RepositoryID, CodexConfigID: taskValue.CodexConfigID, BaseCommit: taskValue.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Inspect production read-only.", EngineeringGoal: featureAreaValue.ProblemStatement}))
	if err != nil {
		t.Fatal(err)
	}
	reviewRun, err := fixture.plane.Runs.CreateFocusedScopeReviewForReview(ctx, reviewValue.ID, "Inspect production read-only.", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.plane.Reviews.LinkRun(ctx, reviewValue.ID, reviewRun.ID); err != nil {
		t.Fatal(err)
	}
	const reason = `start codex app-server: exec: "codex": executable file not found in $PATH`
	if _, err := fixture.plane.Runs.Fail(ctx, reviewRun.ID, run.Failure{Kind: run.FailureInfrastructure, Stage: "start", Code: "CODEX_START_FAILED", Message: reason}); err != nil {
		t.Fatal(err)
	}

	status, _, body := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("workspace blocker probe status=%d body=%s", status, body)
	}
	if !strings.Contains(body, `"state":"BLOCKED"`) || !strings.Contains(body, "executable file not found in $PATH") {
		t.Fatalf("workspace must surface the durable Run failure message verbatim: %s", body)
	}
	var workspace struct {
		FeatureAreas []struct {
			LatestReview *struct {
				State         string `json:"state"`
				BlockerReason string `json:"blocker_reason"`
			} `json:"latest_review"`
			Reviews []struct {
				State         string `json:"state"`
				BlockerReason string `json:"blocker_reason"`
			} `json:"reviews"`
		} `json:"featureAreas"`
	}
	if err := json.Unmarshal([]byte(body), &workspace); err != nil {
		t.Fatal(err)
	}
	if len(workspace.FeatureAreas) == 0 || workspace.FeatureAreas[0].LatestReview == nil {
		t.Fatalf("latest Review projection missing: %s", body)
	}
	latest := workspace.FeatureAreas[0].LatestReview
	if latest.State != "BLOCKED" || latest.BlockerReason != reason {
		t.Fatalf("Review blocker projection = %+v, want BLOCKED with verbatim reason", latest)
	}

	// A later successful Review must neither inherit the historical blocker nor
	// erase it from the older Review's durable projection.
	time.Sleep(time.Millisecond)
	successfulReview, err := fixture.plane.Reviews.Create(ctx, fixture.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: taskValue.ID, ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, CreatedByAISessionID: fixture.sessionID, RepositoryID: taskValue.RepositoryID, CodexConfigID: taskValue.CodexConfigID, BaseCommit: taskValue.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Inspect production read-only.", EngineeringGoal: "Verify current Codex runtime resolution."}))
	if err != nil {
		t.Fatal(err)
	}
	successfulRun, err := fixture.plane.Runs.CreateFocusedScopeReviewForReview(ctx, successfulReview.ID, "Inspect production read-only.", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.plane.Reviews.LinkRun(ctx, successfulReview.ID, successfulRun.ID); err != nil {
		t.Fatal(err)
	}
	for _, next := range []run.Status{run.StatusPreparing, run.StatusRunning, run.StatusCollecting} {
		if successfulRun, err = fixture.plane.Runs.Transition(ctx, successfulRun.ID, next); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = fixture.plane.Runs.Succeed(ctx, successfulRun.ID, run.Result{AgentClaim: evidence.AgentClaim{Message: "current Review succeeded"}}); err != nil {
		t.Fatal(err)
	}

	status, _, body = getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("workspace successful-review probe status=%d body=%s", status, body)
	}
	var afterSuccess struct {
		FeatureAreas []struct {
			LatestReview *struct {
				ID            string `json:"id"`
				State         string `json:"state"`
				BlockerReason string `json:"blocker_reason"`
			} `json:"latest_review"`
			Reviews []struct {
				ID            string `json:"id"`
				State         string `json:"state"`
				BlockerReason string `json:"blocker_reason"`
			} `json:"reviews"`
		} `json:"featureAreas"`
	}
	if err := json.Unmarshal([]byte(body), &afterSuccess); err != nil {
		t.Fatal(err)
	}
	if len(afterSuccess.FeatureAreas) == 0 || afterSuccess.FeatureAreas[0].LatestReview == nil {
		t.Fatalf("latest successful Review projection missing: %s", body)
	}
	current := afterSuccess.FeatureAreas[0].LatestReview
	if current.ID != successfulReview.ID || current.State != "COMPLETE" || current.BlockerReason != "" {
		t.Fatalf("new successful Review projection = %+v, want COMPLETE without historical blocker", current)
	}
	foundHistorical := false
	for _, projected := range afterSuccess.FeatureAreas[0].Reviews {
		if projected.ID == reviewValue.ID {
			foundHistorical = true
			if projected.State != "BLOCKED" || projected.BlockerReason != reason {
				t.Fatalf("historical Review projection = %+v, want original durable blocker", projected)
			}
		}
	}
	if !foundHistorical {
		t.Fatalf("historical blocked Review %q missing after successful Review", reviewValue.ID)
	}
}

func TestProductReviewStartFailureProjectsRecordedBlockerVerbatim(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()
	featureAreaValue, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Start failure blocker", ProblemStatement: "Show the recorded start blocker verbatim."})
	if err != nil {
		t.Fatal(err)
	}
	ensureConsoleFeatureActive(t, fixture, featureAreaValue.ID)
	taskValue, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, ProblemStatement: featureAreaValue.ProblemStatement, Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	reviewValue, err := fixture.plane.Reviews.Create(ctx, fixture.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: taskValue.ID, ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, CreatedByAISessionID: fixture.sessionID, RepositoryID: taskValue.RepositoryID, CodexConfigID: taskValue.CodexConfigID, BaseCommit: taskValue.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Inspect production read-only.", EngineeringGoal: featureAreaValue.ProblemStatement}))
	if err != nil {
		t.Fatal(err)
	}
	const blocker = `start codex app-server: exec: "codex": executable file not found in $PATH`
	if err := fixture.plane.Reviews.RecordStartFailure(ctx, reviewValue.ID, "CODEX_START_FAILED", blocker); err != nil {
		t.Fatal(err)
	}

	status, _, body := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("workspace start-failure probe status=%d body=%s", status, body)
	}
	var workspace struct {
		FeatureAreas []struct {
			LatestReview *struct {
				State         string `json:"state"`
				BlockerReason string `json:"blocker_reason"`
			} `json:"latest_review"`
			Reviews []struct {
				State         string `json:"state"`
				BlockerReason string `json:"blocker_reason"`
			} `json:"reviews"`
		} `json:"featureAreas"`
	}
	if err := json.Unmarshal([]byte(body), &workspace); err != nil {
		t.Fatal(err)
	}
	if len(workspace.FeatureAreas) == 0 || workspace.FeatureAreas[0].LatestReview != nil || len(workspace.FeatureAreas[0].Reviews) != 1 {
		t.Fatalf("legacy rejected Review must remain history without becoming latest admitted Review: %s", body)
	}
	historical := workspace.FeatureAreas[0].Reviews[0]
	if historical.State != "BLOCKED" || historical.BlockerReason != blocker {
		t.Fatalf("Review start-failure history = %+v, want BLOCKED with verbatim blocker", historical)
	}
}

func TestHistoricalStartFailureDoesNotOverrideCurrentTaskReview(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := fixture.ctx
	area, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Active Review projection", ProblemStatement: "Keep the admitted Review authoritative."})
	if err != nil {
		t.Fatal(err)
	}
	ensureConsoleFeatureActive(t, fixture, area.ID)
	taskValue, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: area.ID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, ProblemStatement: area.ProblemStatement, Planning: true})
	if err != nil {
		t.Fatal(err)
	}

	legacy, err := fixture.plane.Reviews.Create(ctx, fixture.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: taskValue.ID, ProjectID: fixture.projectID, FeatureAreaID: area.ID, CreatedByAISessionID: fixture.sessionID, RepositoryID: taskValue.RepositoryID, CodexConfigID: taskValue.CodexConfigID, BaseCommit: taskValue.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Inspect production read-only.", EngineeringGoal: "Legacy rejected admission row"}))
	if err != nil {
		t.Fatal(err)
	}
	const legacyBlocker = "FEATURE_AREA_BUSY: historical admission rejection"
	if err = fixture.plane.Reviews.RecordStartFailure(ctx, legacy.ID, "FEATURE_AREA_BUSY", legacyBlocker); err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Millisecond)
	active, err := fixture.plane.Reviews.Create(ctx, fixture.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: taskValue.ID, ProjectID: fixture.projectID, FeatureAreaID: area.ID, CreatedByAISessionID: fixture.sessionID, RepositoryID: taskValue.RepositoryID, CodexConfigID: taskValue.CodexConfigID, BaseCommit: taskValue.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Inspect production read-only.", EngineeringGoal: area.ProblemStatement}))
	if err != nil {
		t.Fatal(err)
	}
	activeRun, err := fixture.plane.Runs.CreateFocusedScopeReviewForReview(ctx, active.ID, "Inspect production read-only.", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.plane.Reviews.LinkRun(ctx, active.ID, activeRun.ID); err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(fixture.plane)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := facade.CurrentWorkflowForSession(ctx, fixture.sessionID, area.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.ActiveReview == nil || workflow.LatestReview == nil || workflow.CurrentReview == nil || workflow.ActiveReview.ID != active.ID || workflow.LatestReview.ID != active.ID || workflow.CurrentReview.ID != active.ID || len(workflow.Reviews) != 2 || len(workflow.Runs) == 0 || workflow.Runs[0].ID != activeRun.ID {
		t.Fatalf("legacy row overrode active workflow: %+v", workflow)
	}

	status, _, body := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("workspace projection status=%d body=%s", status, body)
	}
	var workspace struct {
		FeatureAreas []struct {
			Status       string `json:"status"`
			State        string `json:"state"`
			Presentation struct {
				TaskView struct {
					CurrentReview struct {
						ID     string `json:"id"`
						TaskID string `json:"taskId"`
					} `json:"currentReview"`
				} `json:"taskView"`
			} `json:"presentation"`
			ActiveReview *struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"active_review"`
			LatestReview *struct {
				ID string `json:"id"`
			} `json:"latest_review"`
			Reviews []struct {
				ID            string `json:"id"`
				State         string `json:"state"`
				BlockerReason string `json:"blocker_reason"`
			} `json:"reviews"`
		} `json:"featureAreas"`
	}
	if err = json.Unmarshal([]byte(body), &workspace); err != nil {
		t.Fatal(err)
	}
	if len(workspace.FeatureAreas) == 0 || workspace.FeatureAreas[0].Status != "ACTIVE" || workspace.FeatureAreas[0].State != "REVIEWING" || workspace.FeatureAreas[0].Presentation.TaskView.CurrentReview.ID != active.ID || workspace.FeatureAreas[0].Presentation.TaskView.CurrentReview.TaskID != taskValue.ID || workspace.FeatureAreas[0].LatestReview == nil || workspace.FeatureAreas[0].LatestReview.ID != active.ID {
		t.Fatalf("Console projection was masked by legacy blocker: %s", body)
	}
	foundLegacy := false
	for _, historical := range workspace.FeatureAreas[0].Reviews {
		if historical.ID == legacy.ID {
			foundLegacy = historical.State == "BLOCKED" && historical.BlockerReason == legacyBlocker
		}
	}
	if !foundLegacy {
		t.Fatalf("legacy blocker was not preserved verbatim in history: %s", body)
	}
}

func TestProductReviewWorkspaceProjectsAuthoritativeOperationUsage(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()
	featureAreaValue, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Usage review", ProblemStatement: "Show the Review operation delta."})
	if err != nil {
		t.Fatal(err)
	}
	ensureConsoleFeatureActive(t, fixture, featureAreaValue.ID)
	taskValue, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, ProblemStatement: featureAreaValue.ProblemStatement, Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	reviewValue, err := fixture.plane.Reviews.Create(ctx, fixture.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: taskValue.ID, ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, CreatedByAISessionID: fixture.sessionID, RepositoryID: taskValue.RepositoryID, CodexConfigID: taskValue.CodexConfigID, BaseCommit: taskValue.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Inspect production read-only.", EngineeringGoal: featureAreaValue.ProblemStatement}))
	if err != nil {
		t.Fatal(err)
	}
	reviewRun, err := fixture.plane.Runs.CreateFocusedScopeReviewForReview(ctx, reviewValue.ID, "Inspect production read-only.", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.plane.Reviews.LinkRun(ctx, reviewValue.ID, reviewRun.ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.Status{run.StatusPreparing, run.StatusRunning, run.StatusCollecting} {
		if reviewRun, err = fixture.plane.Runs.Transition(ctx, reviewRun.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	if reviewRun, err = fixture.plane.Runs.SucceedWithUsage(ctx, reviewRun.ID, run.Result{AgentClaim: evidence.AgentClaim{Message: "bounded usage review"}}, &run.TokenUsageObservation{
		Reliable:        true,
		StartCumulative: run.TokenCounts{InputTokens: 20000, CachedInputTokens: 100, OutputTokens: 5000, TotalTokens: 25000},
		EndCumulative:   run.TokenCounts{InputTokens: 39842, CachedInputTokens: 35102, OutputTokens: 2817 + 5000, TotalTokens: 42659 + 25000},
	}); err != nil {
		t.Fatal(err)
	}

	status, _, body := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("workspace usage probe status=%d body=%s", status, body)
	}
	for _, expected := range []string{`"state":"COMPLETE"`, `"input_tokens":19842`, `"cached_input_tokens":35002`, `"total_tokens":42659`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("workspace usage probe missing %q: %s", expected, body)
		}
	}
	var workspace struct {
		FeatureAreas []struct {
			LatestReview *struct {
				Usage *consoleapi.OperationUsage `json:"usage"`
			} `json:"latest_review"`
		} `json:"featureAreas"`
	}
	if err := json.Unmarshal([]byte(body), &workspace); err != nil {
		t.Fatal(err)
	}
	if len(workspace.FeatureAreas) == 0 || workspace.FeatureAreas[0].LatestReview == nil || workspace.FeatureAreas[0].LatestReview.Usage == nil {
		t.Fatalf("latest Review usage projection missing: %s", body)
	}
	usage := workspace.FeatureAreas[0].LatestReview.Usage
	if usage.Status != run.TokenUsageAvailable || usage.Usage == nil || usage.Usage.InputTokens == nil || *usage.Usage.InputTokens != 19842 || usage.Usage.CachedInputTokens == nil || *usage.Usage.CachedInputTokens != 35002 || usage.Usage.OutputTokens == nil || *usage.Usage.OutputTokens != 2817 || usage.Usage.TotalTokens == nil || *usage.Usage.TotalTokens != 42659 || usage.DurationMilliseconds == nil {
		t.Fatalf("Review operation usage = %+v", usage)
	}

	status, _, tasksBody := getRaw(t, fixture.handler, "/ui/api/tasks/"+taskValue.ID+"?project_id="+fixture.projectID)
	if status != http.StatusOK {
		t.Fatalf("Task detail status=%d body=%s", status, tasksBody)
	}
	var detail consoleapi.ProductTaskSummary
	if err := json.Unmarshal([]byte(tasksBody), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Presentation == nil || detail.Presentation.TaskView == nil || len(detail.Presentation.TaskView.ReviewHistory) != 1 || detail.Presentation.TaskView.ReviewHistory[0].ID != reviewValue.ID {
		t.Fatalf("Review usage must remain attached to exact Task history: %s", tasksBody)
	}
	if strings.Contains(tasksBody, `"executionId":"`+reviewValue.ID+`"`) {
		t.Fatal("Review operation was represented as Task Execution")
	}
}

func TestProductExecutionProjectsDurableResultAndPerExecutionUsage(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()
	verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Synthetic probe"}}
	failed := createConsoleTask(t, fixture, fixture.moduleID, verification, "Execution result projection failed")
	succeeded := createConsoleTask(t, fixture, fixture.moduleID, verification, "Execution result projection succeeded")
	first := startTestRun(t, fixture, failed, false)
	if _, err := fixture.plane.Runs.Fail(ctx, first.ID, run.Failure{Kind: run.FailureTask, Stage: "execute", Code: "EXECUTION_FAILED", Message: strings.Repeat("x", 600)}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.NewExecutionProposalStore(fixture.plane.DB).Complete(ctx, first.ExecutionProposalID, executionproposal.OutcomeFailed, "EXECUTION_FAILED", strings.Repeat("y", 600), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	second := startTestRun(t, fixture, succeeded, false)
	if err := fixture.plane.Evidence.Save(ctx, evidence.GitEvidence{RunID: second.ID, BaseCommit: fixture.baseCommit, HeadCommit: fixture.baseCommit}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.Runs.AttachEndSnapshot(ctx, second.ID, strings.Repeat("2", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.Runs.Transition(ctx, second.ID, run.StatusCollecting); err != nil {
		t.Fatal(err)
	}
	if err := fixture.plane.AgentConversations.FinishRun(ctx, second.ID, agentconversation.KnownAdvance, "turn-"+second.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.Runs.SucceedWithUsage(ctx, second.ID, run.Result{
		AgentClaim:  evidence.AgentClaim{Message: "The execution produced a bounded change."},
		GitEvidence: evidence.GitEvidence{RunID: second.ID, BaseCommit: fixture.baseCommit, HeadCommit: fixture.baseCommit},
	}, &run.TokenUsageObservation{
		Reliable:        true,
		StartCumulative: run.TokenCounts{InputTokens: 10000, CachedInputTokens: 400, OutputTokens: 2000, ReasoningOutputTokens: 1500, TotalTokens: 12000},
		EndCumulative:   run.TokenCounts{InputTokens: 21000, CachedInputTokens: 3400, OutputTokens: 5000, ReasoningOutputTokens: 4000, TotalTokens: 24400},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.NewExecutionProposalStore(fixture.plane.DB).Complete(ctx, second.ExecutionProposalID, executionproposal.OutcomeSucceeded, "", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	control, err := controlapi.NewFromControlPlane(fixture.plane)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := consoleapi.New(fixture.plane, control)
	if err != nil {
		t.Fatal(err)
	}
	view, err := reader.GetProductProjectWorkspace(fixture.operatorContext(t), fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	byState := map[string]consoleapi.ProductExecutionSummary{}
	developmentCount := 0
	for _, value := range view.Executions {
		if value.Kind != "DEVELOPMENT" {
			continue
		}
		developmentCount++
		byState[value.State] = value
	}
	if developmentCount != 2 {
		t.Fatalf("want two Development Executions in diagnostic inventory: %+v", view.Executions)
	}
	completed := byState["COMPLETED"]
	failedExecution := byState["FAILED"]
	if completed.ID == "" || failedExecution.ID == "" {
		t.Fatalf("expected one COMPLETED and one FAILED Execution: %+v", view.Executions)
	}
	if completed.Result != "The execution produced a bounded change." || completed.CommitRef != fixture.baseCommit {
		t.Fatalf("completed Execution result = %q commit=%q", completed.Result, completed.CommitRef)
	}
	if completed.Usage == nil || completed.Usage.Status != run.TokenUsageAvailable || completed.Usage.Usage == nil || completed.Usage.Usage.InputTokens == nil || *completed.Usage.Usage.InputTokens != 11000 || completed.Usage.Usage.CachedInputTokens == nil || *completed.Usage.Usage.CachedInputTokens != 3000 || completed.Usage.Usage.OutputTokens == nil || *completed.Usage.Usage.OutputTokens != 3000 || completed.Usage.Usage.ReasoningOutputTokens == nil || *completed.Usage.Usage.ReasoningOutputTokens != 2500 || completed.Usage.Usage.TotalTokens == nil || *completed.Usage.Usage.TotalTokens != 12400 {
		t.Fatalf("completed Execution usage = %+v", completed.Usage)
	}
	if failedExecution.Usage != nil && (failedExecution.Usage.Status != run.TokenUsageUnavailable || failedExecution.Usage.Usage != nil) {
		t.Fatalf("failed Execution must not inherit token usage: %+v", failedExecution.Usage)
	}
	if !strings.Contains(failedExecution.Failure, strings.Repeat("y", 600)) {
		t.Fatalf("failed Execution failure message truncated to %d bytes: %q", len(failedExecution.Failure), failedExecution.Failure)
	}
}

func TestProductReviewProjectsResultAndProvenance(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()
	fullResult := "The Topic Review established a bounded change.\n\n```go\n" + strings.Repeat("result-line\n", 300) + "```\n"
	featureAreaValue, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Review result projection", ProblemStatement: "Show the durable Review result verbatim."})
	if err != nil {
		t.Fatal(err)
	}
	ensureConsoleFeatureActive(t, fixture, featureAreaValue.ID)
	taskValue, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, ProblemStatement: featureAreaValue.ProblemStatement, Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	reviewValue, err := fixture.plane.Reviews.Create(ctx, fixture.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: taskValue.ID, ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, CreatedByAISessionID: fixture.sessionID, RepositoryID: taskValue.RepositoryID, CodexConfigID: taskValue.CodexConfigID, BaseCommit: taskValue.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Inspect production read-only.", EngineeringGoal: featureAreaValue.ProblemStatement}))
	if err != nil {
		t.Fatal(err)
	}
	reviewRun, err := fixture.plane.Runs.CreateFocusedScopeReviewForReview(ctx, reviewValue.ID, "Inspect production read-only.", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.plane.Reviews.LinkRun(ctx, reviewValue.ID, reviewRun.ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.Status{run.StatusPreparing, run.StatusRunning, run.StatusCollecting} {
		if reviewRun, err = fixture.plane.Runs.Transition(ctx, reviewRun.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	if reviewRun, err = fixture.plane.Runs.SucceedWithUsage(ctx, reviewRun.ID, run.Result{
		AgentClaim:  evidence.AgentClaim{Message: fullResult},
		GitEvidence: evidence.GitEvidence{RunID: reviewRun.ID, BaseCommit: fixture.baseCommit, HeadCommit: fixture.baseCommit},
	}, &run.TokenUsageObservation{
		Reliable:        true,
		StartCumulative: run.TokenCounts{InputTokens: 1000, CachedInputTokens: 600, OutputTokens: 200, TotalTokens: 1200},
		EndCumulative:   run.TokenCounts{InputTokens: 3000, CachedInputTokens: 2600, OutputTokens: 700, TotalTokens: 3700},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.plane.Evidence.Save(ctx, evidence.GitEvidence{RunID: reviewRun.ID, BaseCommit: fixture.baseCommit, HeadCommit: fixture.baseCommit}); err != nil {
		t.Fatal(err)
	}

	control, err := controlapi.NewFromControlPlane(fixture.plane)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := consoleapi.New(fixture.plane, control)
	if err != nil {
		t.Fatal(err)
	}
	view, err := reader.GetProductProjectWorkspace(fixture.operatorContext(t), fixture.projectID)
	if err != nil || len(view.FeatureAreas) != 1 || len(view.FeatureAreas[0].Reviews) != 1 {
		t.Fatalf("workspace = %+v err=%v", view, err)
	}
	summary := view.FeatureAreas[0].Reviews[0]
	wantPreview := strings.TrimSpace(fullResult)[:2048] + "…"
	if summary.State != "COMPLETE" || !summary.ResultTruncated || summary.Result != wantPreview || summary.Provenance != fixture.baseCommit {
		t.Fatalf("Review projection = %+v", summary)
	}
	if summary.Usage == nil || summary.Usage.Status != run.TokenUsageAvailable || summary.Usage.Usage == nil || summary.Usage.Usage.InputTokens == nil || *summary.Usage.Usage.InputTokens != 2000 || summary.Usage.Usage.ReasoningOutputTokens != nil {
		t.Fatalf("Review usage = %+v", summary.Usage)
	}
	exact, err := reader.GetProductReviewResult(fixture.operatorContext(t), fixture.projectID, reviewValue.ID)
	if err != nil || exact.ReviewID != reviewValue.ID || exact.Result != fullResult {
		t.Fatalf("exact Review result = %+v err=%v", exact, err)
	}
	runDetail, err := reader.GetRun(fixture.operatorContext(t), fixture.projectID, reviewRun.ID)
	currentTask, taskErr := fixture.plane.Tasks.Get(ctx, taskValue.ID)
	if err != nil || taskErr != nil || runDetail.Run.ExecutorKind != string(reviewValue.ExecutorKind.Effective()) || runDetail.Run.TaskTitle != currentTask.Title || runDetail.Run.TaskPhase != string(currentTask.Phase) {
		t.Fatalf("Run must retain its exact Review executor and Task title: %+v err=%v", runDetail.Run, err)
	}
	status, _, body := getRaw(t, fixture.handler, "/ui/api/reviews/"+reviewValue.ID+"/result?project_id="+fixture.projectID)
	var response consoleapi.ProductReviewResult
	if status != http.StatusOK || json.Unmarshal([]byte(body), &response) != nil || response.Result != fullResult {
		t.Fatalf("exact Review result HTTP status=%d response=%+v", status, response)
	}
	status, _, _ = getRaw(t, fixture.handler, "/ui/api/reviews/"+reviewValue.ID+"/result?project_id="+fixture.projectID+"&unexpected=true")
	if status != http.StatusBadRequest {
		t.Fatalf("exact Review result accepted an unsupported query: %d", status)
	}
	other, err := fixture.plane.Projects.Create(ctx, "Other Review result project")
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = getRaw(t, fixture.handler, "/ui/api/reviews/"+reviewValue.ID+"/result?project_id="+other.ID)
	if status != http.StatusNotFound {
		t.Fatalf("cross-Project Review result status=%d, want 404", status)
	}
}

func TestProductFeatureAreaCloseUsesAuthoritativeLifecycleAndPreservesHistory(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := fixture.ctx
	area, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Closable Feature Area", ProblemStatement: "Close without deleting durable history."})
	if err != nil {
		t.Fatal(err)
	}
	ensureConsoleFeatureActive(t, fixture, area.ID)
	taskValue, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: area.ID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, ProblemStatement: area.ProblemStatement, Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	documentValue, err := fixture.plane.Documents.Create(ctx, document.CreateInput{AISessionID: fixture.sessionID, FeatureAreaID: area.ID, Title: "Closure history", Content: "This durable content must remain available."})
	if err != nil {
		t.Fatal(err)
	}

	status, _, body := getRaw(t, fixture.handler, "/ui/api/project-workspace?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(body, `"can_retire":false`) || !strings.Contains(body, "OPERATOR_ACTION_IDENTITY_UNAVAILABLE") {
		t.Fatalf("active Feature Area close capability status=%d body=%s", status, body)
	}
	postJSON(t, fixture.handler, "/ui/api/featureAreas/"+area.ID+"/close", map[string]string{"project_id": fixture.projectID, "request_id": "console-close-request"}, http.StatusInternalServerError, nil)
	stored, err := fixture.plane.FeatureAreas.GetForSession(ctx, fixture.sessionID, area.ID)
	if err != nil || stored.Status != featurearea.StatusActive || taskValue.ID == "" || documentValue.ID == "" {
		t.Fatalf("unowned close changed durable history: %+v %v", stored, err)
	}
}

func waitConsoleRetirement(t *testing.T, plane *controlplane.ControlPlane, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a, err := sqlite.NewFeatureAreaStore(plane.DB).Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if a.Status == featurearea.StatusRetired {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("retirement stuck: %+v %+v", a, a.Retirement)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
