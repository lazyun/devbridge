package mcpserver

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
)

// Keep the new selection-bearing command distinct from unchanged historical
// starts and instruction-only finalization. Runtime fields cannot be silently
// ignored by either of the older branches.
const surfaceExecutionStartInputSchema = `{"type":"object","properties":{
"aiSessionId":{"type":"string","minLength":1,"maxLength":128},
"taskId":{"type":"string","minLength":1,"maxLength":128},
"requestId":{"type":"string","minLength":1,"maxLength":128},
"executionInstructions":` + ExecutionInstructionsJSONSchema + `,
"runtimeConfig":` + nextRunRuntimeConfigSchema + `,
"backend":` + externalReviewBackendSchema + `,
"expectedTarget":{"type":"object","properties":{
"featureAreaId":{"type":"string","minLength":1},
"taskDefinitionRevisionId":{"type":"string","minLength":1},
"executionId":{"type":"string"},
"phase":{"enum":["IMPLEMENTATION_READY","VERIFICATION_READY"]},
"primaryMemberId":{"type":"string","minLength":1},
"approvedWorksetHash":{"type":"string","pattern":"^[a-f0-9]{64}$"}},
"required":["featureAreaId","taskDefinitionRevisionId","executionId","phase"],"additionalProperties":false}},
"required":["aiSessionId","taskId"],"additionalProperties":false,
"oneOf":[
{"required":["runtimeConfig","backend","requestId","expectedTarget"],"not":{"required":["executionInstructions"]},"properties":{"expectedTarget":{"properties":{"phase":{"const":"IMPLEMENTATION_READY"}},"required":["primaryMemberId","approvedWorksetHash"]}}},
{"required":["executionInstructions","requestId","expectedTarget"],"not":{"anyOf":[{"required":["runtimeConfig"]},{"required":["backend"]}]}},
{"not":{"anyOf":[{"required":["executionInstructions"]},{"required":["runtimeConfig"]},{"required":["backend"]},{"required":["requestId"]}]}}
]}`

func (s *StaticService) StartApprovedTaskExecution(ctx context.Context, in controlapi.ApprovedTaskStartInput) (executionproposal.StartResult, error) {
	if s.control == nil {
		return executionproposal.StartResult{}, errors.New("workflow unavailable")
	}
	return s.control.StartApprovedTaskExecution(ctx, in)
}
