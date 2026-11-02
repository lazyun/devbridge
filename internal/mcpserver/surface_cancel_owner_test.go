package mcpserver

import (
	"context"
	"encoding/json"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"reflect"
	"testing"
)

type cancellationGuidanceSpy struct {
	BridgeService
	scope wp.ResourceScope
}

func (s *cancellationGuidanceSpy) WorkflowGuidanceForResource(_ context.Context, scope wp.ResourceScope) (wp.Guidance, error) {
	s.scope = scope
	return wp.Guidance{ProjectID: "project", TaskID: "task", ReviewID: scope.ReviewID, ExecutionID: scope.ExecutionID, CurrentState: "REVIEW_REQUIRED"}, nil
}

func TestCancelEnvelopeUsesReviewOwnerWithoutRewritingReceipt(t *testing.T) {
	for _, input := range []string{`{"aiSessionId":"session","ownerType":"REVIEW","ownerId":"review","requestId":"cancel"}`, `{"aiSessionId":"session","executionId":"review:review","requestId":"cancel"}`} {
		service := &cancellationGuidanceSpy{}
		value := map[string]any{"ownerType": "REVIEW", "ownerId": "review", "executionId": "review:review", "taskId": "task", "runId": "run", "state": "CANCELED", "backendDisposition": "NO_BACKEND"}
		before, _ := json.Marshal(value)
		result := &sdkmcp.CallToolResult{StructuredContent: value}
		_, err := surfaceBusinessResult(context.Background(), service, &sdkmcp.CallToolParamsRaw{Name: "operation_cancel", Arguments: json.RawMessage(input)}, result, nil)
		if err != nil {
			t.Fatal(err)
		}
		if service.scope.ReviewID != "review" || service.scope.OwnerID != "review" || service.scope.ExecutionID != "" {
			t.Fatalf("wrong cancellation readback scope: %+v", service.scope)
		}
		envelope := result.StructuredContent.(map[string]any)
		ctx := envelope["context"].(map[string]any)
		if ctx["reviewId"] != "review" || ctx["executionId"] != nil {
			t.Fatalf("wrong semantic context: %+v", ctx)
		}
		after, _ := json.Marshal(envelope["result"])
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("immutable receipt rewritten: %s -> %s", before, after)
		}
	}
}
func TestCancelEnvelopeRetainsRealExecutionProposalScope(t *testing.T) {
	service := &cancellationGuidanceSpy{}
	value := map[string]any{"ownerType": "EXECUTION", "ownerId": "execution", "executionId": "execution", "taskId": "task", "runId": "run", "state": "CANCELED"}
	result := &sdkmcp.CallToolResult{StructuredContent: value}
	_, err := surfaceBusinessResult(context.Background(), service, &sdkmcp.CallToolParamsRaw{Name: "operation_cancel", Arguments: json.RawMessage(`{"aiSessionId":"session","executionId":"execution","requestId":"cancel"}`)}, result, nil)
	if err != nil || service.scope.ExecutionID != "execution" || service.scope.ReviewID != "" {
		t.Fatalf("native Execution readback changed: %+v %v", service.scope, err)
	}
}

type executionCancellationGuidanceSpy struct {
	BridgeService
	scope    wp.ResourceScope
	guidance wp.Guidance
}

func (s *executionCancellationGuidanceSpy) WorkflowGuidanceForResource(_ context.Context, scope wp.ResourceScope) (wp.Guidance, error) {
	s.scope = scope
	return s.guidance, nil
}

func TestCancelExecutionEnvelopeUsesOriginalSemanticOwner(t *testing.T) {
	for _, tc := range []struct {
		name                                               string
		noRun, universal, successorExecution, successorRun bool
	}{
		{name: "matching-run"},
		{name: "matching-no-run-universal", noRun: true, universal: true},
		{name: "successor-execution", successorExecution: true},
		{name: "successor-run-universal", universal: true, successorRun: true},
		{name: "no-run-successor", noRun: true, successorExecution: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runID := "original-run"
			if tc.noRun {
				runID = ""
			}
			value := map[string]any{"projectId": "project", "ownerType": "EXECUTION", "ownerId": "original-owner", "executionId": "universal-original-inventory", "taskId": "task", "runId": runID, "state": "CANCELED"}
			before, _ := json.Marshal(value)
			input := map[string]any{"aiSessionId": "session", "ownerType": "EXECUTION", "ownerId": "original-owner", "runId": runID, "requestId": "original-cancel"}
			if tc.universal {
				delete(input, "ownerType")
				delete(input, "ownerId")
				input["executionId"] = "universal-original-inventory"
			}
			args, _ := json.Marshal(input)
			service := &executionCancellationGuidanceSpy{guidance: wp.Guidance{ProjectID: "project", TaskID: "task", ExecutionID: "original-owner", RunID: runID, DecisionID: "current-decision", NextAction: wp.Action{Code: "PREPARE_SUCCESSOR_EXECUTION", Required: true}}}
			if tc.successorExecution {
				service.guidance.ExecutionID = "successor-owner"
			}
			if tc.successorRun {
				service.guidance.RunID = "successor-run"
			}
			result := &sdkmcp.CallToolResult{StructuredContent: value}
			_, err := surfaceBusinessResult(context.Background(), service, &sdkmcp.CallToolParamsRaw{Name: "operation_cancel", Arguments: args}, result, nil)
			if err != nil {
				t.Fatal(err)
			}
			if service.scope.OwnerType != "EXECUTION" || service.scope.OwnerID != "original-owner" || service.scope.ExecutionID != "original-owner" || service.scope.RunID != runID || service.scope.TaskID != "task" || service.scope.ProjectID != "project" {
				t.Errorf("readback used alias/current owner: %+v", service.scope)
			}
			envelope := result.StructuredContent.(map[string]any)
			ctx := envelope["context"].(map[string]any)
			workflow := envelope["workflow"].(map[string]any)
			if ctx["executionId"] != "original-owner" {
				t.Errorf("context replaced original semantic owner: %+v", ctx)
			}
			if tc.noRun {
				if ctx["runId"] != nil {
					t.Errorf("invented original Run: %+v", ctx)
				}
			} else if ctx["runId"] != runID {
				t.Errorf("context replaced original Run: %+v", ctx)
			}
			if tc.successorExecution || tc.successorRun {
				if envelope["refreshRequired"] != true || workflow["stale"] != true || workflow["nextAction"] != nil || workflow["pendingDecisions"] != nil || ctx["decisionId"] != nil {
					t.Errorf("old cancel inherited successor guidance: %+v", envelope)
				}
			} else if envelope["refreshRequired"] == true || workflow["nextAction"] == nil || ctx["decisionId"] != "current-decision" {
				t.Errorf("matching owner lost normal guidance: %+v", envelope)
			}
			after, _ := json.Marshal(envelope["result"])
			if !reflect.DeepEqual(before, after) {
				t.Errorf("immutable cancellation receipt rewritten: %s -> %s", before, after)
			}
		})
	}
}
