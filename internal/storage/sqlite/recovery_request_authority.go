package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/agentruntime"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

// The source may retain an explicitly recorded answer with UNKNOWN delivery:
// diagnosing its engineering result does not consume that answer. The child
// has no such exception. Both gates read the authoritative request rows in the
// same transaction as admission/resolution, never an incomplete display copy.
func recoveryRequestDispositionTx(ctx context.Context, tx *sql.Tx, runID string, source bool) (string, error) {
	snapshot, err := readRuntimeRequestsTx(ctx, tx, runID)
	if err != nil {
		return "", err
	}
	disposition := ""
	for _, request := range snapshot.Requests {
		d := agentruntime.ResponseDisposition(request)
		if d == "DISPATCHING" {
			return "", rs.ErrIneligible
		}
		if source && request.Kind == "CODEX_REQUEST" && d == "UNKNOWN" && request.UserDecision != nil {
			disposition = "UNKNOWN"
			continue
		}
		if agentruntime.OpenRequest(request) || d == "UNKNOWN" {
			return "", rs.ErrIneligible
		}
	}
	return disposition, nil
}
