package sqlite

import (
	"context"
	"database/sql"
	"errors"
)

var errDeliveryMemberUnavailable = errors.New("DELIVERY_MEMBER_UNAVAILABLE: the exact authorized repository member and worktree must be ready")

var errMultiRepositoryDeliveryUnsupported = errors.New("MULTI_REPOSITORY_DELIVERY_UNSUPPORTED: aggregate per-repository verification and delivery receipts are required")

// Legacy delivery can operate on one exact Task repository even if the
// Workspace has other resource members. It must never report a single SHA as
// successful delivery for Tasks spanning repositories.
func featureAreaDeliveryRepository(ctx context.Context, q definitionQuerier, area string) (string, error) {
	var count int
	var id sql.NullString
	err := q.QueryRowContext(ctx, `SELECT count(DISTINCT repository_id),min(repository_id) FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT' AND status<>'CANCELED' AND repository_id IS NOT NULL`, area).Scan(&count, &id)
	if err != nil {
		return "", err
	}
	if count > 1 {
		return "", errMultiRepositoryDeliveryUnsupported
	}
	if count == 0 {
		return "", sql.ErrNoRows
	}
	return id.String, nil
}
