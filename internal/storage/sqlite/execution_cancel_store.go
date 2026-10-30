package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

type ExecutionCancellationStore struct{ db *DB }

func NewExecutionCancellationStore(db *DB) *ExecutionCancellationStore {
	return &ExecutionCancellationStore{db: db}
}

// Begin serializes request replay and exact-target authorization with terminal
// commits and claim transfer. No backend side effect precedes this commit.
func (s *ExecutionCancellationStore) Begin(ctx context.Context, in executioncancel.Input, live bool) (executioncancel.Receipt, bool, error) {
	in = in.Normalized()
	empty := executioncancel.Receipt{}
	if err := in.Validate(); err != nil {
		return empty, false, err
	}
	if in.OwnerType == "INSPECTION" {
		return s.beginInspectionCancellation(ctx, in, live)
	}
	if receipt, completed, err := s.completedCancellationReplay(ctx, in); err != nil || completed {
		return receipt, false, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	if err = cancellationActorTx(ctx, tx, in); err != nil {
		return empty, false, err
	}
	var kind, owner, runID, reason, storedPayload, storedEpoch, storedResult, receiptSession, receiptOperator string
	err = tx.QueryRowContext(ctx, `SELECT owner_type,owner_id,COALESCE(run_id,''),reason,COALESCE(request_payload_json,''),COALESCE(runtime_epoch,''),COALESCE(result_json,''),COALESCE(requested_by_ai_session_id,''),COALESCE(requested_by_operator_id,'') FROM execution_cancellations WHERE project_id=? AND request_id=?`, in.ProjectID, in.RequestID).Scan(&kind, &owner, &runID, &reason, &storedPayload, &storedEpoch, &storedResult, &receiptSession, &receiptOperator)
	if err == nil {
		if receiptOperator != in.OperatorPrincipalID || (in.OperatorPrincipalID == "" && receiptSession != in.AISessionID) || kind != in.OwnerType || owner != in.OwnerID || runID != in.RunID || reason != in.Reason {
			return empty, false, executioncancel.ErrRequestConflict
		}
		if (storedPayload != "" && !cancellationPayloadMatches(storedPayload, in)) || (storedPayload == "" && in.ExpectedUpperVersion != nil) {
			return empty, false, executioncancel.ErrRequestConflict
		}
		if storedResult != "" {
			var value executioncancel.Receipt
			e := json.Unmarshal([]byte(storedResult), &value)
			return value, false, e
		}
		value, e := scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
		if e == nil && (value.State == "CANCEL_REQUESTED" || value.State == "RECOVERY_REQUIRED") {
			upper, readErr := upperReviewExecutorTx(ctx, tx, in)
			if readErr != nil {
				return empty, false, readErr
			}
			if upper {
				value, e = cancelUpperReviewTx(ctx, tx, in, s.db.epoch, true, storedEpoch)
				if e == nil {
					e = tx.Commit()
				}
				return value, false, e
			}
		}
		if e == nil && !live && in.RunID != "" && (value.State == "CANCEL_REQUESTED" || value.State == "RECOVERY_REQUIRED") {
			var resumable bool
			if e = tx.QueryRowContext(ctx, `SELECT status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED') OR runtime_epoch IS NOT ? FROM runs WHERE id=?`, s.db.epoch, in.RunID).Scan(&resumable); e != nil {
				return empty, false, e
			}
			if resumable {
				if e = orphanCancellationTx(ctx, tx, in.RunID, s.db.epoch, time.Now().UTC()); e != nil {
					return empty, false, e
				}
				value, e = scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
				if e != nil {
					return empty, false, e
				}
				e = tx.Commit()
			}
		}
		return value, false, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, false, err
	}
	if value, handled, e := abandonUpperPurposeTaskPreparationTx(ctx, tx, in, s.db.epoch); handled || e != nil {
		if e == nil {
			e = tx.Commit()
		}
		return value, false, e
	}
	upper, err := upperReviewExecutorTx(ctx, tx, in)
	if err != nil {
		return empty, false, err
	}
	if upper {
		value, e := cancelUpperReviewTx(ctx, tx, in, s.db.epoch, false, "")
		if e == nil {
			e = tx.Commit()
		}
		return value, false, e
	}
	if in.ExpectedUpperVersion != nil {
		return empty, false, executioncancel.ErrInvalidArgument
	}
	if err = guardTaskDeliveryCancellationTx(ctx, tx, in); err != nil {
		return empty, false, err
	}
	target, err := cancellationOwnerTx(ctx, tx, in)
	if err != nil {
		return empty, false, err
	}
	if target.project != in.ProjectID {
		return empty, false, executioncancel.ErrProjectMismatch
	}
	if target.runID != in.RunID {
		if in.RunID != "" {
			var foreignTask, foreignOwner, foreignKind string
			e := tx.QueryRowContext(ctx, `SELECT COALESCE(task_id,''),COALESCE(operation_id,''),COALESCE(operation_kind,'') FROM runs WHERE id=?`, in.RunID).Scan(&foreignTask, &foreignOwner, &foreignKind)
			if e == nil && (foreignTask != target.taskID || foreignOwner != in.OwnerID || foreignKind != in.OwnerType) {
				return empty, false, executioncancel.ErrRunExecutionMismatch
			}
		}
		if target.runID == "" && in.OwnerType == "EXECUTION" {
			return empty, false, executioncancel.ErrExecutionNotRunning
		}
		return empty, false, executioncancel.ErrRunNotActive
	}
	priorPending, err := pendingCancellationOwnerTx(ctx, tx, in)
	if err != nil {
		return empty, false, err
	}
	if priorPending {
		return empty, false, executioncancel.ErrRequestConflict
	}
	now := time.Now().UTC()
	state, disposition, epoch := "CANCEL_REQUESTED", "ACTIVE", s.db.epoch
	var resolved any
	var r run.Run
	if in.RunID == "" {
		backend, err := noRunCancellationBackendTx(ctx, tx, in)
		if err != nil {
			return empty, false, err
		}
		if backend {
			return empty, false, executioncancel.ErrClaimMismatch
		}
		state, disposition, resolved = "CANCELED", "NO_BACKEND", formatTime(now)
		if target.terminal {
			state = "ALREADY_TERMINAL"
		}
	} else {
		r, err = scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, in.RunID))
		if err != nil {
			return empty, false, executioncancel.ErrRunNotActive
		}
		if err = validateRunAgentExecutionIdentityTx(ctx, tx, r); err != nil {
			return empty, false, err
		}
		if r.TaskID != target.taskID || r.OperationID != in.OwnerID || string(r.OperationKind) != in.OwnerType {
			return empty, false, executioncancel.ErrRunExecutionMismatch
		}
		epoch = r.RuntimeEpoch
		var orphaned bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_orphan_dispositions WHERE run_id=?)`, r.ID).Scan(&orphaned); err != nil {
			return empty, false, err
		}
		if orphaned {
			disposition = "UNKNOWN_ORPHANED"
		}
		if live && epoch != s.db.epoch {
			return empty, false, executioncancel.ErrRecoveryRequired
		}
		if live {
			var unsafe bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_finalization_intents WHERE run_id=? AND (advance_state='UNKNOWN' OR quiesced=0))`, r.ID).Scan(&unsafe); err != nil {
				return empty, false, err
			}
			if unsafe {
				return empty, false, executioncancel.ErrRecoveryRequired
			}
		}
		var claimed bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE run_id=? AND operation_kind=? AND operation_id=? AND runtime_epoch IS ? AND feature_area_id=?)`, r.ID, in.OwnerType, in.OwnerID, nullString(epoch), r.FeatureAreaID).Scan(&claimed); err != nil {
			return empty, false, err
		}
		if terminalAgentRun(r.Status) && !claimed {
			state, resolved = "ALREADY_TERMINAL", formatTime(now)
			if disposition == "ACTIVE" {
				var known bool
				if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_operation_boundaries WHERE run_id=? AND quiesced=1 AND advance_state<>'UNKNOWN')`, r.ID).Scan(&known); err != nil {
					return empty, false, err
				}
				if known {
					disposition = "TERMINATED"
				}
			}
		} else if !claimed {
			return empty, false, executioncancel.ErrClaimMismatch
		}
		if !terminalAgentRun(r.Status) && !live && epoch == s.db.epoch {
			return empty, false, executioncancel.ErrRunNotActive
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_cancellations(project_id,request_id,execution_id,run_id,requested_by_ai_session_id,requested_by_operator_id,reason,requested_at,runtime_epoch,state,interrupt_state,resolved_at,owner_type,owner_id,task_id,backend_disposition) VALUES(?,?,?,?,?,?,?,?,?,?,'NOT_SENT',?,?,?,?,?)`, in.ProjectID, in.RequestID, nullString(in.ExecutionID), nullString(in.RunID), nullString(in.AISessionID), nullString(in.OperatorPrincipalID), in.Reason, formatTime(now), epoch, state, resolved, in.OwnerType, in.OwnerID, target.taskID, disposition)
	if err != nil {
		return empty, false, executioncancel.ErrRequestConflict
	}
	if state == "CANCELED" {
		if err = abandonTaskDeliveryTx(ctx, tx, in, now); err != nil {
			return empty, false, err
		}
		if err = terminalizeCancellationOwnerTx(ctx, tx, in.OwnerType, in.OwnerID, target.taskID, now); err != nil {
			return empty, false, err
		}
	}
	if state == "CANCEL_REQUESTED" && (!live || terminalAgentRun(r.Status)) {
		if err = orphanCancellationTx(ctx, tx, r.ID, s.db.epoch, now); err != nil {
			return empty, false, err
		}
		live = false
	}
	value, err := scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	if err != nil {
		return empty, false, err
	}
	if err = tx.Commit(); err != nil {
		return empty, false, err
	}
	return value, live && value.State == "CANCEL_REQUESTED", nil
}

// ClaimInterrupt is a durable one-shot dispatch marker and exact Thread/Turn
// binding. A crash after this commit never causes an automatic second signal.
func (s *ExecutionCancellationStore) ClaimInterrupt(ctx context.Context, projectID, requestID string) (executioncancel.Target, bool, error) {
	empty := executioncancel.Target{}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	var id, epoch, state, interrupt string
	err = tx.QueryRowContext(ctx, `SELECT run_id,runtime_epoch,state,interrupt_state FROM execution_cancellations WHERE project_id=? AND request_id=?`, projectID, requestID).Scan(&id, &epoch, &state, &interrupt)
	if err != nil {
		return empty, false, err
	}
	if state != "CANCEL_REQUESTED" || interrupt != "NOT_SENT" {
		return empty, false, nil
	}
	r, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, id))
	if err != nil {
		return empty, false, err
	}
	if terminalAgentRun(r.Status) {
		return empty, false, nil
	}
	if epoch != s.db.epoch || r.RuntimeEpoch != epoch {
		return empty, false, executioncancel.ErrRecoveryRequired
	}
	if err = s.db.checkRunTx(ctx, tx, id); err != nil {
		return empty, false, err
	}
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN feature_area_agent_claims c ON c.run_id=r.id AND c.runtime_epoch=r.runtime_epoch AND c.operation_id=r.operation_id AND c.operation_kind=r.operation_kind JOIN agent_operation_boundaries b ON b.run_id=r.id JOIN agent_conversations a ON a.id=b.agent_conversation_id AND a.feature_area_id=c.feature_area_id WHERE r.id=? AND r.status IN ('RUNNING','WAITING_APPROVAL') AND r.turn_id IS NOT NULL AND r.thread_id=a.backend_thread_id AND a.ended_at IS NULL AND b.advance_state='UNKNOWN' AND b.quiesced=0 AND b.thread_start_pending=0 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id))`, id).Scan(&exact)
	if err != nil {
		return empty, false, err
	}
	if !exact && r.OperationKind == run.OperationInspection {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM context_probes p JOIN runs r ON r.id=p.run_id WHERE r.id=? AND p.id=r.operation_id AND r.operation_kind='INSPECTION' AND r.runtime_epoch=p.runtime_epoch AND r.status='RUNNING' AND p.status='RUNNING' AND r.thread_id=p.thread_id AND r.turn_id=p.turn_id AND r.turn_id IS NOT NULL)`, id).Scan(&exact)
		if err != nil {
			return empty, false, err
		}
	}
	if !exact {
		return empty, false, executioncancel.ErrRunNotActive
	}
	_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET interrupt_state='REQUESTED',thread_id=?,turn_id=? WHERE project_id=? AND request_id=? AND interrupt_state='NOT_SENT'`, r.ThreadID, r.TurnID, projectID, requestID)
	if err != nil {
		return empty, false, err
	}
	if err = tx.Commit(); err != nil {
		return empty, false, err
	}
	return executioncancel.Target{RunID: id, RuntimeEpoch: epoch, ThreadID: r.ThreadID, TurnID: r.TurnID}, true, nil
}

