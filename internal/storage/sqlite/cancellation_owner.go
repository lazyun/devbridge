package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

type cancellationOwner struct {
	project, taskID, runID string
	terminal               bool
}

// PreWorkTaskCancellationEligible is the read-only counterpart of Begin's
// no-Run TASK branch. It uses the same owner and backend guards; Begin still
// revalidates them in its write transaction before changing any state.
func (s *ExecutionCancellationStore) PreWorkTaskCancellationEligible(ctx context.Context, projectID, taskID string) (bool, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	in := executioncancel.Input{ProjectID: projectID, OwnerType: "TASK", OwnerID: taskID}
	if err := guardTaskDeliveryCancellationTx(ctx, tx, in); err != nil {
		if errors.Is(err, executioncancel.ErrRecoveryRequired) {
			return false, nil
		}
		return false, err
	}
	owner, err := cancellationOwnerTx(ctx, tx, in)
	if errors.Is(err, executioncancel.ErrRunNotActive) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if owner.project != projectID {
		return false, executioncancel.ErrProjectMismatch
	}
	if owner.terminal || owner.runID != "" {
		return false, nil
	}
	pending, err := pendingCancellationOwnerTx(ctx, tx, in)
	if err != nil || pending {
		return false, err
	}
	backend, err := noRunCancellationBackendTx(ctx, tx, in)
	return !backend && err == nil, err
}

func pendingCancellationOwnerTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input) (v bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE owner_type=? AND owner_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))`, in.OwnerType, in.OwnerID).Scan(&v)
	return
}

func noRunCancellationBackendTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input) (v bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE operation_kind=? AND operation_id=?) OR EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE operation_kind=? AND operation_id=?)`, in.OwnerType, in.OwnerID, in.OwnerType, in.OwnerID).Scan(&v)
	return
}

func cancellationOwnerTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input) (v cancellationOwner, err error) {
	switch in.OwnerType {
	case "EXECUTION":
		err = tx.QueryRowContext(ctx, `SELECT project_id,task_id,COALESCE(verification_run_id,run_id,''),outcome IN ('SUCCEEDED','FAILED','CANCELED') OR status='REJECTED' FROM execution_proposals WHERE id=?`, in.OwnerID).Scan(&v.project, &v.taskID, &v.runID, &v.terminal)
	case "REVIEW":
		err = tx.QueryRowContext(ctx, `SELECT v.project_id,v.task_id,COALESCE(v.linked_run_id,''),r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED') FROM reviews v LEFT JOIN runs r ON r.id=v.linked_run_id WHERE v.id=?`, in.OwnerID).Scan(&v.project, &v.taskID, &v.runID, &v.terminal)
	case "TASK":
		// Admission owns work before a Run exists. Cancel that Execution first;
		// only its settled outcome permits the separate Task cancellation.
		err = tx.QueryRowContext(ctx, `SELECT project_id,id,'',status IN ('SUCCEEDED','FAILED','CANCELED') FROM tasks WHERE id=? AND task_type='DEVELOPMENT' AND legacy_read_only=0 AND NOT EXISTS(SELECT 1 FROM runs WHERE task_id=tasks.id AND status NOT IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')) AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c JOIN runs r ON r.id=c.run_id WHERE r.task_id=tasks.id) AND NOT EXISTS(SELECT 1 FROM execution_proposals WHERE task_id=tasks.id AND outcome IN ('PENDING','RUNNING') AND status IN ('PENDING_CONFIRMATION','CONFIRMED','ADMITTED','CONSUMED'))`, in.OwnerID).Scan(&v.project, &v.taskID, &v.runID, &v.terminal)
	default:
		err = executioncancel.ErrInvalidArgument
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = executioncancel.ErrRunNotActive
		if in.OwnerType == "EXECUTION" {
			err = executioncancel.ErrExecutionNotRunning
		}
	}
	return
}

