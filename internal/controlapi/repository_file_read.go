package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/repository"
)

// ReadRepositoryFileForSession shares the repository-search authorization
// boundary. Binding supplies identity; it does not create a file grant or permit
// bypassing an active Upper Review's narrower immutable read boundary.
func (f *Facade) ReadRepositoryFileForSession(ctx context.Context, sessionID, repositoryID string, in repository.FileReadInput) (repository.FileReadResult, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return repository.FileReadResult{}, err
	}
	value, err := f.deps.Repositories.Get(ctx, repositoryID)
	if err != nil {
		return repository.FileReadResult{}, MapError(err)
	}
	if value.ProjectID != projectID {
		return repository.FileReadResult{}, notFound()
	}
	guard, ok := f.deps.UpperReviewAccess.(interface {
		CheckUnscopedRepositorySearch(context.Context, string, string) error
	})
	if !ok {
		return repository.FileReadResult{}, errors.New("repository read authorization guard unavailable")
	}
	if err = guard.CheckUnscopedRepositorySearch(ctx, sessionID, repositoryID); err != nil {
		return repository.FileReadResult{}, MapError(err)
	}
	reader, ok := f.deps.Repositories.(interface {
		ReadFile(context.Context, string, repository.FileReadInput) (repository.FileReadResult, error)
	})
	if !ok {
		return repository.FileReadResult{}, errors.New("repository file read unavailable")
	}
	out, err := reader.ReadFile(ctx, repositoryID, in)
	if errors.Is(err, repository.ErrFileVersionChanged) {
		return out, &Error{Code: CodeConflict, Message: repository.ErrFileVersionChanged.Error(), cause: err}
	}
	if errors.Is(err, repository.ErrFileReadBounds) || errors.Is(err, repository.ErrFileTooLarge) {
		return out, &Error{Code: CodeInvalidArgument, Message: err.Error(), cause: err}
	}
	if err != nil {
		return out, MapError(err)
	}
	if out.ProjectID != projectID || out.RepositoryID != repositoryID {
		return repository.FileReadResult{}, notFound()
	}
	return out, nil
}
