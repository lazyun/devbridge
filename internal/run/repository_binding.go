package run

import (
	"time"

	"github.com/lazyun/devbridge/internal/workset"
)

// RepositoryBinding records this Run's use of an exact canonical Workspace
// member. Worktree.RunID is creation provenance, not current-use authority.
type RepositoryBinding struct {
	RunID    string `json:"runId"`
	ReviewID string `json:"reviewId"`
	// ExecutionID is populated for Implementation and Verification bindings.
	// Exactly one of ReviewID and ExecutionID owns a durable binding.
	ExecutionID string `json:"executionId"`
	// InspectionID is the concrete PROJECT_INSPECTION runtime owner. It is
	// mutually exclusive with ReviewID and ExecutionID.
	InspectionID  string `json:"inspectionId,omitempty"`
	ProjectID     string `json:"projectId"`
	FeatureAreaID string `json:"featureAreaId"`
	WorkspaceID   string `json:"workspaceId"`
	RepositoryID  string `json:"repositoryId"`
	MemberID      string `json:"memberId"`
	MemberVersion int    `json:"memberVersion"`
	OriginCommit  string `json:"originCommit"`
	WorktreeID    string `json:"worktreeId,omitempty"`
	Preparation   string `json:"preparation"`
	Failure       string `json:"failure,omitempty"`

	// Runtime identity is attached once preparation has resolved a planned
	// member to a managed worktree. These facts are immutable after they have
	// been recorded; zero values retain the v1 scalar-binding representation.
	RepositoryAlias     string             `json:"repositoryAlias,omitempty"`
	StartHEAD           string             `json:"startHead,omitempty"`
	StartSnapshot       string             `json:"startSnapshot,omitempty"`
	IntendedRead        bool               `json:"intendedRead,omitempty"`
	IntendedWrite       workset.WriteScope `json:"intendedWrite,omitempty"`
	RuntimeRootIdentity string             `json:"runtimeRootIdentity,omitempty"`

	// Observation is a single terminal per-member post-dispatch record. It is
	// deliberately separate from the intended runtime identity above.
	EndHEAD          string     `json:"endHead,omitempty"`
	EndSnapshot      string     `json:"endSnapshot,omitempty"`
	EndRootIdentity  string     `json:"endRootIdentity,omitempty"`
	ChangedPaths     []string   `json:"changedPaths,omitempty"`
	DiffDigest       string     `json:"diffDigest,omitempty"`
	ObservationState string     `json:"observationState,omitempty"`
	ObservedAt       *time.Time `json:"observedAt,omitempty"`
}

// RepositoryObservation is the immutable terminal observation for one member
// of a RuntimeWorkset. It cannot introduce a repository or alter intended
// scope; those identities are loaded from RepositoryBinding by the store.
type RepositoryObservation struct {
	RunID            string
	RepositoryID     string
	EndHEAD          string
	EndSnapshot      string
	EndRootIdentity  string
	ChangedPaths     []string
	DiffDigest       string
	ObservationState string
	ObservedAt       time.Time
}
