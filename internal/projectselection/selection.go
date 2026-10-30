package projectselection

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/project"
)

var ErrNotSelected = errors.New("no project is selected")

// Selection is local Console/UI view state. AI bootstrap selection recording
// and session_open(selectionId) never consult it.
type Selection struct {
	CurrentSelectedProjectID string
	UpdatedAt                time.Time
}

type Store interface {
	Get(context.Context) (Selection, error)
	Set(context.Context, Selection) error
}

type ProjectLookup interface {
	Get(context.Context, string) (project.Project, error)
}

type Service struct {
	store    Store
	projects ProjectLookup
	now      func() time.Time
}

func NewService(store Store, projects ProjectLookup, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	value := &Service{store: store, projects: projects, now: now}
	return value
}

// Select changes only the Console Project view.
func (s *Service) Select(ctx context.Context, projectID string) (Selection, error) {
	if s == nil || s.store == nil || s.projects == nil {
		return Selection{}, errors.New("project selection store and project lookup are required")
	}
	if projectID == "" {
		return Selection{}, errors.New("selected project id is required")
	}
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return Selection{}, fmt.Errorf("load selected project: %w", err)
	}
	value := Selection{CurrentSelectedProjectID: projectID, UpdatedAt: s.now().UTC()}
	if err := s.store.Set(ctx, value); err != nil {
		return Selection{}, fmt.Errorf("store project selection: %w", err)
	}
	return value, nil
}

func (s *Service) Current(ctx context.Context) (Selection, error) {
	if s == nil || s.store == nil {
		return Selection{}, errors.New("project selection store is required")
	}
	return s.store.Get(ctx)
}
