package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/agentruntime"
)

// ReadRuntimeRequests reads request authority in one database snapshot. Process
// metadata, health and reconnect display projections are not progression facts.
func (s *RunStore) ReadRuntimeRequests(ctx context.Context, runID string) (agentruntime.Snapshot, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return agentruntime.Snapshot{}, err
	}
	defer tx.Rollback()
	return readRuntimeRequestsTx(ctx, tx, runID)
}

func readRuntimeRequestsTx(ctx context.Context, tx *sql.Tx, runID string) (agentruntime.Snapshot, error) {
	var encoded string
	err := tx.QueryRowContext(ctx, `SELECT snapshot_json FROM agent_runtime_states WHERE run_id=?`, runID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		var requestsExist bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM codex_server_requests WHERE run_id=?)`, runID).Scan(&requestsExist); err != nil {
			return agentruntime.Snapshot{}, err
		}
		if requestsExist {
			return agentruntime.Snapshot{}, agentruntime.ErrInvalid
		}
		return agentruntime.Snapshot{Identity: agentruntime.Identity{RunID: runID}, Availability: "UNAVAILABLE"}, nil
	}
	if err != nil {
		return agentruntime.Snapshot{}, err
	}
	var v agentruntime.Snapshot
	if err = json.Unmarshal([]byte(encoded), &v); err != nil {
		return agentruntime.Snapshot{}, err
	}
	if v.RunID != runID || v.ConnectionID == "" || v.ThreadID == "" || v.TurnID == "" {
		return agentruntime.Snapshot{}, agentruntime.ErrInvalid
	}
	indices := make(map[string]int, len(v.Requests))
	for i, request := range v.Requests {
		key := string(request.RequestID)
		if _, duplicate := indices[key]; duplicate {
			return agentruntime.Snapshot{}, agentruntime.ErrInvalid
		}
		indices[key] = i
	}
	rows, err := tx.QueryContext(ctx, `SELECT request_id,request_json FROM codex_server_requests WHERE run_id=? AND connection_id=?`, runID, v.ConnectionID)
	if err != nil {
		return agentruntime.Snapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, requestJSON string
		if err = rows.Scan(&key, &requestJSON); err != nil {
			return agentruntime.Snapshot{}, err
		}
		i, found := indices[key]
		if !found {
			return agentruntime.Snapshot{}, agentruntime.ErrInvalid
		}
		expected := v.Requests[i]
		var request agentruntime.Request
		if err = json.Unmarshal([]byte(requestJSON), &request); err != nil {
			return agentruntime.Snapshot{}, err
		}
		if request.Identity != v.Identity || request.ID != expected.ID || string(request.RequestID) != string(expected.RequestID) {
			return agentruntime.Snapshot{}, agentruntime.ErrInvalid
		}
		v.Requests[i] = request
		delete(indices, key)
	}
	if err = rows.Err(); err != nil {
		return agentruntime.Snapshot{}, err
	}
	if len(indices) != 0 {
		return agentruntime.Snapshot{}, agentruntime.ErrInvalid
	}
	return v, nil
}
