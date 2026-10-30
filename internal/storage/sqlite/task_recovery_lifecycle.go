package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

// taskRecoveryRequiredFromRunFailure reports the frozen terminal-Run entries
// into TASK_RECOVERY_REQUIRED: the implementation scope conflict, the
// verification worktree mutation, and the unknown cancellation outcome. The
// pairing is operation- and purpose-scoped; ordinary failures keep the
// historical retry semantics.
func taskRecoveryRequiredFromRunFailure(r run.Run) bool {
	if r.OperationKind != run.OperationExecution || r.TaskID == "" || r.Failure == nil {
		return false
	}
	switch r.Status {
	case run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
	default:
		return false
	}
	switch r.Failure.Code {
	case "EXECUTION_CANCEL_OUTCOME_UNKNOWN":
		return r.Purpose == run.PurposeImplementation || r.Purpose == run.PurposeVerification
	case "SCOPE_CONFLICT":
		return r.Purpose == run.PurposeImplementation
	case "VERIFICATION_MUTATED_WORKTREE":
		return r.Purpose == run.PurposeVerification
	}
	return false
}

// markTaskRecoveryRequiredTx enters the durable RECOVERY_REQUIRED authority.
// It must run inside the transaction that commits the authorized failure
// evidence producing the fact; DELIVERING stays under the delivery authority,
// an already-marked or terminalized Task is left untouched, and a lost race
// (zero rows) is a no-op, never a repair.
func markTaskRecoveryRequiredTx(ctx context.Context, q deliveryWriter, projectID, taskID, authorizationSource string, now time.Time) error {
	var phase task.Phase
	var status, owningProject string
	err := q.QueryRowContext(ctx, `SELECT phase,status,project_id FROM tasks WHERE id=?`, taskID).Scan(&phase, &status, &owningProject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != string(task.StatusActive) || phase == task.PhaseRecoveryRequired || phase == task.PhaseDelivering || phase == task.PhaseNoRunSuccessorRequired {
		return nil
	}
	result, err := q.ExecContext(ctx, `UPDATE tasks SET phase='RECOVERY_REQUIRED',updated_at=? WHERE id=? AND status='ACTIVE' AND phase=?`, formatTime(now), taskID, string(phase))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return nil
	}
	if projectID == "" {
		projectID = owningProject
	}
	payload, _ := event.Payload(map[string]any{"from": task.StatusActive, "to": task.StatusActive, "phase_from": phase, "phase_to": task.PhaseRecoveryRequired, "authorization_source": authorizationSource})
	return appendEvent(ctx, q, event.Event{Type: event.TaskStatusChanged, ProjectID: projectID, TaskID: taskID, EntityType: "task", EntityID: taskID, Payload: payload, CreatedAt: now})
}

// taskRecoveryUncertainSQL is the live counterpart of the v88 at-rest
// normalization predicate. The in-flight arm requires an unresolved agent
// operation bound to the Task's current Execution (a Run still PREPARING,
// RUNNING, WAITING_APPROVAL or COLLECTING, a Run/Execution canonicalized to
// the boot-interruption code, or a live EXECUTION claim); an Execution that
// terminalized with an ordinary failure and rests awaiting acceptance is a
// durable fact, never uncertainty, and stays out of recovery. Cancellations
// left unresolved, recovery records still awaiting resolution, and delivery
// receipts whose Git effect never completed are the remaining arms. Recovery
// records are fenced to the Task's current owner (the exact current Execution
// or Review) so a record superseded by a formal continuation can never bounce
// the advanced Task back into recovery. Exact settled no-Run cancellation is
// excluded only from cancellation uncertainty; it cannot hide a new Review's
// independent recovery evidence while its predecessor pointer is retained.
// These exact in-flight Runs remain owned by the App Server bootstrap queue.
// Deferral applies only to their run/claim uncertainty, never to an independent
// cancellation, formal recovery record, or delivery failure on the same Task.
const taskAppServerContinuationSQL = `(` + appServerCandidateSQL + ` AND r.task_id=t.id AND r.operation_kind='EXECUTION' AND r.operation_id=t.current_execution_id AND r.execution_proposal_id=t.current_execution_id)`

