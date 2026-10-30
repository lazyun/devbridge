package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// All other service methods are nil: any accidental write, Project open or
// legacy workflow read will panic rather than make this test pass.
type presentationOnlyService struct {
	BridgeService
	projection     presentation.Projection
	taskProjection presentation.Projection
	areas          []featurearea.FeatureArea
	tasks          []task.Task
	executions     []agentexecution.Projection
	reads          int
}

func (s *presentationOnlyService) InitializeSession(_ context.Context, _ aiprojectselection.ClientBindingReference, _ string, id string) (aicontext.InitializationResult, error) {
	if id != "session" && id != "fresh" {
		return aicontext.InitializationResult{}, errors.New("session not found")
	}
	projectID := s.projection.DevelopmentContext.Project.ID
	if projectID == "" {
		projectID = "project"
	}
	return aicontext.InitializationResult{AISessionID: id, State: "PROJECT_BOUND", BoundProjectID: &projectID, AvailableProjects: []aicontext.ProjectIdentity{}, AvailableProjectsStatus: "NOT_APPLICABLE", AvailableProjectsComplete: true}, nil
}
func (s *presentationOnlyService) BindProject(context.Context, aiprojectselection.ClientBindingReference, string, string, string) (aicontext.ProjectBindingResult, error) {
	return aicontext.ProjectBindingResult{}, errors.New("fixture binding is already established")
}

func (s *presentationOnlyService) GetSession(_ context.Context, id string) (aicontext.SessionContext, error) {
	if id != "session" && id != "fresh" {
		return aicontext.SessionContext{}, errors.New("session not found")
	}
	projectID := s.projection.DevelopmentContext.Project.ID
	if projectID == "" {
		projectID = "project"
	}
	return aicontext.SessionContext{Session: aisession.Session{ID: id, ProjectID: projectID}, Project: project.Project{ID: projectID}}, nil
}
func (s *presentationOnlyService) GetTask(_ context.Context, _ string, id string) (task.Task, error) {
	for _, value := range s.tasks {
		if value.ID == id {
			if value.ProjectID == "" {
				value.ProjectID = "project"
			}
			return value, nil
		}
	}
	return task.Task{}, errors.New("task not found")
}
func (s *presentationOnlyService) FeatureAreaPresentationForSession(_ context.Context, sessionID, areaID string) (presentation.Projection, error) {
	if sessionID != "session" && sessionID != "fresh" || areaID != s.projection.FeatureArea.ID {
		return presentation.Projection{}, errors.New("not found")
	}
	s.reads++
	return s.projection, nil
}
func (s *presentationOnlyService) DevelopmentContextPresentationForSession(_ context.Context, sessionID string) (presentation.DevelopmentContext, error) {
	s.reads++
	return s.projection.DevelopmentContext, nil
}
func (s *presentationOnlyService) TaskPresentationForSession(_ context.Context, sessionID, areaID, taskID string) (presentation.Projection, error) {
	if sessionID != "session" || areaID == "" || taskID == "" {
		return presentation.Projection{}, errors.New("not found")
	}
	return s.taskProjection, nil
}
func (s *presentationOnlyService) ListFeatureAreas(context.Context, string) ([]featurearea.FeatureArea, error) {
	return append([]featurearea.FeatureArea(nil), s.areas...), nil
}
func (s *presentationOnlyService) GetFeatureArea(_ context.Context, _ string, id string) (featurearea.FeatureArea, error) {
	for _, value := range s.areas {
		if value.ID == id {
			if value.ProjectID == "" {
				value.ProjectID = "project"
			}
			return value, nil
		}
	}
	return featurearea.FeatureArea{}, errors.New("not found")
}
func (s *presentationOnlyService) ListTasks(_ context.Context, _ string, options aicontext.ListOptions) ([]task.Task, error) {
	start := options.Offset
	if start > len(s.tasks) {
		start = len(s.tasks)
	}
	end := start + options.Limit
	if end > len(s.tasks) {
		end = len(s.tasks)
	}
	return append([]task.Task(nil), s.tasks[start:end]...), nil
}
func (s *presentationOnlyService) ListAgentExecutionsForSession(context.Context, string) ([]agentexecution.Projection, error) {
	return append([]agentexecution.Projection(nil), s.executions...), nil
}

