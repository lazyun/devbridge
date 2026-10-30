package repository

import "time"

// RegistrationState distinguishes a repository that is available for new
// Project work from one retained solely as immutable engineering history.
type RegistrationState string

const (
	RegistrationActive   RegistrationState = "ACTIVE"
	RegistrationUnlinked RegistrationState = "UNLINKED"
)

type BranchKind string

const (
	BranchBranch   BranchKind = "BRANCH"
	BranchDetached BranchKind = "DETACHED"
	BranchUnknown  BranchKind = "UNKNOWN"
)

// State is a point-in-time factual observation of a registered source
// working tree. Reviews use it to distinguish their immutable commit baseline
// from branch identity and uncommitted source-worktree state.
type State struct {
	HeadCommit       string
	BranchKind       BranchKind
	BranchRef        string
	WorkingTreeDirty bool
	CapturedAt       time.Time
}

type Repository struct {
	ID                     string
	ProjectID              string
	Name                   string
	Path                   string
	RegistrationState      RegistrationState
	RegistrationGeneration int64
	UnlinkedAt             *time.Time
	UnlinkRequestID        string
	UnlinkReason           string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func clone(repository Repository) Repository { return repository }
