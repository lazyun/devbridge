package localmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/workflowtest"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var projectFixtureSequence atomic.Uint64

type fixtureTransport string

const (
	fixtureTransportGPT   fixtureTransport = "gpt"
	fixtureTransportLocal fixtureTransport = "local"
)

// fixtureSessionTransport is test-only routing metadata. A failed unknown-tool
// call closes an MCP client session, so migration fixtures must select the
// adapter's current project-binding protocol before their first mutation.
var fixtureSessionTransport sync.Map // map[*sdkmcp.ClientSession]fixtureTransport

func rawToolMap(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any) (map[string]any, error) {
	t.Helper()
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, err
	}
	if result.IsError {
		encoded, _ := json.Marshal(result.Content)
		return nil, errors.New(string(encoded))
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return nil, err
	}
	var output map[string]any
	err = json.Unmarshal(encoded, &output)
	return output, err
}

// projectOpenFixture migrates old cross-adapter scenarios to Session-first
// identity. Local MCP uses the retained project_open command; ChatGPT uses its
// purpose-confirmation replacement. The adapter is test-only and preserves the
// actual production handlers on both paths.
func projectOpenFixture(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, arguments map[string]any) (map[string]any, error) {
	t.Helper()
	seq := projectFixtureSequence.Add(1)
	projectID, _ := arguments["projectId"].(string)
	sessionID, _ := arguments["aiSessionId"].(string)
	if sessionID == "" {
		initialized, err := rawToolMap(t, ctx, session, "session_initialize", map[string]any{"requestId": fmt.Sprintf("legacy-fixture-initialize-%d", seq)})
		if err != nil {
			return nil, err
		}
		result, _ := initialized["result"].(map[string]any)
		sessionID, _ = result["aiSessionId"].(string)
	}
	requestID := fmt.Sprintf("legacy-fixture-bind-%d", seq)
	if value, ok := arguments["requestId"].(string); ok && value != "" {
		requestID = value
	}
	transport, known := fixtureSessionTransport.Load(session)
	if !known {
		return nil, errors.New("fixture transport is unknown")
	}
	if transport == fixtureTransportLocal {
		opened, err := rawToolMap(t, ctx, session, "project_open", map[string]any{"aiSessionId": sessionID, "projectId": projectID, "requestId": requestID})
		if err != nil {
			return nil, err
		}
		return surfaceMutationTestResult("project_open", opened), nil
	}
	if transport != fixtureTransportGPT {
		return nil, errors.New("unsupported fixture transport")
	}
	current, readErr := rawToolMap(t, ctx, session, "session_initialize", map[string]any{"aiSessionId": sessionID})
	if readErr != nil {
		return nil, readErr
	}
	currentResult, _ := current["result"].(map[string]any)
	if bound, _ := currentResult["boundProjectId"].(string); bound != "" {
		if bound != projectID {
			return nil, errors.New("AI_SESSION_PROJECT_ALREADY_BOUND")
		}
		return map[string]any{"aiSessionId": sessionID, "projectId": projectID}, nil
	}
	prepared, err := rawToolMap(t, ctx, session, "project_setup_prepare", map[string]any{"aiSessionId": sessionID, "requestId": requestID + "-prepare", "purpose": "PROJECT_SETUP", "action": "SELECT_PROJECT", "projectSetup": map[string]any{"summary": "Bind the explicitly selected fixture Project"}, "variants": []any{map[string]any{"id": "selected", "label": "Selected Project", "summary": "Bind the explicitly selected fixture Project"}}})
	if err != nil {
		return nil, err
	}
	intent, _ := prepared["intent"].(map[string]any)
	_, err = rawToolMap(t, ctx, session, "confirmation_commit", map[string]any{"aiSessionId": sessionID, "intentId": intent["intentId"], "version": intent["version"], "purpose": "PROJECT_SETUP", "action": "SELECT_PROJECT", "requestId": requestID + "-commit", "contentHash": intent["contentHash"], "variantId": "selected", "selection": map[string]any{"projectId": projectID}})
	if err != nil {
		return nil, err
	}
	return map[string]any{"aiSessionId": sessionID, "projectId": projectID}, nil
}

