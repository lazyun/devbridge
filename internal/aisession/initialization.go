package aisession

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
)

const ProjectSelectionRequired = "PROJECT_SELECTION_REQUIRED"
const ProjectBound = "PROJECT_BOUND"

// Initialization is the actual Session row. NULL Project is distinct from an
// unavailable Project referenced by a previously bound Session.
type Initialization struct {
	AISessionID    string          `json:"aiSessionId"`
	State          string          `json:"state"`
	BoundProjectID *string         `json:"boundProjectId"`
	CreatedAt      time.Time       `json:"-"`
	UpdatedAt      time.Time       `json:"-"`
	Receipt        *RequestReceipt `json:"receipt,omitempty"`
}
type RequestReceipt struct {
	RequestID     string  `json:"requestId"`
	OperationKind string  `json:"operationKind"`
	AISessionID   string  `json:"aiSessionId"`
	ProjectID     *string `json:"projectId"`
	Replayed      bool    `json:"replayed"`
}
type InitializationStore interface {
	InitializeSession(context.Context, string, string, string, time.Time) (Initialization, error)
	ReadInitialization(context.Context, string, string) (Initialization, error)
	BindSessionProject(context.Context, string, string, string, string, string, time.Time) (Session, RequestReceipt, bool, error)
}

func ValidRequestID(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, unicode.IsSpace) < 0 && !strings.ContainsRune(value, 0)
}
func (s *Service) InitializeSession(ctx context.Context, ref aiprojectselection.ClientBindingReference, requestID, sessionID string) (Initialization, error) {
	if s == nil || !ref.Valid() || (requestID == "") == (sessionID == "") || (requestID != "" && !ValidRequestID(requestID)) || (sessionID != "" && !ValidRequestID(sessionID)) {
		return Initialization{}, ErrInvalidInitialization
	}
	store, ok := s.store.(InitializationStore)
	if !ok {
		return Initialization{}, ErrInitializationRequired
	}
	if sessionID != "" {
		return store.ReadInitialization(ctx, ref.CallerScope, sessionID)
	}
	id, err := s.newID()
	if err != nil {
		return Initialization{}, err
	}
	return store.InitializeSession(ctx, ref.CallerScope, requestID, id, s.now().UTC())
}
func (s *Service) BindProject(ctx context.Context, ref aiprojectselection.ClientBindingReference, sessionID, projectID, requestID string) (Session, RequestReceipt, bool, error) {
	if s == nil || !ref.Valid() || !ValidRequestID(sessionID) || !ValidRequestID(projectID) || !ValidRequestID(requestID) {
		return Session{}, RequestReceipt{}, false, ErrInvalidInitialization
	}
	store, ok := s.store.(InitializationStore)
	if !ok {
		return Session{}, RequestReceipt{}, false, ErrInitializationRequired
	}
	selectionID, err := randomID()
	if err != nil {
		return Session{}, RequestReceipt{}, false, err
	}
	return store.BindSessionProject(ctx, ref.CallerScope, requestID, sessionID, projectID, "project_selection_"+strings.TrimPrefix(selectionID, "ai_session_"), s.now().UTC())
}
