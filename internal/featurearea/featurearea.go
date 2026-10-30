// Package featurearea owns the durable product-level engineering capability
// that precedes executable planning and execution details. A Feature Area is a
// Project-owned product capability, not a conversation topic or a module.
package featurearea

import (
	"encoding/json"
	"time"
)

// Status is the canonical v0.1 Feature Area lifecycle.
type Status string

const (
	// StatusDiscovering means the product-capability boundary is not yet
	// sufficiently established. Reviews are allowed; Tasks are not.
	StatusDiscovering Status = "DISCOVERING"
	// StatusActive means this is an established, valid current product
	// capability. It does not mean work is currently executing.
	StatusActive Status = "ACTIVE"
	// StatusDecomposed marks historical split provenance. The object is no
	// longer an active capability or container.
	StatusDecomposed Status = "DECOMPOSED"
	// StatusRetired is reserved for a product capability that is actually
	// retired, not merely idle or concluded.
	StatusRetired  Status = "RETIRED"
	StatusRetiring Status = "RETIRING"
)

// RelationKind records bounded provenance between Feature Areas. v0.1 only
// defines SPLIT_INTO; no generic graph framework exists.
type RelationKind string

const RelationSplitInto RelationKind = "SPLIT_INTO"

// Relation is immutable split provenance: source was split into target.
// Child Feature Areas remain independent Project-owned objects, not
// lifecycle-owned children.
type Relation struct {
	SourceFeatureAreaID string
	TargetFeatureAreaID string
	Kind                RelationKind
	CreatedAt           time.Time
}

// FeatureArea is Project-owned. CreatedByAISessionID is immutable creation
// provenance; any AI session bound to the same Project may subsequently read
// and work with the Feature Area.
type Retirement struct {
	RequestID            string     `json:"requestId"`
	RequestedAt          time.Time  `json:"requestedAt"`
	CompletedAt          *time.Time `json:"completedAt,omitempty"`
	Summary              string     `json:"summary"`
	RemainingTasks       int        `json:"remainingTasks"`
	EngineeringAvailable bool       `json:"engineeringAvailable"`
	LastError            string     `json:"lastError,omitempty"`
}

type FeatureArea struct {
	MergedCommit string
	MergedAt     *time.Time

	CurrentWorkspaceID   string
	Retirement           *Retirement
	LegacyReadOnly       bool
	LegacyStatus         string
	ID                   string
	ProjectID            string
	CreatedByAISessionID string
	Title                string
	ProblemStatement     string
	Status               Status
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// CreationReceipt is the durable root identity for a composed Feature Area
// creation request. Prepared is the exact, already-admitted execution input;
// it is immutable after the root Feature Area has been written.
type CreationReceipt struct {
	AISessionID, ProjectID, Command, RequestID, PayloadSHA256, ContractVersion string
	Prepared                                                                   json.RawMessage
	FeatureAreaID, TaskID, ReviewID, RunID                                     string
	ReviewAdmitted                                                             bool
	FailedStep, ErrorCode, ErrorMessage                                        string
}