func TestDevBridgeCardProjectionKeepsNavigationExplicitAndTaskScoped(t *testing.T) {
	areaA := featurearea.FeatureArea{ID: "area-a", Title: "Area A", Status: featurearea.StatusActive}
	areaB := featurearea.FeatureArea{ID: "area-b", Title: "Area B", Status: featurearea.StatusDiscovering}
	areaProjection := presentation.Projection{FeatureArea: presentation.Identity{ID: areaA.ID, Name: areaA.Title}, Details: presentation.Details{FeatureAreaID: areaA.ID}}
	taskProjection := areaProjection
	taskProjection.Details.TaskID = "task-new"
	taskProjection.TaskView = &presentation.TaskView{ID: "task-new", Phase: task.PhaseImplementing, CurrentExecution: &presentation.ExecutionView{ID: "execution-new", State: "RUNNING", ImplementationRunID: "run-new", UpdatedAt: time.Unix(4, 0)}}
	service := &presentationOnlyService{
		projection:     areaProjection,
		taskProjection: taskProjection,
		areas:          []featurearea.FeatureArea{areaA, areaB},
		tasks: []task.Task{
			{ID: "task-old", FeatureAreaID: areaA.ID, Type: task.TypeDevelopment, ProblemStatement: "Older task", Status: task.StatusSucceeded, UpdatedAt: time.Unix(1, 0)},
			{ID: "task-other", FeatureAreaID: areaB.ID, ProblemStatement: "Other area", Status: task.StatusReady, UpdatedAt: time.Unix(3, 0)},
			{ID: "task-new", FeatureAreaID: areaA.ID, Type: task.TypeDevelopment, ProblemStatement: "Newest task", Status: task.StatusRunning, UpdatedAt: time.Unix(2, 0)},
		},
	}
	contextOnly, err := devBridgeCardProjection(context.Background(), service, devBridgeCardInput{AISessionID: "session"})
	if err != nil || contextOnly.Presentation != nil || contextOnly.SelectedFeatureAreaID != "" || len(contextOnly.Tasks) != 0 || len(contextOnly.FeatureAreas) != 2 {
		t.Fatalf("context navigation inferred a selection: %+v err=%v", contextOnly, err)
	}
	selected, err := devBridgeCardProjection(context.Background(), service, devBridgeCardInput{AISessionID: "session", FeatureAreaID: areaA.ID, TaskID: "task-new"})
	if err != nil || selected.Presentation == nil || selected.Presentation.Details.TaskID != "task-new" || selected.SelectedTaskID != "task-new" || selected.SelectedWork == nil || selected.SelectedWork.Kind != "EXECUTION" || selected.SelectedWork.OperationID != "execution-new" || selected.SelectedWork.OwnerTaskID != "task-new" || selected.SelectedWork.RunID != "run-new" {
		t.Fatalf("exact Task projection lost: %+v err=%v", selected, err)
	}
	if len(selected.Tasks) != 2 || selected.Tasks[0].ID != "task-new" || selected.Tasks[1].ID != "task-old" {
		t.Fatalf("Task navigation was not scoped and newest-first: %+v", selected.Tasks)
	}
	if _, err := devBridgeCardProjection(context.Background(), service, devBridgeCardInput{AISessionID: "session", FeatureAreaID: areaA.ID, TaskID: "task-other"}); err == nil {
		t.Fatal("cross-FeatureArea Task navigation succeeded")
	}
}

