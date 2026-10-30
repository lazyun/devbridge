package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/featurearea"
)

// A matching durable receipt is read authority, not a new lifecycle mutation.
// Fresh commands still recheck receipt identity under their writer transaction.
func (s *FeatureAreaStore) readLifecycleReplay(ctx context.Context, in featurearea.LifecycleMutation) (featurearea.LifecycleResult, bool, error) {
	empty := featurearea.LifecycleResult{}
	var action, hash, encoded string
	err := s.db.sql.QueryRowContext(ctx, `SELECT r.action,r.payload_sha256,r.result_json
 FROM feature_area_lifecycle_requests r JOIN feature_areas a ON a.id=r.feature_area_id
 WHERE a.id=? AND a.project_id=? AND r.request_id=?`, in.FeatureAreaID, in.ProjectID, in.RequestID).Scan(&action, &hash, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	if action != string(in.Action) || hash != in.PayloadSHA256 {
		return empty, true, featurearea.ErrConflict
	}
	if in.Action == featurearea.ActionRetire {
		// Retirement replays the current convergence of the original intent;
		// activation and decomposition retain their historical response below.
		area, err := scanFeatureArea(s.db.sql.QueryRowContext(ctx, selectFeatureArea+` WHERE id=? AND project_id=?`, in.FeatureAreaID, in.ProjectID))
		if err != nil {
			return empty, true, err
		}
		return featurearea.LifecycleResult{Parent: area, Children: []featurearea.FeatureArea{}, Relations: []featurearea.Relation{}}, true, nil
	}
	var result featurearea.LifecycleResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		return empty, true, err
	}
	return result, true, nil
}
