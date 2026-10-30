package workspace

import "time"

// Member is a resource fact, never an operation authorization. OriginCommit and
// identity version remain immutable when subsequent Tasks reuse this member.
type Member struct {
	ID                 string    `json:"id"`
	WorkspaceID        string    `json:"workspaceId"`
	RepositoryID       string    `json:"repositoryId"`
	Version            int       `json:"version"`
	Alias              string    `json:"alias"`
	OriginCommit       string    `json:"originCommit"`
	OriginRef          string    `json:"originRef,omitempty"`
	DevelopmentCycleID string    `json:"developmentCycleId,omitempty"`
	WorktreeID         string    `json:"worktreeId,omitempty"`
	Preparation        string    `json:"preparation"` // PENDING, READY, FAILED, UNKNOWN
	Failure            string    `json:"failure,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}