func TestDevBridgeCardProjectsExplicitProjectInspectionOperation(t *testing.T) {
	area := featurearea.FeatureArea{ID: "area", Title: "Area", Status: featurearea.StatusActive}
	service := &presentationOnlyService{
		projection: presentation.Projection{FeatureArea: presentation.Identity{ID: area.ID}},
		areas:      []featurearea.FeatureArea{area},
		tasks:      []task.Task{{ID: "inspection-task", ProjectID: "project", Type: task.TypeInspection, Scope: task.ScopeProject, WorkOwnerID: "probe", Status: task.StatusRunning}},
		executions: []agentexecution.Projection{{ProjectID: "project", TaskID: "inspection-task", Kind: agentexecution.KindInspection, SemanticOwnerID: "probe", RunID: "inspection-run", Status: "RUNNING", UpdatedAt: time.Unix(8, 0)}},
	}
	out, err := devBridgeCardProjection(context.Background(), service, devBridgeCardInput{AISessionID: "session", TaskID: "inspection-task"})
	if err != nil || out.SelectedTaskID != "" || out.SelectedWork == nil {
		t.Fatalf("workspace=%+v err=%v", out, err)
	}
	if out.SelectedWork.Kind != "INSPECTION" || out.SelectedWork.OperationID != "probe" || out.SelectedWork.OwnerTaskID != "" || out.SelectedWork.RunID != "inspection-run" || out.SelectedWork.Status != "RUNNING" {
		t.Fatalf("selected inspection=%+v", out.SelectedWork)
	}
}

func TestFeatureAreaPresentationTransportParityAndPureRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service := &presentationOnlyService{projection: presentation.Projection{SchemaVersion: "1", FeatureArea: presentation.Identity{ID: "area", Name: "Persistent FeatureArea"}, DevelopmentContext: presentation.DevelopmentContext{Project: presentation.Identity{ID: "project", Name: "Bridge"}, Repositories: []presentation.RepositoryContext{}}, LifecycleStatus: "ACTIVE", WorkflowStatus: "UNKNOWN", CurrentPhase: "IMPLEMENTATION", Progress: []presentation.PhaseProgress{{Phase: "IMPLEMENTATION", Status: "UNKNOWN"}}, PendingDecision: &presentation.Decision{ID: "execution", Kind: "EXECUTION_AUTHORIZATION", Summary: "Authorization required", AllowedActions: []string{"AUTHORIZE"}}, Recovery: &presentation.Recovery{Status: "RECOVERY_REQUIRED", Required: true, Summary: "Recovery required"}, Capabilities: []presentation.Capability{{Action: "REFRESH", Available: true}, {Action: "AUTHORIZE", Available: false}}, Blockers: []string{}, Details: presentation.Details{View: "FEATURE_AREA_DETAILS", ProjectID: "project", FeatureAreaID: "area"}, UpdatedAt: time.Unix(1, 0).UTC()}}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "presentation", Version: "1"}, nil)
	RegisterFeatureAreaPresentation(server, service)
	RegisterDevelopmentContextCard(server)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 7 {
		t.Fatalf("unexpected tools: %+v", listed.Tools)
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("not read-only: %+v", tool)
		}
	}
	for _, sessionID := range []string{"session", "session", "fresh"} {
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "feature_area_presentation_get", Arguments: map[string]any{"aiSessionId": sessionID, "featureAreaId": "area"}})
		if err != nil || result.IsError {
			t.Fatalf("read=%+v err=%v", result, err)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var got presentation.Projection
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, service.projection) {
			t.Fatalf("structured parity lost: %+v", got)
		}
		if len(result.Content) != 1 || result.Content[0].(*sdkmcp.TextContent).Text != presentation.RenderText(got) {
			t.Fatalf("text parity lost: %+v", result.Content)
		}
		if result.Meta["aiSessionId"] != sessionID {
			t.Fatalf("remount metadata lost: %+v", result.Meta)
		}
	}
	contextResult, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "development_context_presentation_get", Arguments: map[string]any{"aiSessionId": "session"}})
	if err != nil || contextResult.IsError {
		t.Fatalf("context read=%+v err=%v", contextResult, err)
	}
	if service.reads != 4 {
		t.Fatalf("unexpected read count=%d", service.reads)
	}
	bad, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "feature_area_presentation_get", Arguments: map[string]any{"aiSessionId": "foreign", "featureAreaId": "area"}})
	if err == nil && !bad.IsError {
		t.Fatal("foreign identity became success")
	}
	resource, err := session.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: featureAreaResourceURI})
	if err != nil || len(resource.Contents) != 1 {
		t.Fatalf("resource=%+v err=%v", resource, err)
	}
	if !strings.Contains(resource.Contents[0].Text, "dev_bridge_card_get") || strings.Contains(resource.Contents[0].Text, "ui_workflow_state_get") {
		t.Fatal("Card uses the wrong Refresh contract")
	}
}

