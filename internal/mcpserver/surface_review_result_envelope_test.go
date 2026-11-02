package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReviewResultEnvelopeUsesRecordedReviewAndRun(t *testing.T) {
	for _, tool := range []string{"review_result_submit", "review_result_get"} {
		for _, name := range []string{"matching", "later-review", "later-run", "later-execution", "wrong-task", "wrong-area", "missing-review", "missing-run"} {
			t.Run(tool+"/"+name, func(t *testing.T) {
				value := map[string]any{"reviewId": "original-review", "runId": "original-run", "state": "RECORDED"}
				service := &executionCancellationGuidanceSpy{guidance: wp.Guidance{TaskID: "task", FeatureAreaID: "area", ReviewID: "original-review", RunID: "original-run", DecisionID: "current-decision", NextAction: wp.Action{Code: "RESOLVE_REVIEW_APPROVAL", Required: true}}}
				switch name {
				case "later-review":
					service.guidance.ReviewID = "later-review"
				case "later-run":
					service.guidance.RunID = "later-run"
				case "later-execution":
					service.guidance.ExecutionID = "execution"
				case "wrong-task":
					service.guidance.TaskID = "other-task"
				case "wrong-area":
					service.guidance.FeatureAreaID = "other-area"
				case "missing-review":
					delete(value, "reviewId")
				case "missing-run":
					delete(value, "runId")
				}
				before, _ := json.Marshal(value)
				result := &sdkmcp.CallToolResult{StructuredContent: value}
				_, err := surfaceBusinessResult(context.Background(), service, &sdkmcp.CallToolParamsRaw{Name: tool, Arguments: json.RawMessage(`{"aiSessionId":"session","taskId":"task","featureAreaId":"area","reviewId":"wrong-input-review","runId":"wrong-input-run","executionId":"wrong-input-execution","decisionId":"wrong-input-decision","taskDefinitionRevisionId":"wrong-input-revision"}`)}, result, nil)
				if err != nil {
					t.Fatal(err)
				}
				envelope := result.StructuredContent.(map[string]any)
				ctx, workflow := envelope["context"].(map[string]any), envelope["workflow"].(map[string]any)
				if ctx["reviewId"] != value["reviewId"] || ctx["runId"] != value["runId"] || ctx["executionId"] != nil {
					t.Errorf("recorded identity lost: %+v", ctx)
				}
				if name == "matching" {
					if envelope["refreshRequired"] == true || workflow["nextAction"] == nil || ctx["decisionId"] != "current-decision" {
						t.Errorf("exact current guidance lost: %+v", envelope)
					}
				} else if envelope["refreshRequired"] != true || workflow["stale"] != true || workflow["nextAction"] != nil || workflow["pendingDecisions"] != nil || ctx["decisionId"] != nil {
					t.Errorf("historical result borrowed current actions: %+v", envelope)
				}
				after, _ := json.Marshal(envelope["result"])
				if string(before) != string(after) {
					t.Fatal("original report result changed")
				}
			})
		}
	}
	// A not-started exact read has no Run; don't invent one or drop its own guidance.
	service := &executionCancellationGuidanceSpy{guidance: wp.Guidance{ReviewID: "not-started", NextAction: wp.Action{Code: "START_REVIEW", Required: true}}}
	result := &sdkmcp.CallToolResult{StructuredContent: map[string]any{"reviewId": "not-started", "state": "NOT_STARTED"}}
	_, err := surfaceBusinessResult(context.Background(), service, &sdkmcp.CallToolParamsRaw{Name: "review_result_get", Arguments: json.RawMessage(`{"aiSessionId":"session","reviewId":"not-started"}`)}, result, nil)
	if err != nil {
		t.Fatal(err)
	}
	envelope := result.StructuredContent.(map[string]any)
	if envelope["refreshRequired"] == true || envelope["workflow"].(map[string]any)["nextAction"] == nil || envelope["context"].(map[string]any)["runId"] != nil {
		t.Fatalf("exact no-Run read lost: %+v", envelope)
	}
}
