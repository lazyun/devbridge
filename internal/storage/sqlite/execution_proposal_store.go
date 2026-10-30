package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/taskplan"
)

type ExecutionProposalStore struct{ db *DB }

func NewExecutionProposalStore(db *DB) *ExecutionProposalStore {
	return &ExecutionProposalStore{db: db}
}

func (s *ExecutionProposalStore) ReverificationAuthorized(ctx context.Context, id string) (bool, error) {
	var value bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM decision_resolution_receipts WHERE decision_kind='TASK_ACCEPTANCE' AND subject_kind='EXECUTION_PROPOSAL' AND option='REQUEST_REVERIFICATION' AND result_kind='EXECUTION_PROPOSAL' AND result_id=?)`, id).Scan(&value)
	return value, err
}

func (s *ExecutionProposalStore) Create(ctx context.Context, value executionproposal.Proposal) error {
	if value.Status != executionproposal.StatusPendingConfirmation || value.RunID != "" || value.ConfirmedAt != nil || value.RejectedAt != nil || value.ConsumedAt != nil {
		return executionproposal.ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if value.WorkSpec.Goal == "" {
		source, sourceErr := scanTaskPlan(tx.QueryRowContext(ctx, selectTaskPlan+` WHERE id=?`, value.TaskPlanID))
		if sourceErr != nil {
			return executionProposalConflict(sourceErr)
		}
		value.WorkSpec = source.WorkSpec
		value.VerificationSpec = source.VerificationSpec
	}
	if value.WorkSpec.ImplementationMode == task.ImplementationCodingAgent && value.BackendSelection == nil || value.WorkSpec.ImplementationMode == task.ImplementationDirectFile && value.BackendSelection != nil {
		return executionproposal.ErrConflict
	}
	workSpec, err := encodeWorkSpec(value.WorkSpec)
	if err != nil {
		return err
	}
	verificationSpec, err := encodeVerificationSpec(value.VerificationSpec)
	if err != nil {
		return err
	}
	if err := bindExecutionDefinitionTx(ctx, tx, &value); err != nil {
		return err
	}
	runtimeConfig, err := encodeExecutionRuntimeConfig(value.RuntimeConfig)
	if err != nil {
		return err
	}
	backendSelection, err := encodeBackendSelection(value.BackendSelection)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_proposals(id,ai_session_id,project_id,task_id,task_plan_id,status,run_id,created_at,updated_at,confirmed_at,rejected_at,consumed_at,work_spec_json,verification_spec_json,outcome,task_definition_revision_id,runtime_config_json,backend_selection_json,primary_member_id,approved_workset_hash,runtime_adapter_identity,runtime_protocol_identity) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		value.ID, value.AISessionID, value.ProjectID, value.TaskID, value.TaskPlanID, string(value.Status), nil, formatTime(value.CreatedAt), formatTime(value.UpdatedAt), nil, nil, nil, workSpec, verificationSpec, string(executionproposal.OutcomePending), value.TaskDefinitionRevisionID, runtimeConfig, backendSelection, nullString(value.PrimaryMemberID), nullString(value.ApprovedWorksetHash), nullString(value.RuntimeAdapterIdentity), nullString(value.RuntimeProtocolIdentity))
	if err != nil {
		var active int
		if queryErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_proposals WHERE task_plan_id=? AND status IN ('PENDING_CONFIRMATION','CONFIRMED','CONSUMED') AND outcome IN ('PENDING','RUNNING')`, value.TaskPlanID).Scan(&active); queryErr == nil && active != 0 {
			return executionproposal.ErrActiveExecution
		}
		return executionProposalConflict(fmt.Errorf("insert execution proposal: %w", err))
	}
	payload, _ := event.Payload(map[string]any{"ai_session_id": value.AISessionID, "task_plan_id": value.TaskPlanID, "task_id": value.TaskID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ExecutionProposalCreated, ProjectID: value.ProjectID, TaskID: value.TaskID, EntityType: "execution_proposal", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ExecutionProposalStore) Get(ctx context.Context, id string) (executionproposal.Proposal, error) {
	value, err := scanExecutionProposal(s.db.sql.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return executionproposal.Proposal{}, executionproposal.ErrNotFound
	}
	if err != nil {
		return executionproposal.Proposal{}, fmt.Errorf("get execution proposal: %w", err)
	}
	return value, nil
}

// GetByID resolves the minimal taskgoal.Execution view without session
// scoping. Trusted runtime path only (goal progress initialization).
func (s *ExecutionProposalStore) GetByID(ctx context.Context, id string) (taskgoal.Execution, error) {
	value, err := s.Get(ctx, id)
	if err != nil {
		return taskgoal.Execution{}, err
	}
	return taskgoal.Execution{ID: value.ID, TaskID: value.TaskID, ProjectID: value.ProjectID, Status: string(value.Status), TaskDefinitionRevisionID: value.TaskDefinitionRevisionID, RunID: value.RunID, VerificationRunID: value.VerificationRunID, Outcome: string(value.Outcome), RuntimeConfig: value.RuntimeConfig, BackendSelection: value.BackendSelection, PrimaryMemberID: value.PrimaryMemberID, ApprovedWorksetHash: value.ApprovedWorksetHash, RuntimeAdapterIdentity: value.RuntimeAdapterIdentity, RuntimeProtocolIdentity: value.RuntimeProtocolIdentity}, nil
}

func (s *ExecutionProposalStore) ListBySession(ctx context.Context, aiSessionID string) ([]executionproposal.Proposal, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectExecutionProposal+` WHERE ai_session_id=? ORDER BY created_at DESC,id`, aiSessionID)
	if err != nil {
		return nil, fmt.Errorf("list execution proposals: %w", err)
	}
	defer rows.Close()
	values := make([]executionproposal.Proposal, 0)
	for rows.Next() {
		value, err := scanExecutionProposal(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// ListByTask exposes complete immutable Execution history across AI sessions.
func (s *ExecutionProposalStore) ListByTask(ctx context.Context, taskID string) ([]executionproposal.Proposal, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectExecutionProposal+` WHERE task_id=? ORDER BY created_at DESC,id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list Task executions: %w", err)
	}
	defer rows.Close()
	values := make([]executionproposal.Proposal, 0)
	for rows.Next() {
		value, err := scanExecutionProposal(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// LatestByTask resolves the newest Execution for a Task across all sessions:
// Executions are durable Project-owned state, not conversation-private state.
func (s *ExecutionProposalStore) LatestByTask(ctx context.Context, taskID string) (executionproposal.Proposal, error) {
	value, err := scanExecutionProposal(s.db.sql.QueryRowContext(ctx, selectExecutionProposal+` WHERE task_id=? ORDER BY created_at DESC,id LIMIT 1`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return executionproposal.Proposal{}, executionproposal.ErrNotFound
	}
	if err != nil {
		return executionproposal.Proposal{}, fmt.Errorf("get latest execution proposal: %w", err)
	}
	return value, nil
}

func (s *ExecutionProposalStore) LatestBySessionTask(ctx context.Context, aiSessionID, taskID string) (executionproposal.Proposal, error) {
	value, err := scanExecutionProposal(s.db.sql.QueryRowContext(ctx, selectExecutionProposal+` WHERE ai_session_id=? AND task_id=? ORDER BY created_at DESC,id LIMIT 1`, aiSessionID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return executionproposal.Proposal{}, executionproposal.ErrNotFound
	}
	if err != nil {
		return executionproposal.Proposal{}, fmt.Errorf("get latest execution proposal: %w", err)
	}
	return value, nil
}

func (s *ExecutionProposalStore) Confirm(ctx context.Context, id string, now time.Time) (executionproposal.Proposal, error) {
	return s.transition(ctx, id, executionproposal.StatusPendingConfirmation, executionproposal.StatusConfirmed, "confirmed_at", event.ExecutionProposalConfirmed, now)
}

func (s *ExecutionProposalStore) Reject(ctx context.Context, id string, now time.Time) (executionproposal.Proposal, error) {
	return s.transition(ctx, id, executionproposal.StatusPendingConfirmation, executionproposal.StatusRejected, "rejected_at", event.ExecutionProposalRejected, now)
}

func (s *ExecutionProposalStore) transition(ctx context.Context, id string, from, to executionproposal.Status, timestampColumn, eventType string, now time.Time) (executionproposal.Proposal, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	defer tx.Rollback()
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return executionproposal.Proposal{}, executionproposal.ErrNotFound
	}
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	if value.Status != from {
		return executionproposal.Proposal{}, &executionproposal.TransitionError{From: value.Status, To: to}
	}
	if to == executionproposal.StatusConfirmed {
		if value.TaskDefinitionRevisionID == "" {
			return executionproposal.Proposal{}, executionproposal.ErrConflict
		}
		if err := bindExecutionDefinitionTx(ctx, tx, &value); err != nil {
			return executionproposal.Proposal{}, err
		}
		var currentOwner bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=? AND t.project_id=? AND t.legacy_read_only=0 AND f.project_id=t.project_id AND f.legacy_read_only=0 AND f.status='ACTIVE')`, value.TaskID, value.ProjectID).Scan(&currentOwner); err != nil {
			return executionproposal.Proposal{}, err
		}
		if !currentOwner {
			return executionproposal.Proposal{}, executionproposal.ErrConflict
		}
	}
	query := fmt.Sprintf(`UPDATE execution_proposals SET status=?,updated_at=?,%s=? WHERE id=? AND status=? AND updated_at=?`, timestampColumn)
	result, err := tx.ExecContext(ctx, query, string(to), formatTime(now), formatTime(now), id, string(from), formatTime(value.UpdatedAt))
	if err != nil {
		return executionproposal.Proposal{}, executionProposalConflict(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	if affected != 1 {
		return executionproposal.Proposal{}, executionproposal.ErrConflict
	}
	value.Status = to
	value.UpdatedAt = now.UTC()
	if to == executionproposal.StatusConfirmed {
		value.ConfirmedAt = timePointer(now.UTC())
	} else {
		value.RejectedAt = timePointer(now.UTC())
	}
	payload, _ := event.Payload(map[string]any{"from": from, "to": to, "task_plan_id": value.TaskPlanID})
	if err := appendEvent(ctx, tx, event.Event{Type: eventType, ProjectID: value.ProjectID, TaskID: value.TaskID, EntityType: "execution_proposal", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
		return executionproposal.Proposal{}, err
	}
	if err := tx.Commit(); err != nil {
		return executionproposal.Proposal{}, err
	}
	return value, nil
}

// Deprecated: ConsumeAndCreateRun is a legacy synchronous Run creation API.
// Production callers must use the two-step admission pattern: admitExecutionTx
// (Transaction A: admission → ADMITTED, no Run) followed by PrepareAdmittedExecution
// (Transaction B: Run creation → CONSUMED). This function has zero production
// callers and is retained only for test compatibility.
//
// ConsumeAndCreateRun is the authorization boundary: proposal consumption,
// Run attempt allocation, Run persistence, and both ledger events commit in
// one SQLite transaction.
func (s *ExecutionProposalStore) ConsumeAndCreateRun(ctx context.Context, id, runID string, now time.Time) (executionproposal.Proposal, run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	defer tx.Rollback()
	claim, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET updated_at=updated_at WHERE id=? AND status=? AND run_id IS NULL`, id, string(executionproposal.StatusConfirmed))
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, executionProposalConflict(err)
	}
	affected, err := claim.RowsAffected()
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	if affected != 1 {
		current, getErr := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, id))
		if errors.Is(getErr, sql.ErrNoRows) {
			return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrNotFound
		}
		if getErr != nil {
			return executionproposal.Proposal{}, run.Run{}, getErr
		}
		return current, run.Run{}, &executionproposal.TransitionError{From: current.Status, To: executionproposal.StatusConsumed}
	}
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, id))
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	var sourceStatus taskplan.Status
	var sourceTaskID string
	if err := tx.QueryRowContext(ctx, `SELECT status,task_id FROM task_plans WHERE id=? AND project_id=?`, value.TaskPlanID, value.ProjectID).Scan(&sourceStatus, &sourceTaskID); err != nil {
		return executionproposal.Proposal{}, run.Run{}, executionProposalConflict(err)
	}
	if sourceStatus != taskplan.StatusApplied || sourceTaskID != value.TaskID {
		return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrConflict
	}
	runValue, err := createRunAttempt(ctx, s.db, tx, value.TaskID, runID, now.UTC(), runMetadata{Purpose: run.PurposeImplementation, ExecutionProposalID: value.ID})
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET status=?,run_id=?,outcome=?,updated_at=?,consumed_at=? WHERE id=? AND status=? AND run_id IS NULL AND outcome=?`, string(executionproposal.StatusConsumed), runID, string(executionproposal.OutcomeRunning), formatTime(now), formatTime(now), id, string(executionproposal.StatusConfirmed), string(executionproposal.OutcomePending))
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, executionProposalConflict(err)
	}
	affected, err = result.RowsAffected()
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	if affected != 1 {
		return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrConflict
	}
	value.Status = executionproposal.StatusConsumed
	value.RunID = runID
	value.ImplementationRunID = runID
	value.Outcome = executionproposal.OutcomeRunning
	value.UpdatedAt = now.UTC()
	value.ConsumedAt = timePointer(now.UTC())
	payload, _ := event.Payload(map[string]any{"from": executionproposal.StatusConfirmed, "to": executionproposal.StatusConsumed, "task_plan_id": value.TaskPlanID, "task_id": value.TaskID, "run_id": runID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ExecutionProposalConsumed, ProjectID: value.ProjectID, TaskID: value.TaskID, RunID: runID, EntityType: "execution_proposal", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	return value, runValue, nil
}

// ConsumeAndCreateVerificationRun atomically reserves the single Run of a
// REQUEST_REVERIFICATION successor. run_id and verification_run_id identify
// that same VERIFICATION Run; no synthetic Implementation Run is created.
func (s *ExecutionProposalStore) ConsumeAndCreateVerificationRun(ctx context.Context, id, runID string, now time.Time) (executionproposal.Proposal, run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	defer tx.Rollback()
	value, reserved, err := consumeAndCreateVerificationRunTx(ctx, s.db, tx, id, runID, now)
	if err != nil {
		return value, reserved, err
	}
	return value, reserved, tx.Commit()
}

func consumeAndCreateVerificationRunTx(ctx context.Context, db *DB, tx *sql.Tx, id, runID string, now time.Time) (executionproposal.Proposal, run.Run, error) {
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, run.Run{}, executionproposal.ErrNotFound
	}
	if err != nil {
		return value, run.Run{}, err
	}
	if value.Status != executionproposal.StatusConfirmed || value.Outcome != executionproposal.OutcomePending || value.RunID != "" || value.VerificationRunID != "" || value.VerificationSpec.Mode != task.VerificationModeRun {
		return value, run.Run{}, executionproposal.ErrConflict
	}
	var authorized bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM decision_resolution_receipts r JOIN execution_proposals predecessor ON predecessor.id=r.subject_id WHERE r.decision_kind='TASK_ACCEPTANCE' AND r.subject_kind='EXECUTION_PROPOSAL' AND r.option='REQUEST_REVERIFICATION' AND r.result_kind='EXECUTION_PROPOSAL' AND r.result_id=? AND r.project_id=? AND predecessor.task_id=?)`, value.ID, value.ProjectID, value.TaskID).Scan(&authorized); err != nil {
		return value, run.Run{}, err
	}
	if !authorized {
		if err = requireDirectFileVerificationSourceTx(ctx, tx, value); err != nil {
			return value, run.Run{}, err
		}
	}
	verificationRun, err := createRunAttempt(ctx, db, tx, value.TaskID, runID, now.UTC(), runMetadata{Purpose: run.PurposeVerification, ExecutionProposalID: value.ID})
	if err != nil {
		return value, run.Run{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET status='CONSUMED',run_id=?,verification_run_id=?,outcome='RUNNING',updated_at=?,consumed_at=? WHERE id=? AND status='CONFIRMED' AND outcome='PENDING' AND run_id IS NULL AND verification_run_id IS NULL`, runID, runID, formatTime(now), formatTime(now), value.ID)
	if err != nil {
		return value, run.Run{}, executionProposalConflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return value, run.Run{}, executionproposal.ErrConflict
	}
	value.Status, value.RunID, value.VerificationRunID, value.Outcome, value.UpdatedAt, value.ConsumedAt = executionproposal.StatusConsumed, runID, runID, executionproposal.OutcomeRunning, now.UTC(), timePointer(now.UTC())
	payload, _ := event.Payload(map[string]any{"from": executionproposal.StatusConfirmed, "to": executionproposal.StatusConsumed, "task_plan_id": value.TaskPlanID, "task_id": value.TaskID, "run_id": runID, "purpose": run.PurposeVerification, "authorization_source": "TASK_ACCEPTANCE"})
	if err = appendEvent(ctx, tx, event.Event{Type: event.ExecutionProposalConsumed, ProjectID: value.ProjectID, TaskID: value.TaskID, RunID: runID, EntityType: "execution_proposal", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
		return value, run.Run{}, err
	}
	return value, verificationRun, nil
}

// ReserveVerificationRun creates the one allowed VERIFICATION Run only after
// the linked IMPLEMENTATION Run has durably succeeded.
func (s *ExecutionProposalStore) ReserveVerificationRun(ctx context.Context, id, runID string, now time.Time) (executionproposal.Proposal, run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	defer tx.Rollback()
	value, reserved, err := reserveVerificationRunTx(ctx, s.db, tx, id, runID, now)
	if err != nil {
		return value, reserved, err
	}
	return value, reserved, tx.Commit()
}

func reserveVerificationRunTx(ctx context.Context, db *DB, tx *sql.Tx, id, runID string, now time.Time) (executionproposal.Proposal, run.Run, error) {
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrNotFound
	}
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	if value.VerificationRunID != "" {
		existing, getErr := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.execution_proposal_id=? AND r.purpose='VERIFICATION'`, value.VerificationRunID, value.ID))
		return value, existing, getErr
	}
	if value.Status != executionproposal.StatusConsumed || value.Outcome != executionproposal.OutcomeRunning || value.VerificationSpec.Mode != task.VerificationModeRun || value.ImplementationRunID == "" || value.VerificationRunID != "" {
		return value, run.Run{}, executionproposal.ErrConflict
	}
	var implementationStatus run.Status
	if err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=? AND task_id=? AND purpose='IMPLEMENTATION' AND execution_proposal_id=?`, value.ImplementationRunID, value.TaskID, value.ID).Scan(&implementationStatus); err != nil {
		return value, run.Run{}, executionProposalConflict(err)
	}
	if implementationStatus != run.StatusSucceeded {
		return value, run.Run{}, executionproposal.ErrConflict
	}
	verificationRun, err := createRunAttempt(ctx, db, tx, value.TaskID, runID, now.UTC(), runMetadata{Purpose: run.PurposeVerification, ExecutionProposalID: value.ID})
	if err != nil {
		return value, run.Run{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET verification_run_id=?,updated_at=? WHERE id=? AND status='CONSUMED' AND outcome='RUNNING' AND verification_run_id IS NULL`, runID, formatTime(now), value.ID)
	if err != nil {
		return value, run.Run{}, executionProposalConflict(err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		if err == nil {
			err = executionproposal.ErrConflict
		}
		return value, run.Run{}, err
	}
	value.VerificationRunID = runID
	value.UpdatedAt = now.UTC()
	return value, verificationRun, nil
}

func (s *ExecutionProposalStore) Complete(ctx context.Context, id string, outcome executionproposal.Outcome, failureCode, failureMessage string, now time.Time) (executionproposal.Proposal, error) {
	if outcome != executionproposal.OutcomeSucceeded && outcome != executionproposal.OutcomeFailed && outcome != executionproposal.OutcomeCanceled {
		return executionproposal.Proposal{}, executionproposal.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	defer tx.Rollback()
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return executionproposal.Proposal{}, executionproposal.ErrNotFound
	}
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	if value.Outcome != executionproposal.OutcomeRunning {
		if value.Outcome == executionproposal.OutcomeCanceled && outcome == executionproposal.OutcomeCanceled {
			var committed bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE execution_id=? AND state='CANCELED')`, value.ID).Scan(&committed); err != nil {
				return value, err
			}
			if committed {
				return value, nil
			}
		}
		return value, &executionproposal.TransitionError{From: value.Status, To: value.Status}
	}
	if outcome == executionproposal.OutcomeCanceled {
		// The exact Run terminal transaction publishes all three cancellation
		// terminals atomically. Completion may only read back that committed
		// result via the replay case above; it cannot create another writer.
		return value, executionproposal.ErrConflict
	}
	runID := value.VerificationRunID
	if runID == "" {
		runID = value.ImplementationRunID
	}
	if err := s.db.checkRunTx(ctx, tx, runID); err != nil {
		return value, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET outcome=?,failure_code=?,failure_message=?,updated_at=? WHERE id=? AND outcome='RUNNING'`, string(outcome), nullString(failureCode), nullString(failureMessage), formatTime(now), id)
	if err != nil {
		return value, executionProposalConflict(err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		if err == nil {
			err = executionproposal.ErrConflict
		}
		return value, err
	}
	value.Outcome, value.FailureCode, value.FailureMessage, value.UpdatedAt = outcome, failureCode, failureMessage, now.UTC()
	payload, _ := event.Payload(map[string]any{"outcome": outcome, "failure_code": failureCode, "implementation_run_id": value.ImplementationRunID, "verification_run_id": value.VerificationRunID})
	eventRunID := value.VerificationRunID
	if eventRunID == "" {
		eventRunID = value.ImplementationRunID
	}
	if err := appendEvent(ctx, tx, event.Event{Type: event.ExecutionCompleted, ProjectID: value.ProjectID, TaskID: value.TaskID, RunID: eventRunID, EntityType: "execution_proposal", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
		return value, err
	}
	if err := releaseFinalizedAgentOperationTx(ctx, tx, eventRunID); err != nil {
		return value, err
	}
	if err := settleExecutionCancellationsTx(ctx, tx, eventRunID); err != nil {
		return value, err
	}
	if err := tx.Commit(); err != nil {
		return value, err
	}
	return value, nil
}

type executionProposalScanner interface{ Scan(...any) error }

func scanExecutionProposal(scanner executionProposalScanner) (executionproposal.Proposal, error) {
	var value executionproposal.Proposal
	var runID, verificationRunID, failureCode, failureMessage, confirmed, rejected, consumed sql.NullString
	var primaryPurpose string
	var created, updated, workSpec, verificationSpec string
	var runtimeConfig, backendSelection sql.NullString
	if err := scanner.Scan(&value.ID, &value.AISessionID, &value.ProjectID, &value.TaskID, &value.TaskPlanID, &value.Status, &runID, &created, &updated, &confirmed, &rejected, &consumed, &workSpec, &verificationSpec, &value.Outcome, &verificationRunID, &failureCode, &failureMessage, &value.TaskDefinitionRevisionID, &runtimeConfig, &backendSelection, &value.PrimaryMemberID, &value.ApprovedWorksetHash, &value.RuntimeAdapterIdentity, &value.RuntimeProtocolIdentity, &value.ReviewApprovalID, &primaryPurpose); err != nil {
		return executionproposal.Proposal{}, err
	}
	value.ProvenanceKind = "EXACT"
	if value.TaskDefinitionRevisionID == "" {
		value.ProvenanceKind = "LEGACY_UNKNOWN"
	}
	value.RunID = runID.String
	if primaryPurpose == string(run.PurposeImplementation) {
		value.ImplementationRunID = runID.String
	}
	value.VerificationRunID = verificationRunID.String
	value.FailureCode = failureCode.String
	value.FailureMessage = failureMessage.String
	var err error
	value.WorkSpec, err = decodeWorkSpec(workSpec)
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	value.VerificationSpec, err = decodeVerificationSpec(verificationSpec)
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	if runtimeConfig.Valid && runtimeConfig.String != "" {
		if err = json.Unmarshal([]byte(runtimeConfig.String), &value.RuntimeConfig); err != nil {
			return executionproposal.Proposal{}, fmt.Errorf("decode execution runtime config: %w", err)
		}
	}
	if value.BackendSelection, err = decodeBackendSelection(backendSelection); err != nil {
		return executionproposal.Proposal{}, err
	}
	if value.CreatedAt, err = parseTime(created); err != nil {
		return executionproposal.Proposal{}, err
	}
	if value.UpdatedAt, err = parseTime(updated); err != nil {
		return executionproposal.Proposal{}, err
	}
	if value.ConfirmedAt, err = parseNullableTime(confirmed); err != nil {
		return executionproposal.Proposal{}, err
	}
	if value.RejectedAt, err = parseNullableTime(rejected); err != nil {
		return executionproposal.Proposal{}, err
	}
	if value.ConsumedAt, err = parseNullableTime(consumed); err != nil {
		return executionproposal.Proposal{}, err
	}
	return value, nil
}

func executionProposalConflict(err error) error {
	mapped := conflict(err)
	if errors.Is(mapped, ErrConflict) {
		return fmt.Errorf("%w: %v", executionproposal.ErrConflict, err)
	}
	return err
}

func encodeExecutionRuntimeConfig(value codexruntime.NextRunRuntimeConfig) (any, error) {
	if value.Model == "" {
		return nil, nil
	}
	if value.ReasoningEffort == "" || (value.CodexFastMode && value.ServiceTier != "priority") || (!value.CodexFastMode && value.ServiceTier != "") {
		return nil, executionproposal.ErrConflict
	}
	raw, err := json.Marshal(value)
	return string(raw), err
}

func encodeBackendSelection(value *codexconfig.BackendSelection) (any, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func decodeBackendSelection(raw sql.NullString) (*codexconfig.BackendSelection, error) {
	if !raw.Valid || raw.String == "" || raw.String == "null" {
		return nil, nil
	}
	var value codexconfig.BackendSelection
	if err := json.Unmarshal([]byte(raw.String), &value); err != nil {
		return nil, err
	}
	if value.ConfigID == "" || value.ConfigFingerprint == "" {
		return nil, executionproposal.ErrConflict
	}
	return &value, nil
}

const selectExecutionProposal = `SELECT id,COALESCE(ai_session_id,''),project_id,task_id,task_plan_id,status,run_id,created_at,updated_at,confirmed_at,rejected_at,consumed_at,work_spec_json,verification_spec_json,outcome,verification_run_id,failure_code,failure_message,COALESCE(task_definition_revision_id,''),runtime_config_json,backend_selection_json,COALESCE(primary_member_id,''),COALESCE(approved_workset_hash,''),COALESCE(runtime_adapter_identity,''),COALESCE(runtime_protocol_identity,''),COALESCE(CASE WHEN task_definition_revision_id IS NOT NULL THEN (SELECT json_extract(revision.payload_json,'$.reviewApprovalId') FROM task_definition_revisions revision WHERE revision.id=execution_proposals.task_definition_revision_id AND revision.task_id=execution_proposals.task_id) ELSE (SELECT t.review_approval_id FROM tasks t WHERE t.id=execution_proposals.task_id) END,''),COALESCE((SELECT purpose FROM runs WHERE id=execution_proposals.run_id),'') FROM execution_proposals`
