package sqlite

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func (s *AgentRuntimeStore) BindAppServer(ctx context.Context, runID string, b appserver.Binding) error {
	if b.ID == "" || b.Endpoint == "" || b.ProcessStartIdentity == "" || b.Epoch < 1 {
		return agentruntime.ErrInvalid
	}
	tx, e := s.db.beginWrite(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = s.db.checkRunTx(ctx, tx, runID); e != nil {
		return e
	}
	data, _ := json.Marshal(b)
	_, e = tx.ExecContext(ctx, `INSERT INTO app_server_run_bindings(run_id,binding_json,created_at) VALUES(?,?,?) ON CONFLICT(run_id) DO NOTHING`, runID, string(data), formatTime(time.Now().UTC()))
	if e != nil {
		return e
	}
	var old string
	if e = tx.QueryRowContext(ctx, `SELECT binding_json FROM app_server_run_bindings WHERE run_id=?`, runID).Scan(&old); e != nil {
		return e
	}
	if old != string(data) {
		return agentruntime.ErrStale
	}
	return tx.Commit()
}
func (s *AgentRuntimeStore) AppServerBinding(ctx context.Context, id string) (b appserver.Binding, e error) {
	var data string
	e = s.db.sql.QueryRowContext(ctx, `SELECT binding_json FROM app_server_run_bindings WHERE run_id=?`, id).Scan(&data)
	if e == nil {
		e = json.Unmarshal([]byte(data), &b)
	}
	return
}
func (s *AgentRuntimeStore) SaveAppServerCheckpoint(ctx context.Context, id string, data json.RawMessage) error {
	if !json.Valid(data) {
		return agentruntime.ErrInvalid
	}
	r, e := s.db.execWrite(ctx, `UPDATE app_server_run_bindings SET checkpoint_json=? WHERE run_id=? AND (checkpoint_json IS NULL OR checkpoint_json=?)`, string(data), id, string(data))
	if e != nil {
		return e
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return agentruntime.ErrStale
	}
	return nil
}
func (s *AgentRuntimeStore) AppServerCheckpoint(ctx context.Context, id string) (json.RawMessage, error) {
	var v string
	e := s.db.sql.QueryRowContext(ctx, `SELECT checkpoint_json FROM app_server_run_bindings WHERE run_id=?`, id).Scan(&v)
	return json.RawMessage(v), e
}

// appServerCandidateSQL uses the runs alias r. Keep boot lifecycle deferral
// and the concrete App Server recovery queue on this same candidate predicate.
const appServerCandidateSQL = `r.status='RUNNING' AND r.purpose<>'RECOVERY_STATUS'
 AND EXISTS(SELECT 1 FROM app_server_run_bindings b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM recovery_status_records x WHERE x.source_run_id=r.id)
 AND COALESCE(r.thread_id,'')<>'' AND COALESCE(r.turn_id,'')<>''
 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents f WHERE f.run_id=r.id)`

func (s *AgentRuntimeStore) ListAppServerCandidates(ctx context.Context) ([]string, error) {
	rows, e := s.db.sql.QueryContext(ctx, `SELECT r.id FROM runs r WHERE `+appServerCandidateSQL+` ORDER BY r.id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func sameInstance(a, b appserver.Binding) bool {
	a.LastConfirmedAliveAt = time.Time{}
	b.LastConfirmedAliveAt = time.Time{}
	a.Status = ""
	b.Status = ""
	return a == b
}

// Reattach is called only after the transport verifies the independently lived
// instance and observes the original loaded active Turn. No client DTO calls it.
func (s *AgentRuntimeStore) Reattach(ctx context.Context, id agentruntime.Identity, b appserver.Binding) (agentruntime.Snapshot, error) {
	var v agentruntime.Snapshot
	// Adoption must inspect an old Run; beginWrite's callback fence intentionally
	// cannot be used here. The exclusive current writer is checked explicitly.
	tx, e := s.db.sql.BeginTx(ctx, nil)
	if e != nil {
		return v, e
	}
	defer tx.Rollback()
	if e = s.db.checkWriterTx(ctx, tx); e != nil {
		return v, e
	}
	var data, stored string
	inspectionRecoveryAuthority, authorityErr := s.db.recoveryChildAuthorityTx(ctx, tx, id.RunID)
	if authorityErr != nil {
		return v, authorityErr
	}
	if !inspectionRecoveryAuthority {
		inspectionRecoveryAuthority, authorityErr = s.db.inspectionContinuationAuthorityTx(ctx, tx, id.RunID)
		if authorityErr != nil {
			return v, authorityErr
		}
	}
	e = tx.QueryRowContext(ctx, `SELECT a.snapshot_json,b.binding_json FROM agent_runtime_states a JOIN app_server_run_bindings b ON b.run_id=a.run_id JOIN runs r ON r.id=a.run_id WHERE r.id=? AND r.thread_id=? AND r.turn_id=? AND r.status='RUNNING' AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents f WHERE f.run_id=r.id) AND NOT EXISTS(SELECT 1 FROM agent_orphan_dispositions o WHERE o.run_id=r.id) AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.run_id=r.id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED','CANCELED')) AND (EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id AND c.runtime_epoch=r.runtime_epoch AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id) OR EXISTS(SELECT 1 FROM context_probes p WHERE p.run_id=r.id AND p.runtime_epoch=r.runtime_epoch AND p.status='RUNNING') OR ?)`, id.RunID, id.ThreadID, id.TurnID, inspectionRecoveryAuthority).Scan(&data, &stored)
	if e != nil {
		return v, e
	}
	if e = json.Unmarshal([]byte(data), &v); e != nil {
		return v, e
	}
	var original appserver.Binding
	if json.Unmarshal([]byte(stored), &original) != nil || !sameInstance(original, b) || id.ConnectionID == "" || id.ConnectionID == v.ConnectionID {
		return v, agentruntime.ErrStale
	}
	if v.AppServerInstanceID != b.ID || v.AppServerInstanceEpoch != b.Epoch {
		return v, agentruntime.ErrStale
	}
	old := v.Identity
	for i := range v.Requests {
		r := &v.Requests[i]
		var text string
		if e = tx.QueryRowContext(ctx, `SELECT request_json FROM codex_server_requests WHERE connection_id=? AND request_id=? AND run_id=?`, old.ConnectionID, string(r.RequestID), old.RunID).Scan(&text); e != nil {
			return v, e
		}
		if e = json.Unmarshal([]byte(text), r); e != nil {
			return v, e
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO app_server_reattachments(run_id,previous_epoch,current_epoch,previous_connection_id,current_connection_id,thread_id,turn_id,binding_json,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, old.RunID, old.RuntimeEpoch, s.db.epoch, old.ConnectionID, id.ConnectionID, old.ThreadID, old.TurnID, stored, formatTime(time.Now().UTC()))
	if e != nil {
		return v, e
	}
	for _, q := range []string{`UPDATE runs SET runtime_epoch=? WHERE id=?`, `UPDATE feature_area_agent_claims SET runtime_epoch=? WHERE run_id=?`, `UPDATE context_probes SET runtime_epoch=? WHERE run_id=?`, `UPDATE runtime_handoffs SET runtime_epoch=? WHERE run_id=?`} {
		if _, e = tx.ExecContext(ctx, q, s.db.epoch, old.RunID); e != nil {
			return v, e
		}
	}
	v.RuntimeEpoch = s.db.epoch
	v.ConnectionID = id.ConnectionID
	v.ConnectionState = "CONNECTED"
	v.Sequence++
	v.UpdatedAt = time.Now().UTC()
	for i := range v.Requests {
		r := &v.Requests[i]
		if r.UserDecision != nil {
			copy := r.UserDecision.Identity
			r.OriginalIdentity = &copy
		} else if r.OriginalIdentity == nil {
			copy := r.Identity
			r.OriginalIdentity = &copy
		}
		r.Identity = v.Identity
		r.PendingReplayVerified = false
		r.AvailableActions = nil
		if r.State == "RESPONDING" {
			r.State = "RESPONDING"
			r.Delivery = "UNKNOWN"
		}
		if agentruntime.OpenRequest(*r) {
			r.ResolvedAt = nil
		}
		bytes, _ := json.Marshal(r)
		if _, e = tx.ExecContext(ctx, `INSERT INTO codex_server_requests(connection_id,request_id,run_id,request_json) VALUES(?,?,?,?)`, v.ConnectionID, string(r.RequestID), v.RunID, string(bytes)); e != nil {
			return v, e
		}
	}
	encoded, _ := json.Marshal(v)
	_, e = tx.ExecContext(ctx, `UPDATE agent_runtime_states SET runtime_epoch=?,connection_id=?,sequence=?,snapshot_json=?,updated_at=? WHERE run_id=? AND connection_id=?`, v.RuntimeEpoch, v.ConnectionID, v.Sequence, string(encoded), formatTime(v.UpdatedAt), v.RunID, old.ConnectionID)
	if e != nil {
		return v, e
	}
	if e = s.db.checkUsageRunTx(runtimeepoch.WithFence(ctx, s.db.epoch, v.RunID), tx, v.RunID, v.ThreadID, v.TurnID); e != nil {
		return v, e
	}
	return v, tx.Commit()
}

const upsertAppServerObservationSQL = `INSERT INTO app_server_observations(run_id,observation_json,updated_at) VALUES(?,?,?) ON CONFLICT(run_id) DO UPDATE SET observation_json=excluded.observation_json,updated_at=excluded.updated_at`

// RecordAppServerRecoveryFailure is the bootstrap failure boundary for a Run
// whose lifecycle reconciliation was deferred to the exact App Server owner.
// The failed observation and current Task's recovery requirement commit together;
// historical Runs cannot change a successor Task's phase.
func (s *AgentRuntimeStore) RecordAppServerRecoveryFailure(ctx context.Context, id string, observation json.RawMessage) error {
	var observed agentruntime.RuntimeObservation
	if json.Unmarshal(observation, &observed) != nil || observed.FailureReason == "" || (observed.ConnectionState != "UNKNOWN" && observed.ConnectionState != "RUNTIME_SESSION_LOST") {
		return agentruntime.ErrInvalid
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var projectID, taskID, operationKind, operationID, executionID, currentExecution, status, threadID, turnID, encoded string
	err = tx.QueryRowContext(ctx, `SELECT r.project_id,COALESCE(r.task_id,''),r.operation_kind,r.operation_id,COALESCE(r.execution_proposal_id,''),COALESCE(t.current_execution_id,''),r.status,COALESCE(r.thread_id,''),COALESCE(r.turn_id,''),b.binding_json
 FROM runs r JOIN app_server_run_bindings b ON b.run_id=r.id LEFT JOIN tasks t ON t.id=r.task_id WHERE r.id=?`, id).Scan(&projectID, &taskID, &operationKind, &operationID, &executionID, &currentExecution, &status, &threadID, &turnID, &encoded)
	if err != nil {
		return err
	}
	var bound appserver.Binding
	if json.Unmarshal([]byte(encoded), &bound) != nil || observed.AppServerInstanceID != bound.ID || observed.InstanceEpoch != bound.Epoch || observed.ThreadID != threadID || observed.TurnID != turnID {
		return agentruntime.ErrStale
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, upsertAppServerObservationSQL, id, string(observation), formatTime(now)); err != nil {
		return err
	}
	if operationKind == string(run.OperationExecution) && status == string(run.StatusRunning) && executionID != "" && operationID == executionID && currentExecution == executionID {
		if err = markTaskRecoveryRequiredTx(ctx, tx, projectID, taskID, "APP_SERVER_RECOVERY_FAILED", now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecordAppServerObservation retains transport evidence without changing any
// business terminal state or interpreting a consumed answer as acknowledged.
func (s *AgentRuntimeStore) RecordAppServerObservation(ctx context.Context, id string, observation json.RawMessage) error {
	if !json.Valid(observation) {
		return agentruntime.ErrInvalid
	}
	_, err := s.db.execWrite(ctx, upsertAppServerObservationSQL, id, string(observation), formatTime(time.Now().UTC()))
	return err
}
func (s *AgentRuntimeStore) AppServerObservation(ctx context.Context, id string) (json.RawMessage, error) {
	var text string
	err := s.db.sql.QueryRowContext(ctx, `SELECT observation_json FROM app_server_observations WHERE run_id=?`, id).Scan(&text)
	return json.RawMessage(text), err
}

// AppServerHasActiveRuns is a read-only cleanup fence across every Run sharing
// the independent instance. COLLECTING remains active until finalization commits.
func (s *AgentRuntimeStore) AppServerHasActiveRuns(ctx context.Context, bindingID string) (bool, error) {
	var active bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM app_server_run_bindings b JOIN runs r ON r.id=b.run_id WHERE json_extract(b.binding_json,'$.id')=? AND r.status IN ('CREATED','PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING'))`, bindingID).Scan(&active)
	return active, err
}
