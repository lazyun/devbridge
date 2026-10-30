package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/developmentcycle"
)

// No existing Project or workflow row is updated by this migration.
const schemaV33 = `
CREATE TABLE development_cycles (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 repository_id TEXT NOT NULL REFERENCES repositories(id),
 development_ref TEXT NOT NULL CHECK(development_ref LIKE 'refs/heads/%' AND length(development_ref)>11),
 selected_head_sha TEXT NOT NULL,
 created_at TEXT NOT NULL,
 superseded_at TEXT,
 end_reason TEXT CHECK(end_reason IS NULL OR end_reason='BRANCH_RESELECTED'),
 request_id TEXT NOT NULL UNIQUE CHECK(length(request_id)>0 AND length(request_id)<=128),
 selected_by_ai_session_id TEXT REFERENCES ai_sessions(id),
 CHECK((superseded_at IS NULL AND end_reason IS NULL) OR (superseded_at IS NOT NULL AND end_reason='BRANCH_RESELECTED'))
);
CREATE UNIQUE INDEX development_cycles_one_current ON development_cycles(project_id,repository_id) WHERE superseded_at IS NULL;
CREATE TRIGGER development_cycle_project BEFORE INSERT ON development_cycles
WHEN NOT EXISTS(SELECT 1 FROM repositories WHERE id=NEW.repository_id AND project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'REPOSITORY_PROJECT_MISMATCH'); END;
CREATE TRIGGER development_cycle_session_project BEFORE INSERT ON development_cycles
WHEN NEW.selected_by_ai_session_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM ai_sessions WHERE id=NEW.selected_by_ai_session_id AND project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'REPOSITORY_PROJECT_MISMATCH'); END;
CREATE TRIGGER development_cycle_immutable BEFORE UPDATE ON development_cycles
WHEN OLD.superseded_at IS NOT NULL OR NEW.superseded_at IS NULL OR NEW.end_reason IS NOT 'BRANCH_RESELECTED'
 OR NEW.id IS NOT OLD.id OR NEW.project_id IS NOT OLD.project_id OR NEW.repository_id IS NOT OLD.repository_id
 OR NEW.development_ref IS NOT OLD.development_ref OR NEW.selected_head_sha IS NOT OLD.selected_head_sha
 OR NEW.created_at IS NOT OLD.created_at OR NEW.request_id IS NOT OLD.request_id
 OR NEW.selected_by_ai_session_id IS NOT OLD.selected_by_ai_session_id
BEGIN SELECT RAISE(ABORT,'DevelopmentCycle provenance is immutable'); END;
CREATE TRIGGER development_cycle_no_delete BEFORE DELETE ON development_cycles
BEGIN SELECT RAISE(ABORT,'DevelopmentCycle is durable history'); END;
CREATE TABLE development_branch_requests (
 request_id TEXT PRIMARY KEY REFERENCES development_cycles(request_id),
 result_json TEXT NOT NULL
);
CREATE TRIGGER development_branch_request_immutable BEFORE UPDATE ON development_branch_requests
BEGIN SELECT RAISE(ABORT,'Development branch receipt is immutable'); END;
CREATE TRIGGER development_branch_request_no_delete BEFORE DELETE ON development_branch_requests
BEGIN SELECT RAISE(ABORT,'Development branch receipt is durable'); END;
`

type DevelopmentCycleStore struct{ db *DB }

func NewDevelopmentCycleStore(db *DB) *DevelopmentCycleStore { return &DevelopmentCycleStore{db: db} }

const cycleColumns = `SELECT id,project_id,repository_id,development_ref,selected_head_sha,created_at,superseded_at,COALESCE(end_reason,''),request_id,COALESCE(selected_by_ai_session_id,'') FROM development_cycles`

