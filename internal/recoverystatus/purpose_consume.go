package recoverystatus

import (
	"context"
	"encoding/json"
)

const (
	ApplyConfirmedImplementationPurpose = "APPLY_CONFIRMED_IMPLEMENTATION_PURPOSE"
	ApplyConfirmedVerificationPurpose   = "APPLY_CONFIRMED_VERIFICATION_PURPOSE"
	ApplyConfirmedReviewPurpose         = "APPLY_CONFIRMED_REVIEW_PURPOSE"
)

// PurposeConsumeInput addresses one exact committed purpose receipt. It never
// derives an intent by searching for the latest Confirmation.
type PurposeConsumeInput struct {
	AISessionID string `json:"aiSessionId"`
	IntentID    string `json:"intentId"`
	Version     int    `json:"version"`
	ContentHash string `json:"contentHash"`
	Action      string `json:"action"`
	RequestID   string `json:"requestId"`
	PayloadHash string `json:"payloadHash"`
}

func (in PurposeConsumeInput) ComputePayloadHash() string {
	in.PayloadHash = ""
	b, _ := json.Marshal(in)
	return Hash(b)
}

type PurposeConsumption struct {
	IntentID                 string `json:"intentId"`
	Action                   string `json:"action"`
	RequestID                string `json:"requestId"`
	OwnerKind                string `json:"ownerKind"`
	TaskID                   string `json:"taskId"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId,omitempty"`
	ExecutionID              string `json:"executionId,omitempty"`
	ReviewID                 string `json:"reviewId,omitempty"`
	RunID                    string `json:"runId,omitempty"`
	TaskPhase                string `json:"taskPhase"`
	Replay                   bool   `json:"replay"`
}

type PurposeConsumer interface {
	ApplyConfirmedImplementationPurpose(context.Context, PurposeConsumeInput) (PurposeConsumption, error)
	ApplyConfirmedVerificationPurpose(context.Context, PurposeConsumeInput) (PurposeConsumption, error)
	ApplyConfirmedReviewPurpose(context.Context, PurposeConsumeInput) (PurposeConsumption, error)
}
