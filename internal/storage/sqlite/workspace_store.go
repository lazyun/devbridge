package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/workspace"
)

type WorkspaceStore struct{ db *DB }

func NewWorkspaceStore(db *DB) *WorkspaceStore { return &WorkspaceStore{db: db} }

func (s *WorkspaceStore) Create(ctx context.Context, value workspace.Workspace) error {
	if value.OwnerKind == "" {
		value.OwnerKind, value.OwnerID = workspace.OwnerFeatureArea, value.FeatureAreaID
	}
	if len(value.Members) == 0 {
		value.Members = []workspace.Member{workspace.LegacyMember(value)}
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, member := range value.Members {
		if err := ensureRepositoryActiveTx(ctx, tx, value.ProjectID, member.RepositoryID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspaces(
id,project_id,repository_id,feature_area_id,owner_kind,owner_id,inspection_task_id,inspection_id,base_commit,worktree_id,legacy_thread_id,status,token_baseline_available,
token_input_tokens,token_cached_input_tokens,token_cache_write_input_tokens,
token_output_tokens,token_reasoning_output_tokens,token_total_tokens,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.ProjectID, nullString(value.RepositoryID), nullString(value.FeatureAreaID), string(value.OwnerKind), value.OwnerID, nullString(value.InspectionTaskID), nullString(value.InspectionID), nullString(value.BaseCommit), nullString(value.WorktreeID), nil, string(value.Status), boolInt(value.TokenBaselineAvailable),
		value.TokenBaseline.InputTokens, value.TokenBaseline.CachedInputTokens, value.TokenBaseline.CacheWriteInputTokens,
		value.TokenBaseline.OutputTokens, value.TokenBaseline.ReasoningOutputTokens, value.TokenBaseline.TotalTokens,
		formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		if containsConstraint(err.Error()) {
			return fmt.Errorf("%w: %v", workspace.ErrConflict, err)
		}
		return err
	}
	if err := persistWorkspaceMembers(ctx, tx, value); err != nil {
		return err
	}
	projectID := value.ProjectID
	payload, _ := event.Payload(map[string]any{"status": value.Status})
	if err := appendEvent(ctx, tx, event.Event{Type: event.WorkspaceCreated, ProjectID: projectID, EntityType: "workspace", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}
	if value.OwnerKind == workspace.OwnerFeatureArea {
		if _, err := tx.ExecContext(ctx, `UPDATE feature_areas SET current_workspace_id=? WHERE id=? AND project_id=? AND current_workspace_id IS NULL`, value.ID, value.FeatureAreaID, value.ProjectID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type workspaceScanner interface{ Scan(...any) error }

func scanWorkspace(scanner workspaceScanner) (workspace.Workspace, error) {
	var value workspace.Workspace
	var repositoryID, featureAreaID, ownerID, inspectionTaskID, inspectionID, baseCommit, worktreeID, threadID sql.NullString
	var ownerKind string
	var status, created, updated string
	var baselineAvailable int
	err := scanner.Scan(&value.ID, &value.ProjectID, &repositoryID, &featureAreaID, &ownerKind, &ownerID, &inspectionTaskID, &inspectionID, &baseCommit, &worktreeID, &threadID, &status, &baselineAvailable,
		&value.TokenBaseline.InputTokens, &value.TokenBaseline.CachedInputTokens, &value.TokenBaseline.CacheWriteInputTokens,
		&value.TokenBaseline.OutputTokens, &value.TokenBaseline.ReasoningOutputTokens, &value.TokenBaseline.TotalTokens,
		&created, &updated, &value.RuntimeQuarantined)
	if err != nil {
		return value, err
	}
	value.RepositoryID, value.FeatureAreaID, value.OwnerKind, value.OwnerID, value.InspectionTaskID, value.InspectionID, value.BaseCommit, value.WorktreeID = repositoryID.String, featureAreaID.String, workspace.OwnerKind(ownerKind), ownerID.String, inspectionTaskID.String, inspectionID.String, baseCommit.String, worktreeID.String
	value.LegacyThreadID = threadID.String
	value.Status = workspace.Status(status)
	value.TokenBaselineAvailable = baselineAvailable == 1
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updated)
	return value, err
}

const selectWorkspace = `SELECT id,project_id,repository_id,feature_area_id,owner_kind,owner_id,inspection_task_id,inspection_id,base_commit,worktree_id,legacy_thread_id,status,token_baseline_available,
token_input_tokens,token_cached_input_tokens,token_cache_write_input_tokens,
token_output_tokens,token_reasoning_output_tokens,token_total_tokens,created_at,updated_at,runtime_quarantined FROM workspaces`

func (s *WorkspaceStore) Get(ctx context.Context, id string) (workspace.Workspace, error) {
	value, err := scanWorkspace(s.db.sql.QueryRowContext(ctx, selectWorkspace+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, workspace.ErrNotFound
	}
	if err == nil {
		err = loadWorkspaceMembers(ctx, s.db.sql, &value)
	}
	return value, err
}

func (s *WorkspaceStore) GetByFeatureArea(ctx context.Context, areaID string) (workspace.Workspace, error) {
	value, err := scanWorkspace(s.db.sql.QueryRowContext(ctx, selectWorkspace+` WHERE id=(SELECT current_workspace_id FROM feature_areas WHERE id=?)`, areaID))
	if errors.Is(err, sql.ErrNoRows) {
		return value, workspace.ErrNotFound
	}
	if err == nil {
		err = loadWorkspaceMembers(ctx, s.db.sql, &value)
	}
	return value, err
}

func (s *WorkspaceStore) GetByOwner(ctx context.Context, kind workspace.OwnerKind, ownerID string) (workspace.Workspace, error) {
	if kind == "" || ownerID == "" {
		return workspace.Workspace{}, workspace.ErrNotFound
	}
	value, err := scanWorkspace(s.db.sql.QueryRowContext(ctx, selectWorkspace+` WHERE owner_kind=? AND owner_id=?`, string(kind), ownerID))
	if errors.Is(err, sql.ErrNoRows) {
		return value, workspace.ErrNotFound
	}
	if err == nil {
		err = loadWorkspaceMembers(ctx, s.db.sql, &value)
	}
	return value, err
}

func (s *WorkspaceStore) List(ctx context.Context) ([]workspace.Workspace, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectWorkspace+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []workspace.Workspace
	for rows.Next() {
		value, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range values {
		if err := loadWorkspaceMembers(ctx, s.db.sql, &values[i]); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (s *WorkspaceStore) Update(ctx context.Context, id string, update func(*workspace.Workspace) error) (workspace.Workspace, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return workspace.Workspace{}, err
	}
	defer tx.Rollback()
	value, err := scanWorkspace(tx.QueryRowContext(ctx, selectWorkspace+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, workspace.ErrNotFound
	}
	if err != nil {
		return value, err
	}
	if err := loadWorkspaceMembers(ctx, tx, &value); err != nil {
		return value, err
	}
	previous := workspace.Clone(value)
	if err := update(&value); err != nil {
		return workspace.Workspace{}, err
	}
	if value.ID != previous.ID || value.ProjectID != previous.ProjectID || value.RepositoryID != previous.RepositoryID || value.FeatureAreaID != previous.FeatureAreaID || value.OwnerKind != previous.OwnerKind || value.OwnerID != previous.OwnerID || value.InspectionTaskID != previous.InspectionTaskID || value.InspectionID != previous.InspectionID || value.BaseCommit != previous.BaseCommit || value.LegacyThreadID != previous.LegacyThreadID || value.RuntimeQuarantined != previous.RuntimeQuarantined || !value.CreatedAt.Equal(previous.CreatedAt) {
		return workspace.Workspace{}, workspace.ErrConflict
	}
	if err := workspace.SyncLegacyWorktreeUpdate(previous, &value); err != nil {
		return workspace.Workspace{}, err
	}
	if err := workspace.ValidateMemberUpdate(previous, value); err != nil {
		return workspace.Workspace{}, err
	}
	if err := persistWorkspaceMembers(ctx, tx, value); err != nil {
		return workspace.Workspace{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET worktree_id=?,status=?,token_baseline_available=?,
token_input_tokens=?,token_cached_input_tokens=?,token_cache_write_input_tokens=?,token_output_tokens=?,token_reasoning_output_tokens=?,token_total_tokens=?,updated_at=?
WHERE id=? AND updated_at=?`, nullString(value.WorktreeID), string(value.Status), boolInt(value.TokenBaselineAvailable),
		value.TokenBaseline.InputTokens, value.TokenBaseline.CachedInputTokens, value.TokenBaseline.CacheWriteInputTokens,
		value.TokenBaseline.OutputTokens, value.TokenBaseline.ReasoningOutputTokens, value.TokenBaseline.TotalTokens,
		formatTime(value.UpdatedAt), id, formatTime(previous.UpdatedAt))
	if err != nil {
		if containsConstraint(err.Error()) {
			return workspace.Workspace{}, fmt.Errorf("%w: %v", workspace.ErrConflict, err)
		}
		return workspace.Workspace{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return workspace.Workspace{}, err
	}
	if affected != 1 {
		return workspace.Workspace{}, workspace.ErrConflict
	}
	if !reflect.DeepEqual(value, previous) {
		projectID := value.ProjectID
		payload, _ := event.Payload(map[string]any{"status": value.Status, "worktree_id": value.WorktreeID, "token_baseline_available": value.TokenBaselineAvailable})
		if err := appendEvent(ctx, tx, event.Event{Type: event.WorkspaceUpdated, ProjectID: projectID, EntityType: "workspace", EntityID: value.ID, Payload: payload, CreatedAt: value.UpdatedAt}); err != nil {
			return workspace.Workspace{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return workspace.Workspace{}, err
	}
	return value, nil
}

func loadWorkspaceMembers(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, v *workspace.Workspace) error {
	rows, err := q.QueryContext(ctx, `SELECT id,workspace_id,repository_id,identity_version,alias,origin_commit,origin_ref,development_cycle_id,COALESCE(worktree_id,''),preparation,failure,created_at,updated_at FROM workspace_members WHERE workspace_id=? ORDER BY repository_id`, v.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	v.Members = nil
	for rows.Next() {
		var m workspace.Member
		var created, updated string
		if err = rows.Scan(&m.ID, &m.WorkspaceID, &m.RepositoryID, &m.Version, &m.Alias, &m.OriginCommit, &m.OriginRef, &m.DevelopmentCycleID, &m.WorktreeID, &m.Preparation, &m.Failure, &created, &updated); err != nil {
			return err
		}
		m.CreatedAt, err = parseTime(created)
		if err != nil {
			return err
		}
		m.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return err
		}
		v.Members = append(v.Members, m)
	}
	return rows.Err()
}
func persistWorkspaceMembers(ctx context.Context, tx *sql.Tx, v workspace.Workspace) error {
	if err := workspace.ValidateMembers(v); err != nil {
		return err
	}
	for _, m := range v.Members {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE id=?)`, m.ID).Scan(&exists); err != nil {
			return err
		}
		// Preparation progress on an already admitted historical member is not a
		// new repository admission. A previously absent member must still be
		// active at the exact transaction that creates it.
		if !exists {
			if err := ensureRepositoryActiveTx(ctx, tx, v.ProjectID, m.RepositoryID); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO workspace_members(id,workspace_id,repository_id,identity_version,alias,origin_commit,origin_ref,development_cycle_id,worktree_id,preparation,failure,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET worktree_id=excluded.worktree_id,preparation=excluded.preparation,failure=excluded.failure,updated_at=excluded.updated_at WHERE workspace_members.workspace_id=excluded.workspace_id AND workspace_members.repository_id=excluded.repository_id AND workspace_members.identity_version=excluded.identity_version AND workspace_members.alias=excluded.alias AND workspace_members.origin_commit=excluded.origin_commit AND workspace_members.origin_ref=excluded.origin_ref AND workspace_members.development_cycle_id=excluded.development_cycle_id AND workspace_members.created_at=excluded.created_at`, m.ID, m.WorkspaceID, m.RepositoryID, m.Version, m.Alias, m.OriginCommit, m.OriginRef, m.DevelopmentCycleID, nullString(m.WorktreeID), m.Preparation, m.Failure, formatTime(m.CreatedAt), formatTime(m.UpdatedAt))
		if err != nil {
			return fmt.Errorf("persist workspace member: %w", err)
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return workspace.ErrConflict
		}
	}
	return nil
}
