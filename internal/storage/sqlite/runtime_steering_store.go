package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

const runtimeSteeringV129SQL = `
CREATE TABLE runtime_steering_receipts (
 request_id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES runs(id),
 input_json TEXT NOT NULL CHECK(json_valid(input_json)),
 state TEXT NOT NULL CHECK(state IN ('DISPATCHING','ACCEPTED','REJECTED','UNKNOWN')),
 submitted_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX runtime_steering_run ON runtime_steering_receipts(run_id,submitted_at);
CREATE TRIGGER runtime_steering_input_immutable BEFORE UPDATE OF request_id,run_id,input_json,submitted_at ON runtime_steering_receipts
 BEGIN SELECT RAISE(ABORT,'steering input is immutable'); END;
CREATE TRIGGER runtime_steering_transition BEFORE UPDATE OF state ON runtime_steering_receipts
 WHEN OLD.state<>'DISPATCHING' OR NEW.state='DISPATCHING'
 BEGIN SELECT RAISE(ABORT,'steering dispatch is single attempt'); END;
CREATE TRIGGER runtime_steering_no_delete BEFORE DELETE ON runtime_steering_receipts
 BEGIN SELECT RAISE(ABORT,'steering receipt is durable'); END;
`

type steeringRow interface{ Scan(...any) error }

func scanSteering(row steeringRow) (r agentruntime.SteeringReceipt, err error) {
	var input, submitted, updated string
	if err = row.Scan(&input, &r.State, &submitted, &updated); err != nil {
		return
	}
	if err = json.Unmarshal([]byte(input), &r.SteeringInput); err != nil {
		return
	}
	if r.SubmittedAt, err = time.Parse(time.RFC3339Nano, submitted); err != nil {
		return
	}
	r.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return
}

func (s *AgentRuntimeStore) FindSteering(ctx context.Context, in agentruntime.SteeringInput) (agentruntime.SteeringReceipt, bool, error) {
	r, err := scanSteering(s.db.sql.QueryRowContext(ctx, `SELECT input_json,state,submitted_at,updated_at FROM runtime_steering_receipts WHERE request_id=?`, in.SubmissionRequestID))
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err == nil && r.SteeringInput != in {
		return agentruntime.SteeringReceipt{}, false, agentruntime.ErrRequestConflict
	}
	return r, err == nil, err
}

