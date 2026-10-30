package mcpserver

import "time"

// These schemas are shared by Task Plan and Execution Proposal tools. They
// describe the already-established WorkSpec and VerificationSpec contracts;
// Task creation deliberately does not accept either contract.
const (
	commandPermissionsSchema            = `{"type":"object","description":"Task-scoped command permissions included in the immutable proposal and shown for formal user confirmation. Omitted means network disabled. Never infer consent from a blocked command. Broadening requires a new proposal/version and user confirmation.","properties":{"networkAccess":{"type":"boolean"},"verificationOutputs":{"type":"array","maxItems":32,"description":"Repository-relative Git-ignored output directories to allow writing inside verification copies. Core refuses tracked/nonignored source or symlink paths. Never grants original source writes.","items":{"type":"string","minLength":1,"maxLength":4096}}},"required":["networkAccess"],"additionalProperties":false}`
	executionScopePathsInputSchema      = `{"type":"object","properties":{"kind":{"type":"string","const":"PATHS"},"paths":{"type":"array","minItems":1,"maxItems":128,"items":{"type":"string","minLength":1,"maxLength":4096}}},"required":["kind","paths"],"additionalProperties":false}`
	executionScopeRepositoryInputSchema = `{"type":"object","properties":{"kind":{"type":"string","const":"REPOSITORY"}},"required":["kind"],"additionalProperties":false}`
	executionScopeInputSchema           = `{"oneOf":[` + executionScopePathsInputSchema + `,` + executionScopeRepositoryInputSchema + `,{"type":"object","properties":{"kind":{"const":"REPOSITORY_TARGETS"}},"required":["kind"],"additionalProperties":false}]}`
	workSpecInputSchema                 = `{"type":"object","properties":{"commandPermissions":` + commandPermissionsSchema + `,"implementationMode":{"type":"string","enum":["CODING_AGENT","DIRECT_FILE"]},"goal":{"type":"string","minLength":1,"maxLength":16384},"constraints":{"type":"array","maxItems":32,"items":{"type":"string","maxLength":2048}},"codexInstruction":{"type":"string","description":"Complete, standalone Implementation instructions for the selected implementation channel: state the role, concrete goal and changes, exact scope, preserved content, technical limits, checks and deliverables. Incorporate resolved user decisions. Do not depend on prior chat or require another approval or a receipt search. Include engineering work only; omit workflow IDs, authorization conditions and Core receipt lookups. Core enforces authorization before dispatch.","minLength":1,"maxLength":65536},"executionScope":` + executionScopeInputSchema + `},"required":["implementationMode","goal","codexInstruction","executionScope"],"additionalProperties":false}`
	verificationRunInputSchema          = `{"type":"object","properties":{"mode":{"type":"string","const":"RUN"},"scope":{"type":"string","minLength":1,"maxLength":16384},"checks":{"type":"array","minItems":1,"maxItems":32,"items":` + verificationCheckSchema + `},"acceptanceCriteria":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"string","minLength":1,"maxLength":2048}},"codexInstruction":{"type":"string","description":"Complete, standalone Verification instructions: state the verification role, exact target and scope, checks, expected evidence, acceptance criteria, limits and deliverables. Do not use the Implementation instructions, depend on prior chat or require another approval or a receipt search. Include engineering checks only; omit workflow IDs, authorization conditions and Core receipt lookups. Applicable only when mode is RUN; Core enforces authorization before dispatch.","minLength":1,"maxLength":65536}},"required":["mode","scope","checks","acceptanceCriteria","codexInstruction"],"additionalProperties":false}`
	verificationNoneInputSchema         = `{"type":"object","properties":{"mode":{"type":"string","const":"NONE"},"waiver":{"type":"object","properties":{"source":{"type":"string","const":"USER_WAIVED"},"reason":{"type":"string","minLength":1,"maxLength":2048}},"required":["source","reason"],"additionalProperties":false}},"required":["mode","waiver"],"additionalProperties":false}`
	verificationSpecInputSchema         = `{"oneOf":[` + verificationRunInputSchema + `,` + verificationNoneInputSchema + `]}`
)

const (
	WorkSpecInputJSONSchema         = workSpecInputSchema
	VerificationSpecInputJSONSchema = verificationSpecInputSchema
)

func formattedTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(timeFormat)
	return &formatted
}
