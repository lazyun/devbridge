// Package repositoryunlink owns the durable safety contract for removing a
// Repository from a Project's active resource topology. It never deletes a
// repository, worktree, Git ref, or historical engineering fact.
package repositoryunlink

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/repository"
)

var (
	ErrInvalidInput       = errors.New("invalid repository unlink input")
	ErrBlocked            = errors.New("REPOSITORY_UNLINK_BLOCKED")
	ErrStale              = errors.New("REPOSITORY_UNLINK_STALE")
	ErrRequestConflict    = errors.New("REPOSITORY_UNLINK_REQUEST_CONFLICT")
	ErrAlreadyUnlinked    = errors.New("REPOSITORY_ALREADY_UNLINKED")
	ErrRepositoryMismatch = errors.New("REPOSITORY_PROJECT_MISMATCH")
)

type Blocker struct {
	BlockerKind         string    `json:"blockerKind"`
	EntityType          string    `json:"entityType"`
	EntityID            string    `json:"entityId"`
	OwnerType           string    `json:"ownerType"`
	OwnerID             string    `json:"ownerId"`
	Status              string    `json:"status"`
	ReasonCode          string    `json:"reasonCode"`
	UserFacingSummary   string    `json:"userFacingSummary"`
	AvailableResolution string    `json:"availableResolution"`
	CreatedAt           time.Time `json:"createdAt,omitempty"`
	UpdatedAt           time.Time `json:"updatedAt,omitempty"`
}

type Reference struct {
	EntityType string `json:"entityType"`
	EntityID   string `json:"entityId"`
	Summary    string `json:"summary"`
}

type Eligibility struct {
	ProjectID               string                       `json:"projectId"`
	RepositoryID            string                       `json:"repositoryId"`
	RegistrationState       repository.RegistrationState `json:"registrationState"`
	RegistrationGeneration  int64                        `json:"registrationGeneration"`
	ProjectTopologyRevision int64                        `json:"projectTopologyRevision"`
	CanUnlink               bool                         `json:"canUnlink"`
	Blockers                []Blocker                    `json:"blockers"`
	InformationalReferences []Reference                  `json:"informationalReferences"`
	EvaluatedAt             time.Time                    `json:"evaluatedAt"`
}

type CommitInput struct {
	RequestID                string `json:"requestId"`
	ProjectID                string `json:"projectId"`
	RepositoryID             string `json:"repositoryId"`
	ExpectedGeneration       int64  `json:"expectedRegistrationGeneration"`
	ExpectedTopologyRevision int64  `json:"expectedTopologyRevision"`
	Reason                   string `json:"reason"`
}

func (in CommitInput) Validate() error {
	if strings.TrimSpace(in.RequestID) == "" || len(in.RequestID) > 128 || in.ProjectID == "" || in.RepositoryID == "" || in.ExpectedGeneration < 1 || in.ExpectedTopologyRevision < 1 || len(in.Reason) > 4096 {
		return ErrInvalidInput
	}
	return nil
}

// Store implements both read-only preparation and the canonical atomic commit.
// A caller must not infer safety from a previous Eligibility result: Commit
// always recalculates blockers under the database writer transaction.
type Store interface {
	Eligibility(context.Context, string, string) (Eligibility, error)
	Commit(context.Context, CommitInput) (Eligibility, error)
}

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }
func (s *Service) Prepare(ctx context.Context, projectID, repositoryID string) (Eligibility, error) {
	if s == nil || s.store == nil || projectID == "" || repositoryID == "" {
		return Eligibility{}, ErrInvalidInput
	}
	return s.store.Eligibility(ctx, projectID, repositoryID)
}
func (s *Service) Commit(ctx context.Context, in CommitInput) (Eligibility, error) {
	if s == nil || s.store == nil {
		return Eligibility{}, ErrInvalidInput
	}
	if err := in.Validate(); err != nil {
		return Eligibility{}, err
	}
	return s.store.Commit(ctx, in)
}
