package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/git"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/featurearea"
)

type FeatureAreaStore struct{ db *DB }

func NewFeatureAreaStore(db *DB) *FeatureAreaStore { return &FeatureAreaStore{db: db} }

func (s *FeatureAreaStore) Create(ctx context.Context, value featurearea.FeatureArea) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var repoPath, ref string
	selectErr := tx.QueryRowContext(ctx, `SELECT r.canonical_path,c.development_ref FROM development_cycles c JOIN repositories r ON r.id=c.repository_id WHERE c.project_id=? AND c.superseded_at IS NULL AND (SELECT COUNT(*) FROM development_cycles d WHERE d.project_id=c.project_id AND d.superseded_at IS NULL)=1`, value.ProjectID).Scan(&repoPath, &ref)
	if selectErr == nil {
		value.MergedCommit, err = git.NewClient(git.Config{}).ResolveCommit(ctx, repoPath, ref)
		if err != nil {
			return err
		}
		value.MergedAt = &value.CreatedAt
	} else if !errors.Is(selectErr, sql.ErrNoRows) {
		return selectErr
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO feature_areas(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at,merged_commit,merged_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, value.ID, value.ProjectID, nullString(value.CreatedByAISessionID), value.Title, value.ProblemStatement, string(value.Status), formatTime(value.CreatedAt), formatTime(value.UpdatedAt), nullString(value.MergedCommit), nullableCommitTime(value.MergedAt))
	if err != nil {
		return conflict(fmt.Errorf("insert feature area: %w", err))
	}
	payload, _ := event.Payload(map[string]any{"status": value.Status, "title": value.Title})
	if err := appendEvent(ctx, tx, event.Event{Type: event.FeatureAreaCreated, ProjectID: value.ProjectID, EntityType: "feature_area", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}

type featureAreaScanner interface{ Scan(...any) error }

func scanFeatureArea(scanner featureAreaScanner) (featurearea.FeatureArea, error) {
	var value featurearea.FeatureArea
	var creator, mergedAt sql.NullString
	var created, updated string
	var req, requested, completed, detail, summary string
	var remaining int
	if err := scanner.Scan(&value.ID, &value.ProjectID, &creator, &value.Title, &value.ProblemStatement, &value.Status, &created, &updated, &value.LegacyReadOnly, &value.LegacyStatus, &req, &requested, &completed, &detail, &remaining, &summary, &value.CurrentWorkspaceID, &value.MergedCommit, &mergedAt); err != nil {
		return featurearea.FeatureArea{}, err
	}
	var errTime error
	value.MergedAt, errTime = parseOptionalTime(mergedAt)
	if errTime != nil {
		return value, errTime
	}
	if requested != "" {
		at, e := parseTime(requested)
		if e != nil {
			return value, e
		}
		value.Retirement = &featurearea.Retirement{RequestID: req, RequestedAt: at, Summary: summary, RemainingTasks: remaining, LastError: detail}
		if completed != "" {
			done, e := parseTime(completed)
			if e != nil {
				return value, e
			}
			value.Retirement.CompletedAt = &done
		}
	}
	value.CreatedByAISessionID = creator.String
	var err error
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	value.UpdatedAt, err = parseTime(updated)
	return value, err
}

func (s *FeatureAreaStore) Get(ctx context.Context, id string) (featurearea.FeatureArea, error) {
	value, err := scanFeatureArea(s.db.sql.QueryRowContext(ctx, selectFeatureArea+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return featurearea.FeatureArea{}, featurearea.ErrNotFound
	}
	if err != nil {
		return featurearea.FeatureArea{}, fmt.Errorf("get feature area: %w", err)
	}
	return value, nil
}

func (s *FeatureAreaStore) ListByProject(ctx context.Context, projectID string) ([]featurearea.FeatureArea, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectFeatureArea+` WHERE project_id=? ORDER BY created_at DESC,id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list feature areas: %w", err)
	}
	defer rows.Close()
	values := make([]featurearea.FeatureArea, 0)
	for rows.Next() {
		value, scanErr := scanFeatureArea(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *FeatureAreaStore) ListUsableByProject(ctx context.Context, projectID string, limit int) ([]featurearea.FeatureArea, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectFeatureArea+` WHERE project_id=? AND legacy_read_only=0 AND status IN (?,?) ORDER BY created_at DESC,id LIMIT ?`, projectID, string(featurearea.StatusDiscovering), string(featurearea.StatusActive), limit)
	if err != nil {
		return nil, fmt.Errorf("list usable feature areas: %w", err)
	}
	defer rows.Close()
	values := make([]featurearea.FeatureArea, 0)
	for rows.Next() {
		value, scanErr := scanFeatureArea(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *FeatureAreaStore) CurrentByProject(ctx context.Context, projectID string) (featurearea.FeatureArea, error) {
	value, err := scanFeatureArea(s.db.sql.QueryRowContext(ctx, selectFeatureArea+` WHERE project_id=? AND legacy_read_only=0 AND status IN (?,?) ORDER BY created_at DESC,id LIMIT 1`, projectID, string(featurearea.StatusDiscovering), string(featurearea.StatusActive)))
	if errors.Is(err, sql.ErrNoRows) {
		return featurearea.FeatureArea{}, featurearea.ErrNotFound
	}
	if err != nil {
		return featurearea.FeatureArea{}, fmt.Errorf("get current feature area: %w", err)
	}
	return value, nil
}

func (s *FeatureAreaStore) Update(ctx context.Context, id string, update func(*featurearea.FeatureArea) error) (featurearea.FeatureArea, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	defer tx.Rollback()
	value, err := scanFeatureArea(tx.QueryRowContext(ctx, selectFeatureArea+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return featurearea.FeatureArea{}, featurearea.ErrNotFound
	}
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	previous := value
	if err := update(&value); err != nil {
		return featurearea.FeatureArea{}, err
	}
	if value.CurrentWorkspaceID != previous.CurrentWorkspaceID || value.ID != previous.ID || value.ProjectID != previous.ProjectID || value.CreatedByAISessionID != previous.CreatedByAISessionID || value.Title != previous.Title || value.ProblemStatement != previous.ProblemStatement || !value.CreatedAt.Equal(previous.CreatedAt) {
		return featurearea.FeatureArea{}, featurearea.ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE feature_areas SET status=?,updated_at=? WHERE id=? AND status=? AND updated_at=?`, string(value.Status), formatTime(value.UpdatedAt), id, string(previous.Status), formatTime(previous.UpdatedAt))
	if err != nil {
		return featurearea.FeatureArea{}, conflict(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	if affected != 1 {
		return featurearea.FeatureArea{}, featurearea.ErrConflict
	}
	if value.Status != previous.Status {
		payload, _ := event.Payload(map[string]any{"from": previous.Status, "to": value.Status})
		if err := appendEvent(ctx, tx, event.Event{Type: event.FeatureAreaStatusChanged, ProjectID: value.ProjectID, EntityType: "feature_area", EntityID: value.ID, Payload: payload, CreatedAt: value.UpdatedAt}); err != nil {
			return featurearea.FeatureArea{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return featurearea.FeatureArea{}, err
	}
	return value, nil
}

// RecordRelation stores immutable FeatureAreaRelation split provenance.
func (s *FeatureAreaStore) RecordRelation(ctx context.Context, relation featurearea.Relation) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	source, err := scanFeatureArea(tx.QueryRowContext(ctx, selectFeatureArea+` WHERE id=?`, relation.SourceFeatureAreaID))
	if err != nil {
		return conflict(fmt.Errorf("split source feature area: %w", err))
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO feature_area_relations(source_feature_area_id,target_feature_area_id,kind,project_id,created_at) VALUES(?,?,?,?,?)`, relation.SourceFeatureAreaID, relation.TargetFeatureAreaID, string(relation.Kind), source.ProjectID, formatTime(relation.CreatedAt))
	if err != nil {
		return conflict(fmt.Errorf("insert feature area relation: %w", err))
	}
	return tx.Commit()
}

func (s *FeatureAreaStore) ListRelationsBySource(ctx context.Context, sourceID string) ([]featurearea.Relation, error) {
	return s.listRelations(ctx, ` WHERE source_feature_area_id=?`, sourceID)
}

func (s *FeatureAreaStore) ListRelationsByTarget(ctx context.Context, targetID string) ([]featurearea.Relation, error) {
	return s.listRelations(ctx, ` WHERE target_feature_area_id=?`, targetID)
}

func (s *FeatureAreaStore) listRelations(ctx context.Context, where, id string) ([]featurearea.Relation, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectFeatureAreaRelation+where, id)
	if err != nil {
		return nil, fmt.Errorf("list feature area relations: %w", err)
	}
	defer rows.Close()
	values := make([]featurearea.Relation, 0)
	for rows.Next() {
		var relation featurearea.Relation
		var kind, created string
		if err := rows.Scan(&relation.SourceFeatureAreaID, &relation.TargetFeatureAreaID, &kind, &created); err != nil {
			return nil, err
		}
		relation.Kind = featurearea.RelationKind(kind)
		relation.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		values = append(values, relation)
	}
	return values, rows.Err()
}

const selectFeatureArea = `SELECT id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at,legacy_read_only,COALESCE(legacy_status,'')` + retirementColumns + `,COALESCE(current_workspace_id,''),COALESCE(merged_commit,''),merged_at FROM feature_areas`
const selectFeatureAreaRelation = `SELECT source_feature_area_id,target_feature_area_id,kind,created_at FROM feature_area_relations`

const retirementColumns = `,
 COALESCE((SELECT request_id FROM feature_area_retirements WHERE feature_area_id=feature_areas.id),''),
 COALESCE((SELECT requested_at FROM feature_area_retirements WHERE feature_area_id=feature_areas.id),''),
 COALESCE((SELECT completed_at FROM feature_area_retirements WHERE feature_area_id=feature_areas.id),''),
 COALESCE((SELECT last_error FROM feature_area_retirements WHERE feature_area_id=feature_areas.id),''),
 (SELECT count(*) FROM tasks WHERE feature_area_id=feature_areas.id AND legacy_read_only=0 AND status NOT IN ('SUCCEEDED','FAILED','CANCELED')),
 CASE WHEN status='RETIRED' THEN '已关闭' WHEN status<>'RETIRING' THEN ''
 WHEN EXISTS(SELECT 1 FROM feature_area_agent_claims c JOIN agent_operation_boundaries b ON b.run_id=c.run_id WHERE c.feature_area_id=feature_areas.id AND b.advance_state='UNKNOWN') THEN '正在停止当前 Agent；失联时将隔离旧工作区'
 WHEN EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=feature_areas.id) THEN '正在等待取消完成'
 WHEN EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=feature_areas.id AND status NOT IN ('SUCCEEDED','FAILED','CANCELED')) THEN '正在停止当前任务和审查'
 ELSE '正在完成关闭' END`

// CurrentDevelopmentHead preserves the historical single-repository baseline.
// A project-level FeatureArea has no scalar baseline for a repository set.
func (s *FeatureAreaStore) CurrentDevelopmentHead(ctx context.Context, projectID string) (string, error) {
	var count int
	if err := s.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM development_cycles WHERE project_id=? AND superseded_at IS NULL`, projectID).Scan(&count); err != nil {
		return "", err
	}
	if count != 1 {
		return "", nil
	}
	var path, ref string
	if err := s.db.sql.QueryRowContext(ctx, `SELECT r.canonical_path,c.development_ref FROM development_cycles c JOIN repositories r ON r.id=c.repository_id WHERE c.project_id=? AND c.superseded_at IS NULL`, projectID).Scan(&path, &ref); err != nil {
		return "", err
	}
	return git.NewClient(git.Config{}).ResolveCommit(ctx, path, ref)
}
