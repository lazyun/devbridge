package bridgehttp

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workflowtest"
)

var bridgeTaskCreateSequence uint64

func nextBridgeTaskCreateRequestID() string {
	return "bridge-task-" + strconv.FormatUint(atomic.AddUint64(&bridgeTaskCreateSequence, 1), 10)
}

func TestConsoleUIIsEmbeddedAndSelfContained(t *testing.T) {
	plane, err := openTestPlane(t)
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	status, headers, html := getRaw(t, server.Handler(), "/ui/")
	if status != http.StatusOK || !strings.Contains(headers.Get("Content-Type"), "text/html") {
		t.Fatalf("GET /ui/: status=%d", status)
	}
	if !strings.Contains(headers.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("missing local-only CSP")
	}
	for _, marker := range []string{"product-app.js", "product.css", "main-content", "project-selector-slot", "data-product-nav=\"diagnostics\""} {
		if !strings.Contains(html, marker) {
			t.Fatalf("production shell missing %q", marker)
		}
	}
	for _, asset := range []string{"product-app.js", "product.css", "product-work-refresh.js"} {
		status, _, body := getRaw(t, server.Handler(), "/ui/"+asset)
		if status != http.StatusOK || body == "" {
			t.Fatalf("asset %s: status=%d", asset, status)
		}
		for _, remote := range []string{"https://", "http://", "//cdn"} {
			if strings.Contains(body, remote) {
				t.Fatalf("external dependency %s in %s", remote, asset)
			}
		}
	}
	for _, asset := range []string{"app.js", "app.css"} {
		status, _, _ := getRaw(t, server.Handler(), "/ui/"+asset)
		if status != http.StatusNotFound {
			t.Fatalf("obsolete asset %s remains served: %d", asset, status)
		}
	}
}

func TestConsoleReviewPromptEndpointReturnsExactPersistedPromptWithinProject(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := fixture.ctx
	featureAreaValue, err := fixture.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: fixture.sessionID, Title: "Review prompt", ProblemStatement: "Understand production ownership."})
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Exact synthetic Review prompt\nwith a second line."
	owner, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit, Goal: "Review prompt Task", ProblemStatement: featureAreaValue.ProblemStatement})
	if err != nil {
		t.Fatal(err)
	}
	reviewValue, err := fixture.plane.Reviews.Create(ctx, fixture.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: owner.ID, ProjectID: fixture.projectID, FeatureAreaID: featureAreaValue.ID, CreatedByAISessionID: fixture.sessionID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseCommit: fixture.baseCommit, Focus: review.Focus{Kind: review.FocusRepository, Summary: "Repository production review"}, EngineeringGoal: featureAreaValue.ProblemStatement, Instruction: prompt}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.plane.Runs.CreateFocusedScopeReviewForReview(ctx, reviewValue.ID, reviewValue.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}}); err != nil {
		t.Fatal(err)
	}
	persisted, err := fixture.plane.Reviews.RecordPrompt(ctx, reviewValue.ID, prompt, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	status, _, body := getRaw(t, fixture.handler, "/v1/reviews/"+reviewValue.ID+"/prompt?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(body, `"prompt":"Exact synthetic Review prompt\nwith a second line."`) || !strings.Contains(body, persisted.PromptSHA256) || !strings.Contains(body, `"bytes":49`) {
		t.Fatalf("Review prompt diagnostic status=%d body=%s", status, body)
	}
	other, err := fixture.plane.Projects.Create(ctx, "Other Review project")
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = getRaw(t, fixture.handler, "/v1/reviews/"+reviewValue.ID+"/prompt?project_id="+other.ID)
	if status != http.StatusNotFound {
		t.Fatalf("cross-Project Review prompt status=%d, want 404", status)
	}
	status, _, _ = getRaw(t, fixture.handler, "/v1/reviews/"+reviewValue.ID+"/prompt?project_id="+fixture.projectID+"&path=/etc/passwd")
	if status != http.StatusBadRequest {
		t.Fatalf("arbitrary Review prompt query status=%d, want 400", status)
	}
}

