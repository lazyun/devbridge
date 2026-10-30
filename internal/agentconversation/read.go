package agentconversation

import (
	"context"
	"time"
)

// HistoryReader exposes immutable conversation lineage for a FeatureArea.
// Presentation only reads these durable rotation and quarantine facts.
type HistoryReader interface {
	ListByFeatureArea(context.Context, string) ([]Conversation, error)
	ListRecoveryReceipts(context.Context, string) ([]RecoveryReceipt, error)
}

// RecoveryReceipt is the immutable record of an exact recovery command.
type RecoveryReceipt struct {
	OperatorPrincipalID                          string        `json:"operatorPrincipalId,omitempty"`
	ExpectedUpperVersion                         *UpperVersion `json:"expectedUpperVersion,omitempty"`
	AuthorizedUpperVersion                       *UpperVersion `json:"authorizedUpperVersion,omitempty"`
	GrantState                                   string        `json:"grantState,omitempty"`
	RequestID, FeatureAreaID, RunID, OperationID string
	OperationKind                                OperationKind
	OldConversationID, NewConversationID         string
	AuthorizedEpoch                              string
	CreatedAt                                    time.Time
}
