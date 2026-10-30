package aicontext

import (
	"context"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
)

type bindingSessionService interface {
	CreateSelectionEntry(context.Context, aiprojectselection.ClientBindingReference, string) (aiprojectselection.BindingResult, error)
	SessionBinding(context.Context, aiprojectselection.ClientBindingReference) (aiprojectselection.BindingResult, error)
	OpenWithBinding(context.Context, aiprojectselection.ClientBindingReference, string, string) (aisession.Session, bool, error)
}

func (s *Service) CreateSelectionEntry(ctx context.Context, ref aiprojectselection.ClientBindingReference, requestID string) (aiprojectselection.BindingResult, error) {
	provider, ok := s.sessions.(bindingSessionService)
	if !ok {
		return aiprojectselection.BindingResult{}, aiprojectselection.ErrBindingUnavailable
	}
	return provider.CreateSelectionEntry(ctx, ref, requestID)
}

// SessionBinding is a pure exact association read. It does not bootstrap a
// Project, update usage, or require an AISessionID supplied by the model.
func (s *Service) SessionBinding(ctx context.Context, ref aiprojectselection.ClientBindingReference) (aiprojectselection.BindingResult, error) {
	provider, ok := s.sessions.(bindingSessionService)
	if !ok {
		return aiprojectselection.BindingResult{State: aiprojectselection.BindingUnavailable, Source: "EXACT_SELECTION", Reason: "BINDING_STORE_UNAVAILABLE"}, aiprojectselection.ErrBindingUnavailable
	}
	value, err := provider.SessionBinding(ctx, ref)
	if err != nil {
		return aiprojectselection.BindingResult{State: aiprojectselection.BindingUnavailable, Source: "EXACT_SELECTION", Reason: "BINDING_READ_UNAVAILABLE"}, err
	}
	return value, nil
}

// Legacy v50 compatibility is read-only and cannot initialize an identity.
func (s *Service) OpenProjectWithBinding(ctx context.Context, ref aiprojectselection.ClientBindingReference, aiSessionID, projectID string) (SessionContext, error) {
	if !validID(projectID) {
		return SessionContext{}, ErrInvalidArgument
	}
	if ref.SelectionHandle == "" && ref.HostReference == "" {
		return s.OpenProject(ctx, aiSessionID, projectID)
	}
	provider, ok := s.sessions.(bindingSessionService)
	if !ok {
		return SessionContext{}, aiprojectselection.ErrBindingUnavailable
	}
	session, _, err := provider.OpenWithBinding(ctx, ref, aiSessionID, projectID)
	if err != nil {
		return SessionContext{}, err
	}
	value, err := s.GetSession(ctx, session.ID)
	if err != nil {
		return SessionContext{Session: session}, err
	}
	return value, nil
}
