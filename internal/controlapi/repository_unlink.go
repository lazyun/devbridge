package controlapi

import (
	"context"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/repositoryunlink"
)

// RepositoryUnlinkProjection is the sole Core-owned client read model for a
// repository removal. It reports current safety facts but never grants the
// caller permission to bypass the commit-time check.
type RepositoryUnlinkProjection struct {
	RepositoryID           string                        `json:"repositoryId"`
	RepositoryName         string                        `json:"repositoryName"`
	RepositoryPath         string                        `json:"repositoryPath"`
	RegistrationState      repository.RegistrationState  `json:"registrationState"`
	RegistrationGeneration int64                         `json:"registrationGeneration"`
	UnlinkedAt             *string                       `json:"unlinkedAt,omitempty"`
	HistoryAvailable       bool                          `json:"historyAvailable"`
	AvailableActions       []string                      `json:"availableActions"`
	UnlinkEligibility      repositoryunlink.Eligibility  `json:"unlinkEligibility"`
	Confirmation           *RepositoryUnlinkConfirmation `json:"confirmation,omitempty"`
}

const RepositoryUnlinkPurpose = "REPOSITORY_UNLINK"

// RepositoryUnlinkConfirmation is the exact Core-authored command that may be
// shown to a user after the read-only prepare. Consequences are fixed product
// facts rather than caller-provided policy.
type RepositoryUnlinkConfirmation struct {
	Purpose                  string   `json:"purpose"`
	RequestID                string   `json:"requestId"`
	ProjectID                string   `json:"projectId"`
	RepositoryID             string   `json:"repositoryId"`
	ExpectedGeneration       int64    `json:"expectedRegistrationGeneration"`
	ExpectedTopologyRevision int64    `json:"expectedTopologyRevision"`
	Consequences             []string `json:"consequences"`
}

// RepositoryUnlinkPrepareInput has only identity and a stable request id. The
// request id is displayed in the confirmation/result flow, but prepare itself
// remains a pure read and creates no receipt, worktree, model session or state.
type RepositoryUnlinkPrepareInput struct {
	RequestID    string `json:"requestId"`
	ProjectID    string `json:"projectId"`
	RepositoryID string `json:"repositoryId"`
}

// RepositoryUnlinkCommitInput is a closed DTO. In particular it exposes no
// force flag, client-provided outcome, blocker override, target list or state.
type RepositoryUnlinkCommitInput struct {
	RequestID                string `json:"requestId"`
	ProjectID                string `json:"projectId"`
	RepositoryID             string `json:"repositoryId"`
	ExpectedGeneration       int64  `json:"expectedRegistrationGeneration"`
	ExpectedTopologyRevision int64  `json:"expectedTopologyRevision"`
	Reason                   string `json:"reason"`
}

func (f *Facade) PrepareRepositoryUnlink(ctx context.Context, in RepositoryUnlinkPrepareInput) (RepositoryUnlinkProjection, error) {
	if strings.TrimSpace(in.RequestID) == "" || strings.TrimSpace(in.ProjectID) == "" || strings.TrimSpace(in.RepositoryID) == "" {
		return RepositoryUnlinkProjection{}, invalid("repository unlink prepare requires requestId, projectId and repositoryId")
	}
	if f.deps.RepositoryUnlink == nil {
		return RepositoryUnlinkProjection{}, invalid("repository unlink is unavailable")
	}
	eligibility, err := f.deps.RepositoryUnlink.Prepare(ctx, in.ProjectID, in.RepositoryID)
	if err != nil {
		return RepositoryUnlinkProjection{}, MapError(err)
	}
	out, err := repositoryUnlinkProjection(ctx, f, eligibility)
	if err != nil {
		return RepositoryUnlinkProjection{}, err
	}
	out.Confirmation = &RepositoryUnlinkConfirmation{
		Purpose: RepositoryUnlinkPurpose, RequestID: in.RequestID, ProjectID: in.ProjectID,
		RepositoryID: in.RepositoryID, ExpectedGeneration: eligibility.RegistrationGeneration,
		ExpectedTopologyRevision: eligibility.ProjectTopologyRevision,
		Consequences: []string{
			"Local Repository files will not be deleted.",
			"Managed worktrees will not be deleted.",
			"Historical engineering records will remain available.",
			"The Repository cannot be used for new Project work.",
			"Relink and cross-Project transfer are not supported in v1.",
		},
	}
	if eligibility.CanUnlink {
		out.AvailableActions = []string{"REPOSITORY_UNLINK_COMMIT"}
	}
	return out, nil
}

