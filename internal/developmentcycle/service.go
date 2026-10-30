// Package developmentcycle owns Project-level durable development branch
// selection. It does not bind FeatureAreas or change Git or execution baselines.
package developmentcycle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
)

var (
	ErrNotSelected         = errors.New("DEVELOPMENT_BRANCH_NOT_SELECTED")
	ErrBranchNotFound      = errors.New("DEVELOPMENT_BRANCH_NOT_FOUND")
	ErrRefMissing          = errors.New("DEVELOPMENT_REF_MISSING")
	ErrRepositoryMismatch  = errors.New("REPOSITORY_PROJECT_MISMATCH")
	ErrRequestConflict     = errors.New("REQUEST_CONFLICT")
	ErrFinalizationPending = errors.New("GIT_FINALIZATION_PENDING")
	ErrInvalidArgument     = errors.New("invalid development branch selection argument")
)

type Cycle struct {
	ID                    string     `json:"id"`
	ProjectID             string     `json:"projectId"`
	RepositoryID          string     `json:"repositoryId"`
	DevelopmentRef        string     `json:"developmentRef"`
	SelectedHeadSHA       string     `json:"selectedHeadSha"`
	CreatedAt             time.Time  `json:"createdAt"`
	SupersededAt          *time.Time `json:"supersededAt"`
	EndReason             string     `json:"endReason,omitempty"`
	RequestID             string     `json:"requestId"`
	SelectedByAISessionID string     `json:"selectedByAiSessionId,omitempty"`
}

type SelectInput struct {
	ProjectID      string `json:"projectId"`
	RepositoryID   string `json:"repositoryId"`
	DevelopmentRef string `json:"developmentRef"`
	RequestID      string `json:"requestId"`
	AISessionID    string `json:"aiSessionId,omitempty"`
}

// Request identity is global and payload identity is Project/Repository/ref.
// Session is provenance, not command identity: a fresh session can retry.
func SamePayload(c Cycle, in SelectInput) bool {
	return c.ProjectID == in.ProjectID && c.RepositoryID == in.RepositoryID && c.DevelopmentRef == in.DevelopmentRef
}

type RepositoryContext struct {
	RepositoryPath                          string `json:"repositoryPath"`
	Availability                            string `json:"availability"`
	RepositoryID                            string `json:"repositoryId"`
	RepositoryName                          string `json:"repositoryName"`
	DevelopmentCycle                        *Cycle `json:"developmentCycle"`
	DevelopmentBranchSelectionRequired      bool   `json:"developmentBranchSelectionRequired"`
	DevelopmentRef                          string `json:"developmentRef"`
	BranchShortName                         string `json:"branchShortName"`
	SelectedHeadSHA                         string `json:"selectedHeadSha"`
	CurrentHeadSHA                          string `json:"currentHeadSha"`
	DevelopmentRefStatus                    string `json:"developmentRefStatus"`
	RefChanged                              *bool  `json:"refChanged"`
	RepositoryCheckoutMatchesDevelopmentRef *bool  `json:"repositoryCheckoutMatchesDevelopmentRef"`
	StatusCode                              string `json:"statusCode,omitempty"`
	bridgegit.BranchInventory
}

type Context struct {
	ProjectID                          string              `json:"projectId"`
	DevelopmentBranchSelectionRequired bool                `json:"developmentBranchSelectionRequired"`
	Repositories                       []RepositoryContext `json:"repositories"`
	AvailableActions                   []string            `json:"availableActions"`
}

type Store interface {
	Current(context.Context, string, string) (Cycle, error)
	Request(context.Context, SelectInput) (Cycle, error)
	Select(context.Context, Cycle) (Cycle, error)
}
type Projects interface {
	Get(context.Context, string) (project.Project, error)
}
type Repositories interface {
	Get(context.Context, string) (repository.Repository, error)
	List(context.Context) ([]repository.Repository, error)
}
type Git interface {
	LocalBranches(context.Context, string) (bridgegit.BranchInventory, error)
}
type Service struct {
	store    Store
	projects Projects
	repos    Repositories
	git      Git
}

func NewService(store Store, projects Projects, repos Repositories, git Git) *Service {
	return &Service{store: store, projects: projects, repos: repos, git: git}
}

