package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func (d *DB) allocateRuntimeEpoch(ctx context.Context) error {
	if d.writer == nil || !d.writer.Held() {
		return runtimeepoch.ErrFenced
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	epoch := "runtime_" + hex.EncodeToString(id[:])
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_epochs(id,created_at) VALUES(?,?)`, epoch, formatTime(time.Now().UTC())); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_authority(singleton,epoch_id) VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET epoch_id=excluded.epoch_id`, epoch); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	d.epoch = epoch
	observability.Info(ctx, "bootstrap", "runtime.epoch.allocated", "Exclusive writer runtime epoch allocated", map[string]any{"runtimeEpoch": epoch})
	return nil
}

// beginWrite takes SQLite's write lock before inspecting authority. The epoch
// cannot change between this check and the transaction's actual mutations.
func (d *DB) beginWrite(ctx context.Context) (*sql.Tx, error) {
	if d.writer == nil || !d.writer.Held() {
		return nil, d.fenced(ctx, "")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err = d.checkWriterTx(ctx, tx); err == nil {
		if fence, ok := runtimeepoch.FromContext(ctx); ok && fence.RunID != "" {
			err = d.checkRunTx(ctx, tx, fence.RunID)
		}
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (d *DB) fenced(ctx context.Context, runID string) error {
	observability.Info(ctx, "runtime", "runtime.write.fenced", "Stale runtime mutation rejected", map[string]any{"runtimeEpoch": d.epoch, "runId": runID, "errorCode": "RUNTIME_EPOCH_FENCED"})
	return runtimeepoch.ErrFenced
}

func (d *DB) checkWriterTx(ctx context.Context, tx *sql.Tx) error {
	if d.writer == nil || !d.writer.Held() || d.epoch == "" {
		return d.fenced(ctx, "")
	}
	if fence, ok := runtimeepoch.FromContext(ctx); ok && fence.Epoch != d.epoch {
		return d.fenced(ctx, fence.RunID)
	}
	var epoch string
	err := tx.QueryRowContext(ctx, `UPDATE runtime_authority SET epoch_id=epoch_id WHERE singleton=1 AND epoch_id=? RETURNING epoch_id`, d.epoch).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return d.fenced(ctx, "")
	}
	return err
}

func (d *DB) checkRunTx(ctx context.Context, tx *sql.Tx, runID string) error {
	if fence, ok := runtimeepoch.FromContext(ctx); ok && (fence.Epoch != d.epoch || (fence.RunID != "" && fence.RunID != runID)) {
		return d.fenced(ctx, runID)
	}
	if blocked, err := d.sourceRecoveryFencedTx(ctx, tx, runID); err != nil {
		return err
	} else if blocked {
		if _, callback := runtimeepoch.FromContext(ctx); callback {
			return d.fenced(ctx, runID)
		}
	}
	if child, err := d.recoveryChildAuthorityTx(ctx, tx, runID); err != nil {
		return err
	} else if child {
		return nil
	}
	if continuation, err := d.inspectionContinuationAuthorityTx(ctx, tx, runID); err != nil {
		return err
	} else if continuation {
		return nil
	}
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM runs r
 LEFT JOIN agent_operation_boundaries b ON b.run_id=r.id
 LEFT JOIN agent_conversations a ON a.id=b.agent_conversation_id
 WHERE r.id=? AND r.runtime_epoch=?
 AND EXISTS(SELECT 1 FROM agent_executions e WHERE e.id=r.agent_execution_id AND e.provenance_kind<>'LEGACY_UNKNOWN' AND e.task_id=r.task_id AND e.project_id=r.project_id AND e.feature_area_id IS r.feature_area_id AND e.semantic_owner_kind=r.operation_kind AND e.semantic_owner_id=r.operation_id)
 AND NOT EXISTS(SELECT 1 FROM agent_orphan_dispositions o WHERE o.run_id=r.id)
 AND (b.run_id IS NULL OR a.ended_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.feature_area_id=r.feature_area_id AND (c.run_id<>r.id OR c.runtime_epoch IS NOT r.runtime_epoch
 OR c.operation_kind<>r.operation_kind
 OR c.operation_id<>r.operation_id)))`, runID, d.epoch).Scan(&valid)
	if err != nil {
		return err
	}
	if valid {
		if fence, ok := runtimeepoch.FromContext(ctx); ok && fence.HostTool {
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r
 JOIN feature_area_agent_claims c ON c.run_id=r.id AND c.runtime_epoch=r.runtime_epoch
 JOIN agent_operation_boundaries b ON b.run_id=r.id
 JOIN agent_conversations a ON a.id=b.agent_conversation_id AND a.feature_area_id=c.feature_area_id
 WHERE r.id=? AND r.runtime_epoch=? AND r.status='RUNNING' AND r.turn_id IS NOT NULL
 AND b.quiesced=0 AND b.advance_state='UNKNOWN' AND b.thread_start_pending=0
 AND a.ended_at IS NULL AND a.backend_thread_id=r.thread_id
	AND EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=r.feature_area_id AND f.status IN ('DISCOVERING','ACTIVE'))
 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id)
	AND NOT EXISTS(SELECT 1 FROM execution_cancellations x WHERE x.run_id=r.id AND x.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')))`, runID, d.epoch).Scan(&valid)
			if err != nil {
				return err
			}
		}
	}
	if !valid {
		return d.fenced(ctx, runID)
	}
	return nil
}

