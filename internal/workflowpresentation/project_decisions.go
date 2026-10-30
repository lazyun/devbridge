package workflowpresentation

import (
	"encoding/json"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
)

// PendingDecision is the client-neutral Project-card inventory of one exact
// current user decision or runtime request. It is assembled from authoritative
// Core projections; consumers must never derive it from a Task phase.
//
// Decision and Options deliberately reuse the existing exact decision and
// capability contracts. In particular, every mutable option retains its
// ActionTarget for the canonical resolver to revalidate.
type PendingDecision struct {
	Kind           string                `json:"kind"`
	Owner          string                `json:"owner"`
	TaskID         string                `json:"taskId,omitempty"`
	FeatureAreaID  string                `json:"featureAreaId,omitempty"`
	SemanticState  string                `json:"semanticState"`
	Decision       *Decision             `json:"decision,omitempty"`
	Options        []Capability          `json:"options"`
	RuntimeOptions *TaskOptions          `json:"runtimeOptions,omitempty"`
	DefaultModel   string                `json:"defaultModel,omitempty"`
	RuntimeRequest *agentruntime.Request `json:"runtimeRequest,omitempty"`
	// RuntimeRequestActionable is true only for the exact current request that
	// the runtime has persisted as WAITING_FOR_USER. Recorded, uncertain, and
	// historical requests remain visible facts, but are deliberately read-only.
	RuntimeRequestActionable bool `json:"runtimeRequestActionable,omitempty"`
	// RuntimeApprovalRequest is a bounded, redacted presentation copy of the
	// exact approval request. It is never accepted as resolver input.
	RuntimeApprovalRequest          json.RawMessage `json:"runtimeApprovalRequest,omitempty"`
	RuntimeApprovalRequestTruncated bool            `json:"runtimeApprovalRequestTruncated,omitempty"`
	RequiredInput                   []string        `json:"requiredInput"`
	// CreatedAt is omitted when the decision creation fact is unavailable.
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	// UpdatedAt is the projection update fact, independent of creation.
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// OptionalDecisionTime excludes Go zero times from optional decision timestamps.
func OptionalDecisionTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
