package recoverystatus

import "encoding/json"

// RepairPurposeInput is caller-authored authority for a legacy resolved
// revision-required Recovery with no exact Confirmation link.
type RepairPurposeInput struct {
	RecoveryID  string          `json:"recoveryId"`
	RequestID   string          `json:"requestId"`
	PayloadHash string          `json:"payloadHash"`
	Proposal    PurposeProposal `json:"proposal"`
}

func (in RepairPurposeInput) ComputePayloadHash() string {
	in.PayloadHash = ""
	b, _ := json.Marshal(in)
	return Hash(b)
}

type PurposeRepairReceipt struct {
	RecoveryID         string `json:"recoveryId"`
	RecoveryGeneration int    `json:"recoveryGeneration"`
	RequestID          string `json:"requestId"`
	PayloadHash        string `json:"payloadHash"`
	IntentID           string `json:"intentId"`
	Replay             bool   `json:"replay"`
}
