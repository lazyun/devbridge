// Package aicontext exposes the narrow, AI-facing read/control context without
// depending on any transport. It is the single scope-enforcement boundary for
// AI clients such as MCP adapters.
package aicontext

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/task"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 100
	MaxOffset        = 1_000_000
	MaxIDBytes       = 128
)

var (
	ErrInvalidArgument   = errors.New("invalid AI context argument")
	ErrNotFound          = errors.New("AI context entity not found")
	ErrSelectionConsumed = errors.New("AI Project selection already consumed")
	// ErrSessionProjectAlreadyBound rejects attempts to replace an established
	// conversation binding. A session can be reopened only for its own Project.
	ErrSessionProjectAlreadyBound = errors.New("AI session is already bound to another Project")
)

type SessionService interface {
	OpenFromSelection(context.Context, string) (aisession.Session, error)
	Get(context.Context, string) (aisession.Session, error)
}

type ProjectSelectionService interface {
	Record(context.Context, string) (aiprojectselection.Selection, error)
}

type ProjectService interface {
	Get(context.Context, string) (project.Project, error)
	List(context.Context) ([]project.Project, error)
}

type ModuleService interface {
	Get(context.Context, string) (module.Module, error)
	ListByProject(context.Context, string) ([]module.Module, error)
}

type RepositoryService interface {
	Get(context.Context, string) (repository.Repository, error)
	List(context.Context) ([]repository.Repository, error)
	CurrentHEAD(context.Context, string) (string, error)
}

type TaskService interface {
	Get(context.Context, string) (task.Task, error)
	List(context.Context) ([]task.Task, error)
}

type CodexConfigService interface {
	Get(context.Context, string) (codexconfig.Config, error)
	List(context.Context) ([]codexconfig.Config, error)
}

type ProjectContextService interface {
	EnsureRequired(context.Context, string) (projectcontext.ProjectContext, error)
	Get(context.Context, string) (projectcontext.ProjectContext, error)
	MarkNoGuidance(context.Context, string) (projectcontext.ProjectContext, error)
}

type ProjectContextBootstrapper interface {
	Ensure(context.Context, string) (projectcontext.ProjectContext, error)
}

type Service struct {
	sessions     SessionService
	selections   ProjectSelectionService
	projects     ProjectService
	repositories RepositoryService
	modules      ModuleService
	tasks        TaskService
	configs      CodexConfigService
	contexts     ProjectContextService
	bootstrapper ProjectContextBootstrapper
}

// SetProjectContextBootstrapper installs the Bridge-owned bounded bootstrap.
// It is optional for compatibility with narrow in-memory callers.
func (s *Service) SetProjectContextBootstrapper(value ProjectContextBootstrapper) {
	s.bootstrapper = value
}

type SessionContext struct {
	Session        aisession.Session
	Project        project.Project
	ProjectContext projectcontext.ProjectContext
}

type ListOptions struct {
	Limit  int
	Offset int
}

// RepositoryContext is the bounded, path-free repository view required to
// pin a planning Task to an explicit immutable Git commit.
type RepositoryContext struct {
	ID                        string
	ProjectID                 string
	Name                      string
	CurrentHeadCommit         string
	BranchKind                repository.BranchKind
	BranchRef                 string
	WorkingTreeDirty          bool
	RepositoryStateAvailable  bool
	RepositoryStateCapturedAt time.Time
}

type ProjectContext struct {
	ID                   string
	Name                 string
	ProjectContextStatus projectcontext.Status
	RepositoryCount      int
}

func NewService(sessions SessionService, selections ProjectSelectionService, projects ProjectService, repositories RepositoryService, modules ModuleService, tasks TaskService, configs CodexConfigService, contexts ...ProjectContextService) (*Service, error) {
	if sessions == nil || selections == nil || projects == nil || repositories == nil || modules == nil || tasks == nil || configs == nil {
		return nil, errors.New("AI context dependencies are incomplete")
	}
	value := &Service{sessions: sessions, selections: selections, projects: projects, repositories: repositories, modules: modules, tasks: tasks, configs: configs}
	if len(contexts) > 0 {
		value.contexts = contexts[0]
	}
	return value, nil
}

// Legacy project-first selection is retired. BindProject records and consumes
// the original selection atomically against an initialized Session.
func (s *Service) RecordProjectSelection(ctx context.Context, projectID string) (aiprojectselection.Selection, error) {
	return aiprojectselection.Selection{}, aisession.ErrInitializationRequired
}

