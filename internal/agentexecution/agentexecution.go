// Package agentexecution defines the durable identity envelope of Coding Agent work.
// Lifecycle remains authoritative in semantic owners and exact Runs.
package agentexecution

import (
	"context"
	"errors"
	"time"
)

type Kind string

const (
	KindReview      Kind = "REVIEW"
	KindDevelopment Kind = "DEVELOPMENT"
	KindInspection  Kind = "INSPECTION"
)

type ProvenanceKind string

const (
	ProvenanceNative        ProvenanceKind = "NATIVE"
	ProvenanceBackfilled    ProvenanceKind = "BACKFILLED"
	ProvenanceLegacyUnknown ProvenanceKind = "LEGACY_UNKNOWN"
)

var ErrNotFound = errors.New("agent execution not found")
var ErrInvalidAssociation = errors.New("invalid Agent Execution association")

type Execution struct {
	ID                  string
	ProjectID           string
	TaskID              string
	FeatureAreaID       string
	Kind                Kind
	SemanticOwnerKind   string
	SemanticOwnerID     string
	ExecutionProposalID string
	ProvenanceKind      ProvenanceKind
	CreatedAt           time.Time
}
type Store interface {
	Get(context.Context, string) (Execution, error)
	ListByProject(context.Context, string) ([]Execution, error)
}
