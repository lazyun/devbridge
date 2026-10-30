package directfile

import (
	"time"

	"github.com/lazyun/devbridge/internal/task"
)

// WorkerTarget selects an already-started v2 DIRECT_FILE authorization. It is
// not a Run or an Agent runtime: none is invented by pull delivery.
type WorkerTarget struct {
	AISessionID              string `json:"aiSessionId"`
	FeatureAreaID            string `json:"featureAreaId"`
	TaskID                   string `json:"taskId"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId"`
	DecisionID               string `json:"decisionId"`
	WorkflowID               string `json:"workflowId"`
	ProtocolVersion          int    `json:"protocolVersion"`
	RuntimeEpoch             string `json:"runtimeEpoch"`
	GrantGeneration          int64  `json:"grantGeneration"`
}

// WorkerID is a correlation label under the authenticated original Session,
// not proof of a dot account, model, cloud thread or process identity.
type WorkerClaimIdentity struct {
	RequestID string `json:"requestId"`
	WorkerID  string `json:"workerId"`
}

type WorkerClaimInput struct {
	WorkerTarget
	WorkerClaimIdentity
}

const (
	WorkerDispatchIntent          = "DISPATCH_INTENT"
	WorkerOutcomeUnknown          = "OUTCOME_UNKNOWN"
	WorkerResultRecorded          = "RESULT_RECORDED"
	WorkerContinuationUnsupported = "FILE_WORKER_CONTINUATION_UNSUPPORTED"
)

type WorkerClaimReceipt struct {
	WorkerTarget
	WorkerClaimIdentity
	ClaimID           string    `json:"claimId"`
	InstructionSHA256 string    `json:"instructionSha256"`
	DefinitionSHA256  string    `json:"definitionSha256"`
	WorksetSHA256     string    `json:"worksetSha256"`
	PayloadSHA256     string    `json:"payloadSha256"`
	CreatedAt         time.Time `json:"createdAt"`
}

// Only the first successful dispatch response contains executable input.
// Instruction bytes are unchanged; existing file authority and the separate
// verification requirement are context, not additional task instruction text.
type WorkerExecutionPayload struct {
	Instruction  string                `json:"instruction"`
	Definition   task.Definition       `json:"definition"`
	Access       State                 `json:"access"`
	Verification task.VerificationSpec `json:"verification"`
}

type WorkerClaimResult struct {
	Receipt          WorkerClaimReceipt      `json:"receipt"`
	State            string                  `json:"state"`
	Replay           bool                    `json:"replay"`
	ExecutionPayload *WorkerExecutionPayload `json:"executionPayload,omitempty"`
	ResultReceipt    *ImplementationResult   `json:"resultReceipt,omitempty"`
}
