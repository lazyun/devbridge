package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/repositoryunlink"
)

// RepositoryUnlinkStore is the sole durable eligibility and commit authority.
// Its commit path performs no Git or filesystem I/O.
type RepositoryUnlinkStore struct{ db *DB }

func NewRepositoryUnlinkStore(db *DB) *RepositoryUnlinkStore { return &RepositoryUnlinkStore{db: db} }

func unlinkPayloadHash(in repositoryunlink.CommitInput) string {
	b, _ := json.Marshal(struct {
		ProjectID, RepositoryID string
		Generation, Topology    int64
		Reason                  string
	}{in.ProjectID, in.RepositoryID, in.ExpectedGeneration, in.ExpectedTopologyRevision, in.Reason})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func repositoryUnlinkRow(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, repositoryID string) (repository.Repository, int64, error) {
	var r repository.Repository
	var unlinked sql.NullString
	var created, updated string
	var topology int64
	err := q.QueryRowContext(ctx, `SELECT r.id,r.project_id,r.name,r.canonical_path,r.registration_state,r.registration_generation,r.unlinked_at,COALESCE(r.unlink_request_id,''),r.unlink_reason,r.created_at,r.updated_at,t.revision FROM repositories r JOIN project_repository_topologies t ON t.project_id=r.project_id WHERE r.id=?`, repositoryID).Scan(&r.ID, &r.ProjectID, &r.Name, &r.Path, &r.RegistrationState, &r.RegistrationGeneration, &unlinked, &r.UnlinkRequestID, &r.UnlinkReason, &created, &updated, &topology)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM repositories WHERE id=?)`, repositoryID).Scan(&exists); err != nil {
			return r, 0, err
		}
		if !exists {
			return r, 0, repository.ErrNotFound
		}
		return r, 0, repository.ErrProjectMismatch
	}
	if err != nil {
		return r, 0, err
	}
	if r.ProjectID != projectID {
		return r, topology, repository.ErrProjectMismatch
	}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return r, 0, err
	}
	if r.UpdatedAt, err = parseTime(updated); err != nil {
		return r, 0, err
	}
	if unlinked.Valid {
		at, e := parseTime(unlinked.String)
		if e != nil {
			return r, 0, e
		}
		r.UnlinkedAt = &at
	}
	return r, topology, nil
}

func blocker(kind, entityType, entityID, ownerType, ownerID, status, reason, summary string) repositoryunlink.Blocker {
	return repositoryunlink.Blocker{BlockerKind: kind, EntityType: entityType, EntityID: entityID, OwnerType: ownerType, OwnerID: ownerID, Status: status, ReasonCode: reason, UserFacingSummary: summary, AvailableResolution: "Resolve or safely close the current operation before unlinking."}
}

func appendUnlinkBlockers(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, projectID, repositoryID string, out *[]repositoryunlink.Blocker) error {
	// Active tasks/definitions are current authority; terminal task history is not.
	checks := []struct{ query, kind, typ, owner string }{
		{`SELECT id,status FROM tasks WHERE project_id=? AND repository_id=? AND status='ACTIVE'`, "TASK", "task", "TASK"},
		{`SELECT t.id,t.status FROM tasks t JOIN task_definition_revisions d ON d.id=t.current_definition_revision_id JOIN json_each(d.payload_json,'$.repositoryTargets') x WHERE t.project_id=? AND t.status='ACTIVE' AND json_extract(x.value,'$.repositoryId')=?`, "TASK_DEFINITION", "task_definition", "TASK"},
		{`SELECT v.id,COALESCE(v.review_disposition,'PENDING') FROM reviews v WHERE v.project_id=? AND v.repository_id=? AND (EXISTS(SELECT 1 FROM tasks t WHERE t.id=v.task_id AND t.project_id=v.project_id AND t.status='ACTIVE' AND t.current_review_id=v.id) OR EXISTS(SELECT 1 FROM runs r WHERE (r.id=v.linked_run_id OR r.review_id=v.id) AND r.status NOT IN ('SUCCEEDED','FAILED','CANCELED','INFRASTRUCTURE_FAILED')))`, "REVIEW", "review", "REVIEW"},
		{`SELECT v.id,COALESCE(v.review_disposition,'PENDING') FROM reviews v JOIN json_each(v.repository_targets_json) x WHERE v.project_id=? AND json_extract(x.value,'$.repositoryId')=? AND (EXISTS(SELECT 1 FROM tasks t WHERE t.id=v.task_id AND t.project_id=v.project_id AND t.status='ACTIVE' AND t.current_review_id=v.id) OR EXISTS(SELECT 1 FROM runs r WHERE (r.id=v.linked_run_id OR r.review_id=v.id) AND r.status NOT IN ('SUCCEEDED','FAILED','CANCELED','INFRASTRUCTURE_FAILED')))`, "REVIEW", "review", "REVIEW"},
		{`SELECT r.id,r.status FROM runs r JOIN run_repository_bindings b ON b.run_id=r.id WHERE r.project_id=? AND b.repository_id=? AND r.status NOT IN ('SUCCEEDED','FAILED','CANCELED','INFRASTRUCTURE_FAILED')`, "RUN", "run", "RUN"},
		{`SELECT json_extract(j.value,'$.runtimeRequestId'),json_extract(j.value,'$.state')
		   FROM agent_runtime_states a
		   JOIN runs r ON r.id=a.run_id
		   JOIN run_repository_bindings b ON b.run_id=r.id
		   JOIN json_each(a.snapshot_json,'$.requests') j
		  WHERE r.project_id=? AND b.repository_id=?
		    AND r.status NOT IN ('SUCCEEDED','FAILED','CANCELED','INFRASTRUCTURE_FAILED')
		    AND (json_extract(j.value,'$.state') IN ('PENDING','INTENDED','SENT','WAITING_FOR_USER','USER_DECISION_RECORDED','RESPONDING','UNKNOWN')
		      OR json_extract(j.value,'$.delivery') IN ('NOT_DISPATCHED','DISPATCHING','UNKNOWN','TRANSPORT_WRITTEN'))`, "RUNTIME_REQUEST", "runtime_request", "RUN"},
		{`SELECT a.id,a.status FROM feature_areas a JOIN workspaces w ON w.id=a.current_workspace_id JOIN workspace_members m ON m.workspace_id=w.id WHERE a.project_id=? AND m.repository_id=? AND a.status IN ('DISCOVERING','ACTIVE','RETIRING')`, "WORKSPACE_MEMBER", "workspace_member", "FEATURE_AREA"},
		{`SELECT p.id,p.status FROM context_probes p JOIN workspaces w ON w.id=p.workspace_id JOIN workspace_members m ON m.workspace_id=w.id WHERE p.project_id=? AND m.repository_id=? AND p.status NOT IN ('SUCCEEDED','NO_GUIDANCE','FAILED','CANCELED')`, "PROJECT_INSPECTION", "context_probe", "PROJECT_INSPECTION"},
		{`SELECT workflow_id,state FROM direct_file_task_authorizations WHERE project_id=? AND repository_id=? AND state NOT IN ('CLOSED')`, "DIRECT_FILE", "direct_file_authorization", "DIRECT_FILE"},
		{`SELECT g.workflow_id,g.state FROM direct_file_task_authorizations g JOIN direct_file_task_members m ON m.workflow_id=g.workflow_id WHERE g.project_id=? AND m.repository_id=? AND g.state NOT IN ('CLOSED')`, "DIRECT_FILE", "direct_file_member", "DIRECT_FILE"},
		{`SELECT o.request_id,o.state FROM direct_file_operation_receipts o JOIN direct_file_task_authorizations g ON g.workflow_id=o.workflow_id WHERE g.project_id=? AND o.repository_id=? AND o.state IN ('PENDING','UNKNOWN')`, "DIRECT_FILE", "direct_file_operation_receipt", "DIRECT_FILE"},
		{`SELECT id,state FROM recovery_status_records WHERE state NOT IN ('RESOLVED','FAILED') AND (source_run_id IN (SELECT r.id FROM runs r JOIN run_repository_bindings b ON b.run_id=r.id WHERE r.project_id=? AND b.repository_id=?) OR recovery_run_id IN (SELECT r.id FROM runs r JOIN run_repository_bindings b ON b.run_id=r.id WHERE r.project_id=? AND b.repository_id=?))`, "RECOVERY", "recovery_status", "RECOVERY"},
		{`SELECT task_id,COALESCE(completed_at,'PENDING') FROM task_commit_receipts WHERE task_id IN (SELECT id FROM tasks WHERE project_id=? AND repository_id=?) AND completed_at IS NULL`, "DELIVERY", "task_commit_receipt", "TASK"},
		{`SELECT m.task_id,m.state FROM task_commit_members m JOIN task_commit_receipts c ON c.task_id=m.task_id JOIN tasks t ON t.id=m.task_id WHERE t.project_id=? AND m.repository_id=? AND c.completed_at IS NULL`, "DELIVERY", "task_commit_member", "TASK"},
		{`SELECT f.feature_area_id,COALESCE(f.completed_at,'PENDING') FROM pending_feature_area_commits f WHERE f.repository_id=? AND f.completed_at IS NULL AND EXISTS(SELECT 1 FROM feature_areas a WHERE a.id=f.feature_area_id AND a.project_id=?)`, "DELIVERY", "feature_area_commit_receipt", "FEATURE_AREA"},
		{`SELECT m.request_id,m.state FROM feature_area_commit_members m JOIN pending_feature_area_commits f ON f.request_id=m.request_id JOIN feature_areas a ON a.id=f.feature_area_id WHERE a.project_id=? AND m.repository_id=? AND f.completed_at IS NULL`, "DELIVERY", "feature_area_commit_member", "FEATURE_AREA"},
	}
	for _, check := range checks {
		args := []any{projectID, repositoryID}
		if check.kind == "RECOVERY" {
			args = append(args, projectID, repositoryID)
		}
		if check.kind == "DELIVERY" && check.typ == "feature_area_commit_receipt" {
			args = []any{repositoryID, projectID}
		}
		rows, err := q.QueryContext(ctx, check.query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, status string
			if err := rows.Scan(&id, &status); err != nil {
				rows.Close()
				return err
			}
			*out = append(*out, blocker(check.kind, check.typ, id, check.owner, id, status, "ACTIVE_REPOSITORY_AUTHORITY", "This repository is still used by active engineering authority."))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func appendUnlinkReferences(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, projectID, repositoryID string, out *[]repositoryunlink.Reference) error {
	rows, err := q.QueryContext(ctx, `
		SELECT 'task',id,'Terminal Task history remains available' FROM tasks
		 WHERE project_id=? AND repository_id=? AND status<>'ACTIVE'
		UNION ALL
		SELECT 'run',r.id,'Terminal Run and Repository binding remain available'
		  FROM runs r JOIN run_repository_bindings b ON b.run_id=r.id
		 WHERE r.project_id=? AND b.repository_id=? AND r.status IN ('SUCCEEDED','FAILED','CANCELED','INFRASTRUCTURE_FAILED')
		UNION ALL
		SELECT 'development_cycle',id,'Development branch selection history remains available'
		  FROM development_cycles WHERE project_id=? AND repository_id=?
		ORDER BY 1,2`, projectID, repositoryID, projectID, repositoryID, projectID, repositoryID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ref repositoryunlink.Reference
		if err := rows.Scan(&ref.EntityType, &ref.EntityID, &ref.Summary); err != nil {
			return err
		}
		*out = append(*out, ref)
	}
	return rows.Err()
}

func eligibility(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, projectID, repositoryID string) (repositoryunlink.Eligibility, error) {
	r, topology, err := repositoryUnlinkRow(ctx, q, projectID, repositoryID)
	if err != nil {
		return repositoryunlink.Eligibility{}, err
	}
	result := repositoryunlink.Eligibility{ProjectID: projectID, RepositoryID: repositoryID, RegistrationState: r.RegistrationState, RegistrationGeneration: r.RegistrationGeneration, ProjectTopologyRevision: topology, Blockers: []repositoryunlink.Blocker{}, InformationalReferences: []repositoryunlink.Reference{}, EvaluatedAt: time.Now().UTC()}
	if r.RegistrationState != repository.RegistrationActive {
		return result, repositoryunlink.ErrAlreadyUnlinked
	}
	if err := appendUnlinkBlockers(ctx, q, projectID, repositoryID, &result.Blockers); err != nil {
		return result, err
	}
	if err := appendUnlinkReferences(ctx, q, projectID, repositoryID, &result.InformationalReferences); err != nil {
		return result, err
	}
	result.CanUnlink = len(result.Blockers) == 0
	return result, nil
}

func (s *RepositoryUnlinkStore) Eligibility(ctx context.Context, projectID, repositoryID string) (repositoryunlink.Eligibility, error) {
	return eligibility(ctx, s.db.sql, projectID, repositoryID)
}

func (s *RepositoryUnlinkStore) Commit(ctx context.Context, in repositoryunlink.CommitInput) (repositoryunlink.Eligibility, error) {
	if err := in.Validate(); err != nil {
		return repositoryunlink.Eligibility{}, err
	}
	payloadHash := unlinkPayloadHash(in)
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return repositoryunlink.Eligibility{}, err
	}
	defer tx.Rollback()
	var priorHash, priorResult string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256,result_json FROM project_repository_unlink_receipts WHERE request_id=?`, in.RequestID).Scan(&priorHash, &priorResult)
	if err == nil {
		if priorHash != payloadHash {
			return repositoryunlink.Eligibility{}, repositoryunlink.ErrRequestConflict
		}
		var prior repositoryunlink.Eligibility
		if e := json.Unmarshal([]byte(priorResult), &prior); e != nil {
			return repositoryunlink.Eligibility{}, e
		}
		return prior, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return repositoryunlink.Eligibility{}, err
	}
	current, err := eligibility(ctx, tx, in.ProjectID, in.RepositoryID)
	if err != nil {
		return current, err
	}
	if current.RegistrationGeneration != in.ExpectedGeneration || current.ProjectTopologyRevision != in.ExpectedTopologyRevision {
		return current, repositoryunlink.ErrStale
	}
	if !current.CanUnlink {
		return current, repositoryunlink.ErrBlocked
	}
	now := time.Now().UTC()
	updated, err := tx.ExecContext(ctx, `UPDATE repositories SET registration_state='UNLINKED',registration_generation=registration_generation+1,unlinked_at=?,unlink_request_id=?,unlink_reason=?,updated_at=? WHERE id=? AND project_id=? AND registration_state='ACTIVE' AND registration_generation=?`, formatTime(now), in.RequestID, in.Reason, formatTime(now), in.RepositoryID, in.ProjectID, in.ExpectedGeneration)
	if err != nil {
		return current, err
	}
	if n, e := updated.RowsAffected(); e != nil || n != 1 {
		return current, repositoryunlink.ErrStale
	}
	if _, err = tx.ExecContext(ctx, `UPDATE development_cycles SET superseded_at=?,end_reason='REPOSITORY_UNLINKED' WHERE project_id=? AND repository_id=? AND superseded_at IS NULL`, formatTime(now), in.ProjectID, in.RepositoryID); err != nil {
		return current, err
	}
	updated, err = tx.ExecContext(ctx, `UPDATE project_repository_topologies SET revision=revision+1,updated_at=? WHERE project_id=? AND revision=?`, formatTime(now), in.ProjectID, in.ExpectedTopologyRevision)
	if err != nil {
		return current, err
	}
	if n, e := updated.RowsAffected(); e != nil || n != 1 {
		return current, repositoryunlink.ErrStale
	}
	if _, err = tx.ExecContext(ctx, `UPDATE project_contexts SET status='REQUIRED',root_guidance_identity=NULL,context_fingerprint='',diagnostics_json='{"version":1,"items":[]}',result=NULL,result_artifact_ref=NULL,updated_at=? WHERE project_id=?`, formatTime(now), in.ProjectID); err != nil {
		return current, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM project_module_map_entries WHERE project_id=? AND repository_id=?`, in.ProjectID, in.RepositoryID); err != nil {
		return current, err
	}
	current.RegistrationState = repository.RegistrationUnlinked
	current.RegistrationGeneration++
	current.ProjectTopologyRevision++
	current.CanUnlink = false
	current.EvaluatedAt = now
	encoded, err := json.Marshal(current)
	if err != nil {
		return current, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_repository_unlink_receipts(request_id,project_id,repository_id,expected_registration_generation,expected_topology_revision,payload_sha256,reason,result_json,created_at,completed_at,resulting_registration_generation,resulting_topology_revision,result_state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?, 'UNLINKED')`, in.RequestID, in.ProjectID, in.RepositoryID, in.ExpectedGeneration, in.ExpectedTopologyRevision, payloadHash, in.Reason, string(encoded), formatTime(now), formatTime(now), current.RegistrationGeneration, current.ProjectTopologyRevision); err != nil {
		return current, err
	}
	payload, _ := event.Payload(map[string]any{"repository_id": in.RepositoryID, "request_id": in.RequestID, "topology_revision": current.ProjectTopologyRevision})
	if err = appendEvent(ctx, tx, event.Event{Type: event.ProjectRepositoryUnlinked, ProjectID: in.ProjectID, EntityType: "repository", EntityID: in.RepositoryID, Payload: payload, CreatedAt: now}); err != nil {
		return current, fmt.Errorf("append unlink event: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return current, err
	}
	return current, nil
}
