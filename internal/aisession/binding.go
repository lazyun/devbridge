package aisession

import (
	"context"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
)

// BindingStore is optional so existing narrow session stores retain their
// contract. It now permits only historical BOUND association reads.
type BindingStore interface {
	EnsureBindingEntry(context.Context, aiprojectselection.ClientBindingReference, string, string, time.Time) (aiprojectselection.BindingResult, error)
	ReadBinding(context.Context, aiprojectselection.ClientBindingReference) (aiprojectselection.BindingResult, error)
	OpenBinding(context.Context, aiprojectselection.ClientBindingReference, string, string, string, string, time.Time) (Session, bool, error)
}

// New selection handles are retired; only historical exact reads remain.
func (s *Service) CreateSelectionEntry(ctx context.Context, ref aiprojectselection.ClientBindingReference, requestID string) (aiprojectselection.BindingResult, error) {
	return aiprojectselection.BindingResult{}, ErrInitializationRequired
}

func (s *Service) SessionBinding(ctx context.Context, ref aiprojectselection.ClientBindingReference) (aiprojectselection.BindingResult, error) {
	if s == nil || !ref.Valid() {
		return aiprojectselection.BindingResult{}, aiprojectselection.ErrBindingInvalid
	}
	store, ok := s.store.(BindingStore)
	if !ok {
		return aiprojectselection.BindingResult{}, aiprojectselection.ErrBindingUnavailable
	}
	return store.ReadBinding(ctx, ref)
}

func (s *Service) OpenWithBinding(ctx context.Context, ref aiprojectselection.ClientBindingReference, existingSessionID, projectID string) (Session, bool, error) {
	if s == nil || !ref.Valid() {
		return Session{}, false, aiprojectselection.ErrBindingInvalid
	}
	store, ok := s.store.(BindingStore)
	if !ok {
		return Session{}, false, aiprojectselection.ErrBindingUnavailable
	}
	return store.OpenBinding(ctx, ref, existingSessionID, projectID, "", "", time.Time{})
}
