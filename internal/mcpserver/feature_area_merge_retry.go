package mcpserver

import (
	"context"
	"github.com/lazyun/devbridge/internal/delivery"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *StaticService) ReprepareFeatureAreaMerge(ctx context.Context, session string, in delivery.MergeRetryInput) (delivery.MergeRetryReceipt, error) {
	return s.control.ReprepareFeatureAreaMerge(ctx, session, in)
}
func (s *StaticService) FeatureAreaMergeRetryCandidates(ctx context.Context, session, area string) ([]delivery.MergeRetryCandidate, error) {
	return s.control.FeatureAreaMergeRetryCandidates(ctx, session, area)
}

type mergeRetryOutput struct {
	delivery.MergeRetryReceipt
	DispatchError   string `json:"dispatchError,omitempty"`
	RefreshRequired bool   `json:"refreshRequired,omitempty"`
}
type mergeCandidatesOutput struct {
	Candidates []delivery.MergeRetryCandidate `json:"candidates"`
}

const mergeRetryReceiptSchema = `{"type":"object","properties":{"projectId":{"type":"string"},"featureAreaId":{"type":"string"},"mergeRequestId":{"type":"string"},"repositoryId":{"type":"string"},"predecessorOperationId":{"type":"string"},"operationId":{"type":"string"},"requestId":{"type":"string"},"dispatchError":{"type":"string"},"refreshRequired":{"type":"boolean"},"releasePlan":{"type":"boolean"},"planReleased":{"type":"boolean"}},"required":["projectId","featureAreaId","mergeRequestId","repositoryId","predecessorOperationId","operationId","requestId"],"additionalProperties":false}`
const mergeCandidatesSchema = `{"type":"object","properties":{"candidates":{"type":"array","items":{"type":"object","properties":{"mergeRequestId":{"type":"string"},"repositoryId":{"type":"string"},"operationId":{"type":"string"},"error":{"type":"string"},"canRelease":{"type":"boolean"}},"required":["mergeRequestId","repositoryId","operationId","error"],"additionalProperties":false}}},"required":["candidates"],"additionalProperties":false}`

func registerFeatureAreaMergeRetry(server *sdkmcp.Server, service BridgeService) {
	type mergeService interface {
		ReprepareFeatureAreaMerge(context.Context, string, delivery.MergeRetryInput) (delivery.MergeRetryReceipt, error)
		FeatureAreaMergeRetryCandidates(context.Context, string, string) ([]delivery.MergeRetryCandidate, error)
	}
	type readInput struct {
		AISessionID   string `json:"aiSessionId"`
		FeatureAreaID string `json:"featureAreaId"`
	}
	addExternalTool(server, "feature_area_merge_retry_candidates", "Read rejected FeatureArea merges", "Read exact pre-effect merge refusals and their immutable operation identities. This read never authorizes Git.", `{"type":"object","properties":{"aiSessionId":{"type":"string"},"featureAreaId":{"type":"string"}},"required":["aiSessionId","featureAreaId"],"additionalProperties":false}`, mergeCandidatesSchema, toolAnnotations("Read rejected FeatureArea merges", true, true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in readInput) (*sdkmcp.CallToolResult, mergeCandidatesOutput, error) {
		out := mergeCandidatesOutput{Candidates: []delivery.MergeRetryCandidate{}}
		s, ok := service.(mergeService)
		if !ok {
			return nil, out, publicToolError(errDevelopmentContextUnavailable)
		}
		v, e := s.FeatureAreaMergeRetryCandidates(ctx, in.AISessionID, in.FeatureAreaID)
		if v != nil {
			out.Candidates = v
		}
		return nil, out, publicToolError(e)
	})
	type writeInput struct {
		AISessionID string `json:"aiSessionId"`
		delivery.MergeRetryInput
	}
	addExternalTool(server, "feature_area_merge_reprepare", "Authorize rejected FeatureArea merge retry", "Explicitly authorize one new operation for an exact REJECTED_NOT_CONSUMED merge. Use a new requestId and identities from feature_area_merge_retry_candidates. The frozen workset and development context must still match. With explicit releasePlan=true, close the old plan without Git only when canRelease=true; preserve completed members and prepare a fresh merge confirmation afterward. This releases no UNKNOWN effect and is not a retry. UNKNOWN cannot be retried; exact request replay only reads its receipt.", `{"type":"object","properties":{"aiSessionId":{"type":"string"},"featureAreaId":{"type":"string"},"mergeRequestId":{"type":"string"},"repositoryId":{"type":"string"},"predecessorOperationId":{"type":"string"},"requestId":{"type":"string","minLength":1,"maxLength":128},"releasePlan":{"type":"boolean"}},"required":["aiSessionId","featureAreaId","mergeRequestId","repositoryId","predecessorOperationId","requestId"],"additionalProperties":false}`, mergeRetryReceiptSchema, stateChangingAnnotations("Authorize rejected FeatureArea merge retry", true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in writeInput) (*sdkmcp.CallToolResult, mergeRetryOutput, error) {
		s, ok := service.(mergeService)
		if !ok {
			return nil, mergeRetryOutput{}, publicToolError(errDevelopmentContextUnavailable)
		}
		v, e := s.ReprepareFeatureAreaMerge(ctx, in.AISessionID, in.MergeRetryInput)
		out := mergeRetryOutput{MergeRetryReceipt: v}
		if e != nil && v.OperationID != "" {
			out.DispatchError, out.RefreshRequired = publicToolError(e).Error(), true
			return &sdkmcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, publicToolError(e)
	})
}
