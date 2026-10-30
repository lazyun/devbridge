// Package aiprojectselection models an explicit user Project choice made
// during AI bootstrap. It is deliberately separate from Console Project view
// selection.
package aiprojectselection

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/project"
)

type Status string

const (
	StatusRecorded Status = "RECORDED"
	StatusConsumed Status = "CONSUMED"
)

var (
	ErrNotFound        = errors.New("AI Project selection not found")
	ErrConflict        = errors.New("AI Project selection conflict")
	ErrAlreadyConsumed = errors.New("AI Project selection already consumed")
)

// Selection is a durable, single-use record of an explicit user Project
// choice. AISessionID is populated only when consumption creates the session.
type Selection struct {
	ID          string
	ProjectID   string
	Status      Status
	AISessionID string
	CreatedAt   time.Time
	ConsumedAt  *time.Time
}

type Store interface {
	Create(context.Context, Selection) error
	Get(context.Context, string) (Selection, error)
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

// Record persists an explicit AI-bootstrap Project choice without creating an
// AI session or consulting Console selection.
func (s *Service) Record(ctx context.Context, projectID string) (Selection, error) {
	if s == nil || s.store == nil || s.projects == nil {
		return Selection{}, errors.New("AI Project selection store and project lookup are required")
	}
	if projectID == "" {
		return Selection{}, errors.New("AI Project selection project id is required")
	}
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return Selection{}, fmt.Errorf("load AI Project selection project: %w", err)
	}
	id, err := s.newID()
	if err != nil {
		return Selection{}, fmt.Errorf("allocate AI Project selection id: %w", err)
	}
	value := Selection{ID: id, ProjectID: projectID, Status: StatusRecorded, CreatedAt: s.now().UTC()}
	if err := s.store.Create(ctx, value); err != nil {
		return Selection{}, fmt.Errorf("store AI Project selection: %w", err)
	}
	return value, nil
}

func (s *Service) Get(ctx context.Context, id string) (Selection, error) {
	if s == nil || s.store == nil {
		return Selection{}, errors.New("AI Project selection store is required")
	}
	if id == "" {
		return Selection{}, ErrNotFound
	}
	return s.store.Get(ctx, id)
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "project_selection_" + hex.EncodeToString(raw[:]), nil
}
