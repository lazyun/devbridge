package mcpserver

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/agentexecution"
)

type ExecutionListInput struct {
	AISessionID string `json:"aiSessionId"`
}
type ExecutionListOutput struct {
	Executions []agentexecution.Projection `json:"executions"`
}

const ExecutionListInputSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1}},"required":["aiSessionId"],"additionalProperties":false}`
const ExecutionListOutputSchema = `{"type":"object","properties":{"executions":{"type":"array","items":` + agentExecutionProjectionSchema + `}},"required":["executions"],"additionalProperties":false}`

func ExecutionListCore(ctx context.Context, service BridgeService, in ExecutionListInput) (ExecutionListOutput, error) {
	reader, ok := service.(interface {
		ListAgentExecutionsForSession(context.Context, string) ([]agentexecution.Projection, error)
	})
	if !ok {
		return ExecutionListOutput{}, publicToolError(errors.New("durable execution inventory unavailable"))
	}
	values, err := reader.ListAgentExecutionsForSession(ctx, in.AISessionID)
	if err != nil {
		return ExecutionListOutput{}, publicToolError(err)
	}
	if values == nil {
		values = []agentexecution.Projection{}
	}
	return ExecutionListOutput{Executions: values}, nil
}
