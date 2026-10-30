package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"github.com/lazyun/devbridge/internal/repository"
)

type ProjectRegistrationStore struct{ db *DB }

func NewProjectRegistrationStore(db *DB) *ProjectRegistrationStore {
	return &ProjectRegistrationStore{db: db}
}

type registrationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func replayRegistration(ctx context.Context, q registrationQuery, id, hash string) (projectregistration.Registration, bool, error) {
	var stored, raw string
	err := q.QueryRowContext(ctx, `SELECT payload_sha256,result_json FROM project_registration_receipts WHERE request_id=?`, id).Scan(&stored, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return projectregistration.Registration{}, false, nil
	}
	if err != nil {
		return projectregistration.Registration{}, false, err
	}
	if stored != hash {
		return projectregistration.Registration{}, true, projectregistration.ErrRequestConflict
	}
	var result projectregistration.Registration
	err = json.Unmarshal([]byte(raw), &result)
	return result, true, err
}
func (s *ProjectRegistrationStore) ReplayRegistration(ctx context.Context, id, hash string) (projectregistration.Registration, bool, error) {
	return replayRegistration(ctx, s.db.sql, id, hash)
}

func (s *ProjectRegistrationStore) RegisterAggregate(ctx context.Context, input projectregistration.Input, hash string) (projectregistration.Registration, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return projectregistration.Registration{}, err
	}
	defer tx.Rollback()
	if result, found, e := replayRegistration(ctx, tx, input.RequestID, hash); found || e != nil {
		return result, e
	}
	result := projectregistration.Registration{RequestID: input.RequestID, Repositories: []repository.Repository{}}
	now := time.Now().UTC()
	if input.ProjectID != "" {
		var created, updated string
		err = tx.QueryRowContext(ctx, `SELECT id,name,created_at,updated_at FROM projects WHERE id=?`, input.ProjectID).Scan(&result.Project.ID, &result.Project.Name, &created, &updated)
		if errors.Is(err, sql.ErrNoRows) {
			return result, project.ErrNotFound
		}
		if err != nil {
			return result, err
		}
		if result.Project.CreatedAt, err = parseTime(created); err != nil {
			return result, err
		}
		if result.Project.UpdatedAt, err = parseTime(updated); err != nil {
			return result, err
		}
	} else {
		result.Project = project.Project{ID: "project_" + uuid.NewString(), Name: input.Name, CreatedAt: now, UpdatedAt: now}
	}
	// Recheck every canonical path and alias while holding the database writer.
	// A conflicting member prevents every business write, including Project creation.
	for _, r := range input.Repositories {
		existing, e := scanRepository(tx.QueryRowContext(ctx, `SELECT id,project_id,name,canonical_path,registration_state,registration_generation,unlinked_at,COALESCE(unlink_request_id,''),unlink_reason,created_at,updated_at FROM repositories WHERE canonical_path=?`, r.Path))
		if e == nil {
			if existing.RegistrationState == repository.RegistrationUnlinked {
				return projectregistration.Registration{}, repository.ErrUnlinkedHistoryExists
			}
			if existing.ProjectID != result.Project.ID {
				if input.ProjectID != "" {
					return projectregistration.Registration{}, repository.ErrPathConflict
				}
				var p project.Project
				var created, updated string
				if err := tx.QueryRowContext(ctx, `SELECT id,name,created_at,updated_at FROM projects WHERE id=?`, existing.ProjectID).Scan(&p.ID, &p.Name, &created, &updated); err != nil {
					return projectregistration.Registration{}, err
				}
				p.CreatedAt, _ = parseTime(created)
				p.UpdatedAt, _ = parseTime(updated)
				original := projectregistration.Registration{Project: p, Repository: existing, Repositories: []repository.Repository{existing}}
				return original, &projectregistration.AlreadyRegisteredError{Registration: original}
			}
			result.Repositories = append(result.Repositories, existing)
			continue
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return projectregistration.Registration{}, e
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM repositories WHERE project_id=? AND name=?`, result.Project.ID, r.Alias).Scan(&count); err != nil {
			return projectregistration.Registration{}, err
		}
		if count != 0 {
			return projectregistration.Registration{}, fmt.Errorf("%w: repository alias already used in project", projectregistration.ErrRequestConflict)
		}
		result.Repositories = append(result.Repositories, repository.Repository{ID: "repository_" + uuid.NewString(), ProjectID: result.Project.ID, Name: r.Alias, Path: r.Path, CreatedAt: now, UpdatedAt: now})
	}
	if input.ProjectID == "" {
		p := result.Project
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id,name,created_at,updated_at) VALUES(?,?,?,?)`, p.ID, p.Name, formatTime(now), formatTime(now)); err != nil {
			return projectregistration.Registration{}, err
		}
		payload, _ := event.Payload(map[string]any{"name": p.Name, "request_id": input.RequestID})
		if err := appendEvent(ctx, tx, event.Event{Type: event.ProjectCreated, ProjectID: p.ID, EntityType: "project", EntityID: p.ID, Payload: payload, CreatedAt: now}); err != nil {
			return projectregistration.Registration{}, err
		}
	}
	for _, r := range result.Repositories {
		// Existing members are returned as already-associated facts, not reassigned.
		written, err := tx.ExecContext(ctx, `INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(canonical_path) DO NOTHING`, r.ID, r.ProjectID, r.Name, r.Path, formatTime(r.CreatedAt), formatTime(r.UpdatedAt))
		if err != nil {
			return projectregistration.Registration{}, err
		}
		n, err := written.RowsAffected()
		if err != nil {
			return projectregistration.Registration{}, err
		}
		if n > 0 {
			payload, _ := event.Payload(map[string]any{"canonical_path": r.Path, "name": r.Name, "request_id": input.RequestID})
			if err := appendEvent(ctx, tx, event.Event{Type: event.RepositoryRegistered, ProjectID: r.ProjectID, EntityType: "repository", EntityID: r.ID, Payload: payload, CreatedAt: now}); err != nil {
				return projectregistration.Registration{}, err
			}
		}
	}
	// Legacy projection exists only for a genuinely single-member result.
	if len(result.Repositories) == 1 {
		result.Repository = result.Repositories[0]
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return projectregistration.Registration{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_registration_receipts(request_id,payload_sha256,result_json,created_at) VALUES(?,?,?,?)`, input.RequestID, hash, string(raw), formatTime(now)); err != nil {
		return projectregistration.Registration{}, err
	}
	if err = tx.Commit(); err != nil {
		return projectregistration.Registration{}, err
	}
	return result, nil
}
