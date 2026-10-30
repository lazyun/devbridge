package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
)

func TestOnlyExplicitShowToolsEmitMainCards(t *testing.T) {
	for _, name := range []string{"workspace_show", "project_selection_show"} {
		if primaryCardResource(name) != devBridgeCardResourceURI || presentationPolicy(name) != emissionMain {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"project_list", "project_open", "workspace_get", "work_list", "operation_get", "task_create", "decision_respond", "operation_cancel", "runtime_request_respond", "dev_bridge_card_get", "dev_bridge_review_wakeup_claim", "feature_area_card_show", "development_context_card_show", "feature_area_details_get"} {
		if primaryCardResource(name) != "" || presentationPolicy(name) != emissionSilent {
			t.Fatalf("%s emits UI", name)
		}
	}
	if presentationPolicy("details_show") != emissionDetails {
		t.Fatal("details routing")
	}
}

type workspaceRoutingService struct {
	*presentationOnlyService
	workspace presentation.WorkspaceProjection
	fail      bool
	reads     int
}

func (s *workspaceRoutingService) DevelopmentWorkspaceForSession(context.Context, string, string, string) (presentation.WorkspaceProjection, error) {
	s.reads++
	if s.fail {
		return presentation.WorkspaceProjection{}, fmt.Errorf("refresh failed")
	}
	return s.workspace, nil
}

func TestProductionRoutingPreservesCommittedResultWithoutImplicitFullRead(t *testing.T) {
	ctx := context.Background()
	svc := &workspaceRoutingService{presentationOnlyService: &presentationOnlyService{}, fail: true}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "routing", Version: "1"}, nil)
	server.AddReceivingMiddleware(presentationResultMiddleware(svc))
	addExternalTool(server, "task_create", "Create Task", "fixture", `{"type":"object","properties":{},"additionalProperties":false}`, `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`, nil, func(context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, struct {
		ID string `json:"id"`
	}, error) { return nil, struct {
		ID string `json:"id"`
	}{"committed-task"}, nil })
	cs := connectPresentationTest(t, ctx, server)
	out, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "task_create", Arguments: map[string]any{}})
	if err != nil || out.IsError {
		t.Fatalf("%+v %v", out, err)
	}
	raw, _ := json.Marshal(out.StructuredContent)
	if string(raw) != `{"id":"committed-task"}` || svc.reads != 0 || out.Meta["emission"] != "SILENT" || out.Meta["openai/outputTemplate"] != nil {
		t.Fatalf("mutation receipt changed or implicit full read: %s %+v reads=%d", raw, out.Meta, svc.reads)
	}
}
func connectPresentationTest(t *testing.T, ctx context.Context, server *sdkmcp.Server) *sdkmcp.ClientSession {
	t.Helper()
	a, b := sdkmcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "presentation-test", Version: "1"}, nil).Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}
