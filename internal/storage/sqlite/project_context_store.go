package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/projectcontext"
)

type ProjectContextStore struct{ db *DB }

func NewProjectContextStore(db *DB) *ProjectContextStore { return &ProjectContextStore{db: db} }

const selectProjectContext = `SELECT project_id,status,COALESCE(root_guidance_identity,''),COALESCE(context_fingerprint,''),diagnostics_json,COALESCE(latest_probe_id,''),COALESCE(result,''),COALESCE(result_artifact_ref,''),created_at,updated_at FROM project_contexts`

type projectContextScanner interface{ Scan(...any) error }

func scanProjectContext(row projectContextScanner) (projectcontext.ProjectContext, error) {
	var value projectcontext.ProjectContext
	var created, updated, diagnostics string
	if err := row.Scan(&value.ProjectID, &value.Status, &value.RootGuidanceIdentity, &value.ContextFingerprint, &diagnostics, &value.LatestProbeID, &value.Result, &value.ResultArtifactRef, &created, &updated); err != nil {
		return value, err
	}
	if value.Diagnostics, _ = decodeProjectContextDiagnostics(diagnostics); value.Diagnostics == nil {
		return value, errors.New("decode Project Context diagnostics")
	}
	var err error
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updated)
	return value, err
}

func (s *ProjectContextStore) EnsureRequired(ctx context.Context, projectID string, now time.Time) (projectcontext.ProjectContext, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	defer tx.Rollback()
	inserted, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_contexts(project_id,status,created_at,updated_at) VALUES(?,?,?,?)`, projectID, projectcontext.StatusRequired, formatTime(now), formatTime(now))
	if err != nil {
		return projectcontext.ProjectContext{}, conflict(fmt.Errorf("insert project context: %w", err))
	}
	affected, err := inserted.RowsAffected()
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	current, err := scanProjectContext(tx.QueryRowContext(ctx, selectProjectContext+` WHERE project_id=?`, projectID))
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	from := projectcontext.Status("")
	if affected == 0 {
		if current.Status != projectcontext.StatusFailed || current.ContextFingerprint != "" {
			return current, nil
		}
		from = current.Status
		updated, err := tx.ExecContext(ctx, `UPDATE project_contexts SET status='REQUIRED',root_guidance_identity=NULL,context_fingerprint='',diagnostics_json='{"version":1,"items":[]}',result=NULL,result_artifact_ref=NULL,updated_at=? WHERE project_id=? AND status='FAILED'`, formatTime(now), projectID)
		if err != nil {
			return projectcontext.ProjectContext{}, conflict(err)
		}
		changed, _ := updated.RowsAffected()
		if changed != 1 {
			return projectcontext.ProjectContext{}, projectcontext.ErrConflict
		}
		current.Status, current.RootGuidanceIdentity, current.ContextFingerprint, current.Diagnostics, current.Result, current.ResultArtifactRef, current.UpdatedAt = projectcontext.StatusRequired, "", "", []string{}, "", "", now
	}
	payload, _ := event.Payload(map[string]any{"from": from, "to": projectcontext.StatusRequired})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ProjectContextChanged, ProjectID: projectID, EntityType: "project_context", EntityID: projectID, Payload: payload, CreatedAt: now}); err != nil {
		return projectcontext.ProjectContext{}, err
	}
	if err := tx.Commit(); err != nil {
		return projectcontext.ProjectContext{}, err
	}
	return current, nil
}

func (s *ProjectContextStore) MarkBootstrapped(ctx context.Context, projectID string, status projectcontext.Status, fingerprint, identity, guidance string, diagnostics []string, now time.Time) (projectcontext.ProjectContext, error) {
	encoded, err := encodeProjectContextDiagnostics(diagnostics)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	defer tx.Rollback()
	current, err := scanProjectContext(tx.QueryRowContext(ctx, selectProjectContext+` WHERE project_id=?`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return projectcontext.ProjectContext{}, projectcontext.ErrNotFound
	}
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE project_contexts SET status=?,root_guidance_identity=?,context_fingerprint=?,diagnostics_json=?,latest_probe_id=NULL,result=?,result_artifact_ref=NULL,updated_at=? WHERE project_id=? AND updated_at=?`, status, nullString(identity), fingerprint, encoded, nullString(guidance), formatTime(now), projectID, formatTime(current.UpdatedAt))
	if err != nil {
		return projectcontext.ProjectContext{}, conflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return projectcontext.ProjectContext{}, projectcontext.ErrConflict
	}
	payload, _ := event.Payload(map[string]any{"from": current.Status, "to": status, "module_map": "validated", "diagnostic_count": len(diagnostics)})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ProjectContextChanged, ProjectID: projectID, EntityType: "project_context", EntityID: projectID, Payload: payload, CreatedAt: now}); err != nil {
		return projectcontext.ProjectContext{}, err
	}
	if err := tx.Commit(); err != nil {
		return projectcontext.ProjectContext{}, err
	}
	return s.Get(ctx, projectID)
}

