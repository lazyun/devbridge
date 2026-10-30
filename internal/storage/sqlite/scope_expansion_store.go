package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
)

type ScopeExpansionStore struct{ db *DB }

func NewScopeExpansionStore(db *DB) *ScopeExpansionStore { return &ScopeExpansionStore{db: db} }

func (s *ScopeExpansionStore) Create(ctx context.Context, value scopeexpansion.Expansion) error {
	current, err := encodeReviewScopePaths(value.CurrentScope.Paths)
	if err != nil {
		return err
	}
	additional, err := encodeReviewScopePaths(value.AdditionalScope.Paths)
	if err != nil {
		return err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkScopeExpansionOwnerTx(ctx, tx, value); err != nil {
		return err
	}
	if err := checkScopeExpansionExtensionTx(ctx, tx, value); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO scope_expansions(id,ai_session_id,project_id,review_id,feature_area_id,repository_id,current_scope_kind,current_scope_paths_json,additional_scope_kind,additional_scope_paths_json,reason,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.AISessionID, value.ProjectID, value.ReviewID, value.FeatureAreaID, value.RepositoryID, string(value.CurrentScope.Kind), current, string(value.AdditionalScope.Kind), additional, value.Reason, string(value.Status), formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return scopeExpansionConflict(err)
	}
	payload, _ := event.Payload(map[string]any{"ai_session_id": value.AISessionID, "review_id": value.ReviewID, "feature_area_id": value.FeatureAreaID, "repository_id": value.RepositoryID, "current_scope": value.CurrentScope, "additional_scope": value.AdditionalScope})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ScopeExpansionCreated, ProjectID: value.ProjectID, EntityType: "scope_expansion", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ScopeExpansionStore) Get(ctx context.Context, id string) (scopeexpansion.Expansion, error) {
	value, err := scanScopeExpansion(s.db.sql.QueryRowContext(ctx, selectScopeExpansion+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return scopeexpansion.Expansion{}, scopeexpansion.ErrNotFound
	}
	if err != nil {
		return scopeexpansion.Expansion{}, fmt.Errorf("get scope expansion: %w", err)
	}
	return value, nil
}

func (s *ScopeExpansionStore) ListByProject(ctx context.Context, projectID string) ([]scopeexpansion.Expansion, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectScopeExpansion+` WHERE project_id=? ORDER BY created_at DESC,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]scopeexpansion.Expansion, 0)
	for rows.Next() {
		value, err := scanScopeExpansion(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *ScopeExpansionStore) Decide(ctx context.Context, id string, to scopeexpansion.Status, now time.Time) (scopeexpansion.Expansion, error) {
	if to != scopeexpansion.StatusAllowed && to != scopeexpansion.StatusRejected {
		return scopeexpansion.Expansion{}, scopeexpansion.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return scopeexpansion.Expansion{}, err
	}
	defer tx.Rollback()
	value, err := scanScopeExpansion(tx.QueryRowContext(ctx, selectScopeExpansion+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return scopeexpansion.Expansion{}, scopeexpansion.ErrNotFound
	}
	if err != nil {
		return scopeexpansion.Expansion{}, err
	}
	if err := checkScopeExpansionOwnerTx(ctx, tx, value); err != nil {
		return value, err
	}
	if value.Status != scopeexpansion.StatusPendingConfirmation {
		return value, &scopeexpansion.TransitionError{From: value.Status, To: to}
	}
	if to == scopeexpansion.StatusAllowed {
		if err := checkScopeExpansionExtensionTx(ctx, tx, value); err != nil {
			return value, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE scope_expansions SET status=?,updated_at=?,decided_at=? WHERE id=? AND status='PENDING_CONFIRMATION' AND updated_at=?`, string(to), formatTime(now), formatTime(now), id, formatTime(value.UpdatedAt))
	if err != nil {
		return value, scopeExpansionConflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return value, scopeexpansion.ErrConflict
	}
	eventType := event.ScopeExpansionAllowed
	if to == scopeexpansion.StatusRejected {
		eventType = event.ScopeExpansionRejected
	}
	payload, _ := event.Payload(map[string]any{"review_id": value.ReviewID, "feature_area_id": value.FeatureAreaID, "from": scopeexpansion.StatusPendingConfirmation, "to": to, "additional_scope": value.AdditionalScope})
	if err := appendEvent(ctx, tx, event.Event{Type: eventType, ProjectID: value.ProjectID, EntityType: "scope_expansion", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
		return value, err
	}
	if err := tx.Commit(); err != nil {
		return value, err
	}
	decided := now.UTC()
	value.Status, value.UpdatedAt, value.DecidedAt = to, decided, &decided
	return value, nil
}

func encodeReviewScopePaths(paths []string) (string, error) {
	encoded, err := json.Marshal(map[string]any{"version": 1, "paths": append([]string{}, paths...)})
	return string(encoded), err
}

func decodeReviewScopePaths(encoded string) ([]string, error) {
	var envelope struct {
		Version int      `json:"version"`
		Paths   []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil || envelope.Version != 1 || envelope.Paths == nil {
		if err == nil {
			err = errors.New("unsupported review scope paths encoding")
		}
		return nil, err
	}
	// External read DTOs require an array even for repository-wide scope.  A
	// nil slice serializes as null and violates that closed schema; preserve an
	// explicit empty array through the shared scope decoder.
	return append([]string{}, envelope.Paths...), nil
}

type scopeExpansionScanner interface{ Scan(...any) error }

func scanScopeExpansion(scanner scopeExpansionScanner) (scopeexpansion.Expansion, error) {
	var value scopeexpansion.Expansion
	var currentPaths, additionalPaths, created, updated string
	var decided sql.NullString
	if err := scanner.Scan(&value.ID, &value.AISessionID, &value.ProjectID, &value.ReviewID, &value.FeatureAreaID, &value.RepositoryID, &value.LegacyTaskID, &value.CurrentScope.Kind, &currentPaths, &value.AdditionalScope.Kind, &additionalPaths, &value.Reason, &value.Status, &created, &updated, &decided); err != nil {
		return value, err
	}
	var err error
	if value.CurrentScope.Paths, err = decodeReviewScopePaths(currentPaths); err != nil {
		return value, err
	}
	if value.AdditionalScope.Paths, err = decodeReviewScopePaths(additionalPaths); err != nil {
		return value, err
	}
	if value.CreatedAt, err = parseTime(created); err != nil {
		return value, err
	}
	if value.UpdatedAt, err = parseTime(updated); err != nil {
		return value, err
	}
	value.DecidedAt, err = parseNullableTime(decided)
	return value, err
}

func scopeExpansionConflict(err error) error {
	if errors.Is(conflict(err), ErrConflict) {
		return fmt.Errorf("%w: %v", scopeexpansion.ErrConflict, err)
	}
	return err
}

const selectScopeExpansion = `SELECT id,ai_session_id,project_id,COALESCE(review_id,''),COALESCE(feature_area_id,''),COALESCE(repository_id,''),COALESCE(legacy_task_id,''),current_scope_kind,current_scope_paths_json,additional_scope_kind,additional_scope_paths_json,reason,status,created_at,updated_at,decided_at FROM scope_expansions`

func checkScopeExpansionOwnerTx(ctx context.Context, tx *sql.Tx, value scopeexpansion.Expansion) error {
	if value.ReviewID == "" || value.FeatureAreaID == "" || value.RepositoryID == "" || value.LegacyTaskID != "" {
		return scopeexpansion.ErrConflict
	}
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM reviews r JOIN feature_areas f ON f.id=r.feature_area_id
 JOIN review_grants g ON g.review_id=r.id
 JOIN ai_sessions a ON a.id=? AND a.project_id=r.project_id
 WHERE r.id=? AND r.project_id=? AND r.feature_area_id=? AND r.repository_id=?
 AND r.task_id IS NOT NULL AND f.legacy_read_only=0 AND f.status IN ('DISCOVERING','ACTIVE')
 AND g.project_id=r.project_id AND g.feature_area_id=r.feature_area_id AND g.repository_id=r.repository_id
 )`, value.AISessionID, value.ReviewID, value.ProjectID, value.FeatureAreaID, value.RepositoryID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return scopeexpansion.ErrConflict
	}
	return nil
}

// Recheck authority inside the write transaction: a proposal snapshot may have
// become stale after the service read, and direct store writes cannot invent it.
func checkScopeExpansionExtensionTx(ctx context.Context, tx *sql.Tx, value scopeexpansion.Expansion) error {
	owner, err := scanReview(tx.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, value.ReviewID))
	if err != nil {
		return err
	}
	var grant reviewgrant.Grant
	var paths string
	if err = tx.QueryRowContext(ctx, `SELECT review_id,feature_area_id,project_id,repository_id,initial_scope_kind,initial_scope_paths_json FROM review_grants WHERE review_id=?`, owner.ID).Scan(&grant.ReviewID, &grant.FeatureAreaID, &grant.ProjectID, &grant.RepositoryID, &grant.InitialScope.Kind, &paths); err != nil {
		return err
	}
	grant.InitialScope.Paths, err = decodeReviewScopePaths(paths)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, selectScopeExpansion+` WHERE review_id=? AND status='ALLOWED'`, owner.ID)
	if err != nil {
		return err
	}
	var expansions []scopeexpansion.Expansion
	for rows.Next() {
		expansion, e := scanScopeExpansion(rows)
		if e != nil {
			rows.Close()
			return e
		}
		expansions = append(expansions, expansion)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	current, err := scopeexpansion.AuthorizedScope(owner, grant, expansions)
	if err != nil {
		return err
	}
	if !scopeexpansion.EqualScope(current, value.CurrentScope) || !scopeexpansion.AddsScope(current, value.AdditionalScope) {
		return scopeexpansion.ErrConflict
	}
	for _, scope := range []run.ReviewScope{value.CurrentScope, value.AdditionalScope} {
		if scope.Kind == run.ReviewScopeRepository && len(scope.Paths) == 0 {
			continue
		}
		if scope.Kind != run.ReviewScopePaths || len(scope.Paths) == 0 {
			return scopeexpansion.ErrConflict
		}
		if err = reviewaccess.RepositoryBoundary().ValidatePaths(reviewaccess.OperationScope, scope.Paths); err != nil {
			return err
		}
	}
	return nil
}
