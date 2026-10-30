package controlapi

import (
	"context"
	"errors"
	c "github.com/lazyun/devbridge/internal/confirmation"
)

// DiscardConfirmationProposal is an explicit user decision to retire an
// unconsumed proposal, not a withdrawal of any business effect.
func (f *Facade) DiscardConfirmationProposal(ctx context.Context, in c.ReadInput) (c.Intent, error) {
	if _, err := f.ReadConfirmation(ctx, in); err != nil {
		return c.Intent{}, err
	}
	store, ok := f.deps.Confirmations.(interface {
		DiscardProposal(context.Context, c.ReadInput) (c.Intent, error)
	})
	if !ok {
		return c.Intent{}, errors.New("proposal retirement unavailable")
	}
	out, err := store.DiscardProposal(ctx, in)
	if err != nil {
		return out, err
	}
	if out.Commit != nil {
		in.RequestID = out.Commit.RequestID
	}
	return f.ReadConfirmation(ctx, in)
}