func TestConsoleRunPromptEndpointReturnsExactPersistedPromptWithinProject(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()
	taskValue := createConsoleTask(t, fixture, fixture.moduleID, task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "synthetic prompt diagnostic"}}, "Prompt diagnostic")
	runValue := startTestRun(t, fixture, taskValue, false)
	prompt := "  Exact synthetic Implementation prompt.\n\nKeep spacing.  "
	persisted, err := fixture.plane.Runs.RecordPrompt(ctx, runValue.ID, prompt, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	status, _, body := getRaw(t, fixture.handler, "/v1/runs/"+runValue.ID+"/prompt?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(body, `"prompt":"  Exact synthetic Implementation prompt.\n\nKeep spacing.  "`) || !strings.Contains(body, persisted.PromptSHA256) || !strings.Contains(body, `"purpose":"IMPLEMENTATION"`) {
		t.Fatalf("Run prompt diagnostic status=%d body=%s", status, body)
	}
	other, err := fixture.plane.Projects.Create(ctx, "Other Run project")
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = getRaw(t, fixture.handler, "/v1/runs/"+runValue.ID+"/prompt?project_id="+other.ID)
	if status != http.StatusNotFound {
		t.Fatalf("cross-Project Run prompt status=%d, want 404", status)
	}
	status, _, _ = getRaw(t, fixture.handler, "/v1/runs/"+runValue.ID+"/prompt?project_id="+fixture.projectID+"&path=/etc/passwd")
	if status != http.StatusBadRequest {
		t.Fatalf("arbitrary Run prompt query status=%d, want 400", status)
	}
}