// MarkBootstrappedAtTopology fences publication of a bootstrap that observed a
// specific active Repository inventory. The Module Map writer is independently
// fenced with the same revision; an unlink invalidates both current-only
// projections before a stale bootstrap can become current.
func (s *ProjectContextStore) MarkBootstrappedAtTopology(ctx context.Context, projectID string, status projectcontext.Status, fingerprint, identity, guidance string, diagnostics []string, topologyRevision int64, now time.Time) (projectcontext.ProjectContext, error) {
	encoded, err := encodeProjectContextDiagnostics(diagnostics)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	defer tx.Rollback()
	currentTopology, err := projectRepositoryTopologyRevisionTx(ctx, tx, projectID)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	if currentTopology != topologyRevision {
		return projectcontext.ProjectContext{}, projectcontext.ErrConflict
	}
	current, err := scanProjectContext(tx.QueryRowContext(ctx, selectProjectContext+` WHERE project_id=?`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return projectcontext.ProjectContext{}, projectcontext.ErrNotFound
	}
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE project_contexts SET status=?,root_guidance_identity=?,context_fingerprint=?,diagnostics_json=?,latest_probe_id=NULL,result=?,result_artifact_ref=NULL,updated_at=? WHERE project_id=? AND updated_at=?`, status, nullString(identity), fingerprint, encoded, nullString(guidance), formatTime(now), projectID, formatTime(current.UpdatedAt))
	if err != nil {
		return projectcontext.ProjectContext{}, conflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return projectcontext.ProjectContext{}, projectcontext.ErrConflict
	}
	payload, _ := event.Payload(map[string]any{"from": current.Status, "to": status, "module_map": "validated", "diagnostic_count": len(diagnostics), "repository_topology_revision": topologyRevision})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ProjectContextChanged, ProjectID: projectID, EntityType: "project_context", EntityID: projectID, Payload: payload, CreatedAt: now}); err != nil {
		return projectcontext.ProjectContext{}, err
	}
	if err := tx.Commit(); err != nil {
		return projectcontext.ProjectContext{}, err
	}
	return s.Get(ctx, projectID)
}

func encodeProjectContextDiagnostics(values []string) (string, error) {
	encoded, err := json.Marshal(struct {
		Version int      `json:"version"`
		Items   []string `json:"items"`
	}{Version: 1, Items: append([]string{}, values...)})
	return string(encoded), err
}

func decodeProjectContextDiagnostics(encoded string) ([]string, error) {
	var envelope struct {
		Version int      `json:"version"`
		Items   []string `json:"items"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil || envelope.Version != 1 || envelope.Items == nil {
		if err == nil {
			err = errors.New("unsupported Project Context diagnostics encoding")
		}
		return nil, err
	}
	return append([]string{}, envelope.Items...), nil
}

func (s *ProjectContextStore) Get(ctx context.Context, projectID string) (projectcontext.ProjectContext, error) {
	value, err := scanProjectContext(s.db.sql.QueryRowContext(ctx, selectProjectContext+` WHERE project_id=?`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return value, projectcontext.ErrNotFound
	}
	if err != nil {
		return value, fmt.Errorf("get project context: %w", err)
	}
	return value, nil
}

func (s *ProjectContextStore) MarkNoGuidance(ctx context.Context, projectID string, now time.Time) (projectcontext.ProjectContext, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE project_contexts SET status='NO_GUIDANCE',root_guidance_identity=NULL,latest_probe_id=NULL,result=NULL,result_artifact_ref=NULL,updated_at=? WHERE project_id=? AND status='REQUIRED'`, formatTime(now), projectID)
	if err != nil {
		return projectcontext.ProjectContext{}, conflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		value, getErr := scanProjectContext(tx.QueryRowContext(ctx, selectProjectContext+` WHERE project_id=?`, projectID))
		if getErr == nil && value.Status == projectcontext.StatusNoGuidance {
			return value, nil
		}
		return projectcontext.ProjectContext{}, projectcontext.ErrConflict
	}
	payload, _ := event.Payload(map[string]any{"from": projectcontext.StatusRequired, "to": projectcontext.StatusNoGuidance, "reason": "root_AGENTS_absent"})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ProjectContextChanged, ProjectID: projectID, EntityType: "project_context", EntityID: projectID, Payload: payload, CreatedAt: now}); err != nil {
		return projectcontext.ProjectContext{}, err
	}
	if err := tx.Commit(); err != nil {
		return projectcontext.ProjectContext{}, err
	}
	return s.Get(ctx, projectID)
}

func (s *ProjectContextStore) MarkRunning(ctx context.Context, projectID, probeID string, now time.Time) (projectcontext.ProjectContext, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	defer tx.Rollback()
	current, err := scanProjectContext(tx.QueryRowContext(ctx, selectProjectContext+` WHERE project_id=?`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return current, projectcontext.ErrNotFound
	}
	if err != nil {
		return current, err
	}
	if current.Status == projectcontext.StatusRunning {
		return current, projectcontext.ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE project_contexts SET status=?,latest_probe_id=?,updated_at=? WHERE project_id=? AND status=? AND updated_at=?`, projectcontext.StatusRunning, probeID, formatTime(now), projectID, current.Status, formatTime(current.UpdatedAt))
	if err != nil {
		return current, conflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return current, projectcontext.ErrConflict
	}
	payload, _ := event.Payload(map[string]any{"from": current.Status, "to": projectcontext.StatusRunning, "probe_id": probeID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ProjectContextChanged, ProjectID: projectID, EntityType: "project_context", EntityID: projectID, Payload: payload, CreatedAt: now}); err != nil {
		return current, err
	}
	if err := tx.Commit(); err != nil {
		return current, err
	}
	return s.Get(ctx, projectID)
}

func (s *ProjectContextStore) MarkFinished(ctx context.Context, projectID, probeID string, status projectcontext.Status, resultText, artifactRef, identity string, now time.Time) (projectcontext.ProjectContext, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE project_contexts SET status=?,result=?,result_artifact_ref=?,root_guidance_identity=?,updated_at=? WHERE project_id=? AND status='RUNNING' AND latest_probe_id=?`, status, nullString(resultText), nullString(artifactRef), nullString(identity), formatTime(now), projectID, probeID)
	if err != nil {
		return projectcontext.ProjectContext{}, conflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return projectcontext.ProjectContext{}, projectcontext.ErrConflict
	}
	payload, _ := event.Payload(map[string]any{"from": projectcontext.StatusRunning, "to": status, "probe_id": probeID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ProjectContextChanged, ProjectID: projectID, EntityType: "project_context", EntityID: projectID, Payload: payload, CreatedAt: now}); err != nil {
		return projectcontext.ProjectContext{}, err
	}
	if err := tx.Commit(); err != nil {
		return projectcontext.ProjectContext{}, err
	}
	return s.Get(ctx, projectID)
}

func (s *ProjectContextStore) RecoverRunning(ctx context.Context, now time.Time) error {
	_, err := s.db.execWrite(ctx, `UPDATE project_contexts SET status='FAILED',updated_at=? WHERE status='RUNNING' AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id=project_contexts.latest_probe_id AND c.owner_type='INSPECTION' AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))`, formatTime(now))
	return err
}
