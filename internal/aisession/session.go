package aisession

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/project"
)

var (
	ErrNotFound               = errors.New("AI session not found")
	ErrConflict               = errors.New("AI session conflict")
	ErrProjectUnbound         = errors.New("AI session Project is not bound")
	ErrInitializationRequired = errors.New("SESSION_INITIALIZATION_REQUIRED")
	ErrInvalidInitialization  = errors.New("invalid session initialization input")
	ErrScopeMismatch          = errors.New("AI session caller scope mismatch")
)

// Session is the BOUND engineering view. Get refuses real UNBOUND rows;
// initialization reads use Initialization with a nullable Project identity.
type Session struct {
	ID        string
	ProjectID string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Store interface {
	Create(context.Context, Session) error
	CreateFromSelection(context.Context, string, string, time.Time) (Session, error)
	Get(context.Context, string) (Session, error)
	List(context.Context) ([]Session, error)
}

type ProjectLookup interface {
	Get(context.Context, string) (project.Project, error)
}

type Service struct {
	store    Store
	projects ProjectLookup
	newID    func() (string, error)
	now      func() time.Time
}

func NewService(store Store, projects ProjectLookup, newID func() (string, error), now func() time.Time) *Service {
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, projects: projects, newID: newID, now: now}
}

// Legacy creation entry points are closed. New identities are created only by InitializeSession.
func (s *Service) Open(ctx context.Context, projectID string) (Session, error) {
	return Session{}, ErrInitializationRequired
}
func (s *Service) OpenFromSelection(ctx context.Context, selectionID string) (Session, error) {
	return Session{}, ErrInitializationRequired
}

func (s *Service) Get(ctx context.Context, id string) (Session, error) {
	if s == nil || s.store == nil {
		return Session{}, errors.New("AI session store is required")
	}
	if id == "" {
		return Session{}, ErrNotFound
	}
	value, err := s.store.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if value.ProjectID == "" {
		return Session{}, ErrProjectUnbound
	}
	return value, nil
}

// List returns the durable AI-session bindings without changing their
// immutable Project snapshots.
func (s *Service) List(ctx context.Context) ([]Session, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("AI session store is required")
	}
	return s.store.List(ctx)
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "ai_session_" + hex.EncodeToString(raw[:]), nil
}

// GetOwned is the API ownership boundary; it does not alter the background Get contract.
func (s *Service) GetOwned(ctx context.Context, id string) (Session, error) {
	if s == nil || s.store == nil {
		return Session{}, ErrScopeMismatch
	}
	if id == "" {
		return Session{}, ErrNotFound
	}
	reader, ok := s.store.(interface {
		GetOwned(context.Context, string) (Session, error)
	})
	if !ok {
		return Session{}, ErrScopeMismatch
	}
	value, err := reader.GetOwned(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if value.ProjectID == "" {
		return Session{}, ErrProjectUnbound
	}
	return value, nil
}
