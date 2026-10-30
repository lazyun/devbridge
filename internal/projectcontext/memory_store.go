package projectcontext

import (
	"context"
	"sync"
	"time"
)

type MemoryStore struct {
	mu   sync.Mutex
	data map[string]ProjectContext
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{data: map[string]ProjectContext{}} }

func (s *MemoryStore) EnsureRequired(_ context.Context, projectID string, now time.Time) (ProjectContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[projectID]
	if !ok {
		value = ProjectContext{ProjectID: projectID, Status: StatusRequired, CreatedAt: now, UpdatedAt: now}
	} else if value.Status == StatusFailed && value.ContextFingerprint == "" {
		value.Status, value.RootGuidanceIdentity, value.ContextFingerprint, value.Diagnostics, value.Result, value.ResultArtifactRef, value.UpdatedAt = StatusRequired, "", "", []string{}, "", "", now
	}
	s.data[projectID] = value
	return value, nil
}

func (s *MemoryStore) Get(_ context.Context, projectID string) (ProjectContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[projectID]
	if !ok {
		return ProjectContext{}, ErrNotFound
	}
	value.Diagnostics = append([]string(nil), value.Diagnostics...)
	return value, nil
}

func (s *MemoryStore) MarkNoGuidance(_ context.Context, projectID string, now time.Time) (ProjectContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[projectID]
	if !ok {
		return ProjectContext{}, ErrNotFound
	}
	if value.Status == StatusNoGuidance {
		return value, nil
	}
	if value.Status != StatusRequired {
		return ProjectContext{}, ErrConflict
	}
	value.Status, value.LatestProbeID, value.Result, value.ResultArtifactRef, value.RootGuidanceIdentity, value.UpdatedAt = StatusNoGuidance, "", "", "", "", now
	s.data[projectID] = value
	return value, nil
}

func (s *MemoryStore) MarkBootstrapped(_ context.Context, projectID string, status Status, fingerprint, identity, guidance string, diagnostics []string, now time.Time) (ProjectContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[projectID]
	if !ok {
		return ProjectContext{}, ErrNotFound
	}
	value.Status = status
	value.ContextFingerprint = fingerprint
	value.RootGuidanceIdentity = identity
	value.Result = guidance
	value.ResultArtifactRef = ""
	value.Diagnostics = append([]string(nil), diagnostics...)
	value.UpdatedAt = now
	s.data[projectID] = value
	return value, nil
}

func (s *MemoryStore) MarkRunning(_ context.Context, projectID, probeID string, now time.Time) (ProjectContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[projectID]
	if !ok {
		return ProjectContext{}, ErrNotFound
	}
	if value.Status == StatusRunning {
		return ProjectContext{}, ErrConflict
	}
	value.Status, value.LatestProbeID, value.UpdatedAt = StatusRunning, probeID, now
	s.data[projectID] = value
	return value, nil
}

func (s *MemoryStore) MarkFinished(_ context.Context, projectID, probeID string, status Status, result, artifactRef, identity string, now time.Time) (ProjectContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[projectID]
	if !ok {
		return ProjectContext{}, ErrNotFound
	}
	if value.Status != StatusRunning || value.LatestProbeID != probeID {
		return ProjectContext{}, ErrConflict
	}
	value.Status, value.Result, value.ResultArtifactRef, value.RootGuidanceIdentity, value.UpdatedAt = status, result, artifactRef, identity, now
	s.data[projectID] = value
	return value, nil
}

func (s *MemoryStore) RecoverRunning(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, value := range s.data {
		if value.Status == StatusRunning {
			value.Status, value.UpdatedAt = StatusFailed, now
			s.data[key] = value
		}
	}
	return nil
}
