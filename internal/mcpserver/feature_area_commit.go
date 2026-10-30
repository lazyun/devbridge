package mcpserver

import (
	"context"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type commitService interface {
	CommitFeatureArea(context.Context, string, string, string) (featurearea.FeatureArea, error)
	RetryAcceptedTaskCommit(context.Context, string, string) (task.Task, error)
}

func (s *StaticService) CommitFeatureArea(ctx context.Context, session, area, request string) (featurearea.FeatureArea, error) {
	return s.control.CommitFeatureArea(ctx, session, area, request)
}
func (s *StaticService) RetryAcceptedTaskCommit(ctx context.Context, session, taskID string) (task.Task, error) {
	return s.control.RetryAcceptedTaskCommit(ctx, session, taskID)
}

func (s *StaticService) ReprepareAcceptedTaskCommit(ctx context.Context, session string, in delivery.RetryInput) (delivery.RetryReceipt, error) {
	return s.control.ReprepareAcceptedTaskCommit(ctx, session, in)
}
func (s *StaticService) TaskDeliveryRetryCandidates(ctx context.Context, session, taskID string) ([]delivery.RetryCandidate, error) {
	return s.control.TaskDeliveryRetryCandidates(ctx, session, taskID)
}

const FeatureAreaCommitDescription = "Submit all completed Development Task deliveries to their selected development branches. Multi-repository delivery freezes a per-repository plan and records each merge independently. User initiated; exact requestId replay uses the saved plan and outcomes, and UNKNOWN never authorizes another Git dispatch."
const TaskCommitRetryDescription = "Recover automatic Git finalization of an already durably ACCEPTed Task. Cannot accept, verify, develop, or initially authorize a Task."

type gitCommitOutput struct {
	CommitSHA    string `json:"commitSha"`
	MergedCommit string `json:"mergedCommit"`
}

const gitCommitOutputSchema = `{"type":"object","properties":{"commitSha":{"type":"string"},"mergedCommit":{"type":"string"}},"required":["commitSha","mergedCommit"],"additionalProperties":false}`

const deliveryRetryReceiptSchema = `{"type":"object","properties":{"projectId":{"type":"string"},"taskId":{"type":"string"},"repositoryId":{"type":"string"},"predecessorOperationId":{"type":"string"},"operationId":{"type":"string"},"requestId":{"type":"string"},"dispatchError":{"type":"string"},"refreshRequired":{"type":"boolean"}},"required":["projectId","taskId","repositoryId","predecessorOperationId","operationId","requestId"],"additionalProperties":false}`
const deliveryRetryCandidatesSchema = `{"type":"object","properties":{"candidates":{"type":"array","items":{"type":"object","properties":{"repositoryId":{"type":"string"},"operationId":{"type":"string"},"error":{"type":"string"}},"required":["repositoryId","operationId","error"],"additionalProperties":false}}},"required":["candidates"],"additionalProperties":false}`

type deliveryRetryCandidatesOutput struct {
	Candidates []delivery.RetryCandidate `json:"candidates"`
}

type deliveryRetryReceiptOutput struct {
	delivery.RetryReceipt
	DispatchError   string `json:"dispatchError,omitempty"`
	RefreshRequired bool   `json:"refreshRequired,omitempty"`
}

func registerFeatureAreaCommit(server *sdkmcp.Server, service BridgeService) {
	registerFeatureAreaMergeRetry(server, service)
	type input struct {
		AISessionID   string `json:"aiSessionId"`
		FeatureAreaID string `json:"featureAreaId"`
		RequestID     string `json:"requestId"`
	}
	addExternalTool(server, "feature_area_submit", "Submit FeatureArea", FeatureAreaCommitDescription, `{"type":"object","properties":{"aiSessionId":{"type":"string"},"featureAreaId":{"type":"string"},"requestId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","featureAreaId","requestId"],"additionalProperties":false}`, gitCommitOutputSchema, stateChangingAnnotations("Submit FeatureArea", true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in input) (*sdkmcp.CallToolResult, gitCommitOutput, error) {
		s, ok := service.(commitService)
		if !ok {
			return nil, gitCommitOutput{}, publicToolError(errDevelopmentContextUnavailable)
		}
		v, e := s.CommitFeatureArea(ctx, in.AISessionID, in.FeatureAreaID, in.RequestID)
		return nil, gitCommitOutput{MergedCommit: v.MergedCommit}, publicToolError(e)
	})
	type retry struct {
		AISessionID string `json:"aiSessionId"`
		TaskID      string `json:"taskId"`
	}
	addExternalTool(server, "task_commit_retry", "Retry accepted Task commit", TaskCommitRetryDescription, `{"type":"object","properties":{"aiSessionId":{"type":"string"},"taskId":{"type":"string"}},"required":["aiSessionId","taskId"],"additionalProperties":false}`, gitCommitOutputSchema, stateChangingAnnotations("Retry accepted Task commit", true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in retry) (*sdkmcp.CallToolResult, gitCommitOutput, error) {
		s, ok := service.(commitService)
		if !ok {
			return nil, gitCommitOutput{}, publicToolError(errDevelopmentContextUnavailable)
		}
		v, e := s.RetryAcceptedTaskCommit(ctx, in.AISessionID, in.TaskID)
		return nil, gitCommitOutput{CommitSHA: v.CommitSHA, MergedCommit: v.MergedCommit}, publicToolError(e)
	})
	type reprepare struct {
		AISessionID            string `json:"aiSessionId"`
		TaskID                 string `json:"taskId"`
		RepositoryID           string `json:"repositoryId"`
		PredecessorOperationID string `json:"predecessorOperationId"`
		RequestID              string `json:"requestId"`
	}
	type retryService interface {
		ReprepareAcceptedTaskCommit(context.Context, string, delivery.RetryInput) (delivery.RetryReceipt, error)
		TaskDeliveryRetryCandidates(context.Context, string, string) ([]delivery.RetryCandidate, error)
	}
	addExternalTool(server, "task_commit_reprepare", "Authorize rejected Task commit retry", "Explicitly authorize a new operation for one exactly rejected pre-effect Git commit. Supply a new requestId and the predecessor from task_commit_retry_candidates. UNKNOWN is never retry authority. Exact request replay reads the immutable successor receipt without Git dispatch.", `{"type":"object","properties":{"aiSessionId":{"type":"string"},"taskId":{"type":"string"},"repositoryId":{"type":"string"},"predecessorOperationId":{"type":"string"},"requestId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","taskId","repositoryId","predecessorOperationId","requestId"],"additionalProperties":false}`, deliveryRetryReceiptSchema, stateChangingAnnotations("Authorize rejected Task commit retry", true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in reprepare) (*sdkmcp.CallToolResult, deliveryRetryReceiptOutput, error) {
		s, ok := service.(retryService)
		if !ok {
			return nil, deliveryRetryReceiptOutput{}, publicToolError(errDevelopmentContextUnavailable)
		}
		v, e := s.ReprepareAcceptedTaskCommit(ctx, in.AISessionID, delivery.RetryInput{TaskID: in.TaskID, RepositoryID: in.RepositoryID, PredecessorOperationID: in.PredecessorOperationID, RequestID: in.RequestID})
		out := deliveryRetryReceiptOutput{RetryReceipt: v}
		if e != nil && v.OperationID != "" {
			out.DispatchError, out.RefreshRequired = publicToolError(e).Error(), true
			return &sdkmcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, publicToolError(e)
	})
	addExternalTool(server, "task_commit_retry_candidates", "Read rejected Task commit operations", "Read exact rejected delivery operation identities. This read does not create retry authority or dispatch Git.", `{"type":"object","properties":{"aiSessionId":{"type":"string"},"taskId":{"type":"string"}},"required":["aiSessionId","taskId"],"additionalProperties":false}`, deliveryRetryCandidatesSchema, toolAnnotations("Read rejected Task commit operations", true, true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in retry) (*sdkmcp.CallToolResult, deliveryRetryCandidatesOutput, error) {
		out := deliveryRetryCandidatesOutput{Candidates: []delivery.RetryCandidate{}}
		s, ok := service.(retryService)
		if !ok {
			return nil, out, publicToolError(errDevelopmentContextUnavailable)
		}
		v, e := s.TaskDeliveryRetryCandidates(ctx, in.AISessionID, in.TaskID)
		if v != nil {
			out.Candidates = v
		}
		return nil, out, publicToolError(e)
	})
}
