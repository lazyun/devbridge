package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/agentruntime"
)

// requireResolvedResponsesTx is checked in the canonical owner's transaction.
// Turn completion and product output cannot substitute for a request receipt.
func requireResolvedResponsesTx(ctx context.Context, tx *sql.Tx, runID string) error {
	snapshot, err := readRuntimeRequestsTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	if agentruntime.HasUnresolvedResponse(snapshot) {
		return agentruntime.ErrResponseDispositionUnknown
	}
	return nil
}