func (s *ExecutionCancellationStore) RecordInterrupt(ctx context.Context, projectID, requestID, state string) error {
	if state != "ACKNOWLEDGED" && state != "OUTCOME_UNKNOWN" {
		return executioncancel.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = recordExecutionInterruptTx(ctx, tx, s.db.epoch, projectID, requestID, state); err != nil {
		return err
	}
	return tx.Commit()
}

func recordExecutionInterruptTx(ctx context.Context, tx *sql.Tx, epoch, projectID, requestID, state string) error {
	// Interrupt callbacks can outlive the terminal Run transaction. They may
	// update a pending request, but cannot revise an already settled receipt.
	_, err := tx.ExecContext(ctx, `UPDATE execution_cancellations SET interrupt_state=?,state=CASE WHEN ?='OUTCOME_UNKNOWN' AND state='CANCEL_REQUESTED' THEN 'RECOVERY_REQUIRED' ELSE state END WHERE project_id=? AND request_id=? AND runtime_epoch=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') AND interrupt_state IN ('NOT_SENT','REQUESTED','ACKNOWLEDGED')`, state, state, projectID, requestID, epoch)
	return err
}

func (s *ExecutionCancellationStore) Requested(ctx context.Context, id string) (bool, error) {
	var yes bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE run_id=? AND interrupt_state<>'NOT_SENT' AND runtime_epoch=?)`, id, s.db.epoch).Scan(&yes)
	return yes, err
}

// Explicit recovery resolves the need for intervention, not the historical
// interrupt outcome or boundary. Derive that distinction from its exact durable
// recovery receipt without rewriting cancellation or conversation history.
const selectCancellation = `SELECT c.request_id,c.project_id,COALESCE(c.execution_id,''),COALESCE(c.run_id,''),COALESCE(c.requested_by_ai_session_id,''),c.requested_at,c.reason,
 CASE WHEN k.run_id IS NOT NULL AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') THEN 'OUTCOME_UNKNOWN' ELSE c.state END,
 c.interrupt_state,COALESCE(r.status,''),COALESCE(e.outcome,t.status),COALESCE(c.resolved_at,k.created_at),
 CASE WHEN c.backend_disposition='NO_BACKEND' THEN 'NO_BACKEND' WHEN c.owner_type='INSPECTION' AND c.backend_disposition='TERMINATED' AND c.terminal_observed=1 THEN 'QUIESCED' WHEN b.quiesced=1 AND b.advance_state IN ('KNOWN_ADVANCE','NO_ADVANCE') THEN 'QUIESCED' ELSE 'UNKNOWN' END,
 c.backend_disposition<>'UNKNOWN_ORPHANED' AND k.run_id IS NULL AND (c.state='RECOVERY_REQUIRED' OR (c.state='CANCEL_REQUESTED' AND c.runtime_epoch<>(SELECT epoch_id FROM runtime_authority WHERE singleton=1))),
 c.owner_type,c.owner_id,c.task_id,c.backend_disposition,COALESCE(c.request_payload_json,''),COALESCE(c.result_json,''),COALESCE(c.requested_by_operator_id,'')
 FROM execution_cancellations c LEFT JOIN runs r ON r.id=c.run_id LEFT JOIN execution_proposals e ON e.id=c.execution_id JOIN tasks t ON t.id=c.task_id LEFT JOIN agent_operation_boundaries b ON b.run_id=c.run_id
 LEFT JOIN agent_operation_recoveries k ON k.run_id=c.run_id AND k.operation_kind=c.owner_type AND k.operation_id=c.owner_id AND k.feature_area_id=r.feature_area_id AND k.old_conversation_id=b.agent_conversation_id`

func scanCancellation(scanner interface{ Scan(...any) error }) (executioncancel.Receipt, error) {
	var r executioncancel.Receipt
	var requested, payload, frozen string
	var resolved sql.NullString
	err := scanner.Scan(&r.RequestID, &r.ProjectID, &r.ExecutionID, &r.RunID, &r.RequestedByAISessionID, &requested, &r.Reason, &r.State, &r.BackendInterruptState, &r.RunStatus, &r.ExecutionStatus, &resolved, &r.QuiescenceState, &r.RecoveryRequired, &r.OwnerType, &r.OwnerID, &r.TaskID, &r.BackendDisposition, &payload, &frozen, &r.RequestedByOperatorID)
	if err != nil {
		return r, err
	}
	if frozen != "" {
		err = json.Unmarshal([]byte(frozen), &r)
		return r, err
	}
	if payload != "" {
		var in executioncancel.Input
		if err = json.Unmarshal([]byte(payload), &in); err != nil {
			return r, err
		}
		r.ExpectedUpperVersion = in.ExpectedUpperVersion
	}
	r.RequestedAt, err = parseTime(requested)
	if err != nil {
		return r, err
	}
	if resolved.Valid {
		value, e := parseTime(resolved.String)
		if e != nil {
			return r, e
		}
		r.ResolvedAt = &value
	}
	if r.RecoveryRequired && r.State == "CANCEL_REQUESTED" {
		r.State = "RECOVERY_REQUIRED"
	}
	return r, nil
}
func (s *ExecutionCancellationStore) Get(ctx context.Context, projectID, requestID string) (executioncancel.Receipt, error) {
	return scanCancellation(s.db.sql.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, projectID, requestID))
}
func (s *ExecutionCancellationStore) List(ctx context.Context, projectID, executionID string) ([]executioncancel.Receipt, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.owner_id=? ORDER BY c.requested_at,c.request_id`, projectID, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []executioncancel.Receipt{}
	for rows.Next() {
		r, err := scanCancellation(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, r)
	}
	return values, rows.Err()
}

