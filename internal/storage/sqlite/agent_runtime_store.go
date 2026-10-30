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

const schemaV40 = `
CREATE TABLE agent_runtime_states (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 connection_id TEXT NOT NULL UNIQUE,
 thread_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 sequence INTEGER NOT NULL CHECK(sequence>0),
 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
 updated_at TEXT NOT NULL
);
CREATE TABLE codex_server_requests (
 connection_id TEXT NOT NULL,
 request_id TEXT NOT NULL CHECK(json_valid(request_id)),
 run_id TEXT NOT NULL REFERENCES runs(id),
 request_json TEXT NOT NULL CHECK(json_valid(request_json)),
 PRIMARY KEY(connection_id,request_id)
);
CREATE INDEX codex_server_requests_run ON codex_server_requests(run_id);
CREATE TRIGGER agent_runtime_identity_immutable BEFORE UPDATE OF run_id,runtime_epoch,connection_id,thread_id,turn_id ON agent_runtime_states
WHEN NEW.run_id<>OLD.run_id OR NEW.runtime_epoch<>OLD.runtime_epoch OR NEW.connection_id<>OLD.connection_id OR NEW.thread_id<>OLD.thread_id OR NEW.turn_id<>OLD.turn_id
BEGIN SELECT RAISE(ABORT,'agent runtime identity is immutable'); END;
`

type AgentRuntimeStore struct {
	db  *DB
	Now func() time.Time
}

