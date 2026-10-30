package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/review"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type upperReviewWorkerService interface {
	ClaimUpperReviewWorker(context.Context, review.WorkerClaimInput) (review.WorkerClaimResult, error)
	GetUpperReviewWorkerClaim(context.Context, review.WorkerClaimInput) (review.WorkerClaimResult, error)
}

func ReviewWorkerClaimIdentitySchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"requestId": json.RawMessage(upperIDSchema), "workerId": json.RawMessage(upperIDSchema)}, "required": []string{"requestId", "workerId"}, "additionalProperties": false}
}

func ReviewWorkerClaimSchema() json.RawMessage {
	var schema map[string]any
	_ = json.Unmarshal([]byte(UpperReviewTargetSchema), &schema)
	properties := schema["properties"].(map[string]any)
	properties["requestId"], properties["workerId"] = json.RawMessage(upperIDSchema), json.RawMessage(upperIDSchema)
	schema["required"] = append(schema["required"].([]any), "requestId", "workerId")
	raw, _ := json.Marshal(schema)
	return raw
}

func RegisterUpperReviewWorkerTools(server *sdkmcp.Server, service BridgeService) {
	register := func(name, description string, readOnly bool, call func(upperReviewWorkerService, context.Context, review.WorkerClaimInput) (review.WorkerClaimResult, error)) {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: name, Description: description, InputSchema: ReviewWorkerClaimSchema(), Annotations: toolAnnotations("Upper Review pull delivery", readOnly, readOnly)}, func(ctx context.Context, request *sdkmcp.CallToolRequest, in review.WorkerClaimInput) (*sdkmcp.CallToolResult, review.WorkerClaimResult, error) {
			ctx, err := withSessionRequest(ctx, request)
			if err != nil {
				return nil, review.WorkerClaimResult{}, publicToolError(err)
			}
			worker, ok := service.(upperReviewWorkerService)
			if !ok {
				return nil, review.WorkerClaimResult{}, errors.New("upper Review pull worker unavailable")
			}
			result, err := call(worker, ctx, in)
			return nil, result, publicToolError(err)
		})
	}
	register("review_worker_claim", "Claim delivery of one exact ALREADY authorized UPPER_LLM Review for pull-coordinated external review, including dot. Read the exact event-referenced Review/Run and review_access_get target first; an event is only a hint. This never starts or authorizes work, selects a newer Run, transfers Sessions, or creates a cloud task. Use a new random requestId and stable workerId correlation label; workerId is not authenticated host identity. Core durably records dispatch intent before the first executable payload. Only that first returned executionPayload may be acted on. After any lost/ambiguous response, use review_worker_get with the exact original input; OUTCOME_UNKNOWN never permits redispatch, another requestId, or another worker. No timeout reassignment, same-Run recovery/continuation, cancellation proof, or exactly-once claim. Submit the existing review_result_submit report with workerClaim={requestId,workerId}; this remains engineering evidence, not user acceptance.", false, func(s upperReviewWorkerService, ctx context.Context, in review.WorkerClaimInput) (review.WorkerClaimResult, error) {
		return s.ClaimUpperReviewWorker(ctx, in)
	})
	register("review_worker_get", "Read the exact original pull-delivery receipt and known result without returning executable instructions, advancing work, refreshing a grant, releasing a claim, or retrying dispatch. The original authenticated Session, target, workerId and requestId must match. OUTCOME_UNKNOWN means delivery/execution cannot be safely retried; same-Run recovery is unsupported. Existing cancellation can revoke Core authority; any successor Review needs its own explicit admission. A recorded report receipt is not user acceptance or host-process termination proof.", true, func(s upperReviewWorkerService, ctx context.Context, in review.WorkerClaimInput) (review.WorkerClaimResult, error) {
		return s.GetUpperReviewWorkerClaim(ctx, in)
	})
}

func (s *StaticService) ClaimUpperReviewWorker(ctx context.Context, in review.WorkerClaimInput) (review.WorkerClaimResult, error) {
	if s == nil || s.control == nil {
		return review.WorkerClaimResult{}, errors.New("upper Review pull worker unavailable")
	}
	return s.control.ClaimUpperReviewWorker(ctx, in)
}
func (s *StaticService) GetUpperReviewWorkerClaim(ctx context.Context, in review.WorkerClaimInput) (review.WorkerClaimResult, error) {
	if s == nil || s.control == nil {
		return review.WorkerClaimResult{}, errors.New("upper Review pull worker unavailable")
	}
	return s.control.GetUpperReviewWorkerClaim(ctx, in)
}
