package mcpserver

import (
	"context"

	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type noRunRevisionInput struct {
	AISessionID string `json:"aiSessionId"`
	wp.StopAndReviseNoRunTask
}

const noRunRevisionInputSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1},"projectId":{"type":"string"},"taskId":{"type":"string","minLength":1},"predecessorExecutionId":{"type":"string","minLength":1},"expectedDefinitionRevisionId":{"type":"string","minLength":1}},"required":["aiSessionId","taskId","predecessorExecutionId","expectedDefinitionRevisionId"],"additionalProperties":false}`

func (s *StaticService) StopAndReviseNoRunTaskForSession(ctx context.Context, sessionID string, in wp.StopAndReviseNoRunTask) (task.Task, error) {
	if s.control == nil {
		return task.Task{}, errExecutionCancellationUnavailable
	}
	return s.control.StopAndReviseNoRunTaskForSession(ctx, sessionID, in)
}

func noRunRevisionCore(ctx context.Context, service BridgeService, in noRunRevisionInput) (task.Task, error) {
	provider, ok := service.(interface {
		StopAndReviseNoRunTaskForSession(context.Context, string, wp.StopAndReviseNoRunTask) (task.Task, error)
	})
	if !ok {
		return task.Task{}, publicToolError(errExecutionCancellationUnavailable)
	}
	out, err := provider.StopAndReviseNoRunTaskForSession(ctx, in.AISessionID, in.StopAndReviseNoRunTask)
	return out, publicToolError(err)
}