func NewAgentRuntimeStore(db *DB) *AgentRuntimeStore {
	return &AgentRuntimeStore{db: db, Now: time.Now}
}
func (s *AgentRuntimeStore) Open(ctx context.Context, id agentruntime.Identity) (agentruntime.Identity, error) {
	fence, ok := runtimeepoch.FromContext(ctx)
	if !ok || fence.RunID != id.RunID || id.ThreadID == "" || id.TurnID == "" || id.ConnectionID == "" {
		return id, runtimeepoch.ErrFenced
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return id, err
	}
	defer tx.Rollback()
	if err = s.db.checkUsageRunTx(ctx, tx, id.RunID, id.ThreadID, id.TurnID); err != nil {
		return id, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(project_id,''),COALESCE(feature_area_id,''),COALESCE(task_id,'') FROM runs WHERE id=? AND thread_id=? AND turn_id=?`, id.RunID, id.ThreadID, id.TurnID).Scan(&id.ProjectID, &id.FeatureAreaID, &id.TaskID)
	if err != nil {
		return id, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(r.operation_kind,''),COALESCE(r.operation_id,''),COALESCE(r.review_id,''),COALESCE(r.execution_proposal_id,''),COALESCE(e.task_definition_revision_id,''),COALESCE(b.agent_conversation_id,''),COALESCE(r.runtime_epoch,'') FROM runs r LEFT JOIN execution_proposals e ON e.id=r.execution_proposal_id LEFT JOIN agent_operation_boundaries b ON b.run_id=r.id WHERE r.id=?`, id.RunID).Scan(&id.OperationKind, &id.OperationID, &id.ReviewID, &id.ExecutionID, &id.TaskDefinitionRevisionID, &id.AgentConversationID, &id.RuntimeEpoch)
	if err != nil {
		return id, err
	}
	id.RecoveryStatusID, err = independentRecoveryDecisionIDTx(ctx, tx, id.RunID)
	if err != nil {
		return id, err
	}
	var instanceID string
	var instanceEpoch int64
	bindErr := tx.QueryRowContext(ctx, `SELECT json_extract(binding_json,'$.id'),json_extract(binding_json,'$.epoch') FROM app_server_run_bindings WHERE run_id=?`, id.RunID).Scan(&instanceID, &instanceEpoch)
	if bindErr != nil && !errors.Is(bindErr, sql.ErrNoRows) {
		return id, bindErr
	}
	if bindErr == nil {
		id.AppServerInstanceID = instanceID
		id.AppServerInstanceEpoch = instanceEpoch
	}
	return id, nil
}
func (s *AgentRuntimeStore) Save(ctx context.Context, expected uint64, v agentruntime.Snapshot, resolution bool) error {
	fence, ok := runtimeepoch.FromContext(ctx)
	if !ok || fence.RunID != v.RunID || v.Sequence != expected+1 {
		return runtimeepoch.ErrFenced
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.db.checkUsageRunTx(ctx, tx, v.RunID, v.ThreadID, v.TurnID); err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND thread_id=? AND turn_id=? AND COALESCE(project_id,'')=? AND COALESCE(feature_area_id,'')=? AND COALESCE(task_id,'')=?)`, v.RunID, v.ThreadID, v.TurnID, v.ProjectID, v.FeatureAreaID, v.TaskID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return runtimeepoch.ErrFenced
	}
	if v.RecoveryStatusID != "" {
		exact, err := independentRecoveryDecisionIDTx(ctx, tx, v.RunID)
		if err != nil || exact != v.RecoveryStatusID {
			return agentruntime.ErrStale
		}
	}
	var instanceID string
	var instanceEpoch int64
	bindingErr := tx.QueryRowContext(ctx, `SELECT json_extract(binding_json,'$.id'),json_extract(binding_json,'$.epoch') FROM app_server_run_bindings WHERE run_id=?`, v.RunID).Scan(&instanceID, &instanceEpoch)
	if bindingErr != nil && !errors.Is(bindingErr, sql.ErrNoRows) {
		return bindingErr
	}
	if v.AppServerInstanceID != instanceID || v.AppServerInstanceEpoch != instanceEpoch {
		return agentruntime.ErrStale
	}
	for _, request := range v.Requests {
		if request.Identity != v.Identity {
			return agentruntime.ErrStale
		}
	}
	for _, request := range v.Requests {
		if request.Delivery != "DISPATCHING" || request.State != "INTENDED" {
			continue
		}
		var response struct {
			Decision string `json:"decision"`
		}
		_ = json.Unmarshal(request.Resolution, &response)
		if response.Decision == "cancel" {
			// The canonical supervisor must claim its interrupt before an
			// approval response can produce an interrupted terminal. Otherwise
			// skip this queued response and let that interrupt perform official
			// request cleanup; admission alone is not dispatch authority.
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE run_id=? AND runtime_epoch=? AND interrupt_state<>'NOT_SENT' AND state='CANCEL_REQUESTED')`, v.RunID, fence.Epoch).Scan(&valid)
			if err != nil {
				return err
			}
			if !valid {
				return agentruntime.ErrStale
			}
		}
	}
	if resolution {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r WHERE r.id=? AND r.status='RUNNING'
   AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.run_id=r.id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))
   AND NOT EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=r.feature_area_id AND f.status NOT IN ('ACTIVE','DISCOVERING'))
   AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id))`, v.RunID).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return agentruntime.ErrStale
		}
	}
	if err = reconcileRuntimeDecisions(ctx, tx, &v); err != nil {
		return err
	}
	for _, request := range v.Requests {
		if request.OriginalIdentity != nil && request.PendingReplayVerified && ((request.State == "WAITING_FOR_USER" && len(request.AvailableActions) > 0) || (request.Delivery == "NOT_SENT" || request.Delivery == "NOT_DISPATCHED") || request.State == "RESPONDING") {
			if _, err = tx.ExecContext(ctx, `UPDATE app_server_observations SET observation_json=json_set(observation_json,'$.requestReattachment','RECOVERED','$.connectionState','CONNECTED','$.summary','Codex 会话已恢复，原请求已确认','$.sourceMethod','server-request-replay','$.observedAt',?),updated_at=? WHERE run_id=?`, formatTime(v.UpdatedAt), formatTime(v.UpdatedAt), v.RunID); err != nil {
				return err
			}
			break
		}
	}
	agentruntime.ApplyRequestProjections(&v)
	for _, request := range v.Requests {
		if err = validateResponseTransition(ctx, tx, request); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO agent_runtime_states(run_id,runtime_epoch,connection_id,thread_id,turn_id,sequence,snapshot_json,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(run_id) DO NOTHING`, v.RunID, fence.Epoch, v.ConnectionID, v.ThreadID, v.TurnID, v.Sequence, string(encoded), formatTime(v.UpdatedAt))
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE agent_runtime_states SET sequence=?,snapshot_json=?,updated_at=? WHERE run_id=? AND runtime_epoch=? AND connection_id=? AND thread_id=? AND turn_id=? AND sequence=?`, v.Sequence, string(encoded), formatTime(v.UpdatedAt), v.RunID, fence.Epoch, v.ConnectionID, v.ThreadID, v.TurnID, expected)
	}
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return agentruntime.ErrStale
	}
	for _, request := range v.Requests {
		b, err := json.Marshal(request)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO codex_server_requests(connection_id,request_id,run_id,request_json) VALUES(?,?,?,?) ON CONFLICT(connection_id,request_id) DO UPDATE SET request_json=excluded.request_json WHERE codex_server_requests.run_id=excluded.run_id`, v.ConnectionID, string(request.RequestID), v.RunID, string(b))
		if err != nil {
			return err
		}
	}
	for _, request := range v.Requests {
		if err = persistResponseDispatch(ctx, tx, request); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *AgentRuntimeStore) Read(ctx context.Context, runID string) (v agentruntime.Snapshot, err error) {
	defer func() {
		if err == nil {
			agentruntime.ApplyRequestProjections(&v)
		}
	}()
	defer func() {
		if err != nil {
			return
		}
		var observation string
		oe := s.db.sql.QueryRowContext(ctx, `SELECT observation_json FROM app_server_observations WHERE run_id=?`, runID).Scan(&observation)
		if oe == nil {
			var o agentruntime.RuntimeObservation
			if json.Unmarshal([]byte(observation), &o) == nil {
				v.Observation = &o
				if o.ConnectionState == "RUNTIME_SESSION_LOST" || o.RequestReattachment == "UNPROVEN" {
					v.ConnectionState = o.ConnectionState
					v.WaitingState = "RUNTIME_SESSION_LOST"
					for i := range v.Requests {
						if agentruntime.OpenRequest(v.Requests[i]) {
							v.Requests[i].AvailableActions = nil
						}
					}
				}
			}
		} else if !errors.Is(oe, sql.ErrNoRows) {
			err = oe
			return
		}
		v.Agent.Name = "Codex"
		v.Agent.ProcessAvailability = "UNAVAILABLE"
		var p *agentruntime.ProcessLaunch
		p, err = s.processForRun(ctx, runID)
		if err != nil {
			return
		}
		if p != nil {
			v.Agent.Process = p
			v.Agent.ProcessLaunchID = p.LaunchID
			v.Agent.PID = p.PID
			v.Agent.ProcessAvailability = "AVAILABLE"
		}
		var status string
		err = s.db.sql.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=?`, runID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		now := time.Now()
		if s.Now != nil {
			now = s.Now()
		}
		v.AgentHealth = agentruntime.Health(v, status, now)
	}()
	v = agentruntime.Snapshot{Identity: agentruntime.Identity{RunID: runID}, Availability: "UNAVAILABLE", WaitingState: "RUNNING"}
	var b, epoch, current string
	var active bool
	// Read-only projection uses the same durable successor authority as writes.
	// The main snapshot query below still checks exact Thread/Turn and status.
	continuationAuthority := false
	readTx, readErr := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if readErr != nil {
		return v, readErr
	}
	continuationAuthority, readErr = s.db.inspectionContinuationAuthorityTx(ctx, readTx, runID)
	_ = readTx.Rollback()
	if readErr != nil {
		return v, readErr
	}
	err = s.db.sql.QueryRowContext(ctx, `SELECT a.snapshot_json,a.runtime_epoch,(SELECT epoch_id FROM runtime_authority WHERE singleton=1),
 EXISTS(SELECT 1 FROM runs r WHERE r.id=a.run_id AND r.thread_id=a.thread_id AND r.turn_id=a.turn_id AND r.status IN ('RUNNING','WAITING_APPROVAL','COLLECTING') AND NOT EXISTS(SELECT 1 FROM agent_orphan_dispositions o WHERE o.run_id=r.id) AND (EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id AND c.runtime_epoch=r.runtime_epoch AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id) OR EXISTS(SELECT 1 FROM context_probes p WHERE p.run_id=r.id AND p.runtime_epoch=r.runtime_epoch AND p.status='RUNNING') OR EXISTS(SELECT 1 FROM recovery_status_records x JOIN feature_area_agent_claims c ON c.run_id=x.source_run_id WHERE x.recovery_run_id=r.id AND x.runtime_epoch=r.runtime_epoch AND x.state='RUNNING' AND c.feature_area_id=r.feature_area_id AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id) OR EXISTS(SELECT 1 FROM recovery_status_records x JOIN runs s ON s.id=x.source_run_id JOIN context_probes p ON p.id=s.context_probe_id WHERE x.recovery_run_id=r.id AND x.runtime_epoch=r.runtime_epoch AND x.state='RUNNING' AND r.context_probe_id IS NULL AND r.operation_kind='INSPECTION' AND r.operation_id=p.id AND s.project_id=r.project_id AND s.task_id=r.task_id) OR ?)) FROM agent_runtime_states a WHERE a.run_id=?`, continuationAuthority, runID).Scan(&b, &epoch, &current, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal([]byte(b), &v); err != nil {
		return v, err
	}
	for i := range v.Requests {
		r := &v.Requests[i]
		var encoded string
		e := s.db.sql.QueryRowContext(ctx, `SELECT request_json FROM codex_server_requests WHERE connection_id=? AND request_id=? AND run_id=?`, v.ConnectionID, string(r.RequestID), runID).Scan(&encoded)
		if e != nil {
			return agentruntime.Snapshot{}, e
		}
		if e = json.Unmarshal([]byte(encoded), r); e != nil {
			return agentruntime.Snapshot{}, e
		}
		if !agentruntime.CompleteDecisionIdentity(r.Identity) {
			r.AvailableActions = nil
		}
	}
	var reconnectable bool
	if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM app_server_run_bindings b JOIN runs r ON r.id=b.run_id WHERE b.run_id=? AND r.status='RUNNING' AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents f WHERE f.run_id=r.id))`, runID).Scan(&reconnectable); err != nil {
		return v, err
	}
	if reconnectable && epoch != current {
		v.ConnectionState = "RECONNECTING"
		v.WaitingState = "RECONNECTING"
		for i := range v.Requests {
			v.Requests[i].AvailableActions = nil
			if v.Requests[i].State == "RESPONDING" {
				v.Requests[i].State = "RESPONDING"
				v.Requests[i].Delivery = "UNKNOWN"
			}
		}
	}
	if (epoch != current || !active) && v.ConnectionState == "CONNECTED" {
		v.ConnectionState = "CONNECTION_LOST"
		v.WaitingState = "CONNECTION_LOST"
		for i := range v.Requests {
			if agentruntime.OpenRequest(v.Requests[i]) {
				previousState := v.Requests[i].State
				v.Requests[i].State = "ABANDONED_RUNTIME"
				if epoch != current {
					v.Requests[i].State = "ABANDONED_RESTART"
				}
				if v.Requests[i].Kind == "CODEX_REQUEST" {
					v.Requests[i].AvailableActions = nil
					if previousState == "RESPONDING" || previousState == "UNKNOWN" || v.Requests[i].Delivery == "TRANSPORT_WRITTEN" || v.Requests[i].Delivery == "DISPATCHING" {
						v.Requests[i].State = "RESPONDING"
						v.Requests[i].Delivery = "UNKNOWN"
					} else {
						v.Requests[i].State = "STALE"
					}
				}
			}
		}
	}
	return v, nil
}
