package projectregistration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
)

type projectStub struct {
	values map[string]project.Project
	create int
}

func (s *projectStub) Create(_ context.Context, name string) (project.Project, error) {
	s.create++
	value := project.Project{ID: "project-new", Name: name, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0)}
	s.values[value.ID] = value
	return value, nil
}

func (s *projectStub) Get(_ context.Context, id string) (project.Project, error) {
	value, ok := s.values[id]
	if !ok {
		return project.Project{}, project.ErrNotFound
	}
	return value, nil
}

type repositoryStub struct {
	canonical  string
	values     []repository.Repository
	register   int
	resolveErr error
}

func (s *repositoryStub) ResolveRegistrationPath(context.Context, string) (string, error) {
	return s.canonical, s.resolveErr
}

func (s *repositoryStub) RegisterInProject(_ context.Context, projectID, path, name string) (repository.Repository, error) {
	s.register++
	value := repository.Repository{ID: "repo-new", ProjectID: projectID, Path: path, Name: name}
	s.values = append(s.values, value)
	return value, nil
}

func (s *repositoryStub) List(context.Context) ([]repository.Repository, error) {
	return append([]repository.Repository(nil), s.values...), nil
}

func TestRegisterCreatesOnlyProjectAndCanonicalRepositoryBinding(t *testing.T) {
	projects := &projectStub{values: map[string]project.Project{}}
	repositories := &repositoryStub{canonical: filepath.Join(string(filepath.Separator), "source", "repo")}
	service, err := NewService(projects, repositories)
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.Register(context.Background(), Input{Name: "  Dev Bridge  ", RepositoryPath: "/selected/child"})
	if err != nil {
		t.Fatal(err)
	}
	if value.Project.Name != "Dev Bridge" || value.Repository.ProjectID != value.Project.ID || value.Repository.Path != repositories.canonical || value.Repository.Name != "repo" {
		t.Fatalf("registration = %+v", value)
	}
	if projects.create != 1 || repositories.register != 1 {
		t.Fatalf("create/register = %d/%d", projects.create, repositories.register)
	}
}

func TestRegisterRejectsDuplicateBeforeCreatingProject(t *testing.T) {
	existingProject := project.Project{ID: "project-existing", Name: "Existing"}
	existingRepository := repository.Repository{ID: "repo-existing", ProjectID: existingProject.ID, Path: "/source/repo", Name: "repo"}
	projects := &projectStub{values: map[string]project.Project{existingProject.ID: existingProject}}
	repositories := &repositoryStub{canonical: existingRepository.Path, values: []repository.Repository{existingRepository}}
	service, err := NewService(projects, repositories)
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.Register(context.Background(), Input{Name: "Duplicate", RepositoryPath: "/source/repo/child"})
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("error = %v", err)
	}
	if value.Project.ID != existingProject.ID || value.Repository.ID != existingRepository.ID {
		t.Fatalf("existing registration = %+v", value)
	}
	if projects.create != 0 || repositories.register != 0 {
		t.Fatalf("duplicate created state: %d/%d", projects.create, repositories.register)
	}
}

func TestRegisterValidatesBeforeCreatingProject(t *testing.T) {
	projects := &projectStub{values: map[string]project.Project{}}
	repositories := &repositoryStub{resolveErr: repository.ErrNotGit}
	service, err := NewService(projects, repositories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register(context.Background(), Input{Name: "No Git", RepositoryPath: "/tmp/plain"}); !errors.Is(err, repository.ErrNotGit) {
		t.Fatalf("error = %v", err)
	}
	if projects.create != 0 {
		t.Fatalf("invalid repository created %d projects", projects.create)
	}
}
