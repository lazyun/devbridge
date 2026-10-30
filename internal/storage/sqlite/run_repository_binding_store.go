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

// ListRepositoryBindings returns the full frozen target set for a v2 Run.
// Callers must reject an empty or partial set; this method never falls back to
// Run.WorktreeID or a workspace scalar projection.
func (s *RunStore) ListRepositoryBindings(ctx context.Context, runID string) ([]run.RepositoryBinding, error) {
	if runID == "" {
		return nil, review.ErrConflict
	}
	rows, err := s.db.sql.QueryContext(ctx, `SELECT run_id,COALESCE(review_id,''),COALESCE(execution_id,''),COALESCE(inspection_id,''),project_id,COALESCE(feature_area_id,''),workspace_id,repository_id,member_id,member_version,origin_commit,COALESCE(worktree_id,''),preparation,COALESCE(failure,''),COALESCE(repository_alias,''),COALESCE(start_head,''),COALESCE(start_snapshot,''),COALESCE(intended_read,0),COALESCE(intended_write_scope_json,''),COALESCE(runtime_root_identity,''),COALESCE(end_head,''),COALESCE(end_snapshot,''),COALESCE(end_root_identity,''),COALESCE(changed_paths_json,''),COALESCE(diff_digest,''),COALESCE(observation_state,''),observed_at
FROM run_repository_bindings WHERE run_id=? ORDER BY repository_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]run.RepositoryBinding, 0)
	for rows.Next() {
		value, err := scanRepositoryBinding(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// PutRepositoryBinding records preparation progress without permitting an
// operation to change its reviewed workspace/member/baseline identity.
func (s *RunStore) PutRepositoryBinding(ctx context.Context, value run.RepositoryBinding) error {
	if err := validateRepositoryBinding(value); err != nil {
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
	existing, err := scanRepositoryBinding(tx.QueryRowContext(ctx, `SELECT run_id,COALESCE(review_id,''),COALESCE(execution_id,''),COALESCE(inspection_id,''),project_id,COALESCE(feature_area_id,''),workspace_id,repository_id,member_id,member_version,origin_commit,COALESCE(worktree_id,''),preparation,COALESCE(failure,''),COALESCE(repository_alias,''),COALESCE(start_head,''),COALESCE(start_snapshot,''),COALESCE(intended_read,0),COALESCE(intended_write_scope_json,''),COALESCE(runtime_root_identity,''),COALESCE(end_head,''),COALESCE(end_snapshot,''),COALESCE(end_root_identity,''),COALESCE(changed_paths_json,''),COALESCE(diff_digest,''),COALESCE(observation_state,''),observed_at
FROM run_repository_bindings WHERE run_id=? AND repository_id=?`, value.RunID, value.RepositoryID))
	if errors.Is(err, sql.ErrNoRows) {
		if err := ensureRepositoryActiveTx(ctx, tx, value.ProjectID, value.RepositoryID); err != nil {
			return err
		}
		writeScope, encodeErr := encodeBindingWriteScope(value)
		if encodeErr != nil {
			return encodeErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO run_repository_bindings(run_id,review_id,execution_id,inspection_id,project_id,feature_area_id,workspace_id,repository_id,member_id,member_version,origin_commit,worktree_id,preparation,failure,repository_alias,start_head,start_snapshot,intended_read,intended_write_scope_json,runtime_root_identity)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.RunID, nullString(value.ReviewID), nullString(value.ExecutionID), nullString(value.InspectionID), value.ProjectID, nullString(value.FeatureAreaID), value.WorkspaceID, value.RepositoryID, value.MemberID, value.MemberVersion, value.OriginCommit, nullString(value.WorktreeID), value.Preparation, value.Failure, nullString(value.RepositoryAlias), nullString(value.StartHEAD), nullString(value.StartSnapshot), nullableBool(value.IntendedRead, value.RuntimeRootIdentity != ""), nullString(writeScope), nullString(value.RuntimeRootIdentity))
		if err != nil {
			return conflict(err)
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if !sameRepositoryBindingIdentity(existing, value) || !validBindingTransition(existing, value) {
		return review.ErrConflict
	}
	writeScope, encodeErr := encodeBindingWriteScope(value)
	if encodeErr != nil {
		return encodeErr
	}
	_, err = tx.ExecContext(ctx, `UPDATE run_repository_bindings SET worktree_id=?,preparation=?,failure=?,repository_alias=COALESCE(?,repository_alias),start_head=COALESCE(?,start_head),start_snapshot=COALESCE(?,start_snapshot),intended_read=COALESCE(?,intended_read),intended_write_scope_json=COALESCE(?,intended_write_scope_json),runtime_root_identity=COALESCE(?,runtime_root_identity) WHERE run_id=? AND repository_id=?`, nullString(value.WorktreeID), value.Preparation, value.Failure, nullString(value.RepositoryAlias), nullString(value.StartHEAD), nullString(value.StartSnapshot), nullableBool(value.IntendedRead, value.RuntimeRootIdentity != ""), nullString(writeScope), nullString(value.RuntimeRootIdentity), value.RunID, value.RepositoryID)
	if err != nil {
		return conflict(err)
	}
	return tx.Commit()
}

type repositoryBindingScanner interface{ Scan(...any) error }

func scanRepositoryBinding(scanner repositoryBindingScanner) (run.RepositoryBinding, error) {
	var value run.RepositoryBinding
	var writeScope, changedPaths string
	var observedAt sql.NullString
	var intendedRead int
	err := scanner.Scan(&value.RunID, &value.ReviewID, &value.ExecutionID, &value.InspectionID, &value.ProjectID, &value.FeatureAreaID, &value.WorkspaceID, &value.RepositoryID, &value.MemberID, &value.MemberVersion, &value.OriginCommit, &value.WorktreeID, &value.Preparation, &value.Failure, &value.RepositoryAlias, &value.StartHEAD, &value.StartSnapshot, &intendedRead, &writeScope, &value.RuntimeRootIdentity, &value.EndHEAD, &value.EndSnapshot, &value.EndRootIdentity, &changedPaths, &value.DiffDigest, &value.ObservationState, &observedAt)
	if err != nil {
		return value, err
	}
	value.IntendedRead = intendedRead != 0
	if writeScope != "" && json.Unmarshal([]byte(writeScope), &value.IntendedWrite) != nil {
		return run.RepositoryBinding{}, review.ErrConflict
	}
	if changedPaths != "" && json.Unmarshal([]byte(changedPaths), &value.ChangedPaths) != nil {
		return run.RepositoryBinding{}, review.ErrConflict
	}
	if observedAt.Valid {
		at, e := parseTime(observedAt.String)
		if e != nil {
			return run.RepositoryBinding{}, e
		}
		value.ObservedAt = &at
	}
	return value, nil
}

func nullableBool(value bool, present bool) any {
	if !present {
		return nil
	}
	if value {
		return 1
	}
	return 0
}

func encodeBindingWriteScope(value run.RepositoryBinding) (string, error) {
	if value.RuntimeRootIdentity == "" {
		return "", nil
	}
	raw, err := json.Marshal(value.IntendedWrite)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func validateRepositoryBinding(value run.RepositoryBinding) error {
	ownerCount := 0
	if value.ReviewID != "" {
		ownerCount++
	}
	if value.ExecutionID != "" {
		ownerCount++
	}
	if value.InspectionID != "" {
		ownerCount++
	}
	if value.RunID == "" || ownerCount != 1 || value.ProjectID == "" || value.WorkspaceID == "" || value.RepositoryID == "" || value.MemberID == "" || value.MemberVersion < 1 || !workset.ValidCommit(value.OriginCommit) || (value.InspectionID == "" && value.FeatureAreaID == "") || (value.InspectionID != "" && value.FeatureAreaID != "") {
		return review.ErrConflict
	}
	switch value.Preparation {
	case "PENDING":
		if value.WorktreeID != "" || value.Failure != "" {
			return review.ErrConflict
		}
	case "FAILED":
		if value.Failure == "" {
			return review.ErrConflict
		}
	case "READY":
		if value.WorktreeID == "" || value.Failure != "" {
			return review.ErrConflict
		}
	default:
		return review.ErrConflict
	}
	if value.RuntimeRootIdentity != "" {
		if value.RepositoryAlias == "" || !workset.ValidCommit(value.StartHEAD) || !workset.ValidCommit(value.StartSnapshot) || !value.IntendedRead || workset.ValidateTargets([]workset.Target{{RepositoryID: value.RepositoryID, MemberID: value.MemberID, MemberVersion: value.MemberVersion, OriginCommit: value.OriginCommit, Read: value.IntendedRead, Write: value.IntendedWrite}}) != nil {
			return review.ErrConflict
		}
	} else if value.RepositoryAlias != "" || value.StartHEAD != "" || (value.StartSnapshot != "" && !workset.ValidCommit(value.StartSnapshot)) || value.IntendedRead || value.IntendedWrite.Kind != "" || len(value.IntendedWrite.Paths) != 0 {
		return review.ErrConflict
	}
	return nil
}

func sameRepositoryBindingIdentity(a, b run.RepositoryBinding) bool {
	return a.RunID == b.RunID && a.ReviewID == b.ReviewID && a.ExecutionID == b.ExecutionID && a.InspectionID == b.InspectionID && a.ProjectID == b.ProjectID && a.FeatureAreaID == b.FeatureAreaID && a.WorkspaceID == b.WorkspaceID && a.RepositoryID == b.RepositoryID && a.MemberID == b.MemberID && a.MemberVersion == b.MemberVersion && a.OriginCommit == b.OriginCommit
}

func validBindingTransition(from, to run.RepositoryBinding) bool {
	if from.Preparation == to.Preparation && from.WorktreeID == to.WorktreeID && from.Failure == to.Failure {
		return true
	}
	if to.Preparation == "READY" && to.WorktreeID != "" && (from.Preparation == "PENDING" || from.Preparation == "FAILED") {
		return true
	}
	if to.Preparation == "FAILED" && to.Failure != "" && (from.Preparation == "PENDING" || from.Preparation == "FAILED") {
		return true
	}
	return false
}