func terminalizeCancellationOwnerTx(ctx context.Context, tx *sql.Tx, kind, owner, taskID string, now time.Time) error {
	switch kind {
	case "EXECUTION":
		if _, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET outcome='CANCELED',failure_code='OPERATION_CANCELED',failure_message='Owner cancellation finalized',updated_at=? WHERE id=? AND outcome IN ('PENDING','RUNNING')`, formatTime(now), owner); err != nil {
			return err
		}
	case "REVIEW":
		// Upper LLM review work has no Codex thread to interrupt.  The formal
		// cancellation transaction therefore closes the exact grant together
		// with the Run/claim, fencing a late report without rotating a context.
		var upper bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reviews WHERE id=? AND executor_kind='UPPER_LLM')`, owner).Scan(&upper); err != nil {
			return err
		}
		if upper {
			if _, err := tx.ExecContext(ctx, `UPDATE review_grants SET state='CANCELED' WHERE review_id=? AND state IN ('PREPARING','ACTIVE','AWAITING_REPORT','FINALIZING')`, owner); err != nil {
				return err
			}
		}
		// The same owner cancellation boundary applies to Coding Review. Its
		// immutable Review/Run remains history; only the exact current Task
		// returns to the existing new-Review phase after owner quiescence.
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED',required_review_depth=COALESCE(required_review_depth,(SELECT review_depth FROM reviews WHERE id=?),'DEEP'),updated_at=? WHERE id=? AND current_review_id=? AND status='ACTIVE' AND phase='REVIEWING'`, owner, formatTime(now), taskID, owner); err != nil {
			return err
		}
	}
	if kind != "TASK" {
		return nil
	}
	// A pending definition decision becomes rejected, never confirmed. An
	// already-applied plan remains immutable provenance; owner guards revoke use.
	if _, err := tx.ExecContext(ctx, `UPDATE task_plans SET status='REJECTED',rejected_at=?,updated_at=? WHERE task_id=? AND status='PENDING_CONFIRMATION'`, formatTime(now), formatTime(now), taskID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET status='CANCELED',updated_at=? WHERE id=? AND status<>'CANCELED'`, formatTime(now), taskID)
	return err
}

// FinishInterrupted is called only by the owned supervisor after ExecuteRun
// has returned and joined its local runtime. Local join is NOT a death proof.
// The store consumes the existing exact cancellation intent and immutable
// boundary; it cannot infer a target from Task/latest Run.
func (s *ExecutionCancellationStore) FinishInterrupted(ctx context.Context, id string) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE run_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))`, id).Scan(&pending); err != nil {
		return err
	}
	if !pending {
		return nil
	}
	if err = orphanCancellationTx(ctx, tx, id, s.db.epoch, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func orphanCancellationTx(ctx context.Context, tx *sql.Tx, id, epoch string, now time.Time) error {
	r, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, id))
	if err != nil {
		return err
	}
	if err = validateRunAgentExecutionIdentityTx(ctx, tx, r); err != nil {
		return err
	}
	if handled, e := convergePendingUpperCancellationTx(ctx, tx, r, epoch); handled || e != nil {
		return e
	}
	var request string
	if err = tx.QueryRowContext(ctx, `SELECT request_id FROM execution_cancellations WHERE run_id=? AND owner_type=? AND owner_id=? AND task_id=? AND runtime_epoch IS ? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')`, id, r.OperationKind, r.OperationID, r.TaskID, nullString(r.RuntimeEpoch)).Scan(&request); err != nil {
		return err
	}
	var advance string
	var safe bool
	err = tx.QueryRowContext(ctx, `SELECT advance_state,quiesced=1 AND advance_state<>'UNKNOWN' FROM agent_operation_boundaries WHERE run_id=?`, id).Scan(&advance, &safe)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if safe {
		// An exact known terminal Run can still have a live aggregate owner
		// (for example Execution awaiting finalization). Cancel that owner
		// with the recorded safe boundary; preserve the Run's result history.
		if !terminalAgentRun(r.Status) {
			return agentconversation.ErrUnsafe
		}
		if err = terminalizeCancellationOwnerTx(ctx, tx, string(r.OperationKind), r.OperationID, r.TaskID, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=? AND runtime_epoch IS ? AND operation_kind=? AND operation_id=?`, id, nullString(r.RuntimeEpoch), r.OperationKind, r.OperationID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCELED',backend_disposition='TERMINATED',resolved_at=? WHERE run_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')`, formatTime(now), id)
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Reserved but never dispatched: no thread/start marker means no backend.
		if r.ThreadID != "" || r.TurnID != "" {
			return agentconversation.ErrUnsafe
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET status='CANCELED',finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=?`, formatTime(now), formatTime(now), id); err != nil {
			return err
		}
		if err = terminalizeCancellationOwnerTx(ctx, tx, string(r.OperationKind), r.OperationID, r.TaskID, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=? AND runtime_epoch IS ? AND operation_kind=? AND operation_id=?`, id, nullString(r.RuntimeEpoch), r.OperationKind, r.OperationID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCELED',backend_disposition='NO_BACKEND',resolved_at=? WHERE run_id=?`, formatTime(now), id)
		return err
	}
	if advance != "UNKNOWN" {
		return agentconversation.ErrUnsafe
	}
	if err = orphanOperationTx(ctx, tx, r, epoch, request, "CANCELLATION", now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCELED',backend_disposition='UNKNOWN_ORPHANED',resolved_at=? WHERE run_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')`, formatTime(now), id)
	return err
}

// orphanOperationTx is the one irreversible Bridge-authority revocation seam.
// It preserves the original UNKNOWN boundary, quarantines environment/context,
// terminalizes the owner and writes the permanent fence before claim release.
// No statement here claims physical backend termination.
func orphanOperationTx(ctx context.Context, tx *sql.Tx, r run.Run, epoch, request, source string, now time.Time) error {
	if err := validateRunAgentExecutionIdentityTx(ctx, tx, r); err != nil {
		return err
	}
	var already bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_orphan_dispositions WHERE run_id=?)`, r.ID).Scan(&already); err != nil {
		return err
	}
	if already {
		return nil
	}
	claim, err := scanAgentClaim(tx.QueryRowContext(ctx, `SELECT feature_area_id,operation_kind,operation_id,run_id,created_at,COALESCE(runtime_epoch,'') FROM feature_area_agent_claims WHERE run_id=?`, r.ID))
	if err != nil {
		return err
	}
	if claim.OperationID != r.OperationID || string(claim.Kind) != string(r.OperationKind) || claim.RuntimeEpoch != r.RuntimeEpoch || claim.FeatureAreaID != r.FeatureAreaID {
		return agentconversation.ErrConflict
	}
	var conversationID string
	if err = tx.QueryRowContext(ctx, `SELECT agent_conversation_id FROM agent_operation_boundaries WHERE run_id=? AND advance_state='UNKNOWN'`, r.ID).Scan(&conversationID); err != nil {
		return err
	}
	a, err := scanAgentConversation(tx.QueryRowContext(ctx, selectAgentConversation+` WHERE id=?`, conversationID))
	if err != nil {
		return err
	}
	if a.EndedAt != nil || a.FeatureAreaID != r.FeatureAreaID {
		return agentconversation.ErrConflict
	}
	// Existing final results are evidence. An interrupted nonterminal Run can be
	// canceled; a terminal failure is retained while the owner is abandoned.
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET status='CANCELED',failure_class='CANCELED',failure_code='UNKNOWN_ORPHANED',failure_message='Bridge authority revoked; physical backend state unknown',finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND status NOT IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')`, formatTime(now), formatTime(now), r.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE approvals SET status='EXPIRED' WHERE run_id=? AND status IN ('PENDING','DECIDED')`, r.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE run_token_usage SET status='UNAVAILABLE',updated_at=? WHERE run_id=? AND status='PENDING'`, formatTime(now), r.ID); err != nil {
		return err
	}
	if err = terminalizeCancellationOwnerTx(ctx, tx, string(r.OperationKind), r.OperationID, r.TaskID, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workspaces SET runtime_quarantined=1,status='CONTINUITY_UNAVAILABLE',updated_at=? WHERE id=?`, formatTime(now), r.WorkspaceID); err != nil {
		return err
	}
	var allowed bool
	if err = tx.QueryRowContext(ctx, `SELECT legacy_read_only=0 AND status IN ('DISCOVERING','ACTIVE') FROM feature_areas WHERE id=?`, r.FeatureAreaID).Scan(&allowed); err != nil {
		return err
	}
	if allowed {
		_, err = rotateAgentTx(ctx, tx, a, agentconversation.UnsafeContextBoundary, now)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET ended_at=?,continuation_safe=0,rotation_reason='UNSAFE_CONTEXT_BOUNDARY' WHERE id=?`, formatTime(now), a.ID)
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_orphan_dispositions(run_id,task_id,feature_area_id,owner_type,owner_id,conversation_id,original_runtime_epoch,observed_runtime_epoch,thread_id,turn_id,workspace_id,request_id,source,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.TaskID, r.FeatureAreaID, r.OperationKind, r.OperationID, a.ID, nullString(r.RuntimeEpoch), epoch, nullString(r.ThreadID), nullString(r.TurnID), nullString(r.WorkspaceID), request, source, formatTime(now))
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=? AND operation_kind=? AND operation_id=? AND runtime_epoch IS ?`, r.ID, r.OperationKind, r.OperationID, nullString(r.RuntimeEpoch))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return agentconversation.ErrConflict
	}
	return nil
}
