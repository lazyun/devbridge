// Package projectregistration owns the production application operation that
// registers one local Git repository as a Dev Bridge Project.
package projectregistration

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
)

var (
	ErrInvalidArgument   = errors.New("invalid project registration")
	ErrAlreadyRegistered = errors.New("repository is already registered")
)

type Input struct {
	RequestID      string            `json:"requestId,omitempty"`
	ProjectID      string            `json:"projectId,omitempty"`
	Repositories   []RepositoryInput `json:"repositories,omitempty"`
	Name           string
	RepositoryPath string
}

type Registration struct {
	Repositories []repository.Repository
	RequestID    string
	Project      project.Project
	Repository   repository.Repository
}

// AlreadyRegisteredError identifies the existing durable registration so a
// product shell can guide the user back to it without creating duplicate rows.
type AlreadyRegisteredError struct {
	Registration Registration
}

func (e *AlreadyRegisteredError) Error() string {
	return fmt.Sprintf("%v as project %q", ErrAlreadyRegistered, e.Registration.Project.Name)
}

func (e *AlreadyRegisteredError) Unwrap() error { return ErrAlreadyRegistered }

type ProjectService interface {
	Create(context.Context, string) (project.Project, error)
	Get(context.Context, string) (project.Project, error)
}

type RepositoryService interface {
	ResolveRegistrationPath(context.Context, string) (string, error)
	RegisterInProject(context.Context, string, string, string) (repository.Repository, error)
	List(context.Context) ([]repository.Repository, error)
}

type Service struct {
	atomic       AtomicStore
	projects     ProjectService
	repositories RepositoryService
	mu           sync.Mutex
}

func NewService(projects ProjectService, repositories RepositoryService, stores ...AtomicStore) (*Service, error) {
	if projects == nil || repositories == nil {
		return nil, errors.New("project and repository services are required")
	}
	s := &Service{projects: projects, repositories: repositories}
	if len(stores) > 0 {
		s.atomic = stores[0]
	}
	return s, nil
}

// Register validates before creating durable state, serializes the duplicate
// check with creation, and creates no Module, FeatureArea, Project Context,
// Codex configuration, AI session, Task, Review, or execution state.
func (s *Service) Register(ctx context.Context, input Input) (Registration, error) {
	if input.RequestID != "" || input.RepositoryPath == "" || input.ProjectID != "" || input.Repositories != nil {
		return s.registerAggregate(ctx, input)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Registration{}, fmt.Errorf("%w: project name is required", ErrInvalidArgument)
	}
	canonical, err := s.repositories.ResolveRegistrationPath(ctx, input.RepositoryPath)
	if err != nil {
		return Registration{}, err
	}

	if s.atomic != nil {
		hash, err := registrationHash(input)
		if err != nil {
			return Registration{}, err
		}
		return s.atomic.RegisterAggregate(ctx, Input{RequestID: "legacy_" + uuid.NewString(), Name: name, Repositories: []RepositoryInput{{Path: canonical, Alias: filepath.Base(canonical)}}}, hash)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	repositories, err := s.repositories.List(ctx)
	if err != nil {
		return Registration{}, fmt.Errorf("list registered repositories: %w", err)
	}
	for _, existing := range repositories {
		if existing.Path != canonical {
			continue
		}
		projectValue, getErr := s.projects.Get(ctx, existing.ProjectID)
		if getErr != nil {
			return Registration{}, fmt.Errorf("load project for registered repository: %w", getErr)
		}
		registration := Registration{Project: projectValue, Repository: existing}
		return registration, &AlreadyRegisteredError{Registration: registration}
	}

	projectValue, err := s.projects.Create(ctx, name)
	if err != nil {
		return Registration{}, err
	}
	repositoryValue, err := s.repositories.RegisterInProject(ctx, projectValue.ID, canonical, filepath.Base(canonical))
	if err != nil {
		// Repository validation and duplicate detection happened before the
		// Project write. A failure here is limited to an infrastructure or ID
		// allocation failure and is surfaced rather than masked.
		return Registration{Project: projectValue}, err
	}
	return Registration{Project: projectValue, Repository: repositoryValue}, nil
}