func (s *AgentRuntimeStore) BeginSteering(ctx context.Context, in agentruntime.SteeringInput) (agentruntime.SteeringReceipt, bool, error) {
	out := agentruntime.SteeringReceipt{}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback()
	r, err := scanSteering(tx.QueryRowContext(ctx, `SELECT input_json,state,submitted_at,updated_at FROM runtime_steering_receipts WHERE request_id=?`, in.SubmissionRequestID))
	if err == nil {
		if r.SteeringInput != in {
			return out, false, agentruntime.ErrRequestConflict
		}
		return r, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, false, err
	}
	if err = s.validateSteeringTx(ctx, tx, in.Identity); err != nil {
		return out, false, err
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	out = agentruntime.SteeringReceipt{SteeringInput: in, State: "DISPATCHING", SubmittedAt: now, UpdatedAt: now}
	b, err := json.Marshal(in)
	if err != nil {
		return out, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_steering_receipts(request_id,run_id,input_json,state,submitted_at,updated_at) VALUES(?,?,?,'DISPATCHING',?,?)`, in.SubmissionRequestID, in.RunID, string(b), formatTime(now), formatTime(now))
	if err != nil {
		return out, false, err
	}
	err = tx.Commit()
	return out, err == nil, err
}

func (s *AgentRuntimeStore) FinishSteering(ctx context.Context, r agentruntime.SteeringReceipt) error {
	if r.State != "ACCEPTED" && r.State != "REJECTED" && r.State != "UNKNOWN" {
		return agentruntime.ErrInvalid
	}
	b, err := json.Marshal(r.SteeringInput)
	if err != nil {
		return err
	}
	result, err := s.db.execWrite(ctx, `UPDATE runtime_steering_receipts SET state=?,updated_at=? WHERE request_id=? AND run_id=? AND input_json=? AND state='DISPATCHING'`, r.State, formatTime(r.UpdatedAt), r.SubmissionRequestID, r.RunID, string(b))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return agentruntime.ErrStale
	}
	return nil
}
func (s *AgentRuntimeStore) ListSteering(ctx context.Context, runID string) ([]agentruntime.SteeringReceipt, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT input_json,state,submitted_at,updated_at FROM runtime_steering_receipts WHERE run_id=? ORDER BY submitted_at,request_id LIMIT 100`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []agentruntime.SteeringReceipt{}
	for rows.Next() {
		r, err := scanSteering(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// The capability projection and atomic dispatch admission share the same owner
// predicate; reads never reserve a command or alter the runtime snapshot.
func (s *AgentRuntimeStore) CanSteer(ctx context.Context, id agentruntime.Identity) (bool, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	err = s.validateSteeringTx(ctx, tx, id)
	if errors.Is(err, agentruntime.ErrStale) || errors.Is(err, runtimeepoch.ErrFenced) {
		return false, nil
	}
	return err == nil, err
}
func (s *AgentRuntimeStore) validateSteeringTx(ctx context.Context, tx *sql.Tx, in agentruntime.Identity) error {
	ctx = runtimeepoch.WithFence(ctx, in.RuntimeEpoch, in.RunID)
	if err := s.db.checkUsageRunTx(ctx, tx, in.RunID, in.ThreadID, in.TurnID); err != nil {
		return err
	}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT a.snapshot_json FROM agent_runtime_states a JOIN runs r ON r.id=a.run_id
 JOIN runtime_authority ra ON ra.singleton=1
 JOIN agent_operation_boundaries b ON b.run_id=r.id
 WHERE a.run_id=? AND a.connection_id=? AND a.runtime_epoch=? AND a.runtime_epoch=r.runtime_epoch AND a.runtime_epoch=ra.epoch_id
 AND b.quiesced=0 AND b.advance_state='UNKNOWN' AND b.thread_start_pending=0
 AND r.status='RUNNING' AND r.project_id=? AND r.feature_area_id=? AND r.task_id=?
 AND r.thread_id=? AND r.turn_id=? AND r.operation_kind=? AND r.operation_id=?
 AND EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=r.feature_area_id AND f.status IN ('ACTIVE','DISCOVERING'))
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.run_id=r.id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))
 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents f WHERE f.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_steering_receipts x WHERE x.run_id=r.id AND x.state IN ('DISPATCHING','UNKNOWN'))
 AND (SELECT count(*) FROM runtime_steering_receipts x WHERE x.run_id=r.id)<100`, in.RunID, in.ConnectionID, in.RuntimeEpoch, in.ProjectID, in.FeatureAreaID, in.TaskID, in.ThreadID, in.TurnID, in.OperationKind, in.OperationID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return agentruntime.ErrStale
	}
	if err != nil {
		return err
	}
	var snapshot agentruntime.Snapshot
	if json.Unmarshal([]byte(raw), &snapshot) != nil || snapshot.Identity != in || !agentruntime.SteeringReady(snapshot) {
		return agentruntime.ErrStale
	}
	// Server requests live in independent rows and can precede snapshot refresh.
	var pending bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM codex_server_requests WHERE run_id=? AND json_extract(request_json,'$.state') IN ('PENDING','INTENDED','SENT','WAITING_FOR_USER','USER_DECISION_RECORDED','RESPONDING','UNKNOWN'))`, in.RunID).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return agentruntime.ErrStale
	}
	var bindingID string
	var bindingEpoch int64
	err = tx.QueryRowContext(ctx, `SELECT json_extract(binding_json,'$.id'),json_extract(binding_json,'$.epoch') FROM app_server_run_bindings WHERE run_id=?`, in.RunID).Scan(&bindingID, &bindingEpoch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if bindingID != in.AppServerInstanceID || bindingEpoch != in.AppServerInstanceEpoch {
		return agentruntime.ErrStale
	}
	return nil
}
