package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

const agentSessionHistoryMigrationSQL = `
CREATE TABLE agent_session_records (
 run_id TEXT NOT NULL REFERENCES runs(id),
 item_id TEXT NOT NULL,
 ordinal INTEGER NOT NULL CHECK(ordinal>0),
 revision INTEGER NOT NULL CHECK(revision>0),
 item_json TEXT NOT NULL CHECK(json_valid(item_json)),
 byte_count INTEGER NOT NULL CHECK(byte_count>=0 AND byte_count<=65536),
 PRIMARY KEY(run_id,item_id)
);
CREATE UNIQUE INDEX agent_session_record_page ON agent_session_records(run_id,ordinal);
CREATE TABLE agent_session_coverage (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 dropped_items INTEGER NOT NULL DEFAULT 0,
 capture_started INTEGER NOT NULL DEFAULT 1,
 materialized_sequence INTEGER NOT NULL DEFAULT 0,
 coverage_json TEXT NOT NULL DEFAULT '{"state":"PARTIAL","reasons":["MATERIALIZED_ITEMS_NOT_EVENT_LOG"]}' CHECK(json_valid(coverage_json))
);
`

// SaveSessionHistory is the independent display writer. It uses the original
// Core single-writer/epoch/claim admission, with an additional immutable runtime
// identity fence. It never changes Run, request, approval or cancellation state.
func (s *AgentRuntimeStore) SaveSessionHistory(ctx context.Context, batch agentruntime.HistoryBatch) error {
	fence, ok := runtimeepoch.FromContext(ctx)
	if !ok || fence.RunID != batch.RunID || batch.ThroughSequence == 0 || len(batch.Items) > agentruntime.MaxItems {
		return runtimeepoch.ErrFenced
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.db.checkUsageRunTx(ctx, tx, batch.RunID, batch.ThreadID, batch.TurnID); err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runtime_states a JOIN runs r ON r.id=a.run_id
 WHERE a.run_id=? AND a.runtime_epoch=? AND a.connection_id=? AND a.thread_id=? AND a.turn_id=? AND a.sequence>=?
 AND COALESCE(r.project_id,'')=? AND COALESCE(r.feature_area_id,'')=? AND COALESCE(r.task_id,'')=?)`, batch.RunID, fence.Epoch, batch.ConnectionID, batch.ThreadID, batch.TurnID, batch.ThroughSequence, batch.ProjectID, batch.FeatureAreaID, batch.TaskID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return runtimeepoch.ErrFenced
	}
	if err = saveSessionItems(ctx, tx, batch); err != nil {
		return err
	}
	return tx.Commit()
}

func saveSessionItems(ctx context.Context, tx *sql.Tx, v agentruntime.HistoryBatch) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_session_coverage(run_id,capture_started) VALUES(?,?) ON CONFLICT(run_id) DO NOTHING`, v.RunID, v.ThroughSequence); err != nil {
		return err
	}
	// Compare against the independent materialization, never the runtime
	// snapshot sequence: approval saves can advance that sequence first.
	versions := map[string]uint64{}
	versionRows, versionErr := tx.QueryContext(ctx, `SELECT item_id,revision FROM agent_session_records WHERE run_id=?`, v.RunID)
	if versionErr != nil {
		return versionErr
	}
	for versionRows.Next() {
		var id string
		var revision uint64
		if versionErr = versionRows.Scan(&id, &revision); versionErr != nil {
			versionRows.Close()
			return versionErr
		}
		versions[id] = revision
	}
	versionErr = versionRows.Err()
	versionRows.Close()
	if versionErr != nil {
		return versionErr
	}
	if v.Coverage.State == "GAP" || v.Coverage.State == "UNAVAILABLE" {
		coverage, err := json.Marshal(v.Coverage)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agent_session_coverage SET coverage_json=? WHERE run_id=?`, string(coverage), v.RunID); err != nil {
			return err
		}
	}
	for _, item := range v.Items {
		if item.Revision == 0 || item.Revision <= versions[item.ID] {
			continue
		}
		b, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if len(b) > agentruntime.MaxSessionItemBytes {
			item.Metadata = json.RawMessage(`{"truncated":true}`)
			item.Truncated = true
			b, err = json.Marshal(item)
			if err != nil {
				return err
			}
		}
		if len(b) > agentruntime.MaxSessionItemBytes {
			return errors.New("SESSION_ITEM_CAP_EXCEEDED")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_session_records(run_id,item_id,ordinal,revision,item_json,byte_count)
   VALUES(?,?,(SELECT COALESCE(MAX(ordinal),0)+1 FROM agent_session_records WHERE run_id=?),?,?,?)
   ON CONFLICT(run_id,item_id) DO UPDATE SET revision=excluded.revision,item_json=excluded.item_json,byte_count=excluded.byte_count
   WHERE agent_session_records.revision<excluded.revision`, v.RunID, item.ID, v.RunID, item.Revision, string(b), len(b))
		if err != nil {
			return err
		}
	}
	var count, size int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(byte_count),0) FROM agent_session_records WHERE run_id=?`, v.RunID).Scan(&count, &size); err != nil {
		return err
	}
	dropped := 0
	for count > agentruntime.MaxSessionItems || size > agentruntime.MaxSessionBytes {
		var itemID string
		var bytes int
		if err := tx.QueryRowContext(ctx, `SELECT item_id,byte_count FROM agent_session_records WHERE run_id=? ORDER BY ordinal LIMIT 1`, v.RunID).Scan(&itemID, &bytes); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_session_records WHERE run_id=? AND item_id=?`, v.RunID, itemID); err != nil {
			return err
		}
		count--
		size -= bytes
		dropped++
	}
	_, err := tx.ExecContext(ctx, `UPDATE agent_session_coverage SET dropped_items=dropped_items+?,materialized_sequence=MAX(materialized_sequence,?) WHERE run_id=?`, dropped, v.ThroughSequence, v.RunID)
	return err
}

