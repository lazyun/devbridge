package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/run"
)

// MarkThreadStartPending precedes the external thread/start request. Losing its
// response or the subsequent binding is an unknown context, even without a Turn.
func (s *AgentConversationStore) MarkThreadStartPending(ctx context.Context, runID string) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err := s.ownedAgentBoundaryTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET thread_start_pending=1,advance_state='UNKNOWN'
 WHERE run_id=? AND advance_state='NO_ADVANCE' AND thread_start_pending=0
 AND EXISTS(SELECT 1 FROM agent_conversations a JOIN runs r ON r.id=? WHERE a.id=? AND a.backend_thread_id IS NULL AND r.status='PREPARING')`, runID, runID, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return agentconversation.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET continuation_safe=0 WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordFinalization is the durable observation/intent seam. It records no
// guessed external facts. RunStore.Update consumes this intent in the SAME
// transaction as the terminal Run/result/token write. For product materialization
// and serial verification the intent remains queryable until the exact product
// finalization releases the operation; startup can finish those bounded gaps.
func (s *AgentConversationStore) RecordFinalization(ctx context.Context, runID string, facts agentconversation.Finalization) error {
	if facts.Canceled && (facts.Advance != agentconversation.KnownAdvance || !facts.Quiesced || facts.ExitBoundaryTurnID == "") {
		return agentconversation.ErrUnsafe
	}
	if facts.Advance != agentconversation.NoAdvance && facts.Advance != agentconversation.KnownAdvance && facts.Advance != agentconversation.Unknown {
		return agentconversation.ErrInvalidArgument
	}
	if (facts.Advance == agentconversation.KnownAdvance) != (facts.ExitBoundaryTurnID != "") {
		return agentconversation.ErrInvalidArgument
	}
	if facts.Advance == agentconversation.Unknown {
		facts.Quiesced = false
	}
	if !facts.Quiesced {
		facts.Advance, facts.ExitBoundaryTurnID = agentconversation.Unknown, ""
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.db.checkRunTx(ctx, tx, runID); err != nil {
		return err
	}
	if !facts.Canceled {
		if err = requireResolvedResponsesTx(ctx, tx, runID); err != nil {
			return err
		}
	}
	if facts.Canceled {
		changed, e := tx.ExecContext(ctx, `UPDATE execution_cancellations SET terminal_observed=1 WHERE run_id=? AND runtime_epoch=? AND turn_id=? AND interrupt_state<>'NOT_SENT' AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED','CANCELED')`, runID, s.db.epoch, facts.ExitBoundaryTurnID)
		if e != nil {
			return e
		}
		if n, _ := changed.RowsAffected(); n != 1 {
			return agentconversation.ErrConflict
		}
	}
	var previous string
	var starting bool
	if err = tx.QueryRowContext(ctx, `SELECT advance_state,thread_start_pending FROM agent_operation_boundaries WHERE run_id=?`, runID).Scan(&previous, &starting); err != nil {
		return err
	}
	if starting {
		facts.Advance, facts.ExitBoundaryTurnID = agentconversation.Unknown, ""
	}
	var oldAdvance, oldExit, oldEpoch string
	var oldQuiesced bool
	err = tx.QueryRowContext(ctx, `SELECT advance_state,COALESCE(exit_boundary_turn_id,''),quiesced,runtime_epoch FROM agent_finalization_intents WHERE run_id=?`, runID).Scan(&oldAdvance, &oldExit, &oldQuiesced, &oldEpoch)
	if err == nil {
		if oldAdvance != string(facts.Advance) || oldExit != facts.ExitBoundaryTurnID || oldQuiesced != facts.Quiesced || oldEpoch != s.db.epoch {
			return agentconversation.ErrConflict
		}
		// Replay does not perform a second final transition or claim release.
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	id, err := s.ownedAgentBoundaryTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	if facts.Advance == agentconversation.NoAdvance && previous != "NO_ADVANCE" {
		return agentconversation.ErrUnsafe
	}
	if facts.Advance == agentconversation.KnownAdvance {
		var exact bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN agent_conversations a ON a.id=? WHERE r.id=? AND r.thread_id=a.backend_thread_id AND r.turn_id=?)`, id, runID, facts.ExitBoundaryTurnID).Scan(&exact); err != nil {
			return err
		}
		if !exact {
			return agentconversation.ErrConflict
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_finalization_intents(run_id,feature_area_id,operation_kind,operation_id,agent_conversation_id,runtime_epoch,advance_state,exit_boundary_turn_id,quiesced,created_at)
 SELECT run_id,feature_area_id,operation_kind,operation_id,?,runtime_epoch,?,?,?,? FROM feature_area_agent_claims WHERE run_id=? AND runtime_epoch=?`, id, facts.Advance, nullString(facts.ExitBoundaryTurnID), boolInt(facts.Quiesced), formatTime(time.Now().UTC()), runID, s.db.epoch)
	if err != nil {
		return err
	}
	value, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, runID))
	if err != nil {
		return err
	}
	if err = finalizeAgentRunTx(ctx, tx, value); err != nil {
		return err
	}
	return tx.Commit()
}

func terminalAgentRun(status run.Status) bool {
	return status == run.StatusSucceeded || status == run.StatusFailed || status == run.StatusInfrastructureFailed || status == run.StatusCanceled
}

