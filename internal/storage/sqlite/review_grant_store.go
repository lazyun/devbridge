package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
)

type ReviewGrantStore struct{ db *DB }

func NewReviewGrantStore(db *DB) *ReviewGrantStore { return &ReviewGrantStore{db: db} }

func (s *ReviewGrantStore) Create(ctx context.Context, value reviewgrant.Grant) error {
	paths, err := encodeReviewScopePaths(value.InitialScope.Paths)
	if err != nil {
		return err
	}
	selected, err := json.Marshal(struct {
		Version int                           `json:"version"`
		Items   []reviewgrant.ModuleSelection `json:"items"`
	}{Version: 1, Items: append([]reviewgrant.ModuleSelection{}, value.SelectedModules...)})
	if err != nil {
		return err
	}
	_, err = s.db.execWrite(ctx, `INSERT INTO review_grants(review_id,feature_area_id,project_id,repository_id,mode,initial_scope_kind,initial_scope_paths_json,selected_modules_json,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, value.ReviewID, value.FeatureAreaID, value.ProjectID, value.RepositoryID, value.Mode, value.InitialScope.Kind, paths, string(selected), formatTime(value.CreatedAt))
	if err != nil {
		if errors.Is(conflict(err), ErrConflict) {
			return reviewgrant.ErrConflict
		}
		return err
	}
	return nil
}

func (s *ReviewGrantStore) Get(ctx context.Context, reviewID string) (reviewgrant.Grant, error) {
	var value reviewgrant.Grant
	var kind, paths, selected, created string
	err := s.db.sql.QueryRowContext(ctx, `SELECT review_id,feature_area_id,project_id,repository_id,mode,initial_scope_kind,initial_scope_paths_json,selected_modules_json,created_at FROM review_grants WHERE review_id=?`, reviewID).Scan(&value.ReviewID, &value.FeatureAreaID, &value.ProjectID, &value.RepositoryID, &value.Mode, &kind, &paths, &selected, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return value, reviewgrant.ErrNotFound
	}
	if err != nil {
		return value, err
	}
	value.InitialScope.Kind = run.ReviewScopeKind(kind)
	if value.InitialScope.Paths, err = decodeReviewScopePaths(paths); err != nil {
		return value, err
	}
	var envelope struct {
		Version int                           `json:"version"`
		Items   []reviewgrant.ModuleSelection `json:"items"`
	}
	if err := json.Unmarshal([]byte(selected), &envelope); err != nil || envelope.Version != 1 || envelope.Items == nil {
		return value, fmt.Errorf("decode initial Review module selection")
	}
	value.SelectedModules = envelope.Items
	value.CreatedAt, err = parseTime(created)
	return value, err
}
