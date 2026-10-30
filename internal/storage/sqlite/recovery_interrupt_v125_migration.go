package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The frozen cancellation receipt is produced only after independent child
// termination, owner closure and claim release. It is a separate authority from
// an interrupt ACK, and must match the exact durable recovery resolution.
const recoveryInterruptTerminalV125SQL = `EXISTS(
 SELECT 1 FROM recovery_status_records x
 JOIN recovery_child_admissions a ON a.recovery_id=x.id AND a.source_run_id=x.source_run_id
 JOIN runs source ON source.id=x.source_run_id
 JOIN runs child ON child.id=x.recovery_run_id
 WHERE c.run_id=child.id AND c.project_id=source.project_id AND c.task_id=source.task_id
 AND c.owner_type=source.operation_kind AND c.owner_id=source.operation_id
 AND c.state='CANCELED' AND c.backend_disposition='TERMINATED' AND c.terminal_observed=1 AND c.resolved_at IS NOT NULL
 AND source.status='CANCELED' AND child.status='CANCELED' AND child.finished_at IS NOT NULL
 AND child.purpose='RECOVERY_STATUS' AND child.operation_kind=source.operation_kind AND child.operation_id=source.operation_id
 AND child.project_id=source.project_id AND child.task_id=source.task_id
 AND child.thread_id=c.thread_id AND child.turn_id=c.turn_id AND child.thread_id<>source.thread_id
 AND a.source_thread_id=source.thread_id AND a.source_turn_id=source.turn_id AND a.source_runtime_workset_hash=source.runtime_workset_hash
 AND x.state='FAILED' AND json_extract(x.record_json,'$.failureReason')='RECOVERY_CANCELED'
 AND json_extract(x.record_json,'$.id')=x.id AND json_extract(x.record_json,'$.recoveryRunId')=child.id
 AND json_extract(x.record_json,'$.recoveryTurnId')=child.turn_id AND json_extract(x.record_json,'$.completedAt') IS NOT NULL
 AND json_valid(c.result_json) AND json(c.result_json)=json_extract(x.record_json,'$.resolution')
 AND json_extract(c.result_json,'$.projectId')=c.project_id AND json_extract(c.result_json,'$.requestId')=c.request_id
 AND json_extract(c.result_json,'$.runId')=child.id AND json_extract(c.result_json,'$.taskId')=c.task_id
 AND json_extract(c.result_json,'$.ownerType')=c.owner_type AND json_extract(c.result_json,'$.ownerId')=c.owner_id
 AND json_extract(c.result_json,'$.state')='CANCELED' AND json_extract(c.result_json,'$.quiescenceState')='QUIESCED'
 AND json_extract(c.result_json,'$.backendDisposition')='TERMINATED'
 AND json_extract(c.result_json,'$.backendInterruptState')=c.interrupt_state
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims claim WHERE claim.run_id=source.id)
 AND NOT EXISTS(SELECT 1 FROM project_inspection_agent_claims claim WHERE claim.run_id=source.id))`

func migrateRecoveryInterruptV125(ctx context.Context, tx *sql.Tx) error {
	var original string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='view' AND name='unresolved_cancellation_interrupts'`).Scan(&original); err != nil {
		return err
	}
	const guard = "WHERE c.interrupt_state='OUTCOME_UNKNOWN' AND NOT ("
	if strings.Count(original, guard) != 1 {
		return fmt.Errorf("missing unresolved interrupt guard")
	}
	updated := strings.Replace(original, guard, "WHERE c.interrupt_state='OUTCOME_UNKNOWN' AND NOT "+recoveryInterruptTerminalV125SQL+" AND NOT (", 1)
	if _, err := tx.ExecContext(ctx, `DROP VIEW unresolved_cancellation_interrupts`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, updated)
	return err
}
