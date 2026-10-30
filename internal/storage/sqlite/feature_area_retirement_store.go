package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/featurearea"
)

func (s *FeatureAreaStore) beginRetirement(ctx context.Context, in featurearea.LifecycleMutation) (featurearea.LifecycleResult, error) {
	if result, found, err := s.readLifecycleReplay(ctx, in); found || err != nil {
		return result, err
	}
	empty := featurearea.LifecycleResult{}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	area, err := scanFeatureArea(tx.QueryRowContext(ctx, selectFeatureArea+` WHERE id=? AND project_id=?`, in.FeatureAreaID, in.ProjectID))
	if errors.Is(err, sql.ErrNoRows) {
		return empty, featurearea.ErrNotFound
	}
	if err != nil {
		return empty, err
	}
	var action, hash string
	err = tx.QueryRowContext(ctx, `SELECT action,payload_sha256 FROM feature_area_lifecycle_requests WHERE feature_area_id=? AND request_id=?`, area.ID, in.RequestID).Scan(&action, &hash)
	if err == nil && (action != "RETIRE" || hash != in.PayloadSHA256) {
		return empty, featurearea.ErrConflict
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	result := featurearea.LifecycleResult{Parent: area, Children: []featurearea.FeatureArea{}, Relations: []featurearea.Relation{}}
	if area.Status == featurearea.StatusRetiring || area.Status == featurearea.StatusRetired {
		return result, nil
	}
	if _, err = featurearea.LifecycleTarget(area, featurearea.ActionRetire); err != nil {
		return empty, err
	}
	blocked, err := retirementGitBlocked(ctx, tx, area.ID)
	if err != nil {
		return empty, err
	}
	if blocked {
		return empty, developmentcycle.ErrFinalizationPending
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO feature_area_retirements(feature_area_id,request_id,requested_by_ai_session_id,requested_at,updated_at) VALUES(?,?,?,?,?)`, area.ID, in.RequestID, in.AISessionID, formatTime(in.At), formatTime(in.At)); err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE feature_areas SET status='RETIRING',updated_at=? WHERE id=?`, formatTime(in.At), area.ID); err != nil {
		return empty, err
	}
	invalidateDirectFile(s.db, area.ID)
	result.Parent, err = scanFeatureArea(tx.QueryRowContext(ctx, selectFeatureArea+` WHERE id=?`, area.ID))
	if err != nil {
		return empty, err
	}
	payload, _ := event.Payload(map[string]any{"from": area.Status, "to": "RETIRING", "requestId": in.RequestID})
	if err = appendEvent(ctx, tx, event.Event{Type: event.FeatureAreaStatusChanged, ProjectID: area.ProjectID, EntityType: "feature_area", EntityID: area.ID, Payload: payload, CreatedAt: in.At}); err != nil {
		return empty, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO feature_area_lifecycle_requests(feature_area_id,request_id,action,payload_sha256,result_json,created_at) VALUES(?,?,'RETIRE',?,?,?)`, area.ID, in.RequestID, in.PayloadSHA256, string(encoded), formatTime(in.At)); err != nil {
		return empty, err
	}
	return result, tx.Commit()
}

// PendingRetirements is internal coordinator input, never a read-side mutation.
func (s *FeatureAreaStore) PendingRetirements(ctx context.Context) ([]string, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT id FROM feature_areas WHERE status='RETIRING' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RetirementTargets starts at visible Tasks. Exact owner lookup shares one
// snapshot; it includes terminal Tasks with a stranded current owner/claim.
func (s *FeatureAreaStore) RetirementTargets(ctx context.Context, id string) ([]executioncancel.Input, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var session, project string
	err = tx.QueryRowContext(ctx, `SELECT i.requested_by_ai_session_id,f.project_id FROM feature_area_retirements i JOIN feature_areas f ON f.id=i.feature_area_id WHERE f.id=? AND f.status='RETIRING'`, id).Scan(&session, &project)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.task_type,COALESCE(t.work_owner_id,'') FROM tasks t WHERE t.feature_area_id=? AND t.task_scope='FEATURE_AREA' AND t.legacy_read_only=0 AND
 (t.status NOT IN ('SUCCEEDED','FAILED','CANCELED') OR EXISTS(SELECT 1 FROM feature_area_agent_claims c JOIN runs r ON r.id=c.run_id WHERE r.task_id=t.id) OR EXISTS(SELECT 1 FROM execution_proposals e WHERE e.task_id=t.id AND e.outcome IN ('PENDING','RUNNING') AND e.status IN ('PENDING_CONFIRMATION','CONFIRMED','CONSUMED'))) ORDER BY t.created_at,t.id`, id)
	if err != nil {
		return nil, err
	}
	type entry struct{ id, kind, owner string }
	var entries []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.kind, &e.owner); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []executioncancel.Input
	for _, t := range entries {
		in := executioncancel.Input{ProjectID: project, AISessionID: session, OwnerType: "TASK", OwnerID: t.id, Reason: "FeatureArea retirement"}
		err = tx.QueryRowContext(ctx, `SELECT c.operation_kind,c.operation_id,c.run_id FROM feature_area_agent_claims c JOIN runs r ON r.id=c.run_id WHERE r.task_id=?`, t.id).Scan(&in.OwnerType, &in.OwnerID, &in.RunID)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `SELECT 'EXECUTION',id,COALESCE(verification_run_id,run_id,'') FROM execution_proposals WHERE task_id=? AND outcome IN ('PENDING','RUNNING') AND status IN ('PENDING_CONFIRMATION','CONFIRMED','CONSUMED') ORDER BY created_at DESC,id DESC LIMIT 1`, t.id).Scan(&in.OwnerType, &in.OwnerID, &in.RunID)
		}

		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		// A pending request keeps its original payload, including its observed
		// version. A new retirement intent explicitly captures a new snapshot.
		var payload, pendingOperator string
		err = tx.QueryRowContext(ctx, `SELECT request_id,reason,COALESCE(requested_by_ai_session_id,''),COALESCE(run_id,''),COALESCE(request_payload_json,''),COALESCE(requested_by_operator_id,'') FROM execution_cancellations WHERE owner_type=? AND owner_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')`, in.OwnerType, in.OwnerID).Scan(&in.RequestID, &in.Reason, &in.AISessionID, &in.RunID, &payload, &pendingOperator)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			if pendingOperator != "" {
				// This exact intent already owns cancellation. Its supervisor or
				// startup orphan convergence finishes it without manufacturing a
				// fresh operator context in this background retirement command.
				// FinishRetirement still waits for the actual owner/claim to close.
				continue
			}
			if payload != "" {
				var stored executioncancel.Input
				if err = json.Unmarshal([]byte(payload), &stored); err != nil {
					return nil, err
				}
				in.ExpectedUpperVersion = stored.ExpectedUpperVersion
				if !cancellationPayloadMatches(payload, in) {
					return nil, executioncancel.ErrRequestConflict
				}
			}
		} else {
			upper, e := upperReviewExecutorTx(ctx, tx, in)
			if e != nil {
				return nil, e
			}
			if upper {
				facts, e := upperCancellationFactsTx(ctx, tx, in.OwnerID, s.db.epoch)
				if e != nil {
					return nil, e
				}
				if facts.reason != "" || !facts.currentOwner {
					return nil, executioncancel.ErrRunNotActive
				}
				in.ExpectedUpperVersion = &facts.version
			}
			identity := id + "\x00" + in.OwnerType + "\x00" + in.OwnerID + "\x00" + in.RunID
			if in.ExpectedUpperVersion != nil {
				version, e := json.Marshal(in.ExpectedUpperVersion)
				if e != nil {
					return nil, e
				}
				identity += "\x00" + string(version)
			}
			sum := sha256.Sum256([]byte(identity))
			in.RequestID = "retire_" + hex.EncodeToString(sum[:])
		}
		out = append(out, in)
	}
	return out, nil
}

