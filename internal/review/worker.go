package review

import "time"

// WorkerClaimIdentity correlates one pull delivery under the Review creator's
// authenticated AISession. WorkerID is a caller-generated label, not proof of a
// dot account, cloud thread, process, model identity, or exclusive file reader.
type WorkerClaimIdentity struct {
	RequestID string `json:"requestId"`
	WorkerID  string `json:"workerId"`
}

type WorkerClaimInput struct {
	UpperTarget
	WorkerClaimIdentity
}

const (
	WorkerDispatchIntent = "DISPATCH_INTENT"
	WorkerOutcomeUnknown = "OUTCOME_UNKNOWN"
	WorkerResultRecorded = "RESULT_RECORDED"
)

// WorkerClaimReceipt is subordinate to the existing admitted Review/Run and
// grant. It never grants engineering authority or substitutes a canonical claim.
type WorkerClaimReceipt struct {
	UpperTarget
	WorkerClaimIdentity
	ClaimID           string    `json:"claimId"`
	InstructionSHA256 string    `json:"instructionSha256"`
	WorksetSHA256     string    `json:"worksetSha256"`
	PayloadSHA256     string    `json:"payloadSha256"`
	CreatedAt         time.Time `json:"createdAt"`
}

// WorkerExecutionPayload is returned only by the first successful dispatch
// write. Instruction bytes are separate from existing Core read authority;
// neither delivery nor a durable intent proves model consumption.
type WorkerExecutionPayload struct {
	Instruction string      `json:"instruction"`
	Access      UpperAccess `json:"access"`
}

type WorkerClaimResult struct {
	Receipt          WorkerClaimReceipt      `json:"receipt"`
	State            string                  `json:"state"`
	Replay           bool                    `json:"replay"`
	ExecutionPayload *WorkerExecutionPayload `json:"executionPayload,omitempty"`
	ResultReceipt    *ReportReceipt          `json:"resultReceipt,omitempty"`
}