func (s *Service) OpenSession(ctx context.Context, selectionID string) (SessionContext, error) {
	return SessionContext{}, aisession.ErrInitializationRequired
}

func (s *Service) finishNewSession(ctx context.Context, session aisession.Session) (SessionContext, error) {
	value, err := s.projects.Get(ctx, session.ProjectID)
	if err != nil {
		return SessionContext{Session: session}, mapError(err)
	}
	if ensurer, ok := s.modules.(interface {
		EnsureUnclassified(context.Context, string) (module.Module, error)
	}); ok {
		if _, err := ensurer.EnsureUnclassified(ctx, session.ProjectID); err != nil {
			return SessionContext{Session: session, Project: value}, mapError(err)
		}
	}
	var contextValue projectcontext.ProjectContext
	if s.bootstrapper != nil {
		contextValue, err = s.bootstrapper.Ensure(ctx, session.ProjectID)
	} else {
		contextValue, err = s.ensureProjectContext(ctx, session.ProjectID)
	}
	if err != nil {
		return SessionContext{Session: session, Project: value}, err
	}
	return SessionContext{Session: session, Project: value, ProjectContext: contextValue}, nil
}

// OpenProject is a read-only legacy reopen of an existing BOUND Session.
// New binding requires BindProject and its durable request identity.
func (s *Service) OpenProject(ctx context.Context, aiSessionID, projectID string) (SessionContext, error) {
	if !validID(projectID) {
		return SessionContext{}, ErrInvalidArgument
	}
	if aiSessionID == "" {
		return SessionContext{}, aisession.ErrInitializationRequired
	}
	if !validID(aiSessionID) {
		return SessionContext{}, ErrInvalidArgument
	}
	value, err := s.GetSession(ctx, aiSessionID)
	if err != nil {
		return SessionContext{}, err
	}
	if value.Session.ProjectID != projectID {
		return SessionContext{}, ErrSessionProjectAlreadyBound
	}
	return value, nil
}