func featureAreaCreateFixture(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, arguments map[string]any) (map[string]any, error) {
	t.Helper()
	transport, known := fixtureSessionTransport.Load(session)
	if !known {
		return nil, errors.New("fixture transport is unknown")
	}
	if transport == fixtureTransportLocal {
		output, err := rawToolMap(t, ctx, session, "feature_area_create", arguments)
		if err != nil {
			return nil, err
		}
		return surfaceMutationTestResult("featureArea_create", output), nil
	}
	if transport != fixtureTransportGPT {
		return nil, errors.New("unsupported fixture transport")
	}
	seq := projectFixtureSequence.Add(1)
	sessionID, _ := arguments["aiSessionId"].(string)
	title, _ := arguments["title"].(string)
	problem, _ := arguments["problemStatement"].(string)
	requestID := fmt.Sprintf("legacy-fixture-area-%d", seq)
	prepared, err := rawToolMap(t, ctx, session, "feature_area_prepare", map[string]any{
		"aiSessionId": sessionID, "requestId": requestID + "-prepare", "purpose": "FEATURE_AREA", "action": "CREATE_AREA",
		"featureArea": map[string]any{"title": title, "problemStatement": problem},
		"variants":    []any{map[string]any{"id": "create", "label": "Create Feature Area", "summary": "Create the requested Feature Area"}},
	})
	if err != nil {
		return nil, err
	}
	intent, _ := prepared["intent"].(map[string]any)
	committed, err := rawToolMap(t, ctx, session, "confirmation_commit", map[string]any{
		"aiSessionId": sessionID, "intentId": intent["intentId"], "version": intent["version"], "purpose": "FEATURE_AREA", "action": "CREATE_AREA",
		"requestId": requestID + "-commit", "contentHash": intent["contentHash"], "variantId": "create", "selection": map[string]any{},
	})
	if err != nil {
		return nil, err
	}
	committedIntent, _ := committed["intent"].(map[string]any)
	steps, _ := committedIntent["steps"].([]any)
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		if id, _ := step["featureAreaId"].(string); id != "" {
			return map[string]any{"id": id, "projectId": fixtureProjectIDFromSession(t, ctx, session, sessionID)}, nil
		}
	}
	return nil, errors.New("confirmation result omitted featureAreaId")
}

func taskCreateFixture(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, arguments map[string]any) (map[string]any, error) {
	t.Helper()
	transport, ok := fixtureSessionTransport.Load(session)
	if !ok {
		return nil, errors.New("fixture transport is unknown")
	}
	if transport == fixtureTransportLocal {
		out, err := rawToolMap(t, ctx, session, "task_create", arguments)
		if err != nil {
			return nil, err
		}
		return surfaceMutationTestResult("task_create", out), nil
	}
	if transport != fixtureTransportGPT {
		return nil, errors.New("unsupported fixture transport")
	}
	sid, _ := arguments["aiSessionId"].(string)
	requestID, _ := arguments["requestId"].(string)
	constraints := []any{}
	switch value := arguments["constraints"].(type) {
	case []string:
		for _, item := range value {
			constraints = append(constraints, item)
		}
	case []any:
		constraints = value
	}
	prepared, err := rawToolMap(t, ctx, session, "task_prepare", map[string]any{"aiSessionId": sid, "requestId": requestID + "-prepare", "purpose": "TASK", "action": "CREATE_TASK", "task": map[string]any{"featureAreaId": arguments["featureAreaId"], "repositoryId": arguments["repositoryId"], "title": arguments["title"], "problemStatement": arguments["problemStatement"], "constraints": constraints, "startReview": false}, "variants": []any{map[string]any{"id": "create", "label": "Create Task", "summary": "Create the exact fixture Task."}}})
	if err != nil {
		return nil, err
	}
	intent, _ := prepared["intent"].(map[string]any)
	committed, err := rawToolMap(t, ctx, session, "confirmation_commit", map[string]any{"aiSessionId": sid, "intentId": intent["intentId"], "version": intent["version"], "purpose": "TASK", "action": "CREATE_TASK", "requestId": requestID + "-commit", "contentHash": intent["contentHash"], "variantId": "create", "selection": map[string]any{}})
	if err != nil {
		return nil, err
	}
	value, _ := committed["intent"].(map[string]any)
	for _, raw := range value["steps"].([]any) {
		if step, _ := raw.(map[string]any); step["taskId"] != nil {
			return map[string]any{"id": step["taskId"]}, nil
		}
	}
	return nil, errors.New("confirmation result omitted taskId")
}

