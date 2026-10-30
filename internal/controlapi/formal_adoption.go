package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
)

func formalAdoptionIntent(intent confirmation.Intent, v confirmation.Variant) bool {
	b := intent.Content.Execution
	return intent.Action == "START_APPROVED" && b != nil && b.ExpectedTarget != nil && v.ImplementationMode == task.ImplementationCodingAgent && len(v.RepositoryTargets) > 0
}

var errFormalAdoptionNotApplicable = errors.New("formal adoption is not applicable to this exact existing authority")

func (f *Facade) finishFormalAdoption(ctx context.Context, intent confirmation.Intent) (formalexecution.AdoptionReceipt, error) {
	var zero formalexecution.AdoptionReceipt
	store, ok := f.deps.WorkflowProgression.(formalexecution.AdoptionStore)
	if !ok {
		return zero, formalexecution.ErrMissing
	}
	id, err := f.formalExecutionIdentity(ctx, intent)
	if err != nil {
		return zero, err
	}
	receipt, err := store.ReadAdoption(ctx, id)
	if errors.Is(err, formalexecution.ErrMissing) {
		required, readErr := store.ReadAdoptionRequired(ctx, id)
		if readErr != nil {
			return zero, readErr
		}
		if !required {
			// Historical completed Run receipts remain readable. They acquire
			// no new formal archive or source through replay.
			if intent.State == "COMPLETED" {
				return zero, errFormalAdoptionNotApplicable
			}
			return zero, formalexecution.ErrMissing
		}
		if intent.State == "COMPLETED" {
			return zero, formalexecution.ErrCorrupt
		}
		// The writer replays complete receipts first and rejects committed prefixes.
		// Only an absent unlocked command may resolve a current platform reference.
		receipt, err = store.CommitAdoption(ctx, id, runtimeselection.PlatformReference{})
		if errors.Is(err, formalexecution.ErrQualificationRequired) {
			if f.deps.RuntimeQualification == nil || intent.Commit.BackendSelection == nil {
				return zero, formalexecution.ErrQualificationRequired
			}
			ref, e := f.deps.RuntimeQualification.ResolvePlatformReference(ctx, *intent.Commit.BackendSelection, "IMPLEMENTATION")
			if e != nil {
				return zero, e
			}
			receipt, err = store.CommitAdoption(ctx, id, ref)
		}
	}
	if err != nil {
		return receipt, err
	}
	return receipt, nil
}
