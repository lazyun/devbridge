package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWorkflowGuidancePreservesCompleteCoreSnapshot(t *testing.T) {
	for _, name := range []string{"workflow_guidance_get", "review_approval_respond"} {
		t.Run(name, func(t *testing.T) {
			g := wp.Guidance{Status: "SUCCESS", ProjectID: "project", FeatureAreaID: "area", Generation: "atomic-snapshot", ExecutionID: "execution-exact", TaskID: "task-exact"}
			g.Set("EXECUTION_READY", "START_EXECUTION", "Start the approved Execution.")
			next := func(context.Context, string, sdkmcp.Request) (sdkmcp.Result, error) {
				return &sdkmcp.CallToolResult{StructuredContent: g}, nil
			}
			request := &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(`{"aiSessionId":"session","featureAreaId":"area"}`)}}
			// A second read is unavailable. The already complete Core result must remain usable.
			result, err := WorkflowGuidanceMiddleware(&StaticService{})(next)(context.Background(), "tools/call", request)
			if err != nil {
				t.Fatal(err)
			}
			out := result.(*sdkmcp.CallToolResult).StructuredContent.(map[string]any)
			snapshot, ok := out["workflowGuidance"].(wp.Guidance)
			if !ok || snapshot.Generation != g.Generation || snapshot.ExecutionID != g.ExecutionID || snapshot.RefreshRequired || out["executionId"] != snapshot.ExecutionID || out["currentState"] != snapshot.CurrentState {
				t.Fatalf("mixed snapshot=%+v", out)
			}
		})
	}
}