// requireExecutionCancellationIntentTx closes the generic Run terminal writer.
// It consumes the same durable facts as authoritative cancellation finalization;
// a canceled context or backend interrupt ACK is not cancellation evidence.
func requireExecutionCancellationIntentTx(ctx context.Context, tx *sql.Tx, r run.Run) error {
	var exact bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations c
 JOIN agent_finalization_intents i ON i.run_id=c.run_id
 JOIN feature_area_agent_claims claim ON claim.run_id=c.run_id
 JOIN agent_operation_boundaries b ON b.run_id=c.run_id
 JOIN agent_conversations a ON a.id=i.agent_conversation_id
 WHERE c.run_id=? AND c.owner_type=? AND c.owner_id=? AND c.task_id=? AND c.runtime_epoch=?
 AND c.thread_id=? AND c.turn_id=? AND c.terminal_observed=1 AND c.interrupt_state<>'NOT_SENT'
 AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')
 AND claim.runtime_epoch=c.runtime_epoch AND claim.feature_area_id=? AND claim.operation_kind=c.owner_type AND claim.operation_id=c.owner_id
 AND i.runtime_epoch=c.runtime_epoch AND i.feature_area_id=claim.feature_area_id AND i.operation_kind=claim.operation_kind AND i.operation_id=claim.operation_id
 AND i.advance_state='KNOWN_ADVANCE' AND i.quiesced=1 AND i.exit_boundary_turn_id=c.turn_id
 AND b.agent_conversation_id=i.agent_conversation_id AND b.thread_start_pending=0
 AND a.feature_area_id=claim.feature_area_id AND a.backend_thread_id=c.thread_id AND a.ended_at IS NULL)`, r.ID, r.OperationKind, r.OperationID, r.TaskID, r.RuntimeEpoch, r.ThreadID, r.TurnID, r.FeatureAreaID).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return agentconversation.ErrUnsafe
	}
	return nil
}

// finalizeCanceledExecutionTx runs inside the existing terminal Run transaction.
// It requires both the exact interrupted Turn observation and applied safe exit.
func finalizeCanceledExecutionTx(ctx context.Context, tx *sql.Tx, r run.Run) error {
	if r.Status != run.StatusCanceled {
		return nil
	}
	var observed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations c JOIN agent_finalization_intents i ON i.run_id=c.run_id JOIN agent_operation_boundaries b ON b.run_id=c.run_id WHERE c.run_id=? AND c.terminal_observed=1 AND i.applied_at IS NOT NULL AND b.quiesced=1 AND b.advance_state='KNOWN_ADVANCE' AND i.runtime_epoch=c.runtime_epoch AND i.exit_boundary_turn_id=c.turn_id)`, r.ID).Scan(&observed)
	if err != nil {
		return err
	}
	if !observed {
		return agentconversation.ErrUnsafe
	}
	if err = cancelRuntimeRequestsTx(ctx, tx, r.ID); err != nil {
		return err
	}
	now := time.Now().UTC()
	if r.OperationKind != run.OperationExecution {
		if err = terminalizeCancellationOwnerTx(ctx, tx, string(r.OperationKind), r.OperationID, r.TaskID, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCELED',backend_disposition='TERMINATED',resolved_at=? WHERE run_id=? AND terminal_observed=1`, formatTime(now), r.ID)
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET outcome='CANCELED',failure_code='EXECUTION_CANCELED',failure_message='Exact backend Turn interrupted safely',updated_at=? WHERE id=? AND outcome='RUNNING' AND COALESCE(verification_run_id,run_id)=?`, formatTime(now), r.ExecutionProposalID, r.ID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return agentconversation.ErrConflict
	}
	payload, _ := event.Payload(map[string]any{"outcome": "CANCELED", "run_id": r.ID})
	if err = appendEvent(ctx, tx, event.Event{Type: event.ExecutionCompleted, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID, EntityType: "execution_proposal", EntityID: r.ExecutionProposalID, Payload: payload, CreatedAt: now}); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET backend_disposition='TERMINATED' WHERE run_id=? AND terminal_observed=1`, r.ID)
	return err
}

