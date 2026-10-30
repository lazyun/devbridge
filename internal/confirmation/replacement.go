package confirmation

import "context"

// ReplacementReceipt proves preparation only. It cannot authorize confirmation
// consumption, a business successor or runtime execution.
type ReplacementReceipt struct {
	AISessionID            string `json:"aiSessionId"`
	RequestID              string `json:"replacementRequestId"`
	PayloadSHA256          string `json:"payloadSha256"`
	Kind                   string `json:"replacementKind"`
	SourceIntentID         string `json:"sourceIntentId"`
	SourceVersion          int    `json:"sourceIntentVersion"`
	SourceContentHash      string `json:"sourceIntentContentHash"`
	ReplacementIntentID    string `json:"replacementIntentId"`
	ReplacementVersion     int    `json:"replacementIntentVersion"`
	ReplacementContentHash string `json:"replacementIntentContentHash"`
	Outcome                string `json:"outcome"`
	RecoveryID             string `json:"recoveryId,omitempty"`
	RecoveryGeneration     int    `json:"recoveryGeneration,omitempty"`
	SourceOwnerKind        string `json:"sourceOwnerKind,omitempty"`
	SourceOwnerID          string `json:"sourceOwnerId,omitempty"`
	PredecessorLinkID      string `json:"predecessorLinkId,omitempty"`
	ReplacementLinkID      string `json:"replacementLinkId,omitempty"`
	LinkOrdinal            int    `json:"linkOrdinal,omitempty"`
}

// Optional to legacy adapters: fresh replacements require this production
// protocol, never a fallback to a generic prepare writer.
type ReplacementStore interface {
	ReplayReplacement(context.Context, ReprepareInput) (Intent, error)
	ReprepareDecision(context.Context, string, ReprepareInput, PrepareInput) (Intent, error)
	ReprepareRecoveryPurpose(context.Context, ReprepareInput) (Intent, error)
	ReadReplacementReceipt(context.Context, ReadInput) (*ReplacementReceipt, error)
}
