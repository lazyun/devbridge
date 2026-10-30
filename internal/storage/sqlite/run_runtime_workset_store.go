package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
)

// BindRuntimeWorkset writes the validated, full runtime layout once. The
// database trigger prevents any later substitution; an idempotent replay is
// accepted only when its canonical JSON and hash match exactly.
func (s *RunStore) BindRuntimeWorkset(ctx context.Context, value workset.RuntimeWorkset) (run.Run, error) {
	if err := value.Validate(); err != nil {
		return run.Run{}, review.ErrConflict
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return run.Run{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	if err = validateTaskPermissionAuthorityTx(ctx, tx, value); err != nil {
		return run.Run{}, err
	}
	if err := s.db.checkRunTx(ctx, tx, value.RunID); err != nil {
		return run.Run{}, err
	}
	if p := value.TaskPermissions; p != nil {
		var hasBinding bool
		var purpose, taskID, reviewID, executionID, projectID, areaID, status string
		if err = tx.QueryRowContext(ctx, `SELECT purpose,COALESCE(task_id,''),COALESCE(review_id,''),COALESCE(execution_proposal_id,''),project_id,feature_area_id,status,runtime_workset_json IS NOT NULL FROM runs WHERE id=?`, value.RunID).Scan(&purpose, &taskID, &reviewID, &executionID, &projectID, &areaID, &status, &hasBinding); err != nil {
			return run.Run{}, err
		}
		if purpose != p.Purpose || projectID != value.ProjectID || areaID != value.FeatureAreaID || executionID != value.ExecutionID || (p.Purpose == "SCOPE_REVIEW" && reviewID != p.ReviewID) || (p.Purpose != "SCOPE_REVIEW" && taskID != p.TaskID) || (!hasBinding && status != "CREATED" && status != "PREPARING") {
			return run.Run{}, review.ErrConflict
		}
	}
	var existingJSON, existingHash sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT runtime_workset_json,runtime_workset_hash FROM runs WHERE id=?`, value.RunID).Scan(&existingJSON, &existingHash); err != nil {
		return run.Run{}, err
	}
	if existingJSON.Valid || existingHash.Valid {
		if !existingJSON.Valid || !existingHash.Valid || existingHash.String != value.ContentHash || existingJSON.String != string(raw) {
			return run.Run{}, review.ErrConflict
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE runs SET runtime_workset_json=?,runtime_workset_hash=?,updated_at=? WHERE id=?`, string(raw), value.ContentHash, formatTime(value.CreatedAt), value.RunID); err != nil {
		return run.Run{}, conflict(err)
	}
	stored, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, value.RunID))
	if err != nil {
		return run.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return stored, nil
}

func (s *RunStore) RuntimeWorkset(ctx context.Context, runID string) (workset.RuntimeWorkset, error) {
	if runID == "" {
		return workset.RuntimeWorkset{}, review.ErrConflict
	}
	var raw, hash sql.NullString
	if err := s.db.sql.QueryRowContext(ctx, `SELECT runtime_workset_json,runtime_workset_hash FROM runs WHERE id=?`, runID).Scan(&raw, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workset.RuntimeWorkset{}, run.ErrNotFound
		}
		return workset.RuntimeWorkset{}, err
	}
	if !raw.Valid || !hash.Valid {
		return workset.RuntimeWorkset{}, run.ErrNotFound
	}
	var value workset.RuntimeWorkset
	if err := json.Unmarshal([]byte(raw.String), &value); err != nil || value.ContentHash != hash.String || value.Validate() != nil {
		return workset.RuntimeWorkset{}, review.ErrConflict
	}
	return value, nil
}

func validObservationState(value string) bool {
	switch value {
	case "OBSERVED", "NO_CHANGE", "SCOPE_CONFLICT", "UNKNOWN":
		return true
	default:
		return false
	}
}

// ObserveRepositoryBinding records one terminal fact set after dispatch. The
// storage trigger makes the fact set append-once and prevents revised reports.
func (s *RunStore) ObserveRepositoryBinding(ctx context.Context, value run.RepositoryObservation) error {
	if value.RunID == "" || value.RepositoryID == "" || !validObservationState(value.ObservationState) || value.ObservedAt.IsZero() {
		return review.ErrConflict
	}
	if value.ObservationState == "UNKNOWN" {
		// A response-loss or terminal-before-observation path must record that
		// the fact is unavailable, never manufacture a Git/root result. UNKNOWN
		// is rejected by acceptance and the trigger still makes it append-once.
		if value.EndHEAD != "" || value.EndSnapshot != "" || value.EndRootIdentity != "" || len(value.ChangedPaths) != 0 || value.DiffDigest != "" {
			return review.ErrConflict
		}
	} else if !workset.ValidCommit(value.EndHEAD) || !workset.ValidCommit(value.EndSnapshot) || value.EndRootIdentity == "" {
		return review.ErrConflict
	}
	paths, err := json.Marshal(append([]string(nil), value.ChangedPaths...))
	if err != nil {
		return err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.db.checkRunTx(ctx, tx, value.RunID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_repository_bindings
SET end_head=?,end_snapshot=?,end_root_identity=?,changed_paths_json=?,diff_digest=?,observation_state=?,observed_at=?
WHERE run_id=? AND repository_id=? AND runtime_root_identity IS NOT NULL AND observation_state IS NULL`,
		value.EndHEAD, value.EndSnapshot, value.EndRootIdentity, string(paths), nullString(value.DiffDigest), value.ObservationState, formatTime(value.ObservedAt.UTC()), value.RunID, value.RepositoryID)
	if err != nil {
		return conflict(err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return review.ErrConflict
	}
	return tx.Commit()
}
