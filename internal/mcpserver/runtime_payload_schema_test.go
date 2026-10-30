package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func runtimePayloadRequest() agentruntime.Request {
	return agentruntime.Request{
		Identity:  agentruntime.Identity{ProjectID: "project", FeatureAreaID: "area", TaskID: "task", RunID: "run", ThreadID: "thread", TurnID: "turn", ConnectionID: "connection"},
		RequestID: json.RawMessage(`"request-approval"`), ItemID: "item", Method: "item/commandExecution/requestApproval", WaitingState: "WAITING_FOR_COMMAND_APPROVAL", State: "PENDING", CreatedAt: time.Unix(1, 0).UTC(),
		Payload:            json.RawMessage(`{"startedAtMs":1,"threadId":"thread","turnId":"turn","itemId":"item","availableDecisions":["accept","cancel"],"command":"go test ./...","cwd":"/tmp/fixture","commandActions":[{"type":"unknown","command":"go test ./..."}]}`),
		AvailableResponses: []json.RawMessage{json.RawMessage(`{"decision":"accept"}`), json.RawMessage(`{"decision":"cancel"}`)},
	}
}

func assertRuntimeRequestPreserved(t *testing.T, value any, want agentruntime.Request) {
	t.Helper()
	expected := jsonValue(t, want)
	count := 0
	var visit func(any)
	visit = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if _, ok := v["payload"]; ok && v["requestId"] == expected["requestId"] {
				count++
				actual := make(map[string]any, len(v)-1)
				for key, value := range v {
					if key != "projection" {
						actual[key] = value
					}
				}
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("runtime request changed: got %#v want %#v", v, expected)
				}
			}
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(value)
	if count == 0 {
		t.Fatal("runtime request disappeared")
	}
}

func TestRuntimePayloadSchemaContract(t *testing.T) {
	request := runtimePayloadRequest()
	if err := agentruntime.Validate(request.Method, "request", request.Payload); err != nil {
		t.Fatal(err)
	}
	schema := mustWorkspaceProjectionOutputSchema()
	runtime, err := schemaProperty(schema, []string{"presentation", "taskView", "activity", "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	var manual jsonschema.Schema
	if err := json.Unmarshal([]byte(agentRuntimeOutputSchema), &manual); err != nil {
		t.Fatal(err)
	}
	for _, req := range []*jsonschema.Schema{runtime.Properties["requests"].Items, schema.Properties["pendingDecisions"].Items.Properties["runtimeRequest"], manual.Properties["requests"].Items} {
		for _, name := range []string{"requestId", "payload", "availableResponses", "resolution"} {
			if !reflect.DeepEqual(req.Properties[name], manual.Properties["requests"].Items.Properties[name]) {
				t.Fatalf("%s contract differs", name)
			}
		}
		resolved, err := req.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(jsonValue(t, request)); err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{`[]`, `null`, `"text"`, `42`, `true`} {
			invalid := request
			invalid.Payload = json.RawMessage(raw)
			if resolved.Validate(jsonValue(t, invalid)) == nil {
				t.Fatalf("accepted non-object Params %s", raw)
			}
		}
		for _, id := range []string{`"request-approval"`, `17`} {
			completed := request
			completed.RequestID = json.RawMessage(id)
			completed.State = "RESOLVED"
			completed.Resolution = json.RawMessage(`{"decision":"cancel"}`)
			if err := resolved.Validate(jsonValue(t, completed)); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot := agentruntime.Snapshot{Identity: request.Identity, Requests: []agentruntime.Request{request}, Availability: "AVAILABLE", WaitingState: request.WaitingState}
	if err := resolveSchema(t, json.RawMessage(SurfaceRuntimeRequestOutputSchema)).Validate(jsonValue(t, map[string]any{"kind": "CODEX_REQUEST", "guarantee": "EXACT_RUNTIME_REQUEST_STATE", "runtime": snapshot})); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []*jsonschema.Schema{{Type: "object"}, {Types: []string{"null", "array"}, Items: &jsonschema.Schema{Type: "string"}}} {
		if requireRawMessageInference(changed) == nil {
			t.Fatal("generator change did not fail fast")
		}
	}
}

func TestRuntimePayloadSurfaceActualSDK(t *testing.T) {
	f := &runtimePayloadSurfaceFixture{surfaceReadFixture: newSurfaceReadFixture()}
	request := runtimePayloadRequest()
	snapshot := &agentruntime.Snapshot{Identity: request.Identity, Availability: "AVAILABLE", Requests: []agentruntime.Request{request}, WaitingState: request.WaitingState}
	f.workflow.TaskView = &p.TaskView{ID: "task", Activity: p.TaskActivity{Runtime: snapshot}}
	f.review = review.Review{ID: "review", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", RunID: "run"}
	f.run = run.Run{ID: "run", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", ReviewID: "review", OperationKind: run.OperationReview, OperationID: "review", AgentRuntime: snapshot}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "payload-contract", Version: "1"}, nil)
	RegisterSurfaceBusinessTools(server, f)
	client := connectPresentationTest(t, context.Background(), server)
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]any{}
	for _, tool := range listed.Tools {
		schemas[tool.Name] = tool.OutputSchema
	}
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"workspace_get", map[string]any{"aiSessionId": "session", "taskId": "task", "include": []string{"TASK", "NAVIGATION"}}},
		{"operation_get", map[string]any{"aiSessionId": "session", "target": map[string]any{"kind": "REVIEW", "operationId": "review", "ownerTaskId": "task", "runId": "run"}, "include": []string{"RUNTIME_REQUESTS", "ACTIVITY"}}},
	} {
		result, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil || result.IsError {
			t.Fatalf("%s: %+v %v", call.name, result, err)
		}
		value := jsonValue(t, result.StructuredContent)
		if err := resolveSchema(t, schemas[call.name]).Validate(value); err != nil {
			t.Fatal(err)
		}
		assertRuntimeRequestPreserved(t, value, request)
	}
	if snapshot.Requests[0].State != "PENDING" {
		t.Fatal("read resolved request")
	}
}

type runtimePayloadSurfaceFixture struct{ *surfaceReadFixture }

func (f *runtimePayloadSurfaceFixture) TaskPresentationForSession(context.Context, string, string, string) (p.Projection, error) {
	return f.workflow, nil
}
func (f *runtimePayloadSurfaceFixture) WorkspaceRuntimeDecisionsForSession(context.Context, string, string) ([]p.PendingDecision, error) {
	return []p.PendingDecision{{Kind: "CODEX_REQUEST", RuntimeRequest: &f.run.AgentRuntime.Requests[0]}}, nil
}
func (f *runtimePayloadSurfaceFixture) DevelopmentWorkspaceForSession(context.Context, string, string, string) (p.WorkspaceProjection, error) {
	decisions, _ := f.WorkspaceRuntimeDecisionsForSession(context.Background(), "", "")
	return p.WorkspaceProjection{Presentation: &f.workflow, PendingDecisions: decisions}, nil
}

func TestPresentationApprovalRawJSONAudit(t *testing.T) {
	schema := mustWorkspaceProjectionOutputSchema()
	for _, raw := range []string{`{"network":{"enabled":true}}`, `[1,{"nested":null}]`, `null`, `"text"`, `42`, `true`} {
		decision := p.Decision{Permissions: json.RawMessage(raw)}
		workspace := p.WorkspaceProjection{PendingDecisions: []p.PendingDecision{{RuntimeApprovalRequest: json.RawMessage(raw), Decision: &decision}}, Presentation: &p.Projection{PendingDecision: &decision}}
		if err := resolveSchema(t, schema).Validate(jsonValue(t, workspace)); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}