// Reconcile only observed authoritative facts; it never replays a backend call.
func settleExecutionCancellationsTx(ctx context.Context, tx *sql.Tx, runID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE execution_cancellations SET state=CASE
 WHEN terminal_observed=1 AND EXISTS(SELECT 1 FROM runs r JOIN agent_operation_boundaries b ON b.run_id=r.id JOIN execution_proposals e ON e.id=r.execution_proposal_id WHERE r.id=execution_cancellations.run_id AND r.status='CANCELED' AND e.outcome='CANCELED' AND b.quiesced=1 AND b.advance_state='KNOWN_ADVANCE') THEN 'CANCELED'
 WHEN EXISTS(SELECT 1 FROM runs r JOIN agent_operation_boundaries b ON b.run_id=r.id WHERE r.id=execution_cancellations.run_id AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED') AND b.quiesced=1 AND b.advance_state IN ('NO_ADVANCE','KNOWN_ADVANCE')) THEN 'CANCEL_NOT_APPLIED'
 ELSE 'RECOVERY_REQUIRED' END,
 resolved_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE run_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') AND EXISTS(SELECT 1 FROM runs r WHERE r.id=execution_cancellations.run_id AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED'))`, runID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET backend_disposition='TERMINATED' WHERE run_id=? AND state IN ('CANCELED','CANCEL_NOT_APPLIED') AND EXISTS(SELECT 1 FROM agent_operation_boundaries WHERE run_id=? AND quiesced=1 AND advance_state<>'UNKNOWN')`, runID, runID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET resolved_at=NULL WHERE run_id=? AND state='RECOVERY_REQUIRED'`, runID)
	return err
}