// finalizeAgentRunTx is called inside the Run terminal CAS transaction. UNKNOWN
// is never released here, including when a local transport was closed.
func finalizeAgentRunTx(ctx context.Context, tx *sql.Tx, value run.Run) error {
	if !terminalAgentRun(value.Status) {
		return nil
	}
	var id, advance, exit string
	var quiesced bool
	err := tx.QueryRowContext(ctx, `SELECT i.agent_conversation_id,i.advance_state,COALESCE(i.exit_boundary_turn_id,''),i.quiesced
 FROM agent_finalization_intents i JOIN feature_area_agent_claims c ON c.run_id=i.run_id
 JOIN agent_conversations a ON a.id=i.agent_conversation_id
 WHERE i.run_id=? AND i.runtime_epoch=? AND c.runtime_epoch=i.runtime_epoch
 AND c.feature_area_id=i.feature_area_id AND c.operation_kind=i.operation_kind AND c.operation_id=i.operation_id
 AND a.feature_area_id=c.feature_area_id AND a.ended_at IS NULL`, value.ID, value.RuntimeEpoch).Scan(&id, &advance, &exit, &quiesced)
	if errors.Is(err, sql.ErrNoRows) {
		// No intent after possible dispatch remains UNKNOWN and requires explicit
		// recovery. A never-entered Run can be released after product terminal.
		return releaseFinalizedAgentOperationTx(ctx, tx, value.ID)
	}
	if err != nil {
		return err
	}
	var applied bool
	if err = tx.QueryRowContext(ctx, `SELECT applied_at IS NOT NULL FROM agent_finalization_intents WHERE run_id=?`, value.ID).Scan(&applied); err != nil {
		return err
	}
	if !applied {
		var entry, previous string
		var pending, wasQuiesced bool
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(entry_boundary_turn_id,''),advance_state,thread_start_pending,quiesced FROM agent_operation_boundaries WHERE run_id=? AND agent_conversation_id=?`, value.ID, id).Scan(&entry, &previous, &pending, &wasQuiesced); err != nil {
			return err
		}
		if wasQuiesced {
			return agentconversation.ErrConflict
		}
		if advance == string(agentconversation.KnownAdvance) && (value.TurnID != exit || value.ThreadID == "" || pending || previous != "UNKNOWN") {
			return agentconversation.ErrConflict
		}
		if advance == string(agentconversation.NoAdvance) && (pending || previous != "NO_ADVANCE" || value.TurnID != "") {
			return agentconversation.ErrConflict
		}
		safe := advance != string(agentconversation.Unknown)
		nextHead := entry
		if advance == string(agentconversation.KnownAdvance) {
			nextHead = exit
		}
		result, e := tx.ExecContext(ctx, `UPDATE agent_conversations SET current_boundary_turn_id=?,continuation_safe=? WHERE id=? AND ended_at IS NULL AND COALESCE(current_boundary_turn_id,'')=? AND (?<>'KNOWN_ADVANCE' OR backend_thread_id=?)`, nullString(nextHead), boolInt(safe), id, entry, advance, value.ThreadID)
		if e != nil {
			return e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return agentconversation.ErrConflict
		}
		result, err = tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET advance_state=?,exit_boundary_turn_id=?,quiesced=? WHERE run_id=? AND agent_conversation_id=? AND quiesced=0`, advance, nullString(exit), boolInt(quiesced), value.ID, id)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return agentconversation.ErrConflict
		}
		result, err = tx.ExecContext(ctx, `UPDATE agent_finalization_intents SET applied_at=? WHERE run_id=? AND applied_at IS NULL`, formatTime(time.Now().UTC()), value.ID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return agentconversation.ErrConflict
		}
	}
	if err := finalizeCanceledExecutionTx(ctx, tx, value); err != nil {
		return err
	}
	if err := releaseFinalizedAgentOperationTx(ctx, tx, value.ID); err != nil {
		return err
	}
	return settleExecutionCancellationsTx(ctx, tx, value.ID)
}

// releaseFinalizedAgentOperationTx is a continuation of the durable finalization
// intent, also used by the exact Execution product-terminal tx.
func releaseFinalizedAgentOperationTx(ctx context.Context, tx *sql.Tx, runID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims
 WHERE run_id=? AND runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND EXISTS(SELECT 1 FROM runs r LEFT JOIN agent_operation_boundaries b ON b.run_id=r.id
 WHERE r.id=feature_area_agent_claims.run_id AND r.runtime_epoch=feature_area_agent_claims.runtime_epoch
 AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')
 AND (b.run_id IS NULL OR (b.quiesced=1 AND b.advance_state IN ('NO_ADVANCE','KNOWN_ADVANCE') AND b.thread_start_pending=0 AND EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id AND i.applied_at IS NOT NULL AND i.runtime_epoch=r.runtime_epoch))))
 AND (operation_kind<>'EXECUTION' OR NOT EXISTS(SELECT 1 FROM execution_proposals e JOIN runs r ON r.execution_proposal_id=e.id WHERE r.id=feature_area_agent_claims.run_id AND e.outcome NOT IN ('SUCCEEDED','FAILED','CANCELED')))`, runID)
	return err
}
