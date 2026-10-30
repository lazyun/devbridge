package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentruntime"
)

// Persist intent/evidence in the very same transaction as the owning snapshot.
// Re-saving historical facts is a pure exact-byte comparison, never an update.
func persistResponseDispatch(ctx context.Context, tx *sql.Tx, r agentruntime.Request) error {
	if r.Kind != "CODEX_REQUEST" {
		return nil
	}
	seenAttempts := make(map[string]bool, len(r.DispatchAttempts))
	for index, a := range r.DispatchAttempts {
		if seenAttempts[a.ID] {
			return agentruntime.ErrInvalid
		}
		seenAttempts[a.ID] = true
		encoded, err := json.Marshal(a)
		if err != nil {
			return err
		}
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT attempt_json FROM runtime_response_dispatch_attempts WHERE id=?`, a.ID).Scan(&existing)
		if err == nil {
			if existing != string(encoded) {
				return agentruntime.ErrInvalid
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		h := sha256.Sum256(a.Payload)
		if a.Identity != r.Identity || a.RuntimeRequestID != r.ID || r.UserDecision == nil || a.UserDecisionID != r.UserDecision.ID || a.CommandRequestID != r.UserDecision.SubmissionRequestID || a.BackendRequestID != string(r.RequestID) || a.ItemID != r.ItemID || a.PayloadSHA256 != hex.EncodeToString(h[:]) || string(a.Payload) != string(r.Resolution) || a.StartedAt.IsZero() {
			return agentruntime.ErrInvalid
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_response_dispatch_attempts WHERE runtime_request_id=?`, r.ID).Scan(&count); err != nil {
			return err
		}
		if count != index {
			return agentruntime.ErrInvalid
		}
		if count > 0 {
			// validateResponseTransition preserves the committed prefix, and
			// every preceding entry above was checked against its immutable row.
			// Wall-clock timestamps cannot establish causal dispatch order.
			original := r.DispatchAttempts[0]
			latestAttemptID := r.DispatchAttempts[index-1].ID
			if a.PayloadSHA256 != original.PayloadSHA256 || a.UserDecisionID != original.UserDecisionID {
				return agentruntime.ErrInvalid
			}
			var used bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_response_dispatch_attempts WHERE runtime_request_id=? AND json_extract(attempt_json,'$.recoveryEvidenceId')=?)`, r.ID, a.RecoveryEvidenceID).Scan(&used); err != nil {
				return err
			}
			if used {
				return agentruntime.ErrResponseDispositionUnknown
			}
			proven := false
			for _, e := range r.RecoveryEvidence {
				if e.ID == a.RecoveryEvidenceID && e.Identity == a.Identity && e.RuntimeRequestID == a.RuntimeRequestID && e.BackendRequestID == a.BackendRequestID && e.AttemptID == latestAttemptID && (e.Kind == "EXACT_PENDING_REPLAY" || e.Kind == "PRE_EFFECT_REJECTED") {
					proven = true
				}
			}
			if !proven {
				return agentruntime.ErrResponseDispositionUnknown
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_response_dispatch_attempts(id,run_id,runtime_request_id,user_decision_id,connection_id,backend_request_id,command_request_id,payload_sha256,attempt_json,started_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, a.ID, a.RunID, a.RuntimeRequestID, a.UserDecisionID, a.ConnectionID, a.BackendRequestID, a.CommandRequestID, a.PayloadSHA256, string(encoded), formatTime(a.StartedAt)); err != nil {
			return err
		}
	}
	seenEvidence := make(map[string]bool, len(r.RecoveryEvidence))
	for _, e := range r.RecoveryEvidence {
		if seenEvidence[e.ID] {
			return agentruntime.ErrInvalid
		}
		seenEvidence[e.ID] = true
		if r.UserDecision == nil {
			continue
		}
		encoded, err := json.Marshal(e)
		if err != nil {
			return err
		}
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT evidence_json FROM runtime_response_evidence WHERE id=?`, e.ID).Scan(&existing)
		if err == nil {
			if existing != string(encoded) {
				return agentruntime.ErrInvalid
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if e.Identity != r.Identity || e.RuntimeRequestID != r.ID || e.BackendRequestID != string(r.RequestID) || e.ObservedAt.IsZero() {
			return agentruntime.ErrStale
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_response_evidence(id,run_id,runtime_request_id,connection_id,backend_request_id,kind,evidence_json,observed_at) VALUES(?,?,?,?,?,?,?,?)`, e.ID, e.RunID, e.RuntimeRequestID, e.ConnectionID, e.BackendRequestID, e.Kind, string(encoded), formatTime(e.ObservedAt)); err != nil {
			return err
		}
	}
	return nil
}

func validateResponseTransition(ctx context.Context, tx *sql.Tx, r agentruntime.Request) error {
	if r.Kind != "CODEX_REQUEST" {
		return nil
	}
	var b string
	err := tx.QueryRowContext(ctx, `SELECT request_json FROM codex_server_requests WHERE connection_id=? AND request_id=?`, r.ConnectionID, string(r.RequestID)).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var previous agentruntime.Request
	if err = json.Unmarshal([]byte(b), &previous); err != nil {
		return err
	}
	// A snapshot is an append-only projection of dispatch history. Reordering
	// or dropping immutable ledger entries must not change the current attempt
	// or let previously used evidence appear fresh. Exact contents are checked
	// by persistResponseDispatch in this same transaction.
	if len(r.DispatchAttempts) < len(previous.DispatchAttempts) || len(r.RecoveryEvidence) < len(previous.RecoveryEvidence) {
		return agentruntime.ErrInvalid
	}
	for i, attempt := range previous.DispatchAttempts {
		if r.DispatchAttempts[i].ID != attempt.ID {
			return agentruntime.ErrInvalid
		}
	}
	for i, evidence := range previous.RecoveryEvidence {
		if r.RecoveryEvidence[i].ID != evidence.ID {
			return agentruntime.ErrInvalid
		}
	}
	old := agentruntime.ResponseDisposition(previous)
	next := agentruntime.ResponseDisposition(r)
	if next == "DISPATCHING" && old != "DISPATCHING" && len(r.DispatchAttempts) <= len(previous.DispatchAttempts) {
		return agentruntime.ErrInvalid
	}
	if old == "CONSUMED" && next != "CONSUMED" {
		return agentruntime.ErrStale
	}
	if next != "CONSUMED" && old != "UNKNOWN" && old != "DISPATCHING" {
		return nil
	}
	if next == "CONSUMED" && old == "CONSUMED" {
		return nil
	}
	if next == "UNKNOWN" || next == "DISPATCHING" || r.State == "CANCELED" || r.State == "STALE" {
		return nil
	}
	for _, e := range r.RecoveryEvidence {
		if e.Identity != r.Identity || e.RuntimeRequestID != r.ID || e.BackendRequestID != string(r.RequestID) {
			continue
		}
		fresh := true
		for _, p := range previous.RecoveryEvidence {
			if p.ID == e.ID {
				fresh = false
			}
		}
		if !fresh {
			continue
		}
		if next == "NOT_DISPATCHED" && (e.Kind == "EXACT_PENDING_REPLAY" || e.Kind == "PRE_EFFECT_REJECTED") {
			return nil
		}
		if next == "CONSUMED" && e.Kind == "SERVER_REQUEST_RESOLVED" {
			return nil
		}
	}
	return agentruntime.ErrResponseDispositionUnknown
}

// cancelRuntimeRequestsTx is called only by canonical cancellation after its
// quiescence proof. Cancellation is independent of answer consumption: retain
// the original decision, disposition, attempts and protocol evidence verbatim.
func cancelRuntimeRequestsTx(ctx context.Context, tx *sql.Tx, runID string) error {
	var encoded string
	err := tx.QueryRowContext(ctx, `SELECT snapshot_json FROM agent_runtime_states WHERE run_id=?`, runID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var v agentruntime.Snapshot
	if err = json.Unmarshal([]byte(encoded), &v); err != nil {
		return err
	}
	changed := false
	for i := range v.Requests {
		r := &v.Requests[i]
		if r.Kind != "CODEX_REQUEST" {
			continue
		}
		var stored string
		if err = tx.QueryRowContext(ctx, `SELECT request_json FROM codex_server_requests WHERE run_id=? AND connection_id=? AND request_id=?`, runID, r.ConnectionID, string(r.RequestID)).Scan(&stored); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(stored), r); err != nil {
			return err
		}
		if !agentruntime.OpenRequest(*r) {
			continue
		}
		r.State = "CANCELED"
		r.AvailableActions = nil
		p := agentruntime.ProjectRequest(*r, v.Observation, v.TurnStatus)
		r.Projection = &p
		b, e := json.Marshal(r)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, `UPDATE codex_server_requests SET request_json=? WHERE run_id=? AND connection_id=? AND request_id=?`, string(b), runID, r.ConnectionID, string(r.RequestID)); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	v.Sequence++
	v.WaitingState = "CANCELED"
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_runtime_states SET snapshot_json=?,sequence=? WHERE run_id=?`, string(b), v.Sequence, runID)
	return err
}
