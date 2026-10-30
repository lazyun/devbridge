package task

import "context"

// UnreviewedImplementationInput confirms a complete implementation definition;
// skipping Review at creation never authorizes a runtime or a filesystem write.
type UnreviewedImplementationInput struct {
	AISessionID, TaskID, IntentID, RequestID, ExpectedRevisionID string
	RepositoryID, BaseCommit                                     string
	WorkSpec                                                     WorkSpec
	VerificationSpec                                             VerificationSpec
}

func (s *Service) CanConfirmUnreviewedImplementation(ctx context.Context, session, id string) (bool, error) {
	store, ok := s.store.(interface {
		CanConfirmUnreviewedImplementation(context.Context, string, string) (bool, error)
	})
	if !ok {
		return false, nil
	}
	return store.CanConfirmUnreviewedImplementation(ctx, session, id)
}
func (s *Service) ConfirmUnreviewedImplementation(ctx context.Context, in UnreviewedImplementationInput) (Task, error) {
	store, ok := s.store.(interface {
		ConfirmUnreviewedImplementation(context.Context, UnreviewedImplementationInput) (Task, error)
	})
	if !ok {
		return Task{}, ErrNotFound
	}
	if in.RepositoryID != "" {
		if err := s.ValidateImplementationBaseline(ctx, in.RepositoryID, in.BaseCommit); err != nil {
			return Task{}, err
		}
	}

	return store.ConfirmUnreviewedImplementation(ctx, in)
}

// ValidateImplementationBaseline only resolves immutable Git metadata. It does
// not grant access or change the Task, workspace or current development ref.
func (s *Service) ValidateImplementationBaseline(ctx context.Context, repositoryID, baseCommit string) error {
	if s.resolver == nil {
		return ErrNotFound
	}
	resolved, err := s.resolver.ResolveBase(ctx, repositoryID, baseCommit)
	if err != nil {
		return err
	}
	if !fullCommit(resolved) || resolved != baseCommit {
		return ErrConflict
	}
	return nil
}