func TestExternalPresentationAgentRuntimeMetadataContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Date(2026, time.September, 11, 8, 0, 0, 0, time.UTC)
	longMarker := strings.Repeat("presentation-metadata-marker-", 2048)
	commandMetadata, err := json.Marshal(map[string]any{"id": "command-1", "type": "commandExecution", "command": "go test ./...", "aggregatedOutput": longMarker})
	if err != nil {
		t.Fatal(err)
	}
	contextItem := agentruntime.Item{ID: "compact-1", Type: "contextCompaction", Status: "completed", Metadata: json.RawMessage(`{"id":"compact-1","type":"contextCompaction"}`)}
	snapshot := &agentruntime.Snapshot{
		Agent:           agentruntime.Agent{Name: "codex", SessionName: "presentation", ProcessAvailability: "AVAILABLE"},
		AgentHealth:     "ACTIVE",
		Identity:        agentruntime.Identity{ProjectID: "project", FeatureAreaID: "area", TaskID: "task-running", RunID: "run", ThreadID: "thread", TurnID: "turn", ConnectionID: "connection"},
		Availability:    "AVAILABLE",
		Sequence:        5,
		UpdatedAt:       now,
		LastActivityAt:  now,
		CurrentItem:     &contextItem,
		WaitingState:    "RUNNING",
		ConnectionState: "CONNECTED",
		Items: []agentruntime.Item{
			contextItem,
			{ID: "command-1", Type: "commandExecution", Status: "completed", Metadata: commandMetadata},
			{ID: "message-1", Type: "agentMessage", Status: "completed", Metadata: json.RawMessage(`{"id":"message-1","type":"agentMessage","text":"done"}`)},
			{ID: "array-1", Type: "futureArray", Status: "completed", Metadata: json.RawMessage(`[{"future":"value"}]`)},
			{ID: "null-1", Type: "futureNull", Status: "completed", Metadata: json.RawMessage(`null`)},
		},
	}
	snapshot.Requests = []agentruntime.Request{runtimePayloadRequest()}
	snapshot.Requests[0].Identity = snapshot.Identity
	diagnostic := agentruntime.Diagnostic{Method: "error", At: now, Summary: "fixture", Details: json.RawMessage(`{"message":"fixture diagnostic"}`)}
	snapshot.LastError, snapshot.LastWarning = &diagnostic, &diagnostic
	snapshot.Diagnostics = []agentruntime.Diagnostic{diagnostic}
	taskView := presentation.TaskView{ID: "task-running", Kind: "DEVELOPMENT", Status: "RUNNING", Activity: presentation.TaskActivity{Status: "RUNNING", Runtime: snapshot}, Progress: []presentation.PhaseProgress{}, Goals: []presentation.GoalProgress{}, Actions: []presentation.Capability{}, Details: presentation.TaskDiagnostics{Runs: []presentation.TaskRunDetail{}}, UpdatedAt: now}
	latestView := taskView
	latestView.ID = "task-latest"
	latestView.Status = "SUCCEEDED"
	latestView.Terminal = true
	latestView.ReadOnly = true
	latestView.Activity.Status = "SUCCEEDED"
	idleProjection := presentation.Projection{SchemaVersion: "1", FeatureArea: presentation.Identity{ID: "area", Name: "Area"}, DevelopmentContext: presentation.DevelopmentContext{Project: presentation.Identity{ID: "project", Name: "Project"}, Repositories: []presentation.RepositoryContext{}}, LifecycleStatus: "ACTIVE", WorkflowStatus: "FEATURE_AREA_IDLE", CurrentState: "FEATURE_AREA_IDLE", LatestTaskView: &latestView, Progress: []presentation.PhaseProgress{}, Blockers: []string{}, Capabilities: []presentation.Capability{}, Details: presentation.Details{FeatureAreaID: "area"}, UpdatedAt: now}
	runningProjection := idleProjection
	runningProjection.CurrentState = "IMPLEMENTATION_RUNNING"
	runningProjection.WorkflowStatus = "IMPLEMENTATION_RUNNING"
	runningProjection.LatestTaskView = nil
	runningProjection.TaskView = &taskView
	runningProjection.Details.TaskID = taskView.ID
	service := &presentationOnlyService{
		projection:     idleProjection,
		taskProjection: runningProjection,
		areas:          []featurearea.FeatureArea{{ID: "area", Title: "Area", Status: featurearea.StatusActive}},
		tasks:          []task.Task{{ID: taskView.ID, FeatureAreaID: "area", ProblemStatement: "Running Task", Status: task.StatusRunning, UpdatedAt: now}},
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "presentation-runtime-contract", Version: "1"}, nil)
	RegisterSurfacePresentationTools(server, service)
	server.AddReceivingMiddleware(presentationResultMiddleware(service))
	RegisterFeatureAreaPresentation(server, service)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "presentation-runtime-contract", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*sdkmcp.Tool{}
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"feature_area_presentation_get", "workspace_show", "task_presentation_get", "dev_bridge_card_get"} {
		if tools[name] == nil || tools[name].OutputSchema == nil {
			t.Fatalf("%s has no advertised output schema", name)
		}
	}

	projectionSchema := schemaMap(t, tools["feature_area_presentation_get"].OutputSchema)
	taskSchema := schemaMap(t, tools["task_presentation_get"].OutputSchema)
	if !reflect.DeepEqual(projectionSchema, taskSchema) {
		t.Fatal("FeatureArea and Task reads do not share one normalized Projection schema")
	}
	assertPresentationMetadataSchema(t, projectionSchema, "taskView")
	assertPresentationMetadataSchema(t, projectionSchema, "latestTaskView")
	workspaceSchema := schemaMap(t, tools["dev_bridge_card_get"].OutputSchema)
	if alternatives, ok := workspaceSchema["oneOf"].([]any); ok {
		workspaceSchema = alternatives[0].(map[string]any)
	}
	assertPresentationMetadataSchema(t, workspaceSchema["properties"].(map[string]any)["presentation"].(map[string]any), "taskView")
	assertPresentationMetadataSchema(t, workspaceSchema["properties"].(map[string]any)["presentation"].(map[string]any), "latestTaskView")
	showSchema := schemaMap(t, tools["workspace_show"].OutputSchema)
	if showSchema["properties"].(map[string]any)["result"].(map[string]any)["properties"].(map[string]any)["workspace"] != nil {
		t.Fatal("model show result duplicates full UI payload")
	}

	calls := []struct {
		name      string
		arguments map[string]any
	}{
		{"feature_area_presentation_get", map[string]any{"aiSessionId": "session", "featureAreaId": "area"}},
		{"workspace_show", map[string]any{"aiSessionId": "session", "featureAreaId": "area", "presentationRequestId": "runtime-contract"}},
		{"task_presentation_get", map[string]any{"aiSessionId": "session", "featureAreaId": "area", "taskId": "task-running"}},
		{"dev_bridge_card_get", map[string]any{"aiSessionId": "session", "featureAreaId": "area"}},
		{"dev_bridge_card_get", map[string]any{"aiSessionId": "session", "featureAreaId": "area", "taskId": "task-running"}},
	}
	for _, call := range calls {
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: call.name, Arguments: call.arguments})
		if err != nil || result.IsError {
			t.Fatalf("%s rejected presentation metadata: result=%+v err=%v", call.name, result, err)
		}
		resolved := resolveSchema(t, tools[call.name].OutputSchema)
		structured := jsonValue(t, result.StructuredContent)
		if err := resolved.Validate(structured); err != nil {
			t.Fatalf("%s output fails its Host-compatible advertised schema: %v", call.name, err)
		}
		if call.name == "workspace_show" {
			structured = jsonValue(t, result.Meta["workspace"])
			if err := resolveSchema(t, tools["dev_bridge_card_get"].OutputSchema).Validate(structured); err != nil {
				t.Fatalf("workspace_show private workspace violates Card contract: %v", err)
			}
		}
		encoded, _ := json.Marshal(structured)
		if !strings.Contains(string(encoded), "presentation-metadata-marker-") {
			t.Fatalf("%s lost long command metadata", call.name)
		}
		assertStructuredMetadataValues(t, structured)
		assertRuntimeRequestPreserved(t, structured, snapshot.Requests[0])
	}

	for _, kind := range []string{"REVIEW", "INSPECTION"} {
		reviewView := taskView
		reviewView.ID = strings.ToLower(kind) + "-task"
		reviewView.Kind = kind
		reviewProjection := runningProjection
		reviewProjection.TaskView = &reviewView
		reviewProjection.Details.TaskID = reviewView.ID
		service.taskProjection = reviewProjection
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "task_presentation_get", Arguments: map[string]any{"aiSessionId": "session", "featureAreaId": "area", "taskId": reviewView.ID}})
		if err != nil || result.IsError {
			t.Fatalf("%s Task runtime rejected: result=%+v err=%v", kind, result, err)
		}
	}

	assertPresentationMetadataMatrix(t, tools["feature_area_presentation_get"].OutputSchema, idleProjection, "latestTaskView")
	assertPresentationMetadataMatrix(t, tools["task_presentation_get"].OutputSchema, runningProjection, "taskView")
	workspace := presentation.WorkspaceProjection{SchemaVersion: "1", DevelopmentContext: idleProjection.DevelopmentContext, FeatureAreas: []presentation.FeatureAreaNavigationItem{}, Tasks: []presentation.TaskNavigationItem{}, Presentation: &runningProjection}
	assertWorkspaceMetadataMatrix(t, tools["dev_bridge_card_get"].OutputSchema, workspace, "taskView")
	workspace.Presentation = &idleProjection
	assertWorkspaceMetadataMatrix(t, tools["dev_bridge_card_get"].OutputSchema, workspace, "latestTaskView")
	assertOnlyPresentationMetadataSchemaChanged(t)
}