func (s *AgentRuntimeStore) ReadSessionHistory(ctx context.Context, runID string, before uint64, limit int) (agentruntime.HistoryPage, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > agentruntime.MaxSessionPage {
		limit = agentruntime.MaxSessionPage
	}
	out := agentruntime.HistoryPage{RunID: runID, Items: []agentruntime.SessionRecord{}, Coverage: agentruntime.Coverage{State: "PARTIAL", Reasons: []string{"MATERIALIZED_ITEMS_NOT_EVENT_LOG"}}, Limits: agentruntime.SessionLimits{ItemsPerRun: agentruntime.MaxSessionItems, BytesPerRun: agentruntime.MaxSessionBytes, BytesPerItem: agentruntime.MaxSessionItemBytes, ItemsPerPage: agentruntime.MaxSessionPage}}
	// A read transaction holds a consistent page and its truncation metadata.
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var dropped int
	var started uint64
	var coverageJSON string
	err = tx.QueryRowContext(ctx, `SELECT dropped_items,capture_started,coverage_json,materialized_sequence FROM agent_session_coverage WHERE run_id=?`, runID).Scan(&dropped, &started, &coverageJSON, &out.MaterializedSequence)
	if errors.Is(err, sql.ErrNoRows) {
		out.Coverage = agentruntime.Coverage{State: "UNAVAILABLE", Reasons: []string{"HISTORY_NOT_CAPTURED"}}
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if dropped > 0 {
		out.Coverage = agentruntime.Coverage{State: "TRUNCATED", Reasons: []string{"RUN_RECORD_CAPACITY_EXCEEDED", "MATERIALIZED_ITEMS_NOT_EVENT_LOG"}}
	}
	if started > 1 {
		out.Coverage.Reasons = append(out.Coverage.Reasons, "CAPTURE_STARTED_AFTER_RUN_BEGIN")
	}
	var historyCoverage agentruntime.Coverage
	if json.Unmarshal([]byte(coverageJSON), &historyCoverage) != nil {
		out.Coverage.State = "GAP"
		out.Coverage.Reasons = append(out.Coverage.Reasons, "HISTORY_COVERAGE_UNREADABLE")
	} else if historyCoverage.State == "GAP" || historyCoverage.State == "UNAVAILABLE" {
		out.Coverage.State = "GAP"
		out.Coverage.Reasons = append(out.Coverage.Reasons, historyCoverage.Reasons...)
	}

	var captureJSON, runStatus string
	var currentEpoch bool
	captureErr := tx.QueryRowContext(ctx, `SELECT a.snapshot_json,r.status,a.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1) FROM agent_runtime_states a JOIN runs r ON r.id=a.run_id WHERE a.run_id=?`, runID).Scan(&captureJSON, &runStatus, &currentEpoch)
	if captureErr == nil {
		var capture struct {
			Coverage        agentruntime.Coverage `json:"captureCoverage"`
			Sequence        uint64                `json:"sequence"`
			ConnectionState string                `json:"connectionState"`
			TurnStatus      string                `json:"turnStatus"`
		}
		if json.Unmarshal([]byte(captureJSON), &capture) != nil {
			out.Coverage.State = "GAP"
			out.Coverage.Reasons = append(out.Coverage.Reasons, "CAPTURE_STATE_UNREADABLE")
		} else if capture.Coverage.State == "GAP" || capture.Coverage.State == "UNAVAILABLE" {
			out.Coverage.State = "GAP"
			out.Coverage.Reasons = append(out.Coverage.Reasons, capture.Coverage.Reasons...)
		}
		if (!currentEpoch || (runStatus != "RUNNING" && runStatus != "WAITING_APPROVAL" && runStatus != "COLLECTING")) && capture.TurnStatus != "completed" && capture.TurnStatus != "failed" && capture.TurnStatus != "interrupted" {
			out.Coverage.State = "GAP"
			out.Coverage.Reasons = append(out.Coverage.Reasons, "FINAL_RUNTIME_CAPTURE_UNAVAILABLE")
		}
		if capture.Sequence > out.MaterializedSequence {
			reason := "MATERIALIZATION_PENDING"
			if capture.ConnectionState != "CONNECTED" || !currentEpoch || (runStatus != "RUNNING" && runStatus != "WAITING_APPROVAL" && runStatus != "COLLECTING") {
				out.Coverage.State = "GAP"
				reason = "HISTORY_PROJECTION_INCOMPLETE"
			}
			out.Coverage.Reasons = append(out.Coverage.Reasons, reason)
		}

	} else if !errors.Is(captureErr, sql.ErrNoRows) {
		return out, captureErr
	}

	rows, err := tx.QueryContext(ctx, `SELECT ordinal,revision,item_json FROM agent_session_records WHERE run_id=? AND (?=0 OR ordinal<?) ORDER BY ordinal DESC LIMIT ?`, runID, before, before, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var record agentruntime.SessionRecord
		var b string
		if err = rows.Scan(&record.Ordinal, &record.Revision, &b); err != nil {
			return out, err
		}
		if len(out.Items) == limit {
			out.HasMore = true
			break
		}
		if err = json.Unmarshal([]byte(b), &record.Item); err != nil {
			return out, err
		}
		out.Items = append(out.Items, record)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if out.HasMore && len(out.Items) > 0 {
		out.NextBefore = out.Items[len(out.Items)-1].Ordinal
	}
	return out, nil
}
