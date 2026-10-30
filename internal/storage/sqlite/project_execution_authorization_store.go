package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/project"
)

// AuthorizationStore persists explicit project-level trusted execution
// authorizations. Every change appends an immutable revision row; the head
// table row only moves forward via compare-and-swap on head_revision.
type AuthorizationStore struct{ db *DB }

func NewAuthorizationStore(db *DB) *AuthorizationStore { return &AuthorizationStore{db: db} }

const projectAuthorizationV135SQL = `
CREATE TABLE project_execution_authorizations (
  project_id TEXT PRIMARY KEY REFERENCES projects(id),
  trusted_execution INTEGER NOT NULL CHECK(trusted_execution IN (0,1)),
  network_access INTEGER NOT NULL CHECK(network_access IN (0,1)),
  head_revision INTEGER NOT NULL CHECK(head_revision>0),
  authorized_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE project_execution_authorization_revisions (
  project_id TEXT NOT NULL REFERENCES projects(id),
  revision INTEGER NOT NULL CHECK(revision>0),
  trusted_execution INTEGER NOT NULL CHECK(trusted_execution IN (0,1)),
  network_access INTEGER NOT NULL CHECK(network_access IN (0,1)),
  recorded_at TEXT NOT NULL,
  PRIMARY KEY(project_id,revision)
);
CREATE TRIGGER project_execution_authorization_revisions_no_update BEFORE UPDATE ON project_execution_authorization_revisions BEGIN
  SELECT RAISE(ABORT, 'project authorization revisions are append-only');
END;
CREATE TRIGGER project_execution_authorization_revisions_no_delete BEFORE DELETE ON project_execution_authorization_revisions BEGIN
  SELECT RAISE(ABORT, 'project authorization revisions are append-only');
END;
`

func (s *AuthorizationStore) Get(ctx context.Context, projectID string) (project.ExecutionAuthorization, error) {
	var value project.ExecutionAuthorization
	var trusted, network int
	var authorizedAt, updatedAt string
	err := s.db.sql.QueryRowContext(ctx, `SELECT trusted_execution,network_access,head_revision,authorized_at,updated_at FROM project_execution_authorizations WHERE project_id=?`, projectID).Scan(&trusted, &network, &value.HeadRevision, &authorizedAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return project.ExecutionAuthorization{}, project.ErrAuthorizationNotFound
	}
	if err != nil {
		return project.ExecutionAuthorization{}, fmt.Errorf("get project execution authorization: %w", err)
	}
	authorizedTime, err := parseTime(authorizedAt)
	if err != nil {
		return project.ExecutionAuthorization{}, fmt.Errorf("parse authorized_at: %w", err)
	}
	updatedTime, err := parseTime(updatedAt)
	if err != nil {
		return project.ExecutionAuthorization{}, fmt.Errorf("parse updated_at: %w", err)
	}
	value.ProjectID, value.TrustedExecution, value.NetworkAccess = projectID, trusted == 1, network == 1
	value.AuthorizedAt, value.UpdatedAt = authorizedTime, updatedTime
	return value, nil
}

func (s *AuthorizationStore) GetRevision(ctx context.Context, projectID string, revision int) (project.ExecutionAuthorizationRevision, error) {
	var value project.ExecutionAuthorizationRevision
	var trusted, network int
	var recordedAt string
	err := s.db.sql.QueryRowContext(ctx, `SELECT trusted_execution,network_access,recorded_at FROM project_execution_authorization_revisions WHERE project_id=? AND revision=?`, projectID, revision).Scan(&trusted, &network, &recordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return project.ExecutionAuthorizationRevision{}, project.ErrAuthorizationNotFound
	}
	if err != nil {
		return project.ExecutionAuthorizationRevision{}, fmt.Errorf("get project execution authorization revision: %w", err)
	}
	recordedTime, err := parseTime(recordedAt)
	if err != nil {
		return project.ExecutionAuthorizationRevision{}, fmt.Errorf("parse recorded_at: %w", err)
	}
	value.ProjectID, value.Revision, value.TrustedExecution, value.NetworkAccess = projectID, revision, trusted == 1, network == 1
	value.RecordedAt = recordedTime
	return value, nil
}

func (s *AuthorizationStore) Apply(ctx context.Context, value project.ExecutionAuthorization, expectedRevision int) (project.ExecutionAuthorization, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return project.ExecutionAuthorization{}, err
	}
	defer tx.Rollback()
	// AuthorizedAt is frozen at the first grant; only UpdatedAt advances.
	if expectedRevision != 0 {
		var authorizedAt string
		if err = tx.QueryRowContext(ctx, `SELECT authorized_at FROM project_execution_authorizations WHERE project_id=? AND head_revision=?`, value.ProjectID, expectedRevision).Scan(&authorizedAt); err != nil {
			return project.ExecutionAuthorization{}, project.ErrConflict
		}
		parsed, parseErr := parseTime(authorizedAt)
		if parseErr != nil {
			return project.ExecutionAuthorization{}, fmt.Errorf("parse existing authorized_at: %w", parseErr)
		}
		value.AuthorizedAt = parsed
	}
	// History is append-only: the revision row must not already exist and can
	// never be rewritten; SQL triggers additionally abort UPDATE/DELETE.
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_execution_authorization_revisions(project_id,revision,trusted_execution,network_access,recorded_at) VALUES(?,?,?,?,?)`, value.ProjectID, value.HeadRevision, boolIntAuthorization(value.TrustedExecution), boolIntAuthorization(value.NetworkAccess), formatTime(value.UpdatedAt)); err != nil {
		return project.ExecutionAuthorization{}, conflict(err)
	}
	if expectedRevision == 0 {
		if value.AuthorizedAt.IsZero() {
			value.AuthorizedAt = value.UpdatedAt
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO project_execution_authorizations(project_id,trusted_execution,network_access,head_revision,authorized_at,updated_at) VALUES(?,?,?,?,?,?)`, value.ProjectID, boolIntAuthorization(value.TrustedExecution), boolIntAuthorization(value.NetworkAccess), value.HeadRevision, formatTime(value.AuthorizedAt), formatTime(value.UpdatedAt)); err != nil {
			return project.ExecutionAuthorization{}, conflict(err)
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE project_execution_authorizations SET trusted_execution=?,network_access=?,head_revision=?,authorized_at=?,updated_at=? WHERE project_id=? AND head_revision=?`, boolIntAuthorization(value.TrustedExecution), boolIntAuthorization(value.NetworkAccess), value.HeadRevision, formatTime(value.AuthorizedAt), formatTime(value.UpdatedAt), value.ProjectID, expectedRevision)
		if err != nil {
			return project.ExecutionAuthorization{}, conflict(err)
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return project.ExecutionAuthorization{}, project.ErrConflict
		}
	}
	payload, err := event.Payload(map[string]any{"trusted_execution": value.TrustedExecution, "network_access": value.NetworkAccess, "revision": value.HeadRevision})
	if err != nil {
		return project.ExecutionAuthorization{}, err
	}
	if err = appendEvent(ctx, tx, event.Event{Type: "project.execution_authorization_changed", ProjectID: value.ProjectID, EntityType: "project", EntityID: value.ProjectID, Payload: payload, CreatedAt: value.UpdatedAt}); err != nil {
		return project.ExecutionAuthorization{}, err
	}
	if err = tx.Commit(); err != nil {
		return project.ExecutionAuthorization{}, err
	}
	return value, nil
}

func boolIntAuthorization(value bool) int {
	if value {
		return 1
	}
	return 0
}