func schemaMap(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func resolveSchema(t *testing.T, value any) *jsonschema.Resolved {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func jsonValue(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func presentationItemSchemas(t *testing.T, root map[string]any, view string) (map[string]any, map[string]any) {
	t.Helper()
	properties := root["properties"].(map[string]any)
	viewSchema := properties[view].(map[string]any)
	activity := viewSchema["properties"].(map[string]any)["activity"].(map[string]any)
	runtime := activity["properties"].(map[string]any)["runtime"].(map[string]any)
	runtimeProperties := runtime["properties"].(map[string]any)
	currentItem := runtimeProperties["currentItem"].(map[string]any)
	itemsItem := runtimeProperties["items"].(map[string]any)["items"].(map[string]any)
	return currentItem, itemsItem
}

func assertPresentationMetadataSchema(t *testing.T, root map[string]any, view string) {
	t.Helper()
	currentItem, itemsItem := presentationItemSchemas(t, root, view)
	for path, item := range map[string]map[string]any{"currentItem": currentItem, "items[]": itemsItem} {
		metadata := item["properties"].(map[string]any)["metadata"].(map[string]any)
		if !reflect.DeepEqual(metadata["type"], []any{"object", "array", "null"}) || metadata["additionalProperties"] != true {
			t.Fatalf("%s.%s metadata schema = %#v", view, path, metadata)
		}
		if _, found := metadata["items"]; found {
			t.Fatalf("%s.%s metadata retained byte items: %#v", view, path, metadata)
		}
		for _, name := range item["required"].([]any) {
			if name == "metadata" {
				t.Fatalf("%s.%s metadata remains required", view, path)
			}
		}
	}
}

func assertStructuredMetadataValues(t *testing.T, root map[string]any) {
	t.Helper()
	projection := root
	if nested, ok := root["presentation"].(map[string]any); ok {
		projection = nested
	}
	view, ok := projection["taskView"].(map[string]any)
	if !ok {
		view = projection["latestTaskView"].(map[string]any)
	}
	runtime := view["activity"].(map[string]any)["runtime"].(map[string]any)
	current := runtime["currentItem"].(map[string]any)["metadata"].(map[string]any)
	if !reflect.DeepEqual(current, map[string]any{"id": "compact-1", "type": "contextCompaction"}) {
		t.Fatalf("contextCompaction metadata changed type or value: %#v", current)
	}
	items := runtime["items"].([]any)
	if _, ok := items[1].(map[string]any)["metadata"].(map[string]any); !ok {
		t.Fatal("commandExecution metadata is not an object")
	}
	if _, ok := items[2].(map[string]any)["metadata"].(map[string]any); !ok {
		t.Fatal("agentMessage metadata is not an object")
	}
	if _, ok := items[3].(map[string]any)["metadata"].([]any); !ok {
		t.Fatal("array metadata is not an array")
	}
	if items[4].(map[string]any)["metadata"] != nil {
		t.Fatal("null metadata is not null")
	}
}

func assertPresentationMetadataMatrix(t *testing.T, schema any, value presentation.Projection, view string) {
	t.Helper()
	assertMetadataMatrix(t, resolveSchema(t, schema), jsonValue(t, value), view)
}

func assertWorkspaceMetadataMatrix(t *testing.T, schema any, value presentation.WorkspaceProjection, view string) {
	t.Helper()
	root := jsonValue(t, value)
	assertMetadataMatrix(t, resolveSchema(t, schema), root, "presentation", view)
}

func assertMetadataMatrix(t *testing.T, schema *jsonschema.Resolved, original map[string]any, path ...string) {
	t.Helper()
	cases := []struct {
		name    string
		value   any
		omitted bool
		valid   bool
	}{
		{"object", map[string]any{"future": "value"}, false, true},
		{"array", []any{map[string]any{"future": "value"}}, false, true},
		{"null", nil, false, true},
		{"omitted", nil, true, true},
		{"string", "scalar", false, false},
		{"number", float64(7), false, false},
		{"boolean", true, false, false},
	}
	for _, itemPath := range []string{"currentItem", "items[]"} {
		for _, test := range cases {
			encoded, _ := json.Marshal(original)
			var candidate map[string]any
			_ = json.Unmarshal(encoded, &candidate)
			current := any(candidate)
			for _, name := range path {
				current = current.(map[string]any)[name]
			}
			runtime := current.(map[string]any)["activity"].(map[string]any)["runtime"].(map[string]any)
			var item map[string]any
			if itemPath == "currentItem" {
				item = runtime["currentItem"].(map[string]any)
			} else {
				item = runtime["items"].([]any)[0].(map[string]any)
			}
			if test.omitted {
				delete(item, "metadata")
			} else {
				item["metadata"] = test.value
			}
			err := schema.Validate(candidate)
			if test.valid && err != nil {
				t.Fatalf("valid %s metadata rejected at %s.%s: %v", test.name, strings.Join(path, "."), itemPath, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("scalar %s metadata accepted at %s.%s", test.name, strings.Join(path, "."), itemPath)
			}
		}
	}
}

func assertOnlyPresentationMetadataSchemaChanged(t *testing.T) {
	t.Helper()
	inferred, err := jsonschema.For[presentation.Projection](&jsonschema.ForOptions{TypeSchemas: actionTargetSchemaTypes()})
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyMetadataSchemaChanged(t, inferred, mustProjectionOutputSchema(), projectionRuntimePaths)
	workspaceInferred, err := jsonschema.For[presentation.WorkspaceProjection](&jsonschema.ForOptions{TypeSchemas: actionTargetSchemaTypes()})
	if err != nil {
		t.Fatal(err)
	}
	workspacePaths := make([][]string, 0, len(projectionRuntimePaths))
	for _, path := range projectionRuntimePaths {
		workspacePaths = append(workspacePaths, append([]string{"presentation"}, path...))
	}
	assertOnlyMetadataSchemaChanged(t, workspaceInferred, mustWorkspaceProjectionOutputSchema(), workspacePaths)
}

func assertOnlyMetadataSchemaChanged(t *testing.T, inferred, normalized *jsonschema.Schema, paths [][]string) {
	t.Helper()
	for _, path := range paths {
		inferredRuntime, err := schemaProperty(inferred, path)
		if err != nil {
			t.Fatal(err)
		}
		normalizedRuntime, err := schemaProperty(normalized, path)
		if err != nil {
			t.Fatal(err)
		}
		for _, pair := range [][2]*jsonschema.Schema{{inferredRuntime.Properties["currentItem"], normalizedRuntime.Properties["currentItem"]}, {inferredRuntime.Properties["items"].Items, normalizedRuntime.Properties["items"].Items}} {
			pair[0].Properties["metadata"] = pair[1].Properties["metadata"]
			pair[0].Required = append([]string(nil), pair[1].Required...)
		}
	}
	maskAuditedRawSchemaChanges(t, inferred, normalized)
	if !reflect.DeepEqual(inferred, normalized) {
		t.Fatal("presentation schema changed outside audited RawMessage leaves")
	}
}

func TestDevelopmentContextPreSessionShowIsPureAndExplicit(t *testing.T) {
	ctx := context.Background()
	service := &presentationOnlyService{projection: presentation.Projection{DevelopmentContext: presentation.DevelopmentContext{Project: presentation.Identity{ID: "project", Name: "Project"}, Repositories: []presentation.RepositoryContext{}}}}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "pre-session", Version: "1"}, nil)
	server.AddReceivingMiddleware(presentationResultMiddleware(service))
	RegisterFeatureAreaPresentation(server, service)
	RegisterSurfacePresentationTools(server, service)
	RegisterDevelopmentContextCard(server)
	cs := connectPresentationTest(t, ctx, server)
	out, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "project_selection_show", Arguments: map[string]any{"presentationRequestId": "select-one"}})
	if err != nil || !out.IsError || service.reads != 0 {
		t.Fatalf("bootstrap invented binding: %+v %v", out, err)
	}
	for _, args := range []map[string]any{{}, {"presentationRequestId": "missing"}, {"presentationRequestId": "blank", "aiSessionId": " "}, {"presentationRequestId": "invalid", "aiSessionId": "absent"}, {"presentationRequestId": "conflict", "aiSessionId": "session", "projectId": "other"}} {
		bad, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_show", Arguments: args})
		if err == nil && !bad.IsError {
			t.Fatalf("strict workspace accepted %+v", args)
		}
	}
	for _, id := range []string{"first", "second"} {
		bound, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_show", Arguments: map[string]any{"aiSessionId": "session", "presentationRequestId": id}})
		if err != nil || bound.IsError || bound.Meta["aiSessionId"] != "session" || bound.Meta["projectId"] != "project" || bound.Meta["display"] != "COLLAPSED" || bound.Meta["emission"] != "MAIN" {
			t.Fatalf("bound entry failed: %+v %v", bound, err)
		}
	}
}

func maskAuditedRawSchemaChanges(t *testing.T, inferred, normalized *jsonschema.Schema) {
	t.Helper()
	if inferred == nil || normalized == nil {
		return
	}
	for name, child := range inferred.Properties {
		next := normalized.Properties[name]
		switch name {
		case "requestId", "payload", "resolution", "responseSchema", "details", "permissions", "runtimeApprovalRequest":
			if slices.Equal(child.Types, []string{"null", "array"}) && child.Items != nil && child.Items.Type == "integer" {
				inferred.Properties[name] = next
				continue
			}
		case "availableResponses":
			if child.Items == nil || child.Items.Items == nil || child.Items.Items.Type != "integer" {
				t.Fatal("unexpected availableResponses inference")
			}
			inferred.Properties[name] = next
			continue
		}
		maskAuditedRawSchemaChanges(t, child, next)
	}
	maskAuditedRawSchemaChanges(t, inferred.Items, normalized.Items)
}
