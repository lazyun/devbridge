package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/artifact"
	"github.com/lazyun/devbridge/internal/event"
)

type ArtifactStore struct{ db *DB }

func NewArtifactStore(db *DB) *ArtifactStore { return &ArtifactStore{db: db} }
func (s *ArtifactStore) Create(ctx context.Context, v artifact.Artifact) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.db.checkRunTx(ctx, tx, v.RunID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifacts(id,run_id,kind,path,size_bytes,sha256,created_at) VALUES(?,?,?,?,?,?,?)`, v.ID, v.RunID, string(v.Kind), v.Path, v.SizeBytes, v.SHA256, formatTime(v.CreatedAt))
	if err != nil {
		return conflict(fmt.Errorf("insert artifact metadata: %w", err))
	}
	var projectID, taskID string
	if err := tx.QueryRowContext(ctx, `SELECT u.project_id,COALESCE(u.task_id,'') FROM runs u WHERE u.id=?`, v.RunID).Scan(&projectID, &taskID); err != nil {
		return err
	}
	payload, _ := event.Payload(map[string]any{"kind": v.Kind, "size_bytes": v.SizeBytes, "sha256": v.SHA256})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ArtifactRecorded, ProjectID: projectID, TaskID: taskID, RunID: v.RunID, EntityType: "artifact", EntityID: v.ID, Payload: payload, CreatedAt: v.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}

type artifactScanner interface{ Scan(...any) error }

func scanArtifact(scanner artifactScanner) (artifact.Artifact, error) {
	var v artifact.Artifact
	var created string
	err := scanner.Scan(&v.ID, &v.RunID, &v.Kind, &v.Path, &v.SizeBytes, &v.SHA256, &created)
	if err != nil {
		return v, err
	}
	v.CreatedAt, err = parseTime(created)
	return v, err
}

const selectArtifact = `SELECT id,run_id,kind,path,size_bytes,sha256,created_at FROM artifacts`

func (s *ArtifactStore) Get(ctx context.Context, id string) (artifact.Artifact, error) {
	v, err := scanArtifact(s.db.sql.QueryRowContext(ctx, selectArtifact+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func (s *ArtifactStore) ListByRun(ctx context.Context, runID string) ([]artifact.Artifact, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectArtifact+` WHERE run_id=? ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []artifact.Artifact
	for rows.Next() {
		v, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
