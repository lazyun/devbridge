package project

import (
	"context"
	"errors"
	"time"
)

// ErrAuthorizationNotFound distinguishes "project has no authorization
// recorded yet" (default, trusted execution off) from a missing project.
var ErrAuthorizationNotFound = errors.New("project execution authorization not found")

// ExecutionAuthorization is an explicit, persistent, user-controlled project
// grant. It is never created by an Agent, a model request, or a TaskPermissions
// manifest. Absence means legacy per-path behavior for every Run.
type ExecutionAuthorization struct {
	ProjectID        string
	TrustedExecution bool
	NetworkAccess    bool
	HeadRevision     int
	AuthorizedAt     time.Time
	UpdatedAt        time.Time
}

// ExecutionAuthorizationRevision is one append-only revision entry. The full
// history is durable and never rewritten; HeadRevision on the authorization
// names the current entry.
type ExecutionAuthorizationRevision struct {
	ProjectID        string
	Revision         int
	TrustedExecution bool
	NetworkAccess    bool
	RecordedAt       time.Time
}

// AuthorizationStore persists the authorization and its append-only history.
type AuthorizationStore interface {
	Get(context.Context, string) (ExecutionAuthorization, error)
	GetRevision(context.Context, string, int) (ExecutionAuthorizationRevision, error)
	// Apply creates the initial record or compares-and-swaps the head
	// revision. A stale expectedRevision fails with ErrConflict.
	Apply(context.Context, ExecutionAuthorization, int) (ExecutionAuthorization, error)
}

// AuthorizationService is the local-user control surface. Its only callers
// are the local console HTTP handlers and Core Run preparation.
type AuthorizationService struct {
	store AuthorizationStore
	now   func() time.Time
}

func NewAuthorizationService(store AuthorizationStore, now func() time.Time) *AuthorizationService {
	if now == nil {
		now = time.Now
	}
	return &AuthorizationService{store: store, now: now}
}

func (s *AuthorizationService) Get(ctx context.Context, projectID string) (ExecutionAuthorization, error) {
	return s.store.Get(ctx, projectID)
}

func (s *AuthorizationService) GetRevision(ctx context.Context, projectID string, revision int) (ExecutionAuthorizationRevision, error) {
	return s.store.GetRevision(ctx, projectID, revision)
}

// Update applies the operator's decision. expectedRevision must match the
// current head (0 for the initial grant), which closes the prepare->revoke->bind
// race for first bindings and rejects concurrent double-submits. AuthorizedAt
// is set at the first grant and preserved by the store afterwards; only
// UpdatedAt advances.
func (s *AuthorizationService) Update(ctx context.Context, projectID string, expectedRevision int, trusted, network bool) (ExecutionAuthorization, error) {
	if projectID == "" || expectedRevision < 0 {
		return ExecutionAuthorization{}, ErrConflict
	}
	now := s.now().UTC()
	value := ExecutionAuthorization{ProjectID: projectID, TrustedExecution: trusted, NetworkAccess: network, HeadRevision: expectedRevision + 1, UpdatedAt: now}
	if expectedRevision == 0 {
		value.AuthorizedAt = now
	}
	return s.store.Apply(ctx, value, expectedRevision)
}
