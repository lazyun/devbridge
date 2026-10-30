package mcpserver

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

const ExecutionInstructionsJSONSchema = `{"type":"object","description":"Exact complete standalone execution text authored by GPT after incorporating the user's decisions; only canonical instruction fields change, never goal, scope, implementationMode or runtime configuration. Core does not edit or semantically judge the text.","properties":{"implementation":{"type":"string","minLength":1,"maxLength":65536,"description":"Complete Implementation directive: role, goal, concrete changes, approved scope, preservation requirements, checks and delivery. Do not depend on the upper chat or ask the executor to find approval proof."},"verification":{"type":"string","minLength":1,"maxLength":65536,"description":"Complete Verification directive, required for RUN and forbidden for NONE. Keep distinct from implementation work."}},"required":["implementation"],"additionalProperties":false}`

func (s *StaticService) StartTaskExecutionInstructions(ctx context.Context, sessionID, taskID string, expected wp.TaskExecutionExpectation, requestID string, instructions task.ExecutionInstructions) (executionproposal.StartResult, error) {
	if s.control == nil {
		return executionproposal.StartResult{}, errors.New("workflow unavailable")
	}
	return s.control.StartTaskExecutionInstructions(ctx, sessionID, taskID, expected, requestID, instructions)
}
