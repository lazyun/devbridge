package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSuccessorPreparationEnvelopeRetainsAdmittedIdentity(t *testing.T) {
	for _, name := range []string{"matching-no-run", "third-generation", "later-run", "wrong-task", "wrong-project", "missing-successor", "missing-task"} {
		t.Run(name, func(t *testing.T) {
			value := map[string]any{"taskId": "task", "predecessorExecutionId": "predecessor", "successorExecutionId": "original-successor", "outcome": "SUCCESSOR_CREATED"}
			service := &executionCancellationGuidanceSpy{guidance: wp.Guidance{ProjectID: "project", TaskID: "task", ExecutionID: "original-successor", DecisionID: "current-decision", NextAction: wp.Action{Code: "WAIT_FOR_IMPLEMENTATION_PREPARATION", Required: true}}}
			switch name {
			case "third-generation":
				service.guidance.ExecutionID = "third-generation"
			case "later-run":
				service.guidance.RunID = "run-created-after-preparation"
			case "wrong-task":
				service.guidance.TaskID = "other-task"
			case "wrong-project":
				service.guidance.ProjectID = "other-project"
			case "missing-successor":
				delete(value, "successorExecutionId")
			case "missing-task":
				delete(value, "taskId")
			}
			before, _ := json.Marshal(value)
			result := &sdkmcp.CallToolResult{StructuredContent: value}
			_, err := surfaceBusinessResult(context.Background(), service, &sdkmcp.CallToolParamsRaw{Name: "successor_execution_prepare", Arguments: json.RawMessage(`{"aiSessionId":"session","projectId":"project","taskId":"task","predecessorExecutionId":"predecessor","requestId":"prepare"}`)}, result, nil)
			if err != nil {
				t.Fatal(err)
			}
			envelope := result.StructuredContent.(map[string]any)
			ctx := envelope["context"].(map[string]any)
			workflow := envelope["workflow"].(map[string]any)
			if ctx["executionId"] != value["successorExecutionId"] || ctx["taskId"] != value["taskId"] || ctx["runId"] != nil {
				t.Errorf("preparation result identity replaced or Run invented: %+v", ctx)
			}
			if name == "matching-no-run" {
				if envelope["refreshRequired"] == true || workflow["nextAction"] == nil || ctx["decisionId"] != "current-decision" {
					t.Errorf("matching admitted Execution lost guidance: %+v", envelope)
				}
			} else if envelope["refreshRequired"] != true || workflow["stale"] != true || workflow["nextAction"] != nil || workflow["pendingDecisions"] != nil || ctx["decisionId"] != nil {
				t.Errorf("historical or incomplete receipt inherited live guidance: %+v", envelope)
			}
			if name == "missing-successor" || name == "missing-task" {
				if service.scope.ExecutionID != "" {
					t.Errorf("incomplete receipt issued unrelated readback: %+v", service.scope)
				}
			} else if service.scope.ExecutionID != "original-successor" || service.scope.OwnerID != "original-successor" || service.scope.OwnerType != "EXECUTION" || service.scope.RunID != "" {
				t.Errorf("readback did not use exact admitted Execution: %+v", service.scope)
			}
			after, _ := json.Marshal(envelope["result"])
			if !reflect.DeepEqual(before, after) {
				t.Errorf("immutable preparation receipt changed: %s -> %s", before, after)
			}
		})
	}
}