// taskAcceptanceFixture adapts legacy cross-surface assertions to the current
// split transport: Local MCP keeps the typed decision command while GPT owns
// the same Core transition through an immutable EXECUTION confirmation.
func taskAcceptanceFixture(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, arguments map[string]any) (map[string]any, error) {
	t.Helper()
	transport, ok := fixtureSessionTransport.Load(session)
	if !ok {
		return nil, errors.New("fixture transport is unknown")
	}
	if transport == fixtureTransportLocal {
		args := map[string]any{}
		for k, v := range arguments {
			args[k] = v
		}
		delete(args, "reviewId")
		args["kind"] = "TASK_ACCEPTANCE"
		out, err := rawToolMap(t, ctx, session, "decision_respond", args)
		if err != nil {
			return nil, err
		}
		return surfaceMutationTestResult("task_acceptance_respond", out), nil
	}
	if transport != fixtureTransportGPT {
		return nil, errors.New("unsupported fixture transport")
	}
	sid, _ := arguments["aiSessionId"].(string)
	requestID, _ := arguments["requestId"].(string)
	action, _ := arguments["option"].(string)
	execution := map[string]any{"action": action, "taskId": arguments["taskId"], "featureAreaId": arguments["featureAreaId"], "reviewId": arguments["reviewId"], "executionId": arguments["executionId"], "taskDefinitionRevisionId": arguments["taskDefinitionRevisionId"], "decisionId": arguments["decisionId"]}
	variant := map[string]any{"id": "decision", "label": "Resolve Task acceptance", "summary": "Resolve the exact pending Task acceptance."}
	// REQUEST_CHANGES is an authored successor and therefore keeps its exact
	// specifications in the immutable confirmation variant.
	if action == "REQUEST_CHANGES" {
		variant["implementationMode"] = "CODING_AGENT"
		variant["workSpec"] = arguments["nextWorkSpec"]
		variant["verificationSpec"] = arguments["nextVerificationSpec"]
		if targets := arguments["nextRepositoryTargets"]; targets != nil {
			variant["repositoryTargets"] = targets
		}
		if primary := arguments["nextPrimaryMemberId"]; primary != nil {
			variant["primaryMemberId"] = primary
		}
	}
	prepared, err := rawToolMap(t, ctx, session, "execution_prepare", map[string]any{"aiSessionId": sid, "requestId": requestID + "-prepare", "purpose": "EXECUTION", "action": action, "execution": execution, "variants": []any{variant}})
	if err != nil {
		return nil, fmt.Errorf("execution_prepare input=%v: %w", map[string]any{"action": action, "execution": execution, "variants": []any{variant}}, err)
	}
	intent, _ := prepared["intent"].(map[string]any)
	if intent == nil {
		return nil, errors.New("execution confirmation omitted intent")
	}
	selection := map[string]any{}
	if action == "REQUEST_CHANGES" {
		options, optionErr := rawToolMap(t, ctx, session, "confirmation_options_get", map[string]any{"aiSessionId": sid, "intentId": intent["intentId"], "version": intent["version"], "purpose": "EXECUTION", "contentHash": intent["contentHash"]})
		if optionErr != nil {
			return nil, optionErr
		}
		choice, _ := options["options"].(map[string]any)
		backends, _ := choice["backendCandidates"].([]any)
		runtimes, _ := choice["runtimeOptions"].([]any)
		if len(backends) == 0 || len(runtimes) == 0 {
			return nil, errors.New("execution confirmation has no Coding runtime choice")
		}
		backend, _ := backends[0].(map[string]any)
		runtime, _ := runtimes[0].(map[string]any)
		efforts, _ := runtime["supportedReasoningEfforts"].([]any)
		if len(efforts) == 0 {
			return nil, errors.New("execution confirmation runtime has no reasoning effort")
		}
		selection["backend"] = map[string]any{"configId": backend["configId"], "configFingerprint": backend["configFingerprint"]}
		selection["runtimeConfig"] = map[string]any{"model": runtime["modelId"], "reasoningEffort": efforts[0], "codexFastMode": false}
	}
	committed, err := rawToolMap(t, ctx, session, "confirmation_commit", map[string]any{"aiSessionId": sid, "intentId": intent["intentId"], "version": intent["version"], "purpose": "EXECUTION", "action": action, "requestId": requestID + "-commit", "contentHash": intent["contentHash"], "variantId": "decision", "selection": selection})
	if err != nil {
		return nil, fmt.Errorf("execution confirmation commit action=%s: %w", action, err)
	}
	if committedIntent, _ := committed["intent"].(map[string]any); committedIntent != nil {
		if steps, _ := committedIntent["steps"].([]any); len(steps) != 0 {
			if step, _ := steps[0].(map[string]any); step["state"] == "FAILED" {
				return nil, fmt.Errorf("execution confirmation step failed: %v", step)
			}
		}
	}
	return committed, nil
}