// FinishRetirement is a guarded write-side convergence check. Read APIs never
// invoke it. SQL rechecks all Tasks/owners/claims in the same final transaction.
func (s *FeatureAreaStore) FinishRetirement(ctx context.Context, id, detail string) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_areas WHERE id=? AND status='RETIRING')`, id).Scan(&pending); err != nil || !pending {
		return err
	}
	// Retirement is an explicit Task-level cancellation decision. Apply it only
	// after every exact operation is quiesced or fenced by the cancellation path.
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='CANCELED',updated_at=? WHERE feature_area_id=? AND status='ACTIVE'
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c JOIN runs r ON r.id=c.run_id WHERE r.task_id=tasks.id)
 AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.task_id=tasks.id AND r.status NOT IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED'))
 AND NOT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.task_id=tasks.id AND e.outcome IN ('PENDING','RUNNING') AND e.status IN ('PENDING_CONFIRMATION','CONFIRMED','CONSUMED'))`, formatTime(time.Now().UTC()), id); err != nil {
		return err
	}
	var remaining bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=? AND legacy_read_only=0 AND status NOT IN ('SUCCEEDED','FAILED','CANCELED')) OR EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=?) OR EXISTS(SELECT 1 FROM execution_proposals e JOIN tasks t ON t.id=e.task_id WHERE t.feature_area_id=? AND e.outcome IN ('PENDING','RUNNING') AND e.status IN ('PENDING_CONFIRMATION','CONFIRMED','CONSUMED'))`, id, id, id).Scan(&remaining); err != nil {
		return err
	}
	now := formatTime(time.Now().UTC())
	if remaining {
		if len(detail) > 1024 {
			detail = detail[:1024]
		}
		_, err = tx.ExecContext(ctx, `UPDATE feature_area_retirements SET last_error=?,updated_at=? WHERE feature_area_id=? AND last_error<>?`, detail, now, id, detail)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE approvals SET status='EXPIRED' WHERE status IN ('PENDING','DECIDED') AND run_id IN (SELECT id FROM runs WHERE feature_area_id=?)`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_plans SET status='REJECTED',rejected_at=?,updated_at=? WHERE status='PENDING_CONFIRMATION' AND task_id IN (SELECT id FROM tasks WHERE feature_area_id=?)`, now, now, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE feature_areas SET status='RETIRED',updated_at=? WHERE id=?`, now, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE feature_area_retirements SET completed_at=?,updated_at=?,last_error='' WHERE feature_area_id=?`, now, now, id); err != nil {
		return err
	}
	var project, request string
	if err = tx.QueryRowContext(ctx, `SELECT f.project_id,i.request_id FROM feature_areas f JOIN feature_area_retirements i ON i.feature_area_id=f.id WHERE f.id=?`, id).Scan(&project, &request); err != nil {
		return err
	}
	payload, _ := event.Payload(map[string]any{"from": "RETIRING", "to": "RETIRED", "requestId": request})
	if err = appendEvent(ctx, tx, event.Event{Type: event.FeatureAreaStatusChanged, ProjectID: project, EntityType: "feature_area", EntityID: id, Payload: payload, CreatedAt: time.Now().UTC()}); err != nil {
		return err
	}
	return tx.Commit()
}
