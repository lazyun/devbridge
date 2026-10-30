package projectcontext

import (
	"context"
	"errors"
	"time"
)

type Status string

const (
	StatusRequired   Status = "REQUIRED"
	StatusRunning    Status = "RUNNING"
	StatusReady      Status = "READY"
	StatusNoGuidance Status = "NO_GUIDANCE"
	StatusFailed     Status = "FAILED"
)

var (
	ErrNotFound = errors.New("project context not found")
	ErrConflict = errors.New("project context conflict")
)

type ProjectContext struct {
	ProjectID            string
	Status               Status
	RootGuidanceIdentity string
	ContextFingerprint   string
	Diagnostics          []string
	LatestProbeID        string
	Result               string
	ResultArtifactRef    string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// SnapshotUsable reports whether an authoritative Project Context snapshot is
// available. A refresh is an operation over a snapshot, not a replacement for
// it: RUNNING and FAILED therefore remain usable when a previously published
// fingerprint is present.
func (v ProjectContext) SnapshotUsable() bool {
	return v.Status == StatusReady || v.Status == StatusNoGuidance || v.ContextFingerprint != ""
}

func (v ProjectContext) RefreshInProgress() bool {
	return v.Status == StatusRunning && v.ContextFingerprint != ""
}

func (v ProjectContext) RefreshFailed() bool {
	return v.Status == StatusFailed && v.ContextFingerprint != ""
}

type Store interface {
	EnsureRequired(context.Context, string, time.Time) (ProjectContext, error)
	Get(context.Context, string) (ProjectContext, error)
	MarkRunning(context.Context, string, string, time.Time) (ProjectContext, error)
	MarkFinished(context.Context, string, string, Status, string, string, string, time.Time) (ProjectContext, error)
	RecoverRunning(context.Context, time.Time) error
	MarkNoGuidance(context.Context, string, time.Time) (ProjectContext, error)
	MarkBootstrapped(context.Context, string, Status, string, string, string, []string, time.Time) (ProjectContext, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

// EnsureRequired is deliberately metadata-only. It neither reads a
// repository nor starts Codex. READY and NO_GUIDANCE are usable deterministic
// terminal states and remain current until explicitly invalidated.
func (s *Service) EnsureRequired(ctx context.Context, projectID string) (ProjectContext, error) {
	if s == nil || s.store == nil {
		return ProjectContext{}, errors.New("project context store is required")
	}
	if projectID == "" {
		return ProjectContext{}, errors.New("project context project id is required")
	}
	return s.store.EnsureRequired(ctx, projectID, s.now().UTC())
}

func (s *Service) Get(ctx context.Context, projectID string) (ProjectContext, error) {
	if s == nil || s.store == nil {
		return ProjectContext{}, errors.New("project context store is required")
	}
	return s.store.Get(ctx, projectID)
}

func (s *Service) MarkNoGuidance(ctx context.Context, projectID string) (ProjectContext, error) {
	if projectID == "" {
		return ProjectContext{}, errors.New("project context project id is required")
	}
	return s.store.MarkNoGuidance(ctx, projectID, s.now().UTC())
}

// MarkBootstrapped publishes a validated, durable Project Module Map
// snapshot. READY means guidance was present; NO_GUIDANCE means the same
// usable map was produced by lightweight production discovery.
func (s *Service) MarkBootstrapped(ctx context.Context, projectID string, status Status, fingerprint, guidanceIdentity, preservedGuidance string, diagnostics []string) (ProjectContext, error) {
	if projectID == "" || fingerprint == "" || (status != StatusReady && status != StatusNoGuidance) {
		return ProjectContext{}, errors.New("invalid Project Context bootstrap result")
	}
	return s.store.MarkBootstrapped(ctx, projectID, status, fingerprint, guidanceIdentity, preservedGuidance, append([]string(nil), diagnostics...), s.now().UTC())
}

func (s *Service) MarkRunning(ctx context.Context, projectID, probeID string) (ProjectContext, error) {
	if projectID == "" || probeID == "" {
		return ProjectContext{}, errors.New("project and probe ids are required")
	}
	return s.store.MarkRunning(ctx, projectID, probeID, s.now().UTC())
}

func (s *Service) MarkFinished(ctx context.Context, projectID, probeID string, status Status, result, artifactRef, guidanceIdentity string) (ProjectContext, error) {
	if status != StatusReady && status != StatusNoGuidance && status != StatusFailed {
		return ProjectContext{}, errors.New("invalid terminal project context status")
	}
	return s.store.MarkFinished(ctx, projectID, probeID, status, result, artifactRef, guidanceIdentity, s.now().UTC())
}

func (s *Service) RecoverRunning(ctx context.Context) error {
	return s.store.RecoverRunning(ctx, s.now().UTC())
}
