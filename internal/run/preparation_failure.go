package run

import (
	"context"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
)

func (s *Service) SettleUpperPreparationFailure(ctx context.Context, f runtimepreparation.FailureSettlement) error {
	store, ok := s.store.(interface {
		SettleUpperPreparationFailure(context.Context, runtimepreparation.FailureSettlement) error
	})
	if !ok {
		return ErrNotFound
	}
	return store.SettleUpperPreparationFailure(ctx, f)
}

func (s *Service) ReadSettledPreparationFailure(ctx context.Context, runID string) (string, error) {
	store, ok := s.store.(interface {
		ReadSettledPreparationFailure(context.Context, string) (string, error)
	})
	if !ok {
		return "", nil
	}
	return store.ReadSettledPreparationFailure(ctx, runID)
}

func (s *Service) SettleRecoveryPreparationFailure(ctx context.Context, recoveryID string, generation int, f runtimepreparation.FailureSettlement) error {
	store, ok := s.store.(interface {
		SettleRecoveryPreparationFailure(context.Context, string, int, runtimepreparation.FailureSettlement) error
	})
	if !ok {
		return ErrNotFound
	}
	return store.SettleRecoveryPreparationFailure(ctx, recoveryID, generation, f)
}