func (s *Service) Select(ctx context.Context, in SelectInput) (Cycle, error) {
	if in.ProjectID == "" || in.RepositoryID == "" || in.RequestID == "" || len(in.RequestID) > 128 || len(in.DevelopmentRef) > 1024 || !strings.HasPrefix(in.DevelopmentRef, "refs/heads/") {
		return Cycle{}, ErrInvalidArgument
	}
	if _, err := s.projects.Get(ctx, in.ProjectID); err != nil {
		return Cycle{}, err
	}
	repo, err := s.repos.Get(ctx, in.RepositoryID)
	if err != nil {
		return Cycle{}, err
	}
	if repo.ProjectID != in.ProjectID {
		return Cycle{}, ErrRepositoryMismatch
	}
	// Replay precedes Git observation: ref deletion/movement never changes the
	// original command result or reactivates a superseded cycle.
	if original, err := s.store.Request(ctx, in); !errors.Is(err, ErrNotSelected) {
		return original, err
	}
	inventory, err := s.git.LocalBranches(ctx, repo.Path)
	if err != nil {
		return Cycle{}, err
	}
	var sha string
	for _, branch := range inventory.Branches {
		if branch.FullRef == in.DevelopmentRef {
			sha = branch.HeadSHA
			break
		}
	}
	if sha == "" {
		return Cycle{}, ErrBranchNotFound
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Cycle{}, err
	}
	return s.store.Select(ctx, Cycle{ID: "cycle_" + hex.EncodeToString(raw[:]), ProjectID: in.ProjectID, RepositoryID: in.RepositoryID, DevelopmentRef: in.DevelopmentRef, SelectedHeadSHA: sha, CreatedAt: time.Now().UTC(), RequestID: in.RequestID, SelectedByAISessionID: in.AISessionID})
}

// Context is the common factual projection used by every product surface.
func (s *Service) Context(ctx context.Context, projectID string) (Context, error) {
	result := Context{ProjectID: projectID, Repositories: []RepositoryContext{}, AvailableActions: []string{"development_branch_list", "development_branch_select"}}
	if _, ok := s.git.(branchCreator); ok {
		result.AvailableActions = append(result.AvailableActions, "development_branch_create")
	}
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return result, err
	}
	repos, err := s.repos.List(ctx)
	if err != nil {
		return result, err
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].ID < repos[j].ID })
	for _, repo := range repos {
		if repo.ProjectID != projectID {
			continue
		}
		item := RepositoryContext{RepositoryPath: repo.Path, Availability: "AVAILABLE", RepositoryID: repo.ID, RepositoryName: repo.Name, DevelopmentRefStatus: "NOT_SELECTED", DevelopmentBranchSelectionRequired: true, StatusCode: ErrNotSelected.Error()}
		cycle, err := s.store.Current(ctx, projectID, repo.ID)
		if err != nil && !errors.Is(err, ErrNotSelected) {
			return result, err
		}
		inventory, err := s.git.LocalBranches(ctx, repo.Path)
		if err != nil {
			item.Availability = "UNAVAILABLE"
			item.StatusCode = "REPOSITORY_UNAVAILABLE"
			item.DevelopmentRefStatus = "UNAVAILABLE"
			if cycle.ID != "" {
				item.DevelopmentCycle = &cycle
				item.DevelopmentRef = cycle.DevelopmentRef
				item.DevelopmentBranchSelectionRequired = false
			}
			result.Repositories = append(result.Repositories, item)
			result.DevelopmentBranchSelectionRequired = result.DevelopmentBranchSelectionRequired || item.DevelopmentBranchSelectionRequired
			continue
		}
		item.BranchInventory = inventory
		if cycle.ID != "" {
			item.DevelopmentCycle = &cycle
			item.DevelopmentBranchSelectionRequired = false
			item.DevelopmentRef = cycle.DevelopmentRef
			item.BranchShortName = strings.TrimPrefix(cycle.DevelopmentRef, "refs/heads/")
			item.SelectedHeadSHA = cycle.SelectedHeadSHA
			item.DevelopmentRefStatus = "MISSING"
			item.StatusCode = ErrRefMissing.Error()
			matches := inventory.CheckoutRef == cycle.DevelopmentRef
			item.RepositoryCheckoutMatchesDevelopmentRef = &matches
			for _, branch := range inventory.Branches {
				if branch.FullRef == cycle.DevelopmentRef {
					item.DevelopmentRefStatus = "EXISTS"
					item.StatusCode = ""
					item.CurrentHeadSHA = branch.HeadSHA
					changed := branch.HeadSHA != cycle.SelectedHeadSHA
					item.RefChanged = &changed
					break
				}
			}
		}
		result.DevelopmentBranchSelectionRequired = result.DevelopmentBranchSelectionRequired || item.DevelopmentBranchSelectionRequired
		result.Repositories = append(result.Repositories, item)
	}
	if len(result.Repositories) == 0 {
		result.DevelopmentBranchSelectionRequired = true
	}
	return result, nil
}

// ReadSelected reads only the registered selection for one exact owned repository.
// This is metadata, not live Git existence/HEAD or a new execution baseline.
func (s *Service) ReadSelected(ctx context.Context, projectID, repositoryID string) (Cycle, error) {
	repo, err := s.repos.Get(ctx, repositoryID)
	if err != nil {
		return Cycle{}, err
	}
	if repo.ID != repositoryID || repo.ProjectID != projectID {
		return Cycle{}, ErrRepositoryMismatch
	}
	selected, err := s.store.Current(ctx, projectID, repositoryID)
	if err != nil {
		return Cycle{}, err
	}
	if selected.ProjectID != projectID || selected.RepositoryID != repositoryID {
		return Cycle{}, ErrRepositoryMismatch
	}
	return selected, nil
}
