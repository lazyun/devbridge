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