// reviewStartFixture keeps the old state-machine tests on the current GPT
// contract.  GPT no longer exposes task_review_start: it first stores a
// purpose-scoped REVIEW intent and the component consumes that exact immutable
// intent.  Local MCP intentionally continues to exercise its canonical direct
// command.  This is a test adapter only; it never re-registers the retired
// GPT command or weakens its caller/selection checks.
func reviewStartFixture(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, arguments map[string]any) (map[string]any, error) {
	t.Helper()
	transport, known := fixtureSessionTransport.Load(session)
	if !known {
		return nil, errors.New("fixture transport is unknown")
	}
	if transport == fixtureTransportLocal {
		out, err := rawToolMap(t, ctx, session, "task_review_start", arguments)
		if err != nil {
			return nil, err
		}
		return surfaceMutationTestResult("task_review_start", out), nil
	}
	if transport != fixtureTransportGPT {
		return nil, errors.New("unsupported fixture transport")
	}
	sid, _ := arguments["aiSessionId"].(string)
	taskID, _ := arguments["taskId"].(string)
	areaID, _ := arguments["featureAreaId"].(string)
	repositoryID, _ := arguments["repositoryId"].(string)
	requestID, _ := arguments["requestId"].(string)
	if requestID == "" {
		return nil, errors.New("requestId is required")
	}
	depth, _ := arguments["reviewDepth"].(string)
	plan, _ := arguments["reviewPlan"].(map[string]any)
	instruction, _ := arguments["reviewInstruction"].(string)
	executor, _ := arguments["executorKind"].(string)
	if executor == "" {
		executor = "CODING_AGENT"
	}
	prepared, err := rawToolMap(t, ctx, session, "review_prepare", map[string]any{
		"aiSessionId": sid, "requestId": requestID + "-prepare", "purpose": "REVIEW", "action": "START_REVIEW",
		"review":   map[string]any{"taskId": taskID, "featureAreaId": areaID, "repositoryId": repositoryID, "reviewDepth": depth, "reviewPlan": plan},
		"variants": []any{map[string]any{"id": "review", "label": "Review", "summary": "Run the prepared read-only review.", "executorKind": executor, "reviewInstruction": instruction}},
	})
	if err != nil {
		return nil, err
	}
	intent, _ := prepared["intent"].(map[string]any)
	if intent == nil {
		return nil, errors.New("review preparation omitted intent")
	}
	options, err := rawToolMap(t, ctx, session, "confirmation_options_get", map[string]any{"aiSessionId": sid, "intentId": intent["intentId"], "version": intent["version"], "purpose": "REVIEW", "contentHash": intent["contentHash"]})
	if err != nil {
		return nil, err
	}
	choice, _ := options["options"].(map[string]any)
	selection := map[string]any{}
	if executor == "CODING_AGENT" {
		candidates, _ := choice["backendCandidates"].([]any)
		runtimes, _ := choice["runtimeOptions"].([]any)
		if len(candidates) == 0 || len(runtimes) == 0 {
			return nil, errors.New("review confirmation has no coding runtime choice")
		}
		backend, _ := candidates[0].(map[string]any)
		runtime, _ := runtimes[0].(map[string]any)
		selection["backend"] = map[string]any{"configId": backend["configId"], "configFingerprint": backend["configFingerprint"]}
		efforts, _ := runtime["supportedReasoningEfforts"].([]any)
		if len(efforts) == 0 {
			return nil, errors.New("review confirmation runtime has no reasoning effort")
		}
		selection["runtimeConfig"] = map[string]any{"model": runtime["modelId"], "reasoningEffort": efforts[0], "codexFastMode": false}
	}
	committed, err := rawToolMap(t, ctx, session, "confirmation_commit", map[string]any{"aiSessionId": sid, "intentId": intent["intentId"], "version": intent["version"], "purpose": "REVIEW", "action": "START_REVIEW", "requestId": requestID + "-commit", "contentHash": intent["contentHash"], "variantId": "review", "selection": selection})
	if err != nil {
		return nil, err
	}
	committedIntent, _ := committed["intent"].(map[string]any)
	if committedIntent == nil {
		return nil, errors.New("review confirmation omitted committed intent")
	}
	var reviewID string
	for _, raw := range committedIntent["steps"].([]any) {
		step, _ := raw.(map[string]any)
		if id, _ := step["reviewId"].(string); id != "" {
			reviewID = id
		}
	}
	if reviewID == "" {
		return nil, errors.New("review confirmation omitted reviewId")
	}
	state, err := rawToolMap(t, ctx, session, "operation_get", map[string]any{"aiSessionId": sid, "target": map[string]any{"kind": "REVIEW", "operationId": reviewID, "ownerTaskId": taskID}})
	if err != nil {
		return nil, err
	}
	result, _ := state["result"].(map[string]any)
	if result == nil {
		return nil, errors.New("review readback omitted result")
	}
	return map[string]any{"review": result["reviewSource"], "currentState": state["workflow"].(map[string]any)["state"], "nextAction": state["workflow"].(map[string]any)["nextAction"]}, nil
}

