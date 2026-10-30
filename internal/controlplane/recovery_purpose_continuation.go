package controlplane

import (
	"context"

	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// The existing Core writer loop drains durable purpose receipts on startup
// and periodically. Reads and user confirmation retain their own boundaries;
// consumption does not create or dispatch a Run.
func recoveryPurposeContinuation(store *sqlite.RecoveryPurposeStore) func(context.Context) {
	var after int64
	var lastFailure string
	return func(ctx context.Context) {
		var err error
		after, err = store.ContinueConfirmedPurposes(ctx, after)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			lastFailure = ""
			return
		}
		if current := err.Error(); current != lastFailure {
			lastFailure = current
			observability.Error(ctx, "recovery", "recovery.purpose.consume_failed", "Confirmed Recovery purpose could not yet be applied", map[string]any{"errorSummary": current})
		}
	}
}
