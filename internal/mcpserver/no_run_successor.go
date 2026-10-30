package mcpserver

import (
	"context"

	"github.com/lazyun/devbridge/internal/controlapi"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type successorPrepareInput struct {
	AISessionID string `json:"aiSessionId"`
	wp.PrepareSuccessorExecution
}

const successorPrepareInputSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1},"requestId":{"type":"string","minLength":1},"payloadSha256":{"type":"string","minLength":1},"projectId":{"type":"string"},"taskId":{"type":"string","minLength":1},"predecessorExecutionId":{"type":"string","minLength":1},"expectedTaskState":{"type":"string","const":"TASK_NO_RUN_SUCCESSOR_REQUIRED"},"expectedCurrentExecutionId":{"type":"string","minLength":1},"expectedDefinitionRevisionId":{"type":"string","minLength":1}},"required":["aiSessionId","requestId","payloadSha256","taskId","predecessorExecutionId","expectedTaskState","expectedCurrentExecutionId","expectedDefinitionRevisionId"],"additionalProperties":false}`

func (s *StaticService) PrepareSuccessorExecutionForSession(ctx context.Context, sessionID string, in wp.PrepareSuccessorExecution) (wp.SuccessorCommandReceipt, error) {
	if s.control == nil {
		return wp.SuccessorCommandReceipt{}, errExecutionCancellationUnavailable
	}
	return s.control.PrepareSuccessorExecutionForSession(ctx, sessionID, in)
}

func successorPrepareCore(ctx context.Context, service BridgeService, in successorPrepareInput) (wp.SuccessorCommandReceipt, error) {
	provider, ok := service.(interface {
		PrepareSuccessorExecutionForSession(context.Context, string, wp.PrepareSuccessorExecution) (wp.SuccessorCommandReceipt, error)
	})
	if !ok {
		return wp.SuccessorCommandReceipt{}, publicToolError(errExecutionCancellationUnavailable)
	}
	out, err := provider.PrepareSuccessorExecutionForSession(ctx, in.AISessionID, in.PrepareSuccessorExecution)
	if err != nil {
		return out, publicToolError(controlapi.MapError(err))
	}
	return out, nil
}
