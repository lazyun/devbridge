package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/repository"
)

type RepositoryStore struct{ db *DB }

func NewRepositoryStore(db *DB) *RepositoryStore { return &RepositoryStore{db: db} }

func (s *RepositoryStore) PutIfAbsent(ctx context.Context, value repository.Repository) (repository.Repository, bool, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return repository.Repository{}, false, err
	}
	defer tx.Rollback()
	if value.RegistrationState == "" {
		value.RegistrationState = repository.RegistrationActive
	}
	if value.RegistrationGeneration == 0 {
		value.RegistrationGeneration = 1
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO repositories(id,project_id,name,canonical_path,registration_state,registration_generation,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(canonical_path) DO NOTHING`, value.ID, value.ProjectID, value.Name, value.Path, value.RegistrationState, value.RegistrationGeneration, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return repository.Repository{}, false, conflict(fmt.Errorf("insert repository: %w", err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return repository.Repository{}, false, err
	}
	if affected == 0 {
		var existing repository.Repository
		var created, updated string
		var unlinked sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT id,project_id,name,canonical_path,registration_state,registration_generation,unlinked_at,COALESCE(unlink_request_id,''),COALESCE(unlink_reason,''),created_at,updated_at FROM repositories WHERE canonical_path=?`, value.Path).Scan(&existing.ID, &existing.ProjectID, &existing.Name, &existing.Path, &existing.RegistrationState, &existing.RegistrationGeneration, &unlinked, &existing.UnlinkRequestID, &existing.UnlinkReason, &created, &updated)
		if err != nil {
			return repository.Repository{}, false, fmt.Errorf("find repository path: %w", err)
		}
		if existing.ProjectID != value.ProjectID {
			return repository.Repository{}, false, repository.ErrPathConflict
		}
		if existing.RegistrationState == repository.RegistrationUnlinked {
			return repository.Repository{}, false, repository.ErrUnlinkedHistoryExists
		}
		existing.CreatedAt, err = parseTime(created)
		if err != nil {
			return repository.Repository{}, false, fmt.Errorf("parse repository created_at: %w", err)
		}
		existing.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return repository.Repository{}, false, fmt.Errorf("parse repository updated_at: %w", err)
		}
		if unlinked.Valid {
			at, parseErr := parseTime(unlinked.String)
			if parseErr != nil {
				return repository.Repository{}, false, parseErr
			}
			existing.UnlinkedAt = &at
		}
		return existing, false, nil
	}
	payload, _ := event.Payload(map[string]any{"canonical_path": value.Path, "name": value.Name})
	if err := appendEvent(ctx, tx, event.Event{Type: event.RepositoryRegistered, ProjectID: value.ProjectID, EntityType: "repository", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return repository.Repository{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return repository.Repository{}, false, err
	}
	return value, true, nil
}

func scanRepository(scanner interface{ Scan(...any) error }) (repository.Repository, error) {
	var value repository.Repository
	var created, updated string
	var unlinked sql.NullString
	err := scanner.Scan(&value.ID, &value.ProjectID, &value.Name, &value.Path, &value.RegistrationState, &value.RegistrationGeneration, &unlinked, &value.UnlinkRequestID, &value.UnlinkReason, &created, &updated)
	if err != nil {
		return repository.Repository{}, err
	}
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return repository.Repository{}, err
	}
	value.UpdatedAt, err = parseTime(updated)
	if err == nil && unlinked.Valid {
		at, parseErr := parseTime(unlinked.String)
		if parseErr != nil {
			return repository.Repository{}, parseErr
		}
		value.UnlinkedAt = &at
	}
	return value, err
}
func (s *RepositoryStore) Get(ctx context.Context, id string) (repository.Repository, error) {
	v, err := scanRepository(s.db.sql.QueryRowContext(ctx, `SELECT id,project_id,name,canonical_path,registration_state,registration_generation,unlinked_at,COALESCE(unlink_request_id,''),unlink_reason,created_at,updated_at FROM repositories WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return repository.Repository{}, repository.ErrNotFound
	}
	if err != nil {
		return repository.Repository{}, fmt.Errorf("get repository: %w", err)
	}
	return v, nil
}
func (s *RepositoryStore) List(ctx context.Context) ([]repository.Repository, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT id,project_id,name,canonical_path,registration_state,registration_generation,unlinked_at,COALESCE(unlink_request_id,''),unlink_reason,created_at,updated_at FROM repositories WHERE registration_state='ACTIVE' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []repository.Repository
	for rows.Next() {
		v, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (s *RepositoryStore) ListAll(ctx context.Context) ([]repository.Repository, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT id,project_id,name,canonical_path,registration_state,registration_generation,unlinked_at,COALESCE(unlink_request_id,''),unlink_reason,created_at,updated_at FROM repositories ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []repository.Repository
	for rows.Next() {
		v, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (s *RepositoryStore) TopologyRevision(ctx context.Context, projectID string) (int64, error) {
	var revision int64
	err := s.db.sql.QueryRowContext(ctx, `SELECT revision FROM project_repository_topologies WHERE project_id=?`, projectID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return revision, err
}

// ensureRepositoryActiveTx is the SQLite write-boundary guard shared by all
// new engineering admissions. Historical reads intentionally use Get instead.
func ensureRepositoryActiveTx(ctx context.Context, tx *sql.Tx, projectID, repositoryID string) error {
	var state repository.RegistrationState
	err := tx.QueryRowContext(ctx, `SELECT registration_state FROM repositories WHERE id=? AND project_id=?`, repositoryID, projectID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM repositories WHERE id=?)`, repositoryID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return repository.ErrNotFound
		}
		return repository.ErrProjectMismatch
	}
	if err != nil {
		return err
	}
	if state != repository.RegistrationActive {
		return repository.ErrRepositoryUnlinked
	}
	return nil
}

func projectRepositoryTopologyRevisionTx(ctx context.Context, tx *sql.Tx, projectID string) (int64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM project_repository_topologies WHERE project_id=?`, projectID).Scan(&revision)
	return revision, err
}