func fixtureProjectIDFromSession(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, sessionID string) string {
	t.Helper()
	current, err := rawToolMap(t, ctx, session, "session_initialize", map[string]any{"aiSessionId": sessionID})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := current["result"].(map[string]any)
	projectID, _ := result["boundProjectId"].(string)
	return projectID
}

type integrationFixture struct {
	intentMu     sync.Mutex
	intentIDs    map[string]string
	ctx          context.Context
	plane        *controlplane.ControlPlane
	control      *controlapi.Facade
	service      *mcpserver.StaticService
	gptURL       string
	libreURL     string
	projectID    string
	repositoryID string
	repoPath     string
	moduleID     string
}

func (f *integrationFixture) operatorContext(t *testing.T) context.Context {
	t.Helper()
	ingress, err := localoperator.NewIngress("localmcp-fixture")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/projects", nil)
	req.RemoteAddr = "127.0.0.1:45678"
	req.Host = "127.0.0.1:48765"
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 48765}))
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

func createGitFixture(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("onboarding\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for relative, contents := range map[string]string{"primary/main.go": "package primary\n", "support/helper.go": "package support\n", "unrelated/other.go": "package unrelated\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(path, relative)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, relative), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Dev Bridge Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial"}} {
		command := exec.Command("git", args...)
		command.Dir = path
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return path
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
	return newIntegrationFixtureWithBaseline(t, true)
}
func newIntegrationFixtureWithBaseline(t *testing.T, selectBaseline bool) *integrationFixture {
	t.Helper()
	// Direct Core assertions in this integration fixture use the same verified
	// loopback caller scope as the MCP requests that created the Session.
	ctx := aisession.WithCallerScope(context.Background(), "data-instance-local")
	root := t.TempDir()
	workflowtest.SeedEmptyDatabase(t, filepath.Join(root, "state", "state"))
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	plane.Coordinator.Codex = &stubCodexFactory{}
	control, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true}, plane.AIContext, control)
	if err != nil {
		t.Fatal(err)
	}
	// Both adapters run concurrently over the same application services.
	_, _, gptURL := startGPTTestServer(t, service)
	libreURL := startLocalMCPTestServer(t, service)
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	projectValue := projectValue("project-fixture", "Local MCP fixture", now)
	if err := sqlite.NewProjectStore(plane.DB).Create(ctx, projectValue); err != nil {
		t.Fatal(err)
	}
	repositoryValue := repositoryValue("repository-fixture", projectValue.ID, "Primary", createGitFixture(t, root, "repository"), now)
	if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repositoryValue); err != nil {
		t.Fatal(err)
	}
	// Explicit fixture integration baseline; individual selection tests use their own fixture.
	if selectBaseline {
		branch, e := exec.Command("git", "-C", repositoryValue.Path, "symbolic-ref", "HEAD").Output()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = plane.DevelopmentCycles.Select(ctx, developmentcycle.SelectInput{ProjectID: projectValue.ID, RepositoryID: repositoryValue.ID, DevelopmentRef: strings.TrimSpace(string(branch)), RequestID: "fixture-baseline"}); e != nil {
			t.Fatal(e)
		}
	}
	unclassified, err := plane.Modules.EnsureUnclassified(ctx, projectValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &integrationFixture{
		ctx: ctx, plane: plane, control: control, service: service,
		gptURL: gptURL, libreURL: libreURL, projectID: projectValue.ID,
		repositoryID: repositoryValue.ID, repoPath: repositoryValue.Path, moduleID: unclassified.ID,
	}
}

