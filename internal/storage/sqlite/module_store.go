package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/projectcontext"
)

type ModuleStore struct{ db *DB }

func NewModuleStore(db *DB) *ModuleStore { return &ModuleStore{db: db} }

func (s *ModuleStore) Create(ctx context.Context, value module.Module) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO modules(id,project_id,key,name,description,system_owned,parent_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, value.ID, value.ProjectID, value.Key, value.Name, value.Description, value.SystemOwned, nullableString(value.ParentID), formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return conflict(fmt.Errorf("insert module: %w", err))
	}
	payload, _ := event.Payload(map[string]any{"key": value.Key, "parent_id": value.ParentID, "system_owned": value.SystemOwned})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ModuleCreated, ProjectID: value.ProjectID, EntityType: "module", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}

type moduleScanner interface{ Scan(...any) error }

func scanModule(scanner moduleScanner) (module.Module, error) {
	var value module.Module
	var parent sql.NullString
	var created, updated string
	if err := scanner.Scan(&value.ID, &value.ProjectID, &value.Key, &value.Name, &value.Description, &value.SystemOwned, &parent, &created, &updated); err != nil {
		return module.Module{}, err
	}
	if parent.Valid {
		value.ParentID = &parent.String
	}
	var err error
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return module.Module{}, err
	}
	value.UpdatedAt, err = parseTime(updated)
	return value, err
}

const selectModule = `SELECT id,project_id,key,name,description,system_owned,parent_id,created_at,updated_at FROM modules`

func (s *ModuleStore) Get(ctx context.Context, id string) (module.Module, error) {
	value, err := scanModule(s.db.sql.QueryRowContext(ctx, selectModule+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return module.Module{}, module.ErrNotFound
	}
	if err != nil {
		return module.Module{}, fmt.Errorf("get module: %w", err)
	}
	value.ProductionFacts, err = s.productionFacts(ctx, value.ID)
	if err != nil {
		return module.Module{}, err
	}
	return value, nil
}

func (s *ModuleStore) ListByProject(ctx context.Context, projectID string) ([]module.Module, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectModule+` WHERE project_id=? ORDER BY key`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list modules: %w", err)
	}
	defer rows.Close()
	var values []module.Module
	for rows.Next() {
		value, err := scanModule(rows)
		if err != nil {
			return nil, err
		}
		value.ProductionFacts, err = s.productionFacts(ctx, value.ID)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *ModuleStore) ReplaceContextMap(ctx context.Context, projectID string, values []module.Module, now time.Time) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceContextMapTx(ctx, tx, projectID, values, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceContextMapAtTopology is the durable admission boundary for the
// current-only Project Module Map. Historical Module identities remain intact;
// only their current repository facts are replaced.
func (s *ModuleStore) ReplaceContextMapAtTopology(ctx context.Context, projectID string, values []module.Module, now time.Time, topologyRevision int64) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := projectRepositoryTopologyRevisionTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if current != topologyRevision {
		return projectcontext.ErrConflict
	}
	if err := replaceContextMapTx(ctx, tx, projectID, values, now); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceContextMapTx(ctx context.Context, tx *sql.Tx, projectID string, values []module.Module, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_module_map_entries WHERE project_id=?`, projectID); err != nil {
		return err
	}
	for _, value := range values {
		var moduleID string
		err := tx.QueryRowContext(ctx, `INSERT INTO modules(id,project_id,key,name,description,system_owned,parent_id,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(project_id,key) DO UPDATE SET name=excluded.name,description=excluded.description,updated_at=excluded.updated_at
			RETURNING id`,
			value.ID, projectID, value.Key, value.Name, value.Description, value.SystemOwned, nullableString(value.ParentID), formatTime(value.CreatedAt), formatTime(now)).Scan(&moduleID)
		if err != nil {
			return conflict(fmt.Errorf("upsert context module: %w", err))
		}
		for _, facts := range value.ProductionFacts {
			roots, err := encodeStringList(facts.ProductionRoots)
			if err != nil {
				return err
			}
			entrypoints, err := encodeStringList(facts.Entrypoints)
			if err != nil {
				return err
			}
			related, err := encodeStringList(facts.RelatedModules)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO project_module_map_entries(project_id,module_id,repository_id,responsibility,responsibility_source,production_roots_json,entrypoints_json,related_modules_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, projectID, moduleID, facts.RepositoryID, facts.Responsibility, facts.ResponsibilitySource, roots, entrypoints, related, formatTime(now), formatTime(now)); err != nil {
				return conflict(fmt.Errorf("insert Project Module Map entry: %w", err))
			}
		}
	}
	return nil
}

func (s *ModuleStore) productionFacts(ctx context.Context, moduleID string) ([]module.ProductionFacts, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT e.repository_id,r.name,e.responsibility,e.responsibility_source,e.production_roots_json,e.entrypoints_json,e.related_modules_json
		FROM project_module_map_entries e JOIN repositories r ON r.id=e.repository_id WHERE e.module_id=? ORDER BY r.name,r.id`, moduleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]module.ProductionFacts, 0)
	for rows.Next() {
		var value module.ProductionFacts
		var roots, entrypoints, related string
		if err := rows.Scan(&value.RepositoryID, &value.RepositoryName, &value.Responsibility, &value.ResponsibilitySource, &roots, &entrypoints, &related); err != nil {
			return nil, err
		}
		if value.ProductionRoots, err = decodeStringList(roots); err != nil {
			return nil, err
		}
		if value.Entrypoints, err = decodeStringList(entrypoints); err != nil {
			return nil, err
		}
		if value.RelatedModules, err = decodeStringList(related); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func encodeStringList(values []string) (string, error) {
	encoded, err := json.Marshal(struct {
		Version int      `json:"version"`
		Items   []string `json:"items"`
	}{Version: 1, Items: append([]string{}, values...)})
	return string(encoded), err
}

func decodeStringList(encoded string) ([]string, error) {
	var envelope struct {
		Version int      `json:"version"`
		Items   []string `json:"items"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil || envelope.Version != 1 || envelope.Items == nil {
		if err == nil {
			err = errors.New("unsupported Project Module Map list encoding")
		}
		return nil, err
	}
	return append([]string(nil), envelope.Items...), nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