func TestConsoleContextProbePromptEndpointReturnsExactPersistedPromptWithinProject(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()
	prompt := "  Exact synthetic Context Probe prompt.\n\nKeep spacing.  "
	probe, err := fixture.plane.ContextProbes.Create(ctx, contextprobe.CreateInput{AISessionID: fixture.sessionID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}, CodexInstruction: prompt})
	if err != nil {
		t.Fatal(err)
	}
	probe, err = fixture.plane.ContextProbes.Start(ctx, probe.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := fixture.plane.ContextProbes.RecordPrompt(ctx, probe.ID, prompt, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	status, _, body := getRaw(t, fixture.handler, "/v1/context-probes/"+probe.ID+"/prompt?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(body, `"prompt":"  Exact synthetic Context Probe prompt.\n\nKeep spacing.  "`) || !strings.Contains(body, persisted.PromptSHA256) {
		t.Fatalf("Context Probe prompt diagnostic status=%d body=%s", status, body)
	}
	other, err := fixture.plane.Projects.Create(ctx, "Other Context Probe project")
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = getRaw(t, fixture.handler, "/v1/context-probes/"+probe.ID+"/prompt?project_id="+other.ID)
	if status != http.StatusNotFound {
		t.Fatalf("cross-Project Context Probe prompt status=%d, want 404", status)
	}
	status, _, _ = getRaw(t, fixture.handler, "/v1/context-probes/"+probe.ID+"/prompt?project_id="+fixture.projectID+"&path=/etc/passwd")
	if status != http.StatusBadRequest {
		t.Fatalf("arbitrary Context Probe prompt query status=%d, want 400", status)
	}
}

func TestConsoleLogsEndpointUsesConfiguredDirectoryAndBoundedFilters(t *testing.T) {
	plane, err := openTestPlane(t)
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	logDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(logDirectory, "devbridge.jsonl"), []byte(`{"schemaVersion":1,"timestamp":"2026-01-02T03:04:05Z","level":"INFO","process":"devbridge","component":"review","event":"featurearea.review.start.accepted","featureAreaId":"featureArea-1","runId":"run-1","message":"accepted"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := NewWithLogDirectory(plane, false, logDirectory)
	if err != nil {
		t.Fatal(err)
	}
	status, _, body := getRaw(t, server.Handler(), "/v1/logs?limit=1&run_id=run-1")
	if status != http.StatusOK || !strings.Contains(body, "featurearea.review.start.accepted") {
		t.Fatalf("GET logs: status=%d body=%s", status, body)
	}
	status, _, body = getRaw(t, server.Handler(), "/v1/logs?limit=501&path=/etc/passwd")
	if status != http.StatusBadRequest || !strings.Contains(body, "limit must be between 1 and 500") {
		t.Fatalf("GET logs invalid limit: status=%d body=%s", status, body)
	}
}

func TestConsoleProjectSelectionPreservesExistingAISession(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	secondExisting := fixture.bindSession(t, fixture.projectID, "second-existing")
	projectB, err := fixture.plane.Projects.Create(context.Background(), "Project B")
	if err != nil {
		t.Fatal(err)
	}

	var selection map[string]any
	postJSON(t, fixture.handler, "/v1/project-selection", projectSelectionAction{ProjectID: projectB.ID}, http.StatusOK, &selection)
	if selection["selected_project_id"] != projectB.ID {
		t.Fatalf("selected Project = %v, want %s", selection["selected_project_id"], projectB.ID)
	}
	existing, err := fixture.plane.AISessions.Get(context.Background(), fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if existing.ProjectID != fixture.projectID {
		t.Fatalf("existing AI Session rebound to %s; want immutable %s", existing.ProjectID, fixture.projectID)
	}
	if secondExisting.ProjectID != fixture.projectID {
		t.Fatalf("second existing AI Session bound to %s; want %s", secondExisting.ProjectID, fixture.projectID)
	}
	future := fixture.bindSession(t, projectB.ID, "future")
	if future.ProjectID != projectB.ID {
		t.Fatalf("explicit AI Session bound to %s; want %s", future.ProjectID, projectB.ID)
	}
	status, _, sessionsBody := getRaw(t, fixture.handler, "/v1/ai-sessions?"+qsForTest(map[string]string{"project_id": fixture.projectID, "limit": "100"}))
	if status != http.StatusOK || !strings.Contains(sessionsBody, fixture.sessionID) || !strings.Contains(sessionsBody, secondExisting.ID) || strings.Contains(sessionsBody, future.ID) {
		t.Fatalf("Project AI Sessions status=%d body=%s", status, sessionsBody)
	}

	status, _, body := getRaw(t, fixture.handler, "/v1/projects?limit=100")
	if status != http.StatusOK {
		t.Fatalf("project list status=%d body=%s", status, body)
	}
	for _, secret := range []string{fixture.repositoryPath, "/private/sensitive/codex", "codex_path", "repository_path"} {
		if strings.Contains(body, secret) {
			t.Fatalf("project list leaked %q: %s", secret, body)
		}
	}
	status, _, body = getRaw(t, fixture.handler, "/v1/project-selection")
	if status != http.StatusOK || !strings.Contains(body, projectB.ID) {
		t.Fatalf("selection read status=%d body=%s", status, body)
	}
}

func TestConsoleReadModelsCoverTasksRunsApprovalsAccountingAndEvidence(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := fixture.ctx

	waived := createConsoleTask(t, fixture, fixture.moduleID, task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "User explicitly waived verification."}}, "waived verification")
	unclassifiedModule, err := fixture.plane.Modules.EnsureUnclassified(ctx, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	verification := task.VerificationSpec{
		Mode: task.VerificationModeRun, Scope: "repository", CodexInstruction: "Run the confirmed checks and report structured results.",
		Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"All tests pass."},
	}
	waiting := createConsoleTask(t, fixture, unclassifiedModule.ID, verification, "waiting for approval")
	running := createConsoleTask(t, fixture, fixture.moduleID, verification, "currently running")
	zero := createConsoleTask(t, fixture, fixture.moduleID, verification, "zero token terminal")
	paths := createConsoleTaskWithScope(t, fixture, fixture.moduleID, verification, "paths-scoped task", task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: []string{"tracked.txt"}})

	// Review cannot grant a waiver. Record it through the user's exact
	// REQUEST_CHANGES decision before exercising waived accounting.
	prior := startTestRun(t, fixture, waived, false)
	if err := fixture.plane.AgentConversations.RecordFinalization(ctx, prior.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: prior.TurnID, Quiesced: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.Runs.Fail(ctx, prior.ID, run.Failure{Kind: run.FailureTask, Code: "FIXTURE_REVISION", Message: "User revises verification policy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.NewExecutionProposalStore(fixture.plane.DB).Complete(ctx, prior.ExecutionProposalID, executionproposal.OutcomeFailed, "FIXTURE_REVISION", "User revises verification policy", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	changed, err := fixture.plane.WorkflowProgression.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: fixture.sessionID, ExecutionID: prior.ExecutionProposalID, DecisionID: decisionresolution.TaskAcceptanceDecisionID(prior.ExecutionProposalID), Option: "REQUEST_CHANGES", RequestID: "explicit-console-waiver", NextWorkSpec: &waived.WorkSpec, NextVerificationSpec: &waived.VerificationSpec})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.plane.AgentConversations.FinishRun(ctx, prior.ID, agentconversation.KnownAdvance, prior.TurnID, true); err != nil {
		t.Fatal(err)
	}
	waived = changed.Task

	waitingRun := startTestRun(t, fixture, waiting, false)
	if _, err := fixture.plane.Approvals.Request(ctx, waitingRun.ID, codex.ApprovalRequest{
		ID: "approval-console", ThreadID: waitingRun.ThreadID, TurnID: waitingRun.TurnID, ItemID: "item-console", Kind: codex.ApprovalKindCommandExecution,
		RequestPayload: json.RawMessage(`{"command":"go test ./...","api_key":"do-not-leak"}`), AllowedDecisions: []codex.DecisionType{codex.DecisionApproveOnce, codex.DecisionDecline},
	}); err != nil {
		t.Fatal(err)
	}
	startTestRun(t, fixture, running, false)
	zeroRun := startTestRun(t, fixture, zero, true)
	startTestRun(t, fixture, paths, false)

	recorder, ok := fixture.plane.Coordinator.Results.(*execution.DurableResultRecorder)
	if !ok {
		t.Fatal("control plane does not expose the durable result recorder")
	}
	if _, err := recorder.Record(ctx, zeroRun.ID, evidence.AgentClaim{Message: "I changed the file."}, evidence.GitEvidence{
		BaseCommit: fixture.baseCommit, HeadCommit: fixture.baseCommit, StartSnapshot: strings.Repeat("1", 40), EndSnapshot: strings.Repeat("2", 40), Dirty: true,
		ChangedFiles: []string{"tracked.txt"}, Diff: "diff --git a/tracked.txt b/tracked.txt\n-old\n+new\n",
	}); err != nil {
		t.Fatal(err)
	}

	projectQuery := qsForTest(map[string]string{"project_id": fixture.projectID, "limit": "100"})
	status, _, tasksBody := getRaw(t, fixture.handler, "/v1/tasks?"+projectQuery)
	if status != http.StatusOK {
		t.Fatalf("task list status=%d body=%s", status, tasksBody)
	}
	for _, expected := range []string{"Unclassified / 暂未分类", "WAITING_APPROVAL", "RUN", "NONE", "workspace_", "thread-"} {
		if !strings.Contains(tasksBody, expected) {
			t.Fatalf("task list missing %q: %s", expected, tasksBody)
		}
	}
	if strings.Contains(tasksBody, fixture.repositoryPath) || strings.Contains(tasksBody, "/private/sensitive/codex") {
		t.Fatalf("task list leaked a protected path: %s", tasksBody)
	}

	status, _, waivedBody := getRaw(t, fixture.handler, "/v1/tasks/"+waived.ID+"?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(waivedBody, `"disposition":"USER_WAIVED"`) || !strings.Contains(waivedBody, `"status":"UNAVAILABLE"`) {
		t.Fatalf("waived Task detail status=%d body=%s", status, waivedBody)
	}
	status, _, zeroBody := getRaw(t, fixture.handler, "/v1/tasks/"+zero.ID+"?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(zeroBody, `"status":"AVAILABLE"`) || !strings.Contains(zeroBody, `"total_tokens":0`) {
		t.Fatalf("zero-token Task detail status=%d body=%s", status, zeroBody)
	}
	status, _, pathsBody := getRaw(t, fixture.handler, "/v1/tasks/"+paths.ID+"?project_id="+fixture.projectID)
	if status != http.StatusOK || !strings.Contains(pathsBody, `"executionScope":{"kind":"PATHS","paths":["tracked.txt"]}`) || !strings.Contains(waivedBody, `"executionScope":{"kind":"REPOSITORY"`) {
		t.Fatalf("Task scopes were not preserved: paths status=%d paths=%s repository=%s", status, pathsBody, waivedBody)
	}

	status, _, activeBody := getRaw(t, fixture.handler, "/v1/runs?"+qsForTest(map[string]string{"project_id": fixture.projectID, "active": "true", "limit": "100"}))
	if status != http.StatusOK || !strings.Contains(activeBody, "WAITING_APPROVAL") || !strings.Contains(activeBody, `"status":"RUNNING"`) || !strings.Contains(activeBody, `"pending_approval_count":1`) {
		t.Fatalf("active Runs status=%d body=%s", status, activeBody)
	}

	status, _, approvalsBody := getRaw(t, fixture.handler, "/v1/approvals?"+qsForTest(map[string]string{"project_id": fixture.projectID, "pending": "true", "limit": "100"}))
	if status != http.StatusOK || !strings.Contains(approvalsBody, "approval-console") || !strings.Contains(approvalsBody, "[REDACTED]") || strings.Contains(approvalsBody, "do-not-leak") {
		t.Fatalf("approval list did not redact secrets: status=%d body=%s", status, approvalsBody)
	}

	status, _, runBody := getRaw(t, fixture.handler, "/v1/runs/"+zeroRun.ID+"?project_id="+fixture.projectID)
	for _, expected := range []string{`"available":true`, "tracked.txt", "diff --git", "I changed the file.", `"total_tokens":0`} {
		if status != http.StatusOK || !strings.Contains(runBody, expected) {
			t.Fatalf("Run detail missing %q: status=%d body=%s", expected, status, runBody)
		}
	}

	other, err := fixture.plane.Projects.Create(ctx, "Other project")
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = getRaw(t, fixture.handler, "/v1/tasks/"+waived.ID+"?project_id="+other.ID)
	if status != http.StatusNotFound {
		t.Fatalf("cross-Project Task detail status=%d, want 404", status)
	}
	status, _, bounded := getRaw(t, fixture.handler, "/v1/tasks?"+qsForTest(map[string]string{"project_id": fixture.projectID, "limit": "1"}))
	if status != http.StatusOK || !strings.Contains(bounded, `"has_more":true`) {
		t.Fatalf("bounded Task list status=%d body=%s", status, bounded)
	}
	status, _, _ = getRaw(t, fixture.handler, "/v1/tasks?"+qsForTest(map[string]string{"project_id": fixture.projectID, "limit": "101"}))
	if status != http.StatusBadRequest {
		t.Fatalf("oversized Task list status=%d, want 400", status)
	}
}

func TestConsoleTaskListFiltersAndProjectsWorkKind(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	ctx := context.Background()
	verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "User explicitly waived verification."}}
	unclassified := createConsoleTask(t, fixture, fixture.moduleID, verification, "unclassified task")
	classified, err := fixture.plane.Tasks.Create(ctx, task.CreateInput{
		AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: unclassified.FeatureAreaID, ModuleID: fixture.moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit,
		WorkKind: task.WorkKindBugFix, WorkSpec: task.WorkSpec{Goal: "classified task", Constraints: []string{"bounded"}, CodexInstruction: "Implement only the confirmed bounded change.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: verification,
	})
	if err != nil {
		t.Fatal(err)
	}

	projectQuery := qsForTest(map[string]string{"project_id": fixture.projectID, "limit": "100"})
	status, _, allBody := getRaw(t, fixture.handler, "/v1/tasks?"+projectQuery)
	if status != http.StatusOK || !strings.Contains(allBody, `"work_kind":"BUG_FIX"`) || !strings.Contains(allBody, unclassified.ID) {
		t.Fatalf("task list work kind status=%d body=%s", status, allBody)
	}
	status, _, bugBody := getRaw(t, fixture.handler, "/v1/tasks?"+qsForTest(map[string]string{"project_id": fixture.projectID, "work_kind": "BUG_FIX", "limit": "100"}))
	if status != http.StatusOK || !strings.Contains(bugBody, classified.ID) || strings.Contains(bugBody, unclassified.ID) {
		t.Fatalf("bug fix filter status=%d body=%s", status, bugBody)
	}
	status, _, noneBody := getRaw(t, fixture.handler, "/v1/tasks?"+qsForTest(map[string]string{"project_id": fixture.projectID, "work_kind": "FEATURE", "limit": "100"}))
	if status != http.StatusOK || strings.Contains(noneBody, classified.ID) || strings.Contains(noneBody, unclassified.ID) || !strings.Contains(noneBody, `"items":[]`) {
		t.Fatalf("feature filter status=%d body=%s", status, noneBody)
	}
}

type consoleFixture struct {
	ctx            context.Context
	plane          *controlplane.ControlPlane
	handler        http.Handler
	stateRoot      string
	repositoryPath string
	projectID      string
	repositoryID   string
	moduleID       string
	configID       string
	sessionID      string
	baseCommit     string
}

func newConsoleFixture(t *testing.T) consoleFixture { return newConsoleFixtureWithBaseline(t, true) }
func newConsoleFixtureWithBaseline(t *testing.T, selectBaseline bool) consoleFixture {
	t.Helper()
	callerScope := "bridge-console-" + strings.ReplaceAll(t.Name(), "/", "-")
	ctx := aisession.WithCallerScope(context.Background(), callerScope)
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "source")
	if err := os.MkdirAll(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, repositoryPath, "init", "-b", "main")
	git(t, repositoryPath, "config", "user.name", "Bridge Console test")
	git(t, repositoryPath, "config", "user.email", "bridge-console@example.invalid")
	if err := os.WriteFile(filepath.Join(repositoryPath, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repositoryPath, "add", "tracked.txt")
	git(t, repositoryPath, "commit", "-m", "base")
	base := git(t, repositoryPath, "rev-parse", "HEAD")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	projectValue, err := plane.Projects.Create(ctx, "Console fixture")
	if err != nil {
		t.Fatal(err)
	}
	repositoryValue, err := plane.Repositories.RegisterInProject(ctx, projectValue.ID, repositoryPath, "console-repository")
	if err != nil {
		t.Fatal(err)
	}
	if selectBaseline {
		selectConsoleDevelopmentBaseline(t, plane, projectValue.ID, repositoryValue.ID)
	}
	moduleValue, err := plane.Modules.Create(ctx, module.CreateInput{ProjectID: projectValue.ID, Key: "console", Name: "Console", Description: "Console module"})
	if err != nil {
		t.Fatal(err)
	}
	configValue, err := plane.CodexConfigs.Create(ctx, codexconfig.CreateInput{Name: "Sensitive config", CodexPath: "/private/sensitive/codex", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: callerScope}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, "console-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, projectValue.ID, "console-bind")
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	return consoleFixture{ctx: ctx, plane: plane, handler: withTestLoopbackTransport(server.Handler()), stateRoot: filepath.Join(root, "state"), repositoryPath: repositoryPath, projectID: projectValue.ID, repositoryID: repositoryValue.ID, moduleID: moduleValue.ID, configID: configValue.ID, sessionID: initialized.AISessionID, baseCommit: base}
}

func (f consoleFixture) bindSession(t *testing.T, projectID, suffix string) aisession.Session {
	t.Helper()
	ref := aiprojectselection.ClientBindingReference{CallerScope: "bridge-console-" + strings.ReplaceAll(t.Name(), "/", "-")}
	initialized, err := f.plane.AIContext.InitializeSession(f.ctx, ref, "console-"+suffix+"-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := f.plane.AIContext.BindProject(f.ctx, ref, initialized.AISessionID, projectID, "console-"+suffix+"-bind")
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.plane.AISessions.Get(f.ctx, bound.AISessionID)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// codingReview freezes the same backend and runtime choice that a production
// Review admission persists.  HTTP read fixtures create durable historical
// Reviews directly, so they must supply this already-admitted identity rather
// than relying on the retired implicit defaults.
func (f consoleFixture) codingReview(t *testing.T, value review.Review) review.Review {
	t.Helper()
	configValue, err := f.plane.CodexConfigs.Get(f.ctx, value.CodexConfigID)
	if err != nil {
		t.Fatal(err)
	}
	selection := configValue.Selection("bridgehttp-review-fixture")
	value.ExecutorKind = review.ExecutorCodingAgent
	value.BackendSelection = &selection
	if value.Model == "" {
		value.Model = "gpt-5.6-terra"
	}
	if value.ReasoningEffort == "" {
		value.ReasoningEffort = "high"
	}
	return value
}

func (f consoleFixture) operatorContext(t *testing.T) context.Context {
	t.Helper()
	ingress, err := localoperator.NewIngress(f.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/projects", nil)
	setTestLoopbackTransport(req)
	ctx, err := ingress.AdmitHTTP(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = localoperator.BindProject(ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func createConsoleTask(t *testing.T, fixture consoleFixture, moduleID string, verification task.VerificationSpec, goal string) task.Task {
	return createConsoleTaskWithScope(t, fixture, moduleID, verification, goal, task.ExecutionScope{Kind: task.ExecutionScopeRepository})
}

func createConsoleTaskWithScope(t *testing.T, fixture consoleFixture, moduleID string, verification task.VerificationSpec, goal string, scope task.ExecutionScope) task.Task {
	t.Helper()
	value, err := fixture.plane.Tasks.Create(context.Background(), task.CreateInput{
		AISessionID: fixture.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: fixture.projectID, FeatureAreaID: newActiveConsoleFeature(t, fixture, goal), ModuleID: moduleID, RepositoryID: fixture.repositoryID, CodexConfigID: fixture.configID, BaseRevision: fixture.baseCommit,
		Goal: goal, ProblemStatement: goal, Model: "gpt-5.6-terra", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	value.WorkSpec = task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: goal, Constraints: []string{"bounded"}, CodexInstruction: "Implement only the confirmed bounded change.", ExecutionScope: scope}
	value.VerificationSpec = verification
	return value
}

func startTestRun(t *testing.T, fixture consoleFixture, taskValue task.Task, finishWithZeroUsage bool) run.Run {
	t.Helper()
	return startTestRunWithKey(t, fixture, taskValue, finishWithZeroUsage, "")
}

func startTestRunWithKey(t *testing.T, fixture consoleFixture, taskValue task.Task, finishWithZeroUsage bool, key string) run.Run {
	t.Helper()
	ctx := context.Background()
	var runValue run.Run
	var err error
	if taskValue.ReviewApprovalID == "" {
		taskValue, _, runValue = workflowtest.ApproveAndReserve(t, fixture.plane.DB, taskValue, fixture.sessionID, taskValue.WorkSpec, taskValue.VerificationSpec)
	} else {
		proposal, reserved, reserveErr := sqlite.NewWorkflowProgressionStore(fixture.plane.DB).ReserveTaskExecution(ctx, fixture.sessionID, taskValue.ID)
		runValue, err = reserved, reserveErr
		if err == nil && runValue.ID == "" {
			_, runValue, err = sqlite.NewExecutionProposalStore(fixture.plane.DB).PrepareAdmittedExecution(ctx, proposal.ID, time.Now())
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if runValue, err = fixture.plane.Runs.Transition(ctx, runValue.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	conversation, err := fixture.plane.AgentConversations.BeginRun(ctx, runValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	threadID := conversation.BackendThreadID
	if threadID == "" {
		threadID = "thread-" + runValue.ID
	}
	proposal, err := sqlite.NewExecutionProposalStore(fixture.plane.DB).Get(ctx, runValue.ExecutionProposalID)
	if err != nil || proposal.BackendSelection == nil {
		t.Fatalf("frozen execution backend: proposal=%+v err=%v", proposal, err)
	}
	model := proposal.RuntimeConfig.Model
	if model == "" {
		model = taskValue.Model
	}
	if err := fixture.plane.AgentConversations.BindThreadWithRuntimeProvenance(ctx, runValue.ID, threadID, model, "BRIDGEHTTP_FIXTURE", *proposal.BackendSelection); err != nil {
		t.Fatal(err)
	}
	workspaceValue, err := fixture.plane.Workspaces.EnsureForFeatureArea(ctx, taskValue.ProjectID, taskValue.RepositoryID, taskValue.FeatureAreaID, taskValue.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.Runs.AttachWorkspace(ctx, runValue.ID, workspaceValue.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.Runs.AttachThread(ctx, runValue.ID, threadID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.plane.Runs.AttachStartSnapshot(ctx, runValue.ID, strings.Repeat("1", 40)); err != nil {
		t.Fatal(err)
	}
	if runValue, err = fixture.plane.Runs.Transition(ctx, runValue.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if runValue, err = fixture.plane.Runs.AttachTurn(ctx, runValue.ID, "turn-"+runValue.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.plane.AgentConversations.MarkTurnPending(ctx, runValue.ID); err != nil {
		t.Fatal(err)
	}
	if !finishWithZeroUsage {
		return runValue
	}
	if _, err := fixture.plane.Runs.AttachEndSnapshot(ctx, runValue.ID, strings.Repeat("2", 40)); err != nil {
		t.Fatal(err)
	}
	if runValue, err = fixture.plane.Runs.Transition(ctx, runValue.ID, run.StatusCollecting); err != nil {
		t.Fatal(err)
	}
	zero := run.TokenCounts{}
	window := int64(200000)
	if err := fixture.plane.AgentConversations.FinishRun(ctx, runValue.ID, agentconversation.KnownAdvance, "turn-"+runValue.ID, true); err != nil {
		t.Fatal(err)
	}
	runValue, err = fixture.plane.Runs.SucceedWithUsage(ctx, runValue.ID, run.Result{}, &run.TokenUsageObservation{StartCumulative: zero, EndCumulative: zero, ModelContextWindow: &window, Reliable: true})
	if err != nil {
		t.Fatal(err)
	}
	return runValue
}

func openTestPlane(t *testing.T) (*controlplane.ControlPlane, error) {
	t.Helper()
	return controlplane.Open(context.Background(), controlplane.Config{DataRoot: t.TempDir()})
}

func getRaw(t *testing.T, handler http.Handler, target string) (int, http.Header, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	setTestLoopbackTransport(request)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, string(body)
}

func withTestLoopbackTransport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setTestLoopbackTransport(r)
		next.ServeHTTP(w, r)
	})
}

func setTestLoopbackTransport(r *http.Request) {
	r.RemoteAddr = "127.0.0.1:45678"
	r.Host = "127.0.0.1:48765"
	*r = *r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 48765}))
}

func qsForTest(values map[string]string) string {
	var parts []string
	for key, value := range values {
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, "&")
}

func ensureConsoleFeatureActive(t *testing.T, fixture consoleFixture, id string) {
	t.Helper()
	ctx := context.Background()
	area, err := fixture.plane.FeatureAreas.GetForSession(ctx, fixture.sessionID, id)
	if err != nil {
		t.Fatal(err)
	}
	if area.Status == featurearea.StatusActive {
		return
	}
	if _, err := fixture.plane.FeatureAreas.Activate(ctx, fixture.sessionID, id, "lifecycle-request"); err != nil {
		t.Fatal(err)
	}
}
func newActiveConsoleFeature(t *testing.T, fixture consoleFixture, title string) string {
	t.Helper()
	area, err := fixture.plane.FeatureAreas.Create(context.Background(), featurearea.CreateInput{AISessionID: fixture.sessionID, Title: title, ProblemStatement: title})
	if err != nil {
		t.Fatal(err)
	}
	ensureConsoleFeatureActive(t, fixture, area.ID)
	return area.ID
}
func selectConsoleDevelopmentBaseline(t *testing.T, plane *controlplane.ControlPlane, projectID, repoID string) {
	t.Helper()
	ctx := context.Background()
	c, err := plane.DevelopmentCycles.Context(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range c.Repositories {
		if r.RepositoryID == repoID {
			if len(r.Branches) == 0 {
				t.Fatal("missing branch")
			}
			_, err = plane.DevelopmentCycles.Select(ctx, developmentcycle.SelectInput{ProjectID: projectID, RepositoryID: repoID, DevelopmentRef: r.Branches[0].FullRef, RequestID: "fixture-baseline-" + repoID})
			if err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("missing fixture repository")
}