func callToolMap(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any) map[string]any {
	t.Helper()
	if name == "project_open" {
		output, err := projectOpenFixture(t, ctx, session, arguments)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	if name == "featureArea_create" {
		output, err := featureAreaCreateFixture(t, ctx, session, arguments)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	if name == "task_create" {
		output, err := taskCreateFixture(t, ctx, session, arguments)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	if name == "task_review_start" {
		output, err := reviewStartFixture(t, ctx, session, arguments)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	if name == "task_acceptance_respond" {
		output, err := taskAcceptanceFixture(t, ctx, session, arguments)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	originalName := name
	name, arguments = surfaceMutationTestArgs(name, arguments)
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		encoded, _ := json.Marshal(result.Content)
		t.Fatalf("%s returned error: %s", name, encoded)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	return surfaceMutationTestResult(originalName, output)
}

func callToolErrorText(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any) string {
	t.Helper()
	if name == "project_open" {
		_, err := projectOpenFixture(t, ctx, session, arguments)
		if err == nil {
			t.Fatal("project_open unexpectedly succeeded")
		}
		return err.Error()
	}
	if name == "task_create" {
		_, err := taskCreateFixture(t, ctx, session, arguments)
		if err == nil {
			t.Fatal("task_create unexpectedly succeeded")
		}
		return err.Error()
	}
	if name == "task_acceptance_respond" {
		_, err := taskAcceptanceFixture(t, ctx, session, arguments)
		if err == nil {
			t.Fatal("task_acceptance_respond unexpectedly succeeded")
		}
		return err.Error()
	}
	if name == "task_review_start" {
		_, err := reviewStartFixture(t, ctx, session, arguments)
		if err == nil {
			t.Fatal("task_review_start unexpectedly succeeded")
		}
		return err.Error()
	}
	name, arguments = surfaceMutationTestArgs(name, arguments)
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("%s unexpectedly succeeded: %+v", name, result.StructuredContent)
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestProjectOpenSessionBindingIsImmutableAcrossAdapters(t *testing.T) {
	fixture := newIntegrationFixture(t)
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	foreign := projectValue("project-foreign-binding", "Foreign binding", now)
	if err := sqlite.NewProjectStore(fixture.plane.DB).Create(fixture.ctx, foreign); err != nil {
		t.Fatal(err)
	}

	gpt := connectGPT(t, fixture.gptURL)
	local := connectLocalMCP(t, fixture.libreURL)
	opened := callToolMap(t, fixture.ctx, gpt, "project_open", map[string]any{"projectId": fixture.projectID})
	sessionID := opened["aiSessionId"].(string)

	// A response replay or a different adapter must reuse the durable binding.
	reopened := callToolMap(t, fixture.ctx, local, "project_open", map[string]any{"aiSessionId": sessionID, "projectId": fixture.projectID})
	if reopened["aiSessionId"] != sessionID || reopened["project"].(map[string]any)["id"] != fixture.projectID {
		t.Fatalf("same-project reopen changed binding: %+v", reopened)
	}
	if text := callToolErrorText(t, fixture.ctx, local, "project_open", map[string]any{"aiSessionId": sessionID, "projectId": foreign.ID}); !contains(text, "SESSION_REQUEST_CONFLICT") {
		t.Fatalf("foreign Project rebind did not fail closed: %s", text)
	}
	bound, err := fixture.plane.AISessions.Get(fixture.ctx, sessionID)
	if err != nil || bound.ProjectID != fixture.projectID {
		t.Fatalf("durable binding changed after rejected rebind: %+v err=%v", bound, err)
	}
}

func TestLocalMCPSessionsAreIndependentAndContinueSharedProjectState(t *testing.T) {
	fixture := newIntegrationFixture(t)
	gptSession := connectGPT(t, fixture.gptURL)
	libreOne := connectLocalMCP(t, fixture.libreURL)
	libreTwo := connectLocalMCP(t, fixture.libreURL)

	openedGPT := callToolMap(t, fixture.ctx, gptSession, "project_open", map[string]any{"projectId": fixture.projectID})
	openedOne := callToolMap(t, fixture.ctx, libreOne, "project_open", map[string]any{"projectId": fixture.projectID})
	openedTwo := callToolMap(t, fixture.ctx, libreTwo, "project_open", map[string]any{"projectId": fixture.projectID})
	sessionGPT := openedGPT["aiSessionId"].(string)
	sessionOne := openedOne["aiSessionId"].(string)
	sessionTwo := openedTwo["aiSessionId"].(string)
	if sessionGPT == "" || sessionOne == "" || sessionTwo == "" {
		t.Fatalf("sessions = %q %q %q", sessionGPT, sessionOne, sessionTwo)
	}
	if sessionGPT == sessionOne || sessionOne == sessionTwo || sessionGPT == sessionTwo {
		t.Fatalf("sessions must be independent: %q %q %q", sessionGPT, sessionOne, sessionTwo)
	}
	if openedOne["project"].(map[string]any)["id"] != fixture.projectID || openedTwo["project"].(map[string]any)["id"] != fixture.projectID {
		t.Fatalf("local MCP sessions did not bind the requested Project: %+v %+v", openedOne, openedTwo)
	}

	// The GPT-side conversation creates durable Feature Area state.
	featureAreaValue := callToolMap(t, fixture.ctx, gptSession, "featureArea_create", map[string]any{"aiSessionId": sessionGPT, "title": "Improve onboarding", "problemStatement": "Make first-run setup clear."})
	featureAreaID := featureAreaValue["id"].(string)

	// The local MCP client conversation sees and continues the same Project state:
	// the featureArea appears in its workflow snapshot, and it can attach a durable
	// Document to the GPT-created Feature Area.
	state := callToolMap(t, fixture.ctx, libreOne, "workspace_get", map[string]any{"aiSessionId": sessionOne, "featureAreaId": featureAreaID})
	selected := state["result"].(map[string]any)["selectedFeatureArea"].(map[string]any)
	if selected["id"] != featureAreaID || state["context"].(map[string]any)["projectId"] != fixture.projectID {
		t.Fatalf("Local workspace lost shared FeatureArea: %#v", state)
	}

	document := callToolMap(t, fixture.ctx, libreTwo, "document_create", map[string]any{"aiSessionId": sessionTwo, "featureAreaId": featureAreaID, "title": "Cross-adapter note", "content": "Durable Project knowledge from the local MCP client."})
	if document["projectId"] != fixture.projectID || document["featureAreaId"] != featureAreaID {
		t.Fatalf("cross-adapter Document ownership = %+v", document)
	}
	if len(document["content"].(string)) == 0 || document["content"] != "Durable Project knowledge from the local MCP client." {
		t.Fatalf("Document content changed: %+v", document)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
