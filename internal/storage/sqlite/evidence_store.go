package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/evidence"
)

type EvidenceStore struct{ db *DB }

func NewEvidenceStore(db *DB) *EvidenceStore { return &EvidenceStore{db: db} }
func (s *EvidenceStore) Save(ctx context.Context, v evidence.GitEvidence) error {
	changed, err := json.Marshal(map[string]any{"version": 1, "items": v.ChangedFiles})
	if err != nil {
		return err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.db.checkRunTx(ctx, tx, v.RunID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_evidence(run_id,base_commit,head_commit,dirty,changed_files_json,diff_artifact_id,created_at,start_snapshot,end_snapshot) VALUES(?,?,?,?,?,?,?,?,?)`, v.RunID, v.BaseCommit, v.HeadCommit, v.Dirty, string(changed), nullString(v.DiffArtifactID), formatTime(v.CreatedAt), nullString(v.StartSnapshot), nullString(v.EndSnapshot))
	if err != nil {
		return conflict(fmt.Errorf("insert evidence: %w", err))
	}
	var projectID, taskID string
	if err := tx.QueryRowContext(ctx, `SELECT u.project_id,COALESCE(u.task_id,'') FROM runs u WHERE u.id=?`, v.RunID).Scan(&projectID, &taskID); err != nil {
		return err
	}
	payload, _ := event.Payload(map[string]any{"base_commit": v.BaseCommit, "head_commit": v.HeadCommit, "dirty": v.Dirty})
	if err := appendEvent(ctx, tx, event.Event{Type: event.EvidenceRecorded, ProjectID: projectID, TaskID: taskID, RunID: v.RunID, EntityType: "evidence", EntityID: v.RunID, Payload: payload, CreatedAt: v.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *EvidenceStore) Get(ctx context.Context, runID string) (evidence.GitEvidence, error) {
	return readGitEvidence(ctx, s.db.sql, runID)
}
func readGitEvidence(ctx context.Context, q definitionQuerier, runID string) (evidence.GitEvidence, error) {
	var v evidence.GitEvidence
	var changed, created string
	var diff sql.NullString
	err := q.QueryRowContext(ctx, `SELECT run_id,base_commit,head_commit,dirty,changed_files_json,diff_artifact_id,created_at,COALESCE(start_snapshot,''),COALESCE(end_snapshot,'') FROM run_evidence WHERE run_id=?`, runID).Scan(&v.RunID, &v.BaseCommit, &v.HeadCommit, &v.Dirty, &changed, &diff, &created, &v.StartSnapshot, &v.EndSnapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	var envelope struct {
		Version int      `json:"version"`
		Items   []string `json:"items"`
	}
	if err := json.Unmarshal([]byte(changed), &envelope); err != nil {
		return v, err
	}
	if envelope.Version != 1 {
		return v, fmt.Errorf("unsupported changed-files version %d", envelope.Version)
	}
	v.ChangedFiles = envelope.Items
	v.DiffArtifactID = diff.String
	v.CreatedAt, err = parseTime(created)
	return v, err
}
