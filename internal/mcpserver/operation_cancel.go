package mcpserver

const UpperVersionSchema = `{"type":"object","properties":{"authorityEpoch":{"type":"string","minLength":1,"maxLength":128},"grantEpoch":{"type":"string","minLength":1,"maxLength":128},"grantGeneration":{"type":"integer","minimum":1}},"required":["authorityEpoch","grantEpoch","grantGeneration"],"additionalProperties":false}`

const OperationCancelInputSchema = `{"type":"object","properties":{"expectedUpperVersion":` + UpperVersionSchema + `,"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"ownerType":{"type":"string","enum":["REVIEW","EXECUTION","TASK","INSPECTION"]},"ownerId":{"type":"string","minLength":1,"maxLength":128},"runId":{"type":"string","maxLength":128},"requestId":{"type":"string","minLength":1,"maxLength":128},"reason":{"type":"string","maxLength":2048}},"required":["aiSessionId","ownerType","ownerId","requestId"],"additionalProperties":false}`

const OperationCancelDescription = "Cancel the exact REVIEW, EXECUTION, INSPECTION or pre-work TASK owner through the canonical cancellation protocol. Supply the observed runId; omit it only when the owner has no Run. For new Upper Review commands copy expectedUpperVersion from the exact Core capability; do not substitute a newer version on retry. Replay the same requestId and payload after response loss. UNKNOWN_ORPHANED means Bridge authority revoked and workspace/context quarantined, not physical backend death."

const TaskWorkListDescription = "Read durable visible DEVELOPMENT, REVIEW and Project-scope INSPECTION Tasks, exact work owner, phase, latest Run and cancellation/orphan disposition. This pure read does not advance any workflow."
