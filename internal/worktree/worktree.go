package worktree

import (
	"context"
	"time"
)

type Status string

const (
	StatusReady   Status = "READY"
	StatusRemoved Status = "REMOVED"
	StatusMissing Status = "MISSING"
)

type Worktree struct {
	ID             string
	RepositoryID   string
	RepositoryPath string
	RunID          string
	Path           string
	Branch         string
	BaseCommit     string
	Status         Status
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateInput struct {
	PreserveOnFailure bool
	MemberID          string
	MemberVersion     int
	MemberAlias       string
	RepositoryID      string
	RepositoryPath    string
	FeatureAreaID     string
	// OwnerKind/OwnerID form the physical-worktree namespace for owner variants.
	// Empty preserves the historical FeatureAreaID contract.
	OwnerKind   string
	OwnerID     string
	RunID       string
	WorkspaceID string
	BaseCommit  string
}

type Store interface {
	Create(context.Context, Worktree) error
	Get(context.Context, string) (Worktree, error)
	List(context.Context) ([]Worktree, error)
	Update(context.Context, string, func(*Worktree) error) (Worktree, error)
}
