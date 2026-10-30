package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const unresolvedCancellationInterruptCSQL = `EXISTS(SELECT 1 FROM unresolved_cancellation_interrupts u WHERE u.project_id=c.project_id AND u.request_id=c.request_id)`
const unresolvedCancellationInterruptXSQL = `EXISTS(SELECT 1 FROM unresolved_cancellation_interrupts u WHERE u.project_id=x.project_id AND u.request_id=x.request_id)`

// A lost interrupt response stays UNKNOWN forever. Only independent, applied
// terminal evidence for that exact owner and Run can remove its live fence.
const cancellationTerminalV124SQL = `CREATE VIEW unresolved_cancellation_interrupts AS
 SELECT c.project_id,c.request_id FROM execution_cancellations c
 WHERE c.interrupt_state='OUTCOME_UNKNOWN' AND NOT (
 c.state IN ('CANCELED','CANCEL_NOT_APPLIED','ALREADY_TERMINAL')
 AND c.backend_disposition='TERMINATED' AND c.resolved_at IS NOT NULL
 AND (c.state<>'CANCELED' OR c.terminal_observed=1)
 AND EXISTS(SELECT 1 FROM runs r
 JOIN agent_operation_boundaries b ON b.run_id=r.id
 JOIN agent_finalization_intents i ON i.run_id=r.id
 JOIN agent_conversations a ON a.id=b.agent_conversation_id
 WHERE r.id=c.run_id AND r.project_id=c.project_id AND r.task_id=c.task_id
 AND r.runtime_epoch=c.runtime_epoch AND r.operation_kind=c.owner_type AND r.operation_id=c.owner_id
 AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')
 AND r.thread_id IS c.thread_id AND r.turn_id IS c.turn_id
 AND a.feature_area_id=r.feature_area_id
 AND i.agent_conversation_id=b.agent_conversation_id AND i.runtime_epoch=r.runtime_epoch
 AND i.feature_area_id=r.feature_area_id AND i.operation_kind=r.operation_kind AND i.operation_id=r.operation_id
 AND i.applied_at IS NOT NULL AND i.quiesced=1 AND b.quiesced=1 AND b.thread_start_pending=0
 AND i.advance_state=b.advance_state AND i.exit_boundary_turn_id IS b.exit_boundary_turn_id
 AND ((b.advance_state='KNOWN_ADVANCE' AND b.exit_boundary_turn_id=c.turn_id AND a.backend_thread_id=r.thread_id)
 OR (b.advance_state='NO_ADVANCE' AND c.turn_id IS NULL))));`

func migrateCancellationTerminalV124(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, cancellationTerminalV124SQL); err != nil {
		return err
	}
	for _, guard := range []struct{ name, old, replacement string }{
		{"task_delivery_dispatch_exact", "c.interrupt_state='OUTCOME_UNKNOWN'", unresolvedCancellationInterruptCSQL},
		{"delivery_operation_dispatch_exact", "x.interrupt_state='OUTCOME_UNKNOWN'", unresolvedCancellationInterruptXSQL},
	} {
		var original string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, guard.name).Scan(&original); err != nil {
			return err
		}
		if strings.Count(original, guard.old) != 1 {
			return fmt.Errorf("missing cancellation guard: %s", guard.name)
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+guard.name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, strings.Replace(original, guard.old, guard.replacement, 1)); err != nil {
			return err
		}
	}
	return nil
}
