package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"time"
)

const runtimeUserDecisionV78SQL = `
CREATE TABLE runtime_user_decisions (
 id TEXT PRIMARY KEY,
 runtime_request_id TEXT NOT NULL UNIQUE,
 connection_id TEXT NOT NULL,
 backend_request_id TEXT NOT NULL,
 run_id TEXT NOT NULL REFERENCES runs(id),
 submission_request_id TEXT NOT NULL UNIQUE,
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 decision_json TEXT NOT NULL CHECK(json_valid(decision_json)),
 submitted_at TEXT NOT NULL,
 FOREIGN KEY(connection_id,backend_request_id) REFERENCES codex_server_requests(connection_id,request_id)
);
CREATE TRIGGER runtime_user_decisions_no_update BEFORE UPDATE ON runtime_user_decisions BEGIN SELECT RAISE(ABORT,'user decision is immutable'); END;
CREATE TRIGGER runtime_user_decisions_no_delete BEFORE DELETE ON runtime_user_decisions BEGIN SELECT RAISE(ABORT,'user decision is immutable'); END;
`

func (s *AgentRuntimeStore) UserDecision(ctx context.Context, runID, id string) (d agentruntime.UserDecision, err error) {
	var b string
	err = s.db.sql.QueryRowContext(ctx, `SELECT decision_json FROM runtime_user_decisions WHERE id=? AND run_id=?`, id, runID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return d, agentruntime.ErrStale
	}
	if err == nil {
		err = json.Unmarshal([]byte(b), &d)
	}
	return
}
func (s *AgentRuntimeStore) SubmitUserDecision(ctx context.Context, in agentruntime.UserDecisionInput) (d agentruntime.UserDecision, err error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	var current bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runtime_states WHERE run_id=? AND connection_id=? AND runtime_epoch=? AND (runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1) OR NOT EXISTS(SELECT 1 FROM app_server_run_bindings b WHERE b.run_id=agent_runtime_states.run_id)))`, in.RunID, in.ConnectionID, in.RuntimeEpoch).Scan(&current); err != nil {
		return d, err
	}
	var b string
	err = tx.QueryRowContext(ctx, `SELECT decision_json FROM runtime_user_decisions WHERE submission_request_id=? OR runtime_request_id=?`, in.SubmissionRequestID, in.RuntimeRequestID).Scan(&b)
	if err == nil {
		if json.Unmarshal([]byte(b), &d) != nil {
			return d, agentruntime.ErrInvalid
		}
		if d.SubmissionRequestID != in.SubmissionRequestID || d.PayloadSHA256 != in.PayloadSHA256 || in.PayloadSHA256 != agentruntime.DecisionPayloadSHA256(in) || d.SubmittedBy != in.SubmittedBy {
			return d, agentruntime.ErrRequestConflict
		}
		// Cancellation replay reads an immutable choice; its canonical receipt
		// owns observation and dispatch fencing across a Core restart. Answer
		// submissions still require the current runtime binding.
		if !current && d.Action != "CANCEL_OPERATION" && d.Action != "STOP_AND_REVISE" {
			return d, agentruntime.ErrStale
		}
		return d, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return d, err
	}
	if !current {
		return d, agentruntime.ErrStale
	}
	var r agentruntime.Request
	err = tx.QueryRowContext(ctx, `SELECT request_json FROM codex_server_requests WHERE run_id=? AND connection_id=? AND json_extract(request_json,'$.runtimeRequestId')=?`, in.RunID, in.ConnectionID, in.RuntimeRequestID).Scan(&b)
	if err != nil {
		return d, agentruntime.ErrStale
	}
	if json.Unmarshal([]byte(b), &r) != nil {
		return d, agentruntime.ErrInvalid
	}
	if r.RecoveryStatusID != "" {
		exact, e := independentRecoveryDecisionIDTx(ctx, tx, in.RunID)
		if e != nil || exact != r.RecoveryStatusID {
			return d, agentruntime.ErrStale
		}
	}
	if err = agentruntime.ValidateUserDecision(r, in); err != nil {
		return d, err
	}
	// A PROJECT_INSPECTION recovery child intentionally has no context_probe_id
	// and borrows the source probe's authority through its durable recovery
	// receipt.  Keep that narrow variant out of the generic claim predicates.
	inspectionRecoveryAuthority, err := s.db.recoveryChildAuthorityTx(ctx, tx, in.RunID)
	if err != nil {
		return d, err
	}
	continuationAuthority, err := s.db.inspectionContinuationAuthorityTx(ctx, tx, in.RunID)
	if err != nil {
		return d, err
	}
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runtime_states a JOIN runs r ON r.id=a.run_id JOIN runtime_authority ra ON ra.singleton=1 WHERE a.run_id=? AND a.connection_id=? AND a.runtime_epoch=? AND ra.epoch_id=a.runtime_epoch AND r.runtime_epoch=a.runtime_epoch AND r.thread_id=? AND r.turn_id=? AND r.status='RUNNING' AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.run_id=r.id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')) AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents f WHERE f.run_id=r.id) AND (EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id AND c.runtime_epoch=r.runtime_epoch AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id) OR EXISTS(SELECT 1 FROM context_probes p WHERE p.run_id=r.id AND p.runtime_epoch=r.runtime_epoch AND p.status='RUNNING') OR EXISTS(SELECT 1 FROM recovery_status_records x JOIN feature_area_agent_claims c ON c.run_id=x.source_run_id WHERE x.recovery_run_id=r.id AND x.runtime_epoch=r.runtime_epoch AND x.state='RUNNING' AND c.feature_area_id=r.feature_area_id AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id) OR ? OR ?))`, in.RunID, in.ConnectionID, in.RuntimeEpoch, in.ThreadID, in.TurnID, inspectionRecoveryAuthority, continuationAuthority).Scan(&valid)
	if err != nil {
		return d, err
	}
	if !valid {
		return d, agentruntime.ErrStale
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	d = agentruntime.UserDecision{UserDecisionInput: in, ID: "user_decision_" + in.PayloadSHA256, SubmittedAt: now}
	encoded, err := json.Marshal(d)
	if err != nil {
		return d, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_user_decisions(id,runtime_request_id,connection_id,backend_request_id,run_id,submission_request_id,payload_sha256,decision_json,submitted_at) VALUES(?,?,?,?,?,?,?,?,?)`, d.ID, in.RuntimeRequestID, in.ConnectionID, string(r.RequestID), in.RunID, in.SubmissionRequestID, in.PayloadSHA256, string(encoded), formatTime(now))
	if err != nil {
		return d, err
	}
	r.UserDecision = &d
	r.State = "USER_DECISION_RECORDED"
	r.Delivery = "NOT_DISPATCHED"
	r.AvailableActions = nil
	r.UpdatedAt = now
	encoded, err = json.Marshal(r)
	if err != nil {
		return d, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_server_requests SET request_json=? WHERE run_id=? AND connection_id=? AND json_extract(request_json,'$.runtimeRequestId')=?`, string(encoded), in.RunID, in.ConnectionID, in.RuntimeRequestID)
	if err != nil {
		return d, err
	}
	err = tx.Commit()
	return
}
func reconcileRuntimeDecisions(ctx context.Context, tx *sql.Tx, v *agentruntime.Snapshot) error {
	for i := range v.Requests {
		r := &v.Requests[i]
		if r.OriginalIdentity != nil {
			var receipt bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM app_server_reattachments a JOIN codex_server_requests q ON q.run_id=a.run_id WHERE a.run_id=? AND a.current_connection_id=? AND a.current_epoch=? AND a.thread_id=? AND a.turn_id=? AND q.connection_id=? AND q.request_id=? AND json_extract(q.request_json,'$.runtimeRequestId')=?)`, v.RunID, v.ConnectionID, v.RuntimeEpoch, v.ThreadID, v.TurnID, r.OriginalIdentity.ConnectionID, string(r.RequestID), r.ID).Scan(&receipt); err != nil {
				return err
			}
			if !receipt {
				return agentruntime.ErrStale
			}
			original := *r.OriginalIdentity
			current := r.Identity
			original.ConnectionID = current.ConnectionID
			original.RuntimeEpoch = current.RuntimeEpoch
			if original != current {
				return agentruntime.ErrStale
			}
		}
		if r.ID == "" {
			continue
		}
		var b string
		err := tx.QueryRowContext(ctx, `SELECT decision_json FROM runtime_user_decisions WHERE runtime_request_id=? AND run_id=?`, r.ID, r.RunID).Scan(&b)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var d agentruntime.UserDecision
		if err = json.Unmarshal([]byte(b), &d); err != nil {
			return err
		}
		r.UserDecision = &d
		r.AvailableActions = nil
		if r.State == "WAITING_FOR_USER" {
			r.State = "USER_DECISION_RECORDED"
		}
	}
	return nil
}
