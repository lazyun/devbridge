package project

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/observability"
)

var (
	ErrNotFound = errors.New("project not found")
	ErrConflict = errors.New("project conflict")
)

type Store interface {
	Create(context.Context, Project) error
	Get(context.Context, string) (Project, error)
	List(context.Context) ([]Project, error)
}

type Service struct {
	store Store
	newID func() (string, error)
	now   func() time.Time
}

func NewService(store Store, newID func() (string, error), now func() time.Time) *Service {
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, newID: newID, now: now}
}

func (s *Service) Create(ctx context.Context, name string) (Project, error) {
	if s.store == nil {
		return Project{}, errors.New("project store is required")
	}
	if name == "" {
		return Project{}, errors.New("project name is required")
	}
	id, err := s.newID()
	if err != nil {
		return Project{}, fmt.Errorf("allocate project id: %w", err)
	}
	now := s.now().UTC()
	value := Project{ID: id, Name: name, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Create(ctx, value); err != nil {
		return Project{}, fmt.Errorf("store project: %w", err)
	}
	observability.Info(observability.WithContext(ctx, observability.Correlation{ProjectID: value.ID}), "project", "project.created", "durable Project created", nil)
	return value, nil
}

func (s *Service) Get(ctx context.Context, id string) (Project, error) { return s.store.Get(ctx, id) }
func (s *Service) List(ctx context.Context) ([]Project, error)         { return s.store.List(ctx) }

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "project_" + hex.EncodeToString(raw[:]), nil
}
