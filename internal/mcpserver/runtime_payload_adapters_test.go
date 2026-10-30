package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type payloadAdapterService struct{ *agentRuntimeContractService }

func (s *payloadAdapterService) GetFeatureArea(context.Context, string, string) (featurearea.FeatureArea, error) {
	return featurearea.FeatureArea{ID: "area-1", ProjectID: "project-1", Title: "Area"}, nil
}
func (s *payloadAdapterService) GetTask(context.Context, string, string) (task.Task, error) {
	return task.Task{ID: "task-1", ProjectID: "project-1", FeatureAreaID: "area-1", Title: "Task"}, nil
}

func (s *payloadAdapterService) CompactWorkspaceForSession(context.Context, string, string, string) (p.Projection, error) {
	return p.Projection{FeatureArea: p.Identity{ID: "area-1"}, Details: p.Details{ProjectID: "project-1", FeatureAreaID: "area-1", TaskID: "task-1", RunID: "run-1"}}, nil
}
func (s *payloadAdapterService) DevelopmentWorkspaceForSession(context.Context, string, string, string) (p.WorkspaceProjection, error) {
	projection, _ := s.CompactWorkspaceForSession(context.Background(), "", "", "")
	return p.WorkspaceProjection{
		SchemaVersion: "1", DevelopmentContext: p.DevelopmentContext{Project: p.Identity{ID: "project-1"}},
		SelectedFeatureAreaID: "area-1", SelectedTaskID: "task-1", Presentation: &projection,
		PendingDecisions: []p.PendingDecision{{Kind: "CODEX_REQUEST", RuntimeRequest: &s.snapshot.Requests[0]}},
		FeatureAreas:     []p.FeatureAreaNavigationItem{}, Tasks: []p.TaskNavigationItem{},
	}, nil
}
func (s *payloadAdapterService) WorkspaceRuntimeDecisionsForSession(context.Context, string, string) ([]p.PendingDecision, error) {
	return []p.PendingDecision{{Kind: "CODEX_REQUEST", RuntimeRequest: &s.snapshot.Requests[0]}}, nil
}

func TestRuntimePayloadProductionAdapters(t *testing.T) {
	request := agentruntime.Request{Identity: agentruntime.Identity{ProjectID: "project-1", FeatureAreaID: "area-1", TaskID: "task-1", RunID: "run-1", ThreadID: "thread-1", TurnID: "turn-1", ConnectionID: "connection-1"}, RequestID: json.RawMessage(`17`), ItemID: "item-1", Method: "item/commandExecution/requestApproval", State: "PENDING", WaitingState: "WAITING_FOR_COMMAND_APPROVAL", CreatedAt: time.Unix(1, 0).UTC(), Payload: json.RawMessage(`{"startedAtMs":1,"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"go test ./...","cwd":"/tmp/fixture","availableDecisions":["accept","cancel"],"commandActions":[]}`), AvailableResponses: []json.RawMessage{json.RawMessage(`{"decision":"accept"}`), json.RawMessage(`{"decision":"cancel"}`)}}
	if err := agentruntime.Validate(request.Method, "request", request.Payload); err != nil {
		t.Fatal(err)
	}
	service := &payloadAdapterService{&agentRuntimeContractService{BridgeService: readyService(t), snapshot: agentruntime.Snapshot{Identity: request.Identity, Requests: []agentruntime.Request{request}, Availability: "AVAILABLE", WaitingState: request.WaitingState}}}
	gpt, err := mcpserver.New(mcpserver.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	local, err := localmcp.New(localmcp.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	for name, handler := range map[string]http.Handler{"gpt": gpt.Handler(), "local": local.Handler()} {
		t.Run(name, func(t *testing.T) {
			host := httptest.NewServer(handler)
			defer host.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "payload-test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL + "/mcp", DisableStandaloneSSE: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			list, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			schemas := map[string]any{}
			for _, tool := range list.Tools {
				schemas[tool.Name] = tool.OutputSchema
			}
			for _, call := range []struct {
				name string
				args map[string]any
			}{
				{"workspace_get", map[string]any{"aiSessionId": "session-1", "featureAreaId": "area-1", "taskId": "task-1"}},
				{"operation_get", map[string]any{"aiSessionId": "session-1", "target": map[string]any{"kind": "EXECUTION", "operationId": "execution-1", "ownerTaskId": "task-1", "runId": "run-1"}, "include": []string{"RUNTIME_REQUESTS"}}},
			} {
				result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: call.name, Arguments: call.args})
				if err != nil || result.IsError {
					content, _ := json.Marshal(result.Content)
					t.Fatalf("%s: %s %v", call.name, content, err)
				}
				raw, _ := json.Marshal(schemas[call.name])
				var schema jsonschema.Schema
				if err := json.Unmarshal(raw, &schema); err != nil {
					t.Fatal(err)
				}
				resolved, err := schema.Resolve(nil)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ = json.Marshal(result.StructuredContent)
				var value map[string]any
				_ = json.Unmarshal(raw, &value)
				if err := resolved.Validate(value); err != nil {
					t.Fatal(err)
				}
				var actual any
				if call.name == "workspace_get" {
					actual = value["workflow"].(map[string]any)["pendingDecisions"].([]any)[0].(map[string]any)["runtimeRequest"]
				} else {
					actual = value["result"].(map[string]any)["runtimeRequests"].([]any)[0]
				}
				if requestValue, ok := actual.(map[string]any); ok {
					delete(requestValue, "projection")
				}
				raw, _ = json.Marshal(request)
				var expected any
				_ = json.Unmarshal(raw, &expected)
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("%s changed request: %#v", call.name, actual)
				}
			}
			if service.snapshot.Requests[0].State != "PENDING" {
				t.Fatal("read approved runtime request")
			}
		})
	}
}
