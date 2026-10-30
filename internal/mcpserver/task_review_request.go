package mcpserver

import (
	"context"
	"errors"
	"strings"

	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const TaskReviewRequestTargetSchema = `{"type":"object","properties":{"phase":{"type":"string","minLength":1},"taskDefinitionRevisionId":{"type":"string"},"currentReviewId":{"type":"string"},"currentExecutionId":{"type":"string"},"pendingDecisionId":{"type":"string"}},"required":["phase","taskDefinitionRevisionId","currentReviewId","currentExecutionId","pendingDecisionId"],"additionalProperties":false}`
const TaskReviewRequestInputSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1},"taskId":{"type":"string","minLength":1},"featureAreaId":{"type":"string","minLength":1},"requestId":{"type":"string","minLength":1,"maxLength":128},"purpose":{"type":"string","minLength":1,"maxLength":65536},"expectedTarget":` + TaskReviewRequestTargetSchema + `},"required":["aiSessionId","taskId","featureAreaId","requestId","purpose","expectedTarget"],"additionalProperties":false}`
const TaskReviewRequestOutputSchema = `{"type":"object","properties":{"id":{"type":"string"},"projectId":{"type":"string"},"taskId":{"type":"string"},"featureAreaId":{"type":"string"},"requestId":{"type":"string"},"purpose":{"type":"string"},"sourceRevisionId":{"type":"string"},"sourceReviewId":{"type":"string"},"sourceExecutionId":{"type":"string"},"supersededDecisionId":{"type":"string"},"phase":{"const":"REVIEW_REQUIRED"},"expectedTarget":` + TaskReviewRequestTargetSchema + `,"requestedAt":{"type":"string","format":"date-time"},"replay":{"type":"boolean"}},"required":["id","projectId","taskId","featureAreaId","requestId","purpose","sourceRevisionId","sourceReviewId","sourceExecutionId","supersededDecisionId","phase","expectedTarget","requestedAt","replay"],"additionalProperties":false}`
const TaskReviewRequestDescription = "Request an explicitly user-authorized supplemental Review of the same ACTIVE Task. Copy expectedTarget from the shared REQUEST_TASK_REVIEW action, including all five observed fields; empty IDs assert absence, not wildcards. Store the complete purpose/new questions verbatim. Core atomically records the request, supersedes eligible old candidate/acceptance authority and safely revokes only this Task's old file authorization, then enters REVIEW_REQUIRED. It does not start a Review or Run. After success, use the original task_review_start on this exact Task with explicit executorKind/reviewDepth and complete reviewInstruction; these two internal calls do not require duplicate user confirmation. Active/reserved/unsafe UNKNOWN writers block; no implicit cancellation or recovery. REVIEW_APPROVAL_PENDING uses the existing REVISE decision. Reuse the same requestId and exact payload after response loss; replay returns historical receipt without restoring old authority. This is optional engineering re-review, never a mandatory prerequisite for RETRY_IMPLEMENTATION."

func TaskReviewRequestCore(ctx context.Context, service BridgeService, in wp.TaskReviewRequestInput) (wp.TaskReviewRequestResolution, error) {
	if in.ExpectedTarget == nil || strings.TrimSpace(in.AISessionID) == "" || strings.TrimSpace(in.TaskID) == "" || strings.TrimSpace(in.FeatureAreaID) == "" || strings.TrimSpace(in.RequestID) == "" || strings.TrimSpace(in.Purpose) == "" {
		return wp.TaskReviewRequestResolution{}, errors.New("exact Task, Session, request, purpose and complete observed target required")
	}
	provider, ok := service.(interface {
		RequestTaskReview(context.Context, wp.TaskReviewRequestInput) (wp.TaskReviewRequestResolution, error)
	})
	if !ok {
		return wp.TaskReviewRequestResolution{}, errors.New("Task supplemental Review unavailable")
	}
	out, err := provider.RequestTaskReview(ctx, in)
	return out, publicToolError(err)
}
func (s *StaticService) RequestTaskReview(ctx context.Context, in wp.TaskReviewRequestInput) (wp.TaskReviewRequestResolution, error) {
	if s.control == nil {
		return wp.TaskReviewRequestResolution{}, errors.New("Task supplemental Review unavailable")
	}
	return s.control.RequestTaskReview(ctx, in)
}
func registerTaskReviewRequest(server *sdkmcp.Server, service BridgeService) {
	addExternalTool(server, "task_review_request", "Request supplemental Review", TaskReviewRequestDescription, TaskReviewRequestInputSchema, TaskReviewRequestOutputSchema, stateChangingAnnotations("Request supplemental Review", true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in wp.TaskReviewRequestInput) (*sdkmcp.CallToolResult, wp.TaskReviewRequestResolution, error) {
		out, err := TaskReviewRequestCore(ctx, service, in)
		return nil, out, err
	})
}
