package agentconversation

import (
	"context"
	"strings"
	"unicode"
)

// UpperVersion fences an observation of one Upper grant against both the current
// writer incarnation and the grant's own authorization incarnation.
// GrantEpoch may differ from AuthorityEpoch when recovery is required.
type UpperVersion struct {
	AuthorityEpoch  string `json:"authorityEpoch"`
	GrantEpoch      string `json:"grantEpoch"`
	GrantGeneration int64  `json:"grantGeneration"`
}

func (v *UpperVersion) Valid() bool {
	validEpoch := func(value string) bool {
		return value != "" && len(value) <= 128 && strings.IndexFunc(value, unicode.IsSpace) < 0 && !strings.ContainsRune(value, '\x00')
	}
	return v != nil && validEpoch(v.AuthorityEpoch) && validEpoch(v.GrantEpoch) && v.GrantGeneration > 0
}

func (v *UpperVersion) Equal(other *UpperVersion) bool {
	if v == nil || other == nil {
		return v == nil && other == nil
	}
	return *v == *other
}

// ActionTarget identifies the immutable owner and, for Upper commands, the
// observed authority/grant version. It conveys no authorization: command owners
// revalidate both identity and version inside their write transaction.
type ActionTarget struct {
	ExpectedUpperVersion *UpperVersion `json:"expectedUpperVersion,omitempty"`
	FeatureAreaID        string
	DecisionID           string
	SubjectID            string
	ExecutionID          string
	OperationKind        OperationKind
	OperationID          string
	RunID                string
	AgentConversationID  string
}

// Eligibility is a read-only observation. Available is never an authorization
// grant and commands must revalidate the target against their durable guards.
const ReasonUpperWorkerContinuationUnsupported = "UPPER_WORKER_CONTINUATION_UNSUPPORTED"

type Eligibility struct {
	// ReasonCode is a structured observed refusal, never execution authority.
	ReasonCode string `json:"reasonCode,omitempty"`
	// Prerequisite describes an existing read-only eligibility guard, never a new rule.
	Prerequisite string
	Available    bool
	Reason       string
	Target       *ActionTarget
}

// EligibilityReader exposes the exact, durable predicates used to decorate a
// presentation. Implementations must not probe write paths to derive a result.
type EligibilityReader interface {
	CancelEligibility(context.Context, string) (Eligibility, error)
	RecoveryEligibility(context.Context, string) (Eligibility, error)
}
