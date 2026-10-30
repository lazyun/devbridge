package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/codexusage"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

const schemaV35 = `
CREATE TABLE codex_usage_ledgers (
 thread_id TEXT PRIMARY KEY CHECK(length(thread_id)>0),
 accounting_epoch INTEGER NOT NULL CHECK(accounting_epoch>0),
 previous_raw_cumulative TEXT NOT NULL,
 logical_cumulative TEXT NOT NULL,
 anchored INTEGER NOT NULL CHECK(anchored IN (0,1)),
 last_turn_id TEXT NOT NULL,
 in_flight_turn_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0)
);
CREATE TABLE run_usage_attributions (
 thread_id TEXT NOT NULL REFERENCES codex_usage_ledgers(thread_id),
 turn_id TEXT NOT NULL CHECK(length(turn_id)>0),
 run_id TEXT UNIQUE REFERENCES runs(id),
 attribution_json TEXT NOT NULL,
 PRIMARY KEY(thread_id,turn_id)
);
`

type CodexUsageStore struct{ db *DB }

func NewCodexUsageStore(db *DB) *CodexUsageStore { return &CodexUsageStore{db: db} }

func (s *CodexUsageStore) Load(ctx context.Context, threadID string) (codexusage.CodexUsageLedger, bool, error) {
	var value codexusage.CodexUsageLedger
	var raw, logical string
	err := s.db.sql.QueryRowContext(ctx, `SELECT thread_id,accounting_epoch,previous_raw_cumulative,logical_cumulative,anchored,last_turn_id,in_flight_turn_id,revision FROM codex_usage_ledgers WHERE thread_id=?`, threadID).Scan(
		&value.ThreadID, &value.AccountingEpoch, &raw, &logical, &value.Anchored, &value.LastTurnID, &value.InFlightTurnID, &value.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	if err := json.Unmarshal([]byte(raw), &value.PreviousRawCumulative); err != nil {
		return value, false, err
	}
	if err := json.Unmarshal([]byte(logical), &value.LogicalCumulative); err != nil {
		return value, false, err
	}
	if !codexusage.Valid(value.PreviousRawCumulative) || !codexusage.Valid(value.LogicalCumulative) {
		return value, false, errors.New("invalid persisted Codex usage counts")
	}
	return value, true, nil
}

func (s *CodexUsageStore) Save(ctx context.Context, expected int64, value codexusage.CodexUsageLedger, attribution *codexusage.RunUsageAttribution) error {
	if value.ThreadID == "" || value.AccountingEpoch < 1 || value.Revision != expected+1 || !codexusage.Valid(value.PreviousRawCumulative) || !codexusage.Valid(value.LogicalCumulative) {
		return errors.New("invalid Codex usage ledger")
	}
	if attribution != nil && (attribution.ThreadID != value.ThreadID || attribution.TurnID == "" || attribution.LogicalEnd != value.LogicalCumulative || attribution.EndEpoch != value.AccountingEpoch) {
		return errors.New("Codex usage attribution does not match ledger")
	}
	raw, _ := json.Marshal(value.PreviousRawCumulative)
	logical, _ := json.Marshal(value.LogicalCumulative)
	// A ledger-only bootstrap has no Run owner. beginWrite still verifies the
	// current process writer lock and durable runtime authority. When a caller
	// has a Run fence, retain it for the exact live-owner check below.
	if attribution != nil && attribution.RunID != "" {
		fence, ok := runtimeepoch.FromContext(ctx)
		if !ok || fence.RunID == "" || fence.RunID != attribution.RunID {
			return s.db.fenced(ctx, attribution.RunID)
		}
	}
	if attribution != nil {
		if fence, ok := runtimeepoch.FromContext(ctx); ok && fence.RunID != "" && attribution.RunID != fence.RunID {
			return s.db.fenced(ctx, fence.RunID)
		}
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if fence, ok := runtimeepoch.FromContext(ctx); ok && fence.RunID != "" {
		turnID := ""
		if attribution != nil {
			turnID = attribution.TurnID
		}
		if err := s.db.checkUsageRunTx(ctx, tx, fence.RunID, value.ThreadID, turnID); err != nil {
			return err
		}
	} else {
		// Legacy/test-only unowned telemetry cannot alter a Thread belonging
		// to any durable Run. Production inspections carry their exact Run fence.
		var owned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE thread_id=?)`, value.ThreadID).Scan(&owned); err != nil {
			return err
		}
		if owned {
			return s.db.fenced(ctx, "")
		}
	}
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO codex_usage_ledgers(thread_id,accounting_epoch,previous_raw_cumulative,logical_cumulative,anchored,last_turn_id,in_flight_turn_id,revision) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(thread_id) DO NOTHING`, value.ThreadID, value.AccountingEpoch, string(raw), string(logical), value.Anchored, value.LastTurnID, value.InFlightTurnID, value.Revision)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE codex_usage_ledgers SET accounting_epoch=?,previous_raw_cumulative=?,logical_cumulative=?,anchored=?,last_turn_id=?,in_flight_turn_id=?,revision=? WHERE thread_id=? AND revision=? AND accounting_epoch<=?`, value.AccountingEpoch, string(raw), string(logical), value.Anchored, value.LastTurnID, value.InFlightTurnID, value.Revision, value.ThreadID, expected, value.AccountingEpoch)
	}
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("Codex usage ledger revision conflict: %w", ErrConflict)
	}
	if attribution != nil {
		encoded, err := json.Marshal(attribution)
		if err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `INSERT INTO run_usage_attributions(thread_id,turn_id,run_id,attribution_json) VALUES(?,?,?,?) ON CONFLICT(thread_id,turn_id) DO UPDATE SET attribution_json=excluded.attribution_json WHERE run_usage_attributions.run_id IS excluded.run_id AND json_extract(run_usage_attributions.attribution_json,'$.Complete')=0`, attribution.ThreadID, attribution.TurnID, nullString(attribution.RunID), string(encoded))
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("Codex usage attribution identity or terminal conflict: %w", ErrConflict)
		}
	}
	return tx.Commit()
}

func (s *CodexUsageStore) Attribution(ctx context.Context, runID string) (codexusage.RunUsageAttribution, error) {
	var value codexusage.RunUsageAttribution
	var encoded string
	if err := s.db.sql.QueryRowContext(ctx, `SELECT attribution_json FROM run_usage_attributions WHERE run_id=?`, runID).Scan(&encoded); err != nil {
		return value, err
	}
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		return value, err
	}
	return value, nil
}