// The two JSON parameters exclude only independently validated completed
// recovery cancellation receipts and their exact recovery records.
var taskRecoveryUncertainSQL = `((t.phase IN ('IMPLEMENTING','VERIFYING') AND (EXISTS(SELECT 1 FROM runs r WHERE r.operation_kind='EXECUTION' AND r.operation_id=t.current_execution_id AND (r.status IN ('PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING') OR (r.status='INFRASTRUCTURE_FAILED' AND r.failure_code='bridge_restart_interrupted')) AND NOT ` + taskAppServerContinuationSQL + `) OR EXISTS(SELECT 1 FROM execution_proposals p WHERE p.id=t.current_execution_id AND p.outcome='FAILED' AND p.failure_code='bridge_restart_interrupted') OR EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.operation_kind='EXECUTION' AND c.operation_id=t.current_execution_id AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.id=c.run_id AND ` + taskAppServerContinuationSQL + `))))
 OR EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.task_id=t.id AND NOT EXISTS(SELECT 1 FROM json_each(?) settled WHERE json_extract(settled.value,'$.projectId')=c.project_id AND json_extract(settled.value,'$.requestId')=c.request_id) AND (c.owner_type='TASK' AND c.owner_id=t.id OR c.owner_type='EXECUTION' AND c.owner_id=t.current_execution_id) AND (c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') OR ` + unresolvedCancellationInterruptCSQL + ` OR c.backend_disposition='UNKNOWN_ORPHANED' OR (c.state='CANCELED' AND (c.terminal_observed<>1 OR c.resolved_at IS NULL) AND NOT (c.owner_type='EXECUTION' AND c.owner_id=t.current_execution_id AND (` + noRunSuccessorEligibleSQL + ` OR ` + noRunRevisionPredecessorSQL + `)))))
 OR EXISTS(SELECT 1 FROM recovery_status_records s JOIN runs sr ON sr.id=s.source_run_id WHERE sr.task_id=t.id AND s.state<>'RESOLVED' AND NOT EXISTS(SELECT 1 FROM json_each(?) settled WHERE json_extract(settled.value,'$.recoveryId')=s.id) AND ((sr.operation_kind='EXECUTION' AND sr.execution_proposal_id=t.current_execution_id) OR (sr.operation_kind='REVIEW' AND sr.review_id=t.current_review_id)))
 OR EXISTS(SELECT 1 FROM task_commit_receipts q WHERE q.task_id=t.id AND (COALESCE(q.last_error,'')<>'' OR EXISTS(SELECT 1 FROM task_commit_members m WHERE m.task_id=q.task_id AND m.state='UNKNOWN'))))`