// ReadRepositoryUnlink is an explicit current-state read for a known
// Project/Repository identity. It is not a general history mutation endpoint.
func (f *Facade) ReadRepositoryUnlink(ctx context.Context, projectID, repositoryID string) (RepositoryUnlinkProjection, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(repositoryID) == "" {
		return RepositoryUnlinkProjection{}, invalid("repository unlink read requires projectId and repositoryId")
	}
	if f.deps.RepositoryUnlink == nil {
		return RepositoryUnlinkProjection{}, invalid("repository unlink is unavailable")
	}
	eligibility, err := f.deps.RepositoryUnlink.Prepare(ctx, projectID, repositoryID)
	if err != nil && !errors.Is(err, repositoryunlink.ErrAlreadyUnlinked) {
		return RepositoryUnlinkProjection{}, MapError(err)
	}
	return repositoryUnlinkProjection(ctx, f, eligibility)
}

// ListRepositoryUnlinkProjects is the shared active/history inventory. The
// default omits UNLINKED history; callers must opt in deliberately.
func (f *Facade) ListRepositoryUnlinkProjects(ctx context.Context, projectID string, includeHistorical bool) ([]RepositoryUnlinkProjection, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, invalid("repository unlink list requires projectId")
	}
	if _, err := f.deps.Projects.Get(ctx, projectID); err != nil {
		return nil, MapError(err)
	}
	var values []repository.Repository
	var err error
	if includeHistorical {
		history, ok := f.deps.Repositories.(interface {
			ListAll(context.Context) ([]repository.Repository, error)
		})
		if !ok {
			return nil, invalid("historical repository inventory unavailable")
		}
		values, err = history.ListAll(ctx)
	} else {
		values, err = f.deps.Repositories.List(ctx)
	}
	if err != nil {
		return nil, MapError(err)
	}
	out := make([]RepositoryUnlinkProjection, 0, len(values))
	for _, value := range values {
		if value.ProjectID != projectID {
			continue
		}
		projection, err := f.ReadRepositoryUnlink(ctx, projectID, value.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, projection)
	}
	return out, nil
}

func (f *Facade) CommitRepositoryUnlink(ctx context.Context, in RepositoryUnlinkCommitInput) (RepositoryUnlinkProjection, error) {
	if f.deps.RepositoryUnlink == nil {
		return RepositoryUnlinkProjection{}, invalid("repository unlink is unavailable")
	}
	eligibility, err := f.deps.RepositoryUnlink.Commit(ctx, repositoryunlink.CommitInput{
		RequestID: in.RequestID, ProjectID: in.ProjectID, RepositoryID: in.RepositoryID,
		ExpectedGeneration: in.ExpectedGeneration, ExpectedTopologyRevision: in.ExpectedTopologyRevision,
		Reason: in.Reason,
	})
	if err != nil {
		return RepositoryUnlinkProjection{}, MapError(err)
	}
	return repositoryUnlinkProjection(ctx, f, eligibility)
}

func repositoryUnlinkProjection(ctx context.Context, f *Facade, value repositoryunlink.Eligibility) (RepositoryUnlinkProjection, error) {
	repo, err := f.deps.Repositories.Get(ctx, value.RepositoryID)
	if err != nil {
		return RepositoryUnlinkProjection{}, MapError(err)
	}
	if repo.ProjectID != value.ProjectID {
		return RepositoryUnlinkProjection{}, MapError(repositoryunlink.ErrRepositoryMismatch)
	}
	out := RepositoryUnlinkProjection{
		RepositoryID: repo.ID, RepositoryName: repo.Name, RepositoryPath: repo.Path,
		RegistrationState: repo.RegistrationState, RegistrationGeneration: repo.RegistrationGeneration,
		HistoryAvailable: true, AvailableActions: []string{}, UnlinkEligibility: value,
	}
	if repo.UnlinkedAt != nil {
		at := repo.UnlinkedAt.UTC().Format("2006-01-02T15:04:05Z")
		out.UnlinkedAt = &at
	}
	if value.CanUnlink && repo.RegistrationState == repository.RegistrationActive {
		out.AvailableActions = append(out.AvailableActions, "REPOSITORY_UNLINK_PREPARE")
	}
	return out, nil
}