// ListProjects is the path-free bootstrap view used before an AISession
// exists. It performs no selection and exposes no repository or launcher path.
func (s *Service) ListProjects(ctx context.Context, options ListOptions) ([]ProjectContext, error) {
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	projects, err := s.projects.List(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	repositories, err := s.repositories.List(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	counts := make(map[string]int)
	for _, value := range repositories {
		counts[value.ProjectID]++
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Name == projects[j].Name {
			return projects[i].ID < projects[j].ID
		}
		return projects[i].Name < projects[j].Name
	})
	projects = page(projects, offset, limit)
	result := make([]ProjectContext, 0, len(projects))
	for _, value := range projects {
		status := projectcontext.StatusRequired
		if s.contexts != nil {
			contextValue, getErr := s.contexts.Get(ctx, value.ID)
			if getErr == nil {
				status = contextValue.Status
			} else if !errors.Is(getErr, projectcontext.ErrNotFound) {
				return nil, mapError(getErr)
			}
		}
		result = append(result, ProjectContext{ID: value.ID, Name: value.Name, ProjectContextStatus: status, RepositoryCount: counts[value.ID]})
	}
	return result, nil
}

func (s *Service) ensureProjectContext(ctx context.Context, projectID string) (projectcontext.ProjectContext, error) {
	if s.contexts == nil {
		return projectcontext.ProjectContext{ProjectID: projectID, Status: projectcontext.StatusRequired}, nil
	}
	value, err := s.contexts.EnsureRequired(ctx, projectID)
	if err != nil {
		return projectcontext.ProjectContext{}, mapError(err)
	}
	if value.Status != projectcontext.StatusRequired {
		return value, nil
	}
	repositories, err := s.repositories.List(ctx)
	if err != nil {
		return projectcontext.ProjectContext{}, mapError(err)
	}
	foundRootGuidance := false
	for _, repositoryValue := range repositories {
		if repositoryValue.ProjectID != projectID {
			continue
		}
		info, statErr := os.Stat(filepath.Join(repositoryValue.Path, "AGENTS.md"))
		if statErr == nil {
			if info.Mode().IsRegular() {
				foundRootGuidance = true
				break
			}
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return projectcontext.ProjectContext{}, statErr
		}
	}
	if !foundRootGuidance {
		value, err = s.contexts.MarkNoGuidance(ctx, projectID)
	}
	return value, mapError(err)
}

func (s *Service) GetSession(ctx context.Context, sessionID string) (SessionContext, error) {
	session, err := s.session(ctx, sessionID)
	if err != nil {
		return SessionContext{}, err
	}
	value, err := s.projects.Get(ctx, session.ProjectID)
	if err != nil {
		return SessionContext{}, mapError(err)
	}
	contextValue, err := s.projectContext(ctx, session.ProjectID)
	if err != nil {
		return SessionContext{}, err
	}
	return SessionContext{Session: session, Project: value, ProjectContext: contextValue}, nil
}

func (s *Service) projectContext(ctx context.Context, projectID string) (projectcontext.ProjectContext, error) {
	if s.contexts == nil {
		return projectcontext.ProjectContext{ProjectID: projectID, Status: projectcontext.StatusRequired}, nil
	}
	value, err := s.contexts.Get(ctx, projectID)
	if err != nil {
		return projectcontext.ProjectContext{}, mapError(err)
	}
	return value, nil
}

func (s *Service) GetProjectContext(ctx context.Context, sessionID string) (projectcontext.ProjectContext, error) {
	session, err := s.session(ctx, sessionID)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	return s.projectContext(ctx, session.ProjectID)
}

func (s *Service) ListModules(ctx context.Context, sessionID string, options ListOptions) ([]module.Module, error) {
	session, err := s.session(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	values, err := s.modules.ListByProject(ctx, session.ProjectID)
	if err != nil {
		return nil, mapError(err)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Key == values[j].Key {
			return values[i].ID < values[j].ID
		}
		return values[i].Key < values[j].Key
	})
	return page(values, offset, limit), nil
}

// ListProjectModuleMap returns only current production-backed context modules.
// Classification-only and system fallback Modules cannot displace model-facing
// Project Module Map entries from the bounded result.
func (s *Service) ListProjectModuleMap(ctx context.Context, sessionID string) ([]module.Module, error) {
	session, err := s.session(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var values []module.Module
	if mapped, ok := s.modules.(interface {
		ListContextMapByProject(context.Context, string) ([]module.Module, error)
	}); ok {
		values, err = mapped.ListContextMapByProject(ctx, session.ProjectID)
	} else {
		values, err = s.modules.ListByProject(ctx, session.ProjectID)
		filtered := values[:0]
		for _, value := range values {
			if len(value.ProductionFacts) != 0 {
				filtered = append(filtered, value)
			}
		}
		values = filtered
	}
	if err != nil {
		return nil, mapError(err)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Key < values[j].Key })
	if len(values) > MaxListLimit {
		values = values[:MaxListLimit]
	}
	return values, nil
}

// GetModule validates ownership without revealing whether a foreign opaque ID
// exists. It is available to future project-owned AI operations.
func (s *Service) GetModule(ctx context.Context, sessionID, moduleID string) (module.Module, error) {
	session, err := s.session(ctx, sessionID)
	if err != nil {
		return module.Module{}, err
	}
	if !validID(moduleID) {
		return module.Module{}, ErrInvalidArgument
	}
	value, err := s.modules.Get(ctx, moduleID)
	if errors.Is(err, module.ErrNotFound) {
		return module.Module{}, ErrNotFound
	}
	if err != nil {
		return module.Module{}, err
	}
	if value.ProjectID != session.ProjectID {
		return module.Module{}, ErrNotFound
	}
	return value, nil
}

func (s *Service) ListRepositories(ctx context.Context, sessionID string, options ListOptions) ([]RepositoryContext, error) {
	session, err := s.session(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	values, err := s.repositories.List(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	filtered := make([]repository.Repository, 0, len(values))
	for _, value := range values {
		if value.ProjectID == session.ProjectID {
			filtered = append(filtered, value)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Name == filtered[j].Name {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].Name < filtered[j].Name
	})
	filtered = page(filtered, offset, limit)
	result := make([]RepositoryContext, 0, len(filtered))
	for _, value := range filtered {
		state, err := s.currentRepositoryState(ctx, value.ID)
		if err != nil {
			return nil, mapError(err)
		}
		result = append(result, repositoryContext(value, state))
	}
	return result, nil
}

func (s *Service) GetRepository(ctx context.Context, sessionID, repositoryID string) (RepositoryContext, error) {
	session, err := s.session(ctx, sessionID)
	if err != nil {
		return RepositoryContext{}, err
	}
	if !validID(repositoryID) {
		return RepositoryContext{}, ErrInvalidArgument
	}
	value, err := s.repositories.Get(ctx, repositoryID)
	if errors.Is(err, repository.ErrNotFound) {
		return RepositoryContext{}, ErrNotFound
	}
	if err != nil {
		return RepositoryContext{}, err
	}
	if value.ProjectID != session.ProjectID {
		return RepositoryContext{}, ErrNotFound
	}
	state, err := s.currentRepositoryState(ctx, value.ID)
	if err != nil {
		return RepositoryContext{}, mapError(err)
	}
	return repositoryContext(value, state), nil
}

func (s *Service) currentRepositoryState(ctx context.Context, id string) (repository.State, error) {
	if source, ok := s.repositories.(interface {
		CurrentState(context.Context, string) (repository.State, error)
	}); ok {
		return source.CurrentState(ctx, id)
	}
	head, err := s.repositories.CurrentHEAD(ctx, id)
	if err != nil {
		return repository.State{}, err
	}
	return repository.State{HeadCommit: head, BranchKind: repository.BranchUnknown, CapturedAt: time.Now().UTC()}, nil
}

func repositoryContext(value repository.Repository, state repository.State) RepositoryContext {
	return RepositoryContext{ID: value.ID, ProjectID: value.ProjectID, Name: value.Name, CurrentHeadCommit: state.HeadCommit, BranchKind: state.BranchKind, BranchRef: state.BranchRef, WorkingTreeDirty: state.WorkingTreeDirty, RepositoryStateAvailable: state.BranchKind != repository.BranchUnknown, RepositoryStateCapturedAt: state.CapturedAt}
}

func (s *Service) ListTasks(ctx context.Context, sessionID string, options ListOptions) ([]task.Task, error) {
	session, err := s.session(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	values, err := s.tasks.List(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	filtered := make([]task.Task, 0, len(values))
	for _, value := range values {
		if value.ProjectID == session.ProjectID {
			filtered = append(filtered, value)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].UpdatedAt.Equal(filtered[j].UpdatedAt) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt)
	})
	return page(filtered, offset, limit), nil
}

func (s *Service) GetTask(ctx context.Context, sessionID, taskID string) (task.Task, error) {
	session, err := s.session(ctx, sessionID)
	if err != nil {
		return task.Task{}, err
	}
	if !validID(taskID) {
		return task.Task{}, ErrInvalidArgument
	}
	value, err := s.tasks.Get(ctx, taskID)
	if errors.Is(err, task.ErrNotFound) {
		return task.Task{}, ErrNotFound
	}
	if err != nil {
		return task.Task{}, err
	}
	if value.ProjectID != session.ProjectID {
		return task.Task{}, ErrNotFound
	}
	return value, nil
}

func (s *Service) ListCodexConfigs(ctx context.Context, sessionID string, options ListOptions) ([]codexconfig.Config, error) {
	if _, err := s.session(ctx, sessionID); err != nil {
		return nil, err
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	values, err := s.configs.List(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return page(values, offset, limit), nil
}

// GetCodexConfig requires a valid session even though launch configurations
// are currently global choices rather than project-owned records.
func (s *Service) GetCodexConfig(ctx context.Context, sessionID, configID string) (codexconfig.Config, error) {
	if _, err := s.session(ctx, sessionID); err != nil {
		return codexconfig.Config{}, err
	}
	if !validID(configID) {
		return codexconfig.Config{}, ErrInvalidArgument
	}
	value, err := s.configs.Get(ctx, configID)
	if errors.Is(err, codexconfig.ErrNotFound) {
		return codexconfig.Config{}, ErrNotFound
	}
	if err != nil {
		return codexconfig.Config{}, err
	}
	return value, nil
}

func (s *Service) session(ctx context.Context, id string) (aisession.Session, error) {
	if !validID(id) {
		return aisession.Session{}, ErrInvalidArgument
	}
	value, err := s.sessions.Get(ctx, id)
	if err != nil {
		return aisession.Session{}, mapError(err)
	}
	return value, nil
}

func boundedList(options ListOptions) (int, int, error) {
	limit := options.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit || options.Offset < 0 || options.Offset > MaxOffset {
		return 0, 0, ErrInvalidArgument
	}
	return limit, options.Offset, nil
}

func validID(value string) bool { return value != "" && len(value) <= MaxIDBytes }

func page[T any](values []T, offset, limit int) []T {
	if offset >= len(values) {
		return []T{}
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	return values[offset:end]
}

func mapError(err error) error {
	if errors.Is(err, aiprojectselection.ErrNotFound) || errors.Is(err, aisession.ErrNotFound) || errors.Is(err, project.ErrNotFound) || errors.Is(err, repository.ErrNotFound) || errors.Is(err, module.ErrNotFound) || errors.Is(err, task.ErrNotFound) || errors.Is(err, codexconfig.ErrNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, aiprojectselection.ErrAlreadyConsumed) {
		return ErrSelectionConsumed
	}
	return err
}