// RecoverTaskLifecycles reconciles every non-terminal Development Task whose
// durable lifecycle shows an unresolved recovery fact after a process
// boundary. It runs only at boot (under the exclusive new writer, after active
// Runs and execution plans have been interrupted), which keeps lost
// start/readback responses and pending runtime requests from leaving a Task
// in an in-flight phase. Exact App Server candidates defer only their run/claim
// uncertainty to bootstrap, which atomically marks recovery if reattachment fails.
// Completed recovery cancellations are revalidated before deciding whether to
// enter recovery or close an existing recovery phase. DELIVERING Tasks remain
// under their exact receipt and RetryTaskCommit observation authority.
func (s *RecoveryStore) RecoverTaskLifecycles(ctx context.Context, now time.Time) error {
	if err := s.ReconcileCanceledNoRunSuccessors(ctx); err != nil {
		return err
	}
	rows, err := s.db.sql.QueryContext(ctx, `SELECT t.id,t.project_id FROM tasks t WHERE t.task_type='DEVELOPMENT' AND t.legacy_read_only=0 AND t.status='ACTIVE' AND t.phase NOT IN ('DELIVERING','NO_RUN_SUCCESSOR_REQUIRED') AND (t.phase='RECOVERY_REQUIRED' OR `+taskRecoveryUncertainSQL+`)`, `[]`, `[]`)
	if err != nil {
		return err
	}
	type stuckTask struct{ id, projectID string }
	var stuck []stuckTask
	for rows.Next() {
		var v stuckTask
		if err = rows.Scan(&v.id, &v.projectID); err != nil {
			rows.Close()
			return err
		}
		stuck = append(stuck, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, v := range stuck {
		tx, e := s.db.beginWrite(ctx)
		if e != nil {
			return e
		}
		settled, e := settledRecoveryCancellationsTx(ctx, tx, v.id)
		if e != nil {
			tx.Rollback()
			return e
		}
		raw, e := json.Marshal(settled)
		if e != nil {
			tx.Rollback()
			return e
		}
		var uncertain bool
		e = tx.QueryRowContext(ctx, `SELECT `+taskRecoveryUncertainSQL+` FROM tasks t WHERE t.id=?`, string(raw), string(raw), v.id).Scan(&uncertain)
		if e == nil {
			if uncertain {
				e = markTaskRecoveryRequiredTx(ctx, tx, v.projectID, v.id, "TASK_LIFECYCLE_RECONCILE", now)
			} else {
				e = reconcileCanceledRecoveryTaskTx(ctx, tx, v.id, now)
			}
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	return nil
}

// completeRecoveredTaskPhaseTx is the formal Resolve exit for a recovery whose
// source Task already sits in the durable RECOVERY_REQUIRED authority. The
// frozen matrix targets mirror the ordinary terminal transitions: an
// Implementation source lands on Verification-Ready (user-waived Verification
// on Acceptance-Pending), a Verification source lands on Acceptance-Pending,
// and a Review source re-enters Reviewing so the same transaction's approval
// materialization publishes the recovered decision. Tasks in any other phase
// keep the unchanged Run-completion reducer.
func completeRecoveredTaskPhaseTx(ctx context.Context, tx *sql.Tx, source run.Run) error {
	var phase task.Phase
	var current, currentReview, status string
	err := tx.QueryRowContext(ctx, `SELECT phase,COALESCE(current_execution_id,''),COALESCE(current_review_id,''),status FROM tasks WHERE id=?`, source.TaskID).Scan(&phase, &current, &currentReview, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return completeTaskRunPhaseTx(ctx, tx, source)
	}
	if err != nil {
		return err
	}
	if phase != task.PhaseRecoveryRequired || status != string(task.StatusActive) {
		return completeTaskRunPhaseTx(ctx, tx, source)
	}
	switch source.Purpose {
	case run.PurposeImplementation, run.PurposeVerification:
		if source.OperationKind != run.OperationExecution || current == "" || current != source.ExecutionProposalID {
			return nil
		}
		next := task.PhaseAcceptancePending
		if source.Purpose == run.PurposeImplementation {
			var encoded string
			if e := tx.QueryRowContext(ctx, `SELECT verification_spec_json FROM execution_proposals WHERE id=? AND task_id=?`, current, source.TaskID).Scan(&encoded); e != nil {
				return e
			}
			spec, e := decodeVerificationSpec(encoded)
			if e != nil {
				return e
			}
			next = task.PhaseVerificationReady
			if spec.Mode == task.VerificationModeNone && spec.Waiver != nil && spec.Waiver.Source == task.VerificationWaiverUser && spec.Waiver.Reason != "" {
				next = task.PhaseAcceptancePending
			}
		}
		return advanceTaskPhaseTx(ctx, tx, source.TaskID, task.PhaseRecoveryRequired, next, "current_execution_id", current, source.UpdatedAt)
	case run.PurposeScopeReview:
		if currentReview == "" || source.ReviewID == "" || currentReview != source.ReviewID {
			return nil
		}
		return advanceTaskPhaseTx(ctx, tx, source.TaskID, task.PhaseRecoveryRequired, task.PhaseReviewing, "current_review_id", currentReview, source.UpdatedAt)
	}
	return nil
}