func scanCycle(row interface{ Scan(...any) error }) (developmentcycle.Cycle, error) {
	var c developmentcycle.Cycle
	var created string
	var ended sql.NullString
	err := row.Scan(&c.ID, &c.ProjectID, &c.RepositoryID, &c.DevelopmentRef, &c.SelectedHeadSHA, &created, &ended, &c.EndReason, &c.RequestID, &c.SelectedByAISessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return c, developmentcycle.ErrNotSelected
	}
	if err != nil {
		return c, err
	}
	c.CreatedAt, err = parseTime(created)
	if err != nil {
		return c, err
	}
	if ended.Valid {
		value, e := parseTime(ended.String)
		if e != nil {
			return c, e
		}
		c.SupersededAt = &value
	}
	return c, nil
}
func (s *DevelopmentCycleStore) Current(ctx context.Context, projectID, repoID string) (developmentcycle.Cycle, error) {
	return scanCycle(s.db.sql.QueryRowContext(ctx, cycleColumns+` WHERE project_id=? AND repository_id=? AND superseded_at IS NULL`, projectID, repoID))
}
func readBranchRequest(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, in developmentcycle.SelectInput) (developmentcycle.Cycle, error) {
	var encoded string
	var c developmentcycle.Cycle
	err := q.QueryRowContext(ctx, `SELECT result_json FROM development_branch_requests WHERE request_id=?`, in.RequestID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return c, developmentcycle.ErrNotSelected
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal([]byte(encoded), &c); err != nil {
		return c, err
	}
	if !developmentcycle.SamePayload(c, in) {
		return developmentcycle.Cycle{}, developmentcycle.ErrRequestConflict
	}
	return c, nil
}
func (s *DevelopmentCycleStore) Request(ctx context.Context, in developmentcycle.SelectInput) (developmentcycle.Cycle, error) {
	return readBranchRequest(ctx, s.db.sql, in)
}
func (s *DevelopmentCycleStore) Select(ctx context.Context, c developmentcycle.Cycle) (developmentcycle.Cycle, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return developmentcycle.Cycle{}, err
	}
	defer tx.Rollback()
	in := developmentcycle.SelectInput{ProjectID: c.ProjectID, RepositoryID: c.RepositoryID, DevelopmentRef: c.DevelopmentRef, RequestID: c.RequestID}
	if prior, e := readBranchRequest(ctx, tx, in); !errors.Is(e, developmentcycle.ErrNotSelected) {
		return prior, e
	}
	// The replay receipt above is historical evidence. A new selection, however,
	// must be admitted against the repository state while this writer transaction
	// is held: an unlink that commits between the service's Git observation and
	// this insert may never create a current DevelopmentCycle.
	if err := ensureRepositoryActiveTx(ctx, tx, c.ProjectID, c.RepositoryID); err != nil {
		return developmentcycle.Cycle{}, err
	}
	// A new cycle would invalidate the frozen identity used by delivery recovery,
	// even if the user selects the same ref and HEAD. Serialize this check with
	// delivery reservation; exact historical request replay above remains readable.
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pending_feature_area_commits r WHERE r.completed_at IS NULL AND (
	 (r.protocol_version=1 AND r.repository_id=?) OR EXISTS(SELECT 1 FROM feature_area_commit_members m WHERE m.request_id=r.request_id AND m.repository_id=?))) OR `+pendingTaskDeliveryRepositorySQL, c.RepositoryID, c.RepositoryID, c.RepositoryID, c.RepositoryID).Scan(&pending); err != nil {
		return developmentcycle.Cycle{}, err
	}
	if pending {
		return developmentcycle.Cycle{}, developmentcycle.ErrFinalizationPending
	}
	// Allocate the transition time after acquiring the write lock, so concurrent
	// commands cannot supersede a cycle before its creation timestamp.
	c.CreatedAt = time.Now().UTC()
	_, err = tx.ExecContext(ctx, `UPDATE development_cycles SET superseded_at=?,end_reason='BRANCH_RESELECTED' WHERE project_id=? AND repository_id=? AND superseded_at IS NULL`, formatTime(c.CreatedAt), c.ProjectID, c.RepositoryID)
	if err != nil {
		return developmentcycle.Cycle{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO development_cycles(id,project_id,repository_id,development_ref,selected_head_sha,created_at,request_id,selected_by_ai_session_id) VALUES(?,?,?,?,?,?,?,?)`, c.ID, c.ProjectID, c.RepositoryID, c.DevelopmentRef, c.SelectedHeadSHA, formatTime(c.CreatedAt), c.RequestID, nullString(c.SelectedByAISessionID))
	if err != nil {
		return developmentcycle.Cycle{}, err
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return developmentcycle.Cycle{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO development_branch_requests(request_id,result_json) VALUES(?,?)`, c.RequestID, string(encoded)); err != nil {
		return developmentcycle.Cycle{}, err
	}
	if err = tx.Commit(); err != nil {
		return developmentcycle.Cycle{}, err
	}
	return c, nil
}
