package localmcp_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowtest"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type recoveryIntegrationFixture struct {
	root, sessionID, projectID, featureAreaID, taskID string
	runID, conversationID, executionID                string
}

const recoveryFixtureCallerScope = "data-instance-local"

func createUnknownRecoveryFixture(t *testing.T) recoveryIntegrationFixture {
	t.Helper()
	ctx := aisession.WithCallerScope(context.Background(), recoveryFixtureCallerScope)
	root := filepath.Join(t.TempDir(), "state")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	projectValue := project.Project{ID: "project-recovery", Name: "Recovery", CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewProjectStore(plane.DB).Create(ctx, projectValue); err != nil {
		t.Fatal(err)
	}
	if _, err = plane.ProjectContexts.EnsureRequired(ctx, projectValue.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = plane.ProjectContexts.MarkBootstrapped(ctx, projectValue.ID, projectcontext.StatusNoGuidance, "sha256:recovery-fixture", "", "", nil); err != nil {
		t.Fatal(err)
	}
	repositoryValue := repository.Repository{ID: "repository-recovery", ProjectID: projectValue.ID, Name: "Primary", Path: createGitFixture(t, filepath.Dir(root), "repository"), CreatedAt: now, UpdatedAt: now}
	if _, _, err = sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repositoryValue); err != nil {
		t.Fatal(err)
	}
	moduleValue := module.Module{ID: "module-recovery", ProjectID: projectValue.ID, Key: "recovery", Name: "Recovery", CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewModuleStore(plane.DB).Create(ctx, moduleValue); err != nil {
		t.Fatal(err)
	}
	if _, err = plane.Modules.ReplaceContextMap(ctx, projectValue.ID, []module.ContextModuleInput{{
		Key: "recovery", Name: "Recovery", Description: "Owns recovery integration fixtures.",
		ProductionFacts: []module.ProductionFacts{{RepositoryID: repositoryValue.ID, RepositoryName: repositoryValue.Name, Responsibility: "Exercises durable agent recovery.", ResponsibilitySource: module.ResponsibilityDerived, ProductionRoots: []string{"internal"}, Entrypoints: []string{"internal/localmcp"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	initialized, err := plane.AIContext.InitializeSession(ctx, aiprojectselection.ClientBindingReference{CallerScope: recoveryFixtureCallerScope}, "recovery-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := plane.AIContext.BindProject(ctx, aiprojectselection.ClientBindingReference{CallerScope: recoveryFixtureCallerScope}, initialized.AISessionID, projectValue.ID, "recovery-bind")
	if err != nil {
		t.Fatal(err)
	}
	sessionValue := bound.Context.Session
	featureAreaValue := featurearea.FeatureArea{ID: "feature-area-recovery", ProjectID: projectValue.ID, CreatedByAISessionID: sessionValue.ID, Title: "Recover interrupted agent", ProblemStatement: "An interrupted turn has an unknown boundary.", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewFeatureAreaStore(plane.DB).Create(ctx, featureAreaValue); err != nil {
		t.Fatal(err)
	}
	taskValue := task.Task{ID: "task-recovery", ProjectID: projectValue.ID, FeatureAreaID: featureAreaValue.ID, ModuleID: moduleValue.ID, RepositoryID: repositoryValue.ID, CodexConfigID: codexconfig.DefaultConfigID, Goal: "Exercise recovery", WorkSpec: task.WorkSpec{Goal: "Exercise recovery", CodexInstruction: "Exercise recovery.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "integration fixture"}}, BaseCommit: strings.Repeat("a", 40), Status: task.StatusActive, Phase: task.PhaseReviewRequired, Title: "Recovery intent", CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewTaskStore(plane.DB).Create(ctx, taskValue); err != nil {
		t.Fatal(err)
	}
	executionID := "execution_approved_" + taskValue.ID
	runValue := seedRecoveryExecution(t, plane, taskValue, sessionValue.ID, executionID)
	if _, err = plane.Runs.Transition(ctx, runValue.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	conversation, err := plane.AgentConversations.BeginRun(ctx, runValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = plane.AgentConversations.BindThread(ctx, runValue.ID, "thread-interrupted"); err != nil {
		t.Fatal(err)
	}
	if _, err = plane.Runs.AttachThread(ctx, runValue.ID, "thread-interrupted"); err != nil {
		t.Fatal(err)
	}
	if _, err = plane.Runs.Transition(ctx, runValue.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err = plane.Runs.AttachTurn(ctx, runValue.ID, "turn-interrupted"); err != nil {
		t.Fatal(err)
	}
	if err = plane.AgentConversations.MarkTurnPending(ctx, runValue.ID); err != nil {
		t.Fatal(err)
	}
	if err = plane.AgentConversations.FinishRun(ctx, runValue.ID, agentconversation.Unknown, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = plane.Runs.Fail(ctx, runValue.ID, run.Failure{Kind: run.FailureInfrastructure, Stage: "agent", Code: "INTERRUPTED", Message: "runtime stopped after dispatch"}); err != nil {
		t.Fatal(err)
	}
	if err = plane.Close(); err != nil {
		t.Fatal(err)
	}
	return recoveryIntegrationFixture{root: root, sessionID: sessionValue.ID, projectID: projectValue.ID, featureAreaID: featureAreaValue.ID, taskID: taskValue.ID, runID: runValue.ID, conversationID: conversation.ID, executionID: executionID}
}

func agentOperationFromWorkflow(t *testing.T, output map[string]any) map[string]any {
	t.Helper()
	workflow := output["workflow"].(map[string]any)
	if workflow["state"] != "RECOVERY_REQUIRED" {
		t.Fatalf("workspace recovery state = %#v", workflow)
	}
	for _, raw := range workflow["capabilities"].([]any) {
		cap := raw.(map[string]any)
		if cap["action"] == "RECOVER" && cap["available"] == true {
			return cap["target"].(map[string]any)
		}
	}
	t.Fatalf("workspace missing exact recovery capability: %#v", workflow)
	return nil
}

func recoveryArguments(f recoveryIntegrationFixture, requestID string) map[string]any {
	return map[string]any{"aiSessionId": f.sessionID, "featureAreaId": f.featureAreaID, "operationKind": "EXECUTION", "operationId": f.executionID, "runId": f.runID, "agentConversationId": f.conversationID, "requestId": requestID}
}

func TestAgentOperationRecoveryIsDurableAndEquivalentAcrossAdapters(t *testing.T) {
	fixture := createUnknownRecoveryFixture(t)
	ctx := aisession.WithCallerScope(context.Background(), recoveryFixtureCallerScope)

	// Opening a new exclusive writer creates a new epoch and runs startup
	// RecoveryStore.RecoverAgentOperations before either adapter becomes ready.
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: fixture.root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	control, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true}, plane.AIContext, control)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.GetSession(ctx, fixture.sessionID); err != nil {
		t.Fatalf("get recovered session: %v", err)
	}
	if _, err = service.ListRepositories(ctx, fixture.sessionID, aicontext.ListOptions{Limit: 100}); err != nil {
		t.Fatalf("list recovered repositories: %v", err)
	}
	if _, err = service.ListProjectModuleMap(ctx, fixture.sessionID); err != nil {
		t.Fatalf("list recovered module map: %v", err)
	}
	if _, err = service.CurrentWorkflow(ctx, fixture.sessionID, ""); err != nil {
		t.Fatalf("read recovered workflow: %v", err)
	}
	_, _, gptURL := startGPTTestServer(t, service)
	localURL := startLocalMCPTestServer(t, service)
	gpt := connectGPT(t, gptURL)
	local := connectLocalMCP(t, localURL)
	// Startup recovery may canonicalize a historical execution/run identity.
	// Read the durable current claim rather than asserting the pre-v84 fixture's
	// synthetic identifiers survived that migration.
	state, err := plane.AgentConversations.State(ctx, fixture.featureAreaID)
	if err != nil || state.Claim == nil || state.AgentConversationID == "" {
		t.Fatalf("recovered operation state = %+v, err=%v", state, err)
	}
	fixture.executionID = state.Claim.OperationID
	fixture.runID = state.Claim.RunID
	fixture.conversationID = state.AgentConversationID

	wantTarget := map[string]any{"operationKind": "EXECUTION", "operationId": fixture.executionID, "runId": fixture.runID, "agentConversationId": fixture.conversationID}
	for name, session := range map[string]*sdkmcp.ClientSession{"gpt": gpt, "local-mcp": local} {
		workflow := callToolMap(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": fixture.sessionID, "featureAreaId": fixture.featureAreaID, "taskId": fixture.taskID})
		operation := agentOperationFromWorkflow(t, workflow)
		for key, want := range wantTarget {
			if operation[key] != want {
				t.Fatalf("%s %s=%#v, want %#v; operation=%#v", name, key, operation[key], want, operation)
			}
		}
		if _, exposed := operation["runtimeEpoch"]; exposed {
			t.Fatalf("%s workflow exposed runtimeEpoch: %#v", name, operation)
		}
	}

	wrong := recoveryArguments(fixture, "request-wrong-owner")
	wrong["operationId"] = "some-other-owner"
	if text := callToolErrorText(t, ctx, local, "agent_operation_recover", wrong); !strings.Contains(text, "conflict") {
		t.Fatalf("wrong owner error = %s", text)
	}

	arguments := recoveryArguments(fixture, "request-recover-once")
	first := callToolMap(t, ctx, gpt, "agent_operation_recover", arguments)
	if first["state"] != "READY" || first["boundary"] != "UNKNOWN" || first["contextAbandoned"] != true || first["freshWorkAllowed"] != true || first["oldAgentConversationId"] != fixture.conversationID {
		t.Fatalf("GPT recovery result = %#v", first)
	}
	newConversationID, _ := first["newAgentConversationId"].(string)
	if newConversationID == "" || newConversationID == fixture.conversationID {
		t.Fatalf("successor identity = %q", newConversationID)
	}
	readyReplay := callToolMap(t, ctx, local, "agent_operation_recover", arguments)
	if !reflect.DeepEqual(first, readyReplay) {
		t.Fatalf("READY cross-adapter replay changed result:\nfirst=%#v\nreplayed=%#v", first, readyReplay)
	}
	current, err := plane.AgentConversations.Current(ctx, fixture.featureAreaID)
	if err != nil || current.ID != newConversationID || current.BackendThreadID != "" || current.CurrentBoundaryTurnID != "" || current.PredecessorID != fixture.conversationID {
		t.Fatalf("threadless successor = %+v, err=%v", current, err)
	}

	currentTask, err := plane.Tasks.Get(ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	currentTask.ID += "-successor"
	currentTask.Status = task.StatusActive
	currentTask.Phase = task.PhaseReviewRequired
	currentTask.CurrentReviewID, currentTask.CurrentExecutionID, currentTask.ReviewApprovalID = "", "", ""
	currentTask.CurrentDefinitionRevisionID = ""
	currentTask.ConfirmedTaskPlanID = ""
	if err := sqlite.NewTaskStore(plane.DB).Create(ctx, currentTask); err != nil {
		t.Fatal(err)
	}
	successorRun := seedRecoveryExecution(t, plane, currentTask, fixture.sessionID, "execution-recovery-successor")
	if _, err = plane.Runs.Transition(ctx, successorRun.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err = plane.AgentConversations.BeginRun(ctx, successorRun.ID); err != nil {
		t.Fatal(err)
	}
	state, err = plane.AgentConversations.State(ctx, fixture.featureAreaID)
	if err != nil || state.Status != "BUSY" || state.Claim == nil || state.Claim.RunID != successorRun.ID {
		t.Fatalf("successor state = %+v, err=%v", state, err)
	}

	replayed := callToolMap(t, ctx, local, "agent_operation_recover", arguments)
	// The durable recovery result is stable; guidance is intentionally fresh.
	delete(first, "workflowGuidance")
	delete(replayed, "workflowGuidance")
	delete(first, "currentState")
	delete(first, "nextAction")
	delete(replayed, "currentState")
	delete(replayed, "nextAction")
	if !reflect.DeepEqual(first, replayed) {
		t.Fatalf("cross-adapter replay changed result:\nfirst=%#v\nreplayed=%#v", first, replayed)
	}
	conflictingReplay := recoveryArguments(fixture, "request-recover-once")
	conflictingReplay["runId"] = successorRun.ID
	if text := callToolErrorText(t, ctx, gpt, "agent_operation_recover", conflictingReplay); !strings.Contains(text, "conflict") {
		t.Fatalf("request replay with changed target error = %s", text)
	}
}

func TestRecoveryAuthorityIsAbsentFromModelAndHostToolInputs(t *testing.T) {
	service := readyService(t)
	_, _, gptURL := startGPTTestServer(t, service)
	localURL := startLocalMCPTestServer(t, service)
	for name, session := range map[string]*sdkmcp.ClientSession{"gpt": connectGPT(t, gptURL), "local-mcp": connectLocalMCP(t, localURL)} {
		listed, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tool := range listed.Tools {
			if tool.Name != "agent_operation_recover" {
				continue
			}
			found = true
			encoded, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.ToLower(string(encoded)), "runtimeepoch") {
				t.Fatalf("%s recovery input exposes runtime epoch: %s", name, encoded)
			}
		}
		if !found {
			t.Fatalf("%s does not expose agent_operation_recover", name)
		}
	}
	for _, tool := range execution.FeatureAreaHostToolV1Specs() {
		if tool.Name == "agent_operation_recover" {
			t.Fatalf("recovery command leaked into backend Host Tools")
		}
		encoded, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(encoded)), "runtimeepoch") {
			t.Fatalf("Host Tool %s exposes runtime epoch: %s", tool.Name, encoded)
		}
	}
}

func seedRecoveryExecution(t *testing.T, plane *controlplane.ControlPlane, current task.Task, sessionID, id string) run.Run {
	t.Helper()
	_, _, reserved := workflowtest.ApproveAndReserve(t, plane.DB, current, sessionID, current.WorkSpec, current.VerificationSpec)
	return reserved
}
