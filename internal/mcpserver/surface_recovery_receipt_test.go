package mcpserver

import (
	"context"
	"encoding/json"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
)

func TestRecoveryEnvelopeBindsReceiptOperationAndRun(t *testing.T) {
	for _, kind := range []string{"REVIEW", "EXECUTION"} {
		for _, change := range []string{"matching", "successor-owner", "successor-run", "missing-run", "other-kind", "other-area"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				value := map[string]any{"operationKind": kind, "operationId": "original-owner", "runId": "original-run", "featureAreaId": "area", "state": "REAUTHORIZED"}
				before, _ := json.Marshal(value)
				g := wp.Guidance{TaskID: "task", FeatureAreaID: "area", RunID: "original-run", DecisionID: "current-decision", NextAction: wp.Action{Code: "CURRENT_ACTION", Required: true}}
				if kind == "REVIEW" {
					g.ReviewID = "original-owner"
				} else {
					g.ExecutionID = "original-owner"
					g.ReviewID = "supporting-review"
				}
				switch change {
				case "successor-owner":
					if kind == "REVIEW" {
						g.ReviewID = "successor"
					} else {
						g.ExecutionID = "successor"
					}
				case "successor-run":
					g.RunID = "successor-run"
				case "missing-run":
					g.RunID = ""
				case "other-area":
					g.FeatureAreaID = "other-area"
				case "other-kind":
					if kind == "REVIEW" {
						g.ExecutionID = "execution"
					} else {
						g.ExecutionID = ""
						g.ReviewID = "review"
					}
				}
				service := &executionCancellationGuidanceSpy{guidance: g}
				result := &sdkmcp.CallToolResult{StructuredContent: value}
				// Even an inconsistent enrichment input cannot replace durable result IDs.
				_, err := surfaceBusinessResult(context.Background(), service, &sdkmcp.CallToolParamsRaw{Name: "agent_operation_recover", Arguments: json.RawMessage(`{"aiSessionId":"session","operationId":"wrong-input","runId":"wrong-input","featureAreaId":"wrong-input","taskId":"wrong-input","reviewId":"wrong-input","executionId":"wrong-input","decisionId":"wrong-input"}`)}, result, nil)
				if err != nil {
					t.Fatal(err)
				}
				if service.scope.OwnerType != kind || service.scope.OwnerID != "original-owner" || service.scope.RunID != "original-run" || service.scope.TaskID != "" || service.scope.FeatureAreaID != "area" {
					t.Fatalf("incorrect exact scope: %+v", service.scope)
				}
				envelope := result.StructuredContent.(map[string]any)
				ctx := envelope["context"].(map[string]any)
				workflow := envelope["workflow"].(map[string]any)
				key := "reviewId"
				if kind == "EXECUTION" {
					key = "executionId"
				}
				if ctx[key] != "original-owner" || ctx["runId"] != "original-run" {
					t.Errorf("original identity overwritten: %+v", ctx)
				}
				if change == "matching" {
					if envelope["refreshRequired"] == true || workflow["nextAction"] == nil || ctx["decisionId"] != "current-decision" {
						t.Errorf("matching guidance lost: %+v", envelope)
					}
				} else if envelope["refreshRequired"] != true || workflow["stale"] != true || workflow["nextAction"] != nil || workflow["pendingDecisions"] != nil || ctx["decisionId"] != nil {
					t.Errorf("historical receipt borrowed current action: %+v", envelope)
				}
				after, _ := json.Marshal(envelope["result"])
				if string(before) != string(after) {
					t.Fatal("durable recovery receipt changed")
				}
			})
		}
	}
}

func TestRecoveryEnvelopeRejectsIncompleteReceiptBeforeReadback(t *testing.T) {
	for _, missing := range []string{"operationId", "runId", "featureAreaId"} {
		t.Run(missing, func(t *testing.T) {
			value := map[string]any{"operationKind": "REVIEW", "operationId": "review", "runId": "run", "featureAreaId": "area"}
			delete(value, missing)
			service := &executionCancellationGuidanceSpy{}
			result := &sdkmcp.CallToolResult{StructuredContent: value}
			_, err := surfaceBusinessResult(context.Background(), service, &sdkmcp.CallToolParamsRaw{Name: "agent_operation_recover", Arguments: json.RawMessage(`{"aiSessionId":"session","featureAreaId":"input-area","runId":"input-run","operationId":"input-owner"}`)}, result, nil)
			if err != nil {
				t.Fatal(err)
			}
			if service.scope.AISessionID != "" {
				t.Fatalf("incomplete receipt used fallback readback: %+v", service.scope)
			}
			envelope := result.StructuredContent.(map[string]any)
			if envelope["refreshRequired"] != true || envelope["workflow"].(map[string]any)["nextAction"] != nil {
				t.Fatalf("incomplete receipt permitted guidance: %+v", envelope)
			}
		})
	}
}