// checkUsageRunTx fences telemetry against the exact live Run owner. Unlike a
// Host Tool callback, usage must also be persisted for the narrow dispatch
// interval before the Turn ID is durable, so it deliberately does not require
// an UNKNOWN boundary or a populated turn_id.
func (d *DB) checkUsageRunTx(ctx context.Context, tx *sql.Tx, runID, threadID, turnID string) error {
	if err := d.checkRunTx(ctx, tx, runID); err != nil {
		return err
	}
	if continuation, err := d.inspectionContinuationAuthorityTx(ctx, tx, runID); err != nil {
		return err
	} else if continuation {
		var exact bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN inspection_runtime_records x ON x.run_id=r.id WHERE r.id=? AND r.thread_id=? AND (r.turn_id=? OR (r.turn_id IS NULL AND x.state='UNKNOWN')) AND r.status IN ('PREPARING','RUNNING'))`, runID, threadID, turnID).Scan(&exact); err != nil {
			return err
		}
		if !exact {
			return d.fenced(ctx, runID)
		}
		return nil
	}
	if child, err := d.recoveryChildAuthorityTx(ctx, tx, runID); err != nil {
		return err
	} else if child {
		var exact bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND thread_id=? AND (turn_id=? OR (turn_id IS NULL AND EXISTS(SELECT 1 FROM recovery_status_records x WHERE x.recovery_run_id=runs.id AND x.state='UNKNOWN'))) AND status IN ('PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING'))`, runID, threadID, turnID).Scan(&exact); err != nil {
			return err
		}
		if !exact {
			return d.fenced(ctx, runID)
		}
		return nil
	}
	var inspection bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN context_probes p ON p.run_id=r.id AND p.task_id=r.task_id AND p.runtime_epoch=r.runtime_epoch WHERE r.id=? AND r.operation_kind='INSPECTION' AND r.operation_id=p.id AND r.context_probe_id=p.id AND r.thread_id=? AND r.status='RUNNING' AND p.status='RUNNING' AND (?='' OR r.turn_id IS NULL OR r.turn_id=?))`, runID, threadID, turnID, turnID).Scan(&inspection); err != nil {
		return err
	}
	if inspection {
		return nil
	}
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r
 JOIN feature_area_agent_claims c ON c.run_id=r.id AND c.runtime_epoch=r.runtime_epoch
 JOIN agent_operation_boundaries b ON b.run_id=r.id
 JOIN agent_conversations a ON a.id=b.agent_conversation_id AND a.feature_area_id=c.feature_area_id
 WHERE r.id=? AND r.runtime_epoch=? AND r.thread_id=?
 AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND r.status IN ('RUNNING','WAITING_APPROVAL','COLLECTING')
 AND a.ended_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id)
 AND r.feature_area_id=c.feature_area_id AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id
 AND a.backend_thread_id=r.thread_id
 AND (?='' OR r.turn_id IS NULL OR r.turn_id=?))`, runID, d.epoch, threadID, turnID, turnID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return d.fenced(ctx, runID)
	}
	return nil
}

func (d *DB) execWrite(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx, err := d.beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
