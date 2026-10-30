package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
)

// ConfirmTaskAndCreateExecution atomically converges the internal TaskPlan
// confirmation/application sequence and materializes the one pending external
// Execution authorization. A retry returns the existing authorization.
func (s *ExecutionProposalStore) ConfirmTaskAndCreateExecution(ctx context.Context, candidate executionproposal.Proposal, now time.Time) (taskplan.Plan, task.Task, executionproposal.Proposal, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return taskplan.Plan{}, task.Task{}, executionproposal.Proposal{}, err
	}
	defer tx.Rollback()
	// Acquire the SQLite writer before reading lifecycle state. This avoids a
	// deferred-transaction upgrade race under concurrent card clicks.
	claim, err := tx.ExecContext(ctx, `UPDATE task_plans SET updated_at=updated_at WHERE id=?`, candidate.TaskPlanID)
	if err != nil {
		return taskplan.Plan{}, task.Task{}, executionproposal.Proposal{}, taskPlanConflict(err)
	}
	if affected, _ := claim.RowsAffected(); affected != 1 {
		return taskplan.Plan{}, task.Task{}, executionproposal.Proposal{}, taskplan.ErrNotFound
	}
	planValue, err := scanTaskPlan(tx.QueryRowContext(ctx, selectTaskPlan+` WHERE id=?`, candidate.TaskPlanID))
	if err != nil {
		return taskplan.Plan{}, task.Task{}, executionproposal.Proposal{}, err
	}
	taskValue, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, planValue.TaskID))
	if err != nil {
		return planValue, task.Task{}, executionproposal.Proposal{}, err
	}
	if taskValue.Type == task.TypeDevelopment {
		return planValue, taskValue, executionproposal.Proposal{}, executionproposal.ErrConflict
	}
	var sessionProjectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, candidate.AISessionID).Scan(&sessionProjectID); err != nil {
		return planValue, taskValue, executionproposal.Proposal{}, executionProposalConflict(err)
	}
	if candidate.ID == "" || sessionProjectID != planValue.ProjectID || candidate.ProjectID != planValue.ProjectID || candidate.TaskID != planValue.TaskID {
		return planValue, taskValue, executionproposal.Proposal{}, executionproposal.ErrConflict
	}
	var decisionRequest *decisionresolution.Request
	if attempt, ok := decisionresolution.FromContext(ctx); ok {
		request := attempt.Request
		decisionRequest = &request
		receipt, handled, preflightErr := preflightDecisionResolutionTx(ctx, tx, request, planValue.ProjectID)
		if preflightErr != nil {
			return planValue, taskValue, executionproposal.Proposal{}, preflightErr
		}
		if handled {
			if receipt.DecisionID == decisionresolution.TaskDecisionID(planValue.ID) && receipt.ResultKind == decisionresolution.ResultExecution {
				existing, found, findErr := executionForTaskPlan(ctx, tx, planValue.ID)
				if findErr != nil {
					return planValue, taskValue, executionproposal.Proposal{}, findErr
				}
				if found {
					return planValue, taskValue, existing, nil
				}
			}
			return planValue, taskValue, executionproposal.Proposal{}, nil
		}
	}
	if existing, found, findErr := executionForTaskPlan(ctx, tx, planValue.ID); findErr != nil {
		return planValue, taskValue, executionproposal.Proposal{}, findErr
	} else if found {
		if planValue.Status != taskplan.StatusApplied || taskValue.ConfirmedTaskPlanID != planValue.ID {
			return planValue, taskValue, existing, executionproposal.ErrConflict
		}
		if decisionRequest != nil {
			if _, err := insertDecisionResolutionTx(ctx, tx, *decisionRequest, planValue.ProjectID, decisionresolution.ResultExecution, existing.ID, now); err != nil {
				return planValue, taskValue, existing, err
			}
			if err := tx.Commit(); err != nil {
				return planValue, taskValue, existing, err
			}
		}
		return planValue, taskValue, existing, nil
	}
	if planValue.Status == taskplan.StatusRejected {
		return planValue, taskValue, executionproposal.Proposal{}, &taskplan.TransitionError{From: planValue.Status, To: taskplan.StatusApplied}
	}
	if planValue.Status == taskplan.StatusPendingConfirmation {
		result, updateErr := tx.ExecContext(ctx, `UPDATE task_plans SET status='CONFIRMED',confirmed_at=?,updated_at=? WHERE id=? AND status='PENDING_CONFIRMATION'`, formatTime(now), formatTime(now), planValue.ID)
		if updateErr != nil {
			return planValue, taskValue, executionproposal.Proposal{}, taskPlanConflict(updateErr)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return planValue, taskValue, executionproposal.Proposal{}, taskplan.ErrConflict
		}
		payload, _ := event.Payload(map[string]any{"from": taskplan.StatusPendingConfirmation, "to": taskplan.StatusConfirmed})
		if err := appendEvent(ctx, tx, event.Event{Type: event.TaskPlanConfirmed, ProjectID: planValue.ProjectID, TaskID: planValue.TaskID, EntityType: "task_plan", EntityID: planValue.ID, Payload: payload, CreatedAt: now}); err != nil {
			return planValue, taskValue, executionproposal.Proposal{}, err
		}
		confirmed := now.UTC()
		planValue.Status, planValue.UpdatedAt, planValue.ConfirmedAt = taskplan.StatusConfirmed, confirmed, &confirmed
	}
	if planValue.Status == taskplan.StatusConfirmed {
		if !task.CanAuthorPlan(taskValue.Status) || taskValue.LegacyReadOnly {
			return planValue, taskValue, executionproposal.Proposal{}, taskplan.ErrConflict
		}
		workJSON, encodeErr := encodeWorkSpec(planValue.WorkSpec)
		if encodeErr != nil {
			return planValue, taskValue, executionproposal.Proposal{}, encodeErr
		}
		verificationJSON, encodeErr := encodeVerificationSpec(planValue.VerificationSpec)
		if encodeErr != nil {
			return planValue, taskValue, executionproposal.Proposal{}, encodeErr
		}
		constraints, encodeErr := encodeStringItems(planValue.WorkSpec.Constraints)
		if encodeErr != nil {
			return planValue, taskValue, executionproposal.Proposal{}, encodeErr
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE tasks SET goal=?,constraints_json=?,work_spec_json=?,verification_spec_json=?,confirmed_task_plan_id=?,status='READY',updated_at=? WHERE id=? AND status IN ('PLANNING','READY','SUCCEEDED','FAILED','CANCELED') AND legacy_read_only=0`, planValue.WorkSpec.Goal, constraints, workJSON, verificationJSON, planValue.ID, formatTime(now), taskValue.ID)
		if updateErr != nil {
			return planValue, taskValue, executionproposal.Proposal{}, taskPlanConflict(updateErr)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return planValue, taskValue, executionproposal.Proposal{}, taskplan.ErrConflict
		}
		result, updateErr = tx.ExecContext(ctx, `UPDATE task_plans SET status='APPLIED',applied_at=?,updated_at=? WHERE id=? AND status='CONFIRMED'`, formatTime(now), formatTime(now), planValue.ID)
		if updateErr != nil {
			return planValue, taskValue, executionproposal.Proposal{}, taskPlanConflict(updateErr)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return planValue, taskValue, executionproposal.Proposal{}, taskplan.ErrConflict
		}
		payload, _ := event.Payload(map[string]any{"from": taskValue.Status, "to": task.StatusReady, "task_plan_id": planValue.ID})
		if err := appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: planValue.ProjectID, TaskID: planValue.TaskID, EntityType: "task", EntityID: planValue.TaskID, Payload: payload, CreatedAt: now}); err != nil {
			return planValue, taskValue, executionproposal.Proposal{}, err
		}
		if err := appendEvent(ctx, tx, event.Event{Type: event.TaskPlanApplied, ProjectID: planValue.ProjectID, TaskID: planValue.TaskID, EntityType: "task_plan", EntityID: planValue.ID, Payload: payload, CreatedAt: now}); err != nil {
			return planValue, taskValue, executionproposal.Proposal{}, err
		}
		if planValue.VerificationSpec.Mode == task.VerificationModeNone && planValue.VerificationSpec.Waiver != nil {
			waiverPayload, _ := event.Payload(map[string]any{"source": planValue.VerificationSpec.Waiver.Source, "reason": planValue.VerificationSpec.Waiver.Reason, "task_plan_id": planValue.ID})
			if err := appendEvent(ctx, tx, event.Event{Type: event.VerificationWaived, ProjectID: planValue.ProjectID, TaskID: planValue.TaskID, EntityType: "verification", EntityID: planValue.ID, Payload: waiverPayload, CreatedAt: now}); err != nil {
				return planValue, taskValue, executionproposal.Proposal{}, err
			}
		}
		applied := now.UTC()
		planValue.Status, planValue.UpdatedAt, planValue.AppliedAt = taskplan.StatusApplied, applied, &applied
		taskValue.Goal = planValue.WorkSpec.Goal
		taskValue.Constraints = append([]string(nil), planValue.WorkSpec.Constraints...)
		taskValue.WorkSpec = task.CloneWorkSpec(planValue.WorkSpec)
		taskValue.VerificationSpec = task.CloneVerificationSpec(planValue.VerificationSpec)
		taskValue.ConfirmedTaskPlanID, taskValue.Status, taskValue.UpdatedAt = planValue.ID, task.StatusReady, applied
		if _, err := ensureTaskDefinitionTx(ctx, tx, &taskValue, now); err != nil {
			return planValue, taskValue, executionproposal.Proposal{}, err
		}
	}
	if planValue.Status != taskplan.StatusApplied || taskValue.ConfirmedTaskPlanID != planValue.ID || taskValue.Status == task.StatusPlanning {
		return planValue, taskValue, executionproposal.Proposal{}, executionproposal.ErrConflict
	}
	candidate.WorkSpec = task.CloneWorkSpec(planValue.WorkSpec)
	candidate.VerificationSpec = task.CloneVerificationSpec(planValue.VerificationSpec)
	candidate.Status = executionproposal.StatusPendingConfirmation
	candidate.Outcome = executionproposal.OutcomePending
	candidate.CreatedAt, candidate.UpdatedAt = now.UTC(), now.UTC()
	workSpec, err := encodeWorkSpec(candidate.WorkSpec)
	if err != nil {
		return planValue, taskValue, executionproposal.Proposal{}, err
	}
	verificationSpec, err := encodeVerificationSpec(candidate.VerificationSpec)
	if err != nil {
		return planValue, taskValue, executionproposal.Proposal{}, err
	}
	if err := bindExecutionDefinitionTx(ctx, tx, &candidate); err != nil {
		return planValue, taskValue, executionproposal.Proposal{}, err
	}
	runtimeConfig, err := encodeExecutionRuntimeConfig(candidate.RuntimeConfig)
	if err != nil {
		return taskplan.Plan{}, task.Task{}, executionproposal.Proposal{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_proposals(id,ai_session_id,project_id,task_id,task_plan_id,status,run_id,created_at,updated_at,confirmed_at,rejected_at,consumed_at,work_spec_json,verification_spec_json,outcome,task_definition_revision_id,runtime_config_json) VALUES(?,?,?,?,?,'PENDING_CONFIRMATION',NULL,?,?,NULL,NULL,NULL,?,?,?,?,?)`, candidate.ID, candidate.AISessionID, candidate.ProjectID, candidate.TaskID, candidate.TaskPlanID, formatTime(candidate.CreatedAt), formatTime(candidate.UpdatedAt), workSpec, verificationSpec, string(executionproposal.OutcomePending), candidate.TaskDefinitionRevisionID, runtimeConfig)
	if err != nil {
		return planValue, taskValue, executionproposal.Proposal{}, executionProposalConflict(fmt.Errorf("insert execution proposal: %w", err))
	}
	payload, _ := event.Payload(map[string]any{"ai_session_id": candidate.AISessionID, "task_plan_id": candidate.TaskPlanID, "task_id": candidate.TaskID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ExecutionProposalCreated, ProjectID: candidate.ProjectID, TaskID: candidate.TaskID, EntityType: "execution_proposal", EntityID: candidate.ID, Payload: payload, CreatedAt: now}); err != nil {
		return planValue, taskValue, executionproposal.Proposal{}, err
	}
	if decisionRequest != nil {
		if _, err := insertDecisionResolutionTx(ctx, tx, *decisionRequest, planValue.ProjectID, decisionresolution.ResultExecution, candidate.ID, now); err != nil {
			return planValue, taskValue, executionproposal.Proposal{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return planValue, taskValue, executionproposal.Proposal{}, err
	}
	return planValue, taskValue, candidate, nil
}

// Deprecated: AuthorizeAndCreateRun is a legacy synchronous Run creation API.
// Production callers must use the two-step admission pattern: admitExecutionTx
// (Transaction A: admission → ADMITTED, no Run) followed by PrepareAdmittedExecution
// (Transaction B: Run creation → CONSUMED). This function has zero production
// callers and is retained only for test compatibility.
//
// AuthorizeAndCreateRun atomically records the Execution decision (when
// needed), consumes it, and reserves its single IMPLEMENTATION Run. Retries
// return the already-reserved Run.
func (s *ExecutionProposalStore) AuthorizeAndCreateRun(ctx context.Context, id, runID string, now time.Time) (executionproposal.Proposal, run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	defer tx.Rollback()
	value, reserved, err := authorizeAndCreateRunTx(ctx, s.db, tx, id, runID, now)
	if err != nil {
		return value, reserved, err
	}
	return value, reserved, tx.Commit()
}

// Deprecated: authorizeAndCreateRunTx is a legacy synchronous Run creation
// transaction. See AuthorizeAndCreateRun for migration guidance.
func authorizeAndCreateRunTx(ctx context.Context, db *DB, tx *sql.Tx, id, runID string, now time.Time) (executionproposal.Proposal, run.Run, error) {
	claim, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET updated_at=updated_at WHERE id=?`, id)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, executionProposalConflict(err)
	}
	if affected, _ := claim.RowsAffected(); affected != 1 {
		return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrNotFound
	}
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, id))
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	if err = requireExecutionStartAuthorizationTx(ctx, tx, value); err != nil {
		return value, run.Run{}, err
	}
	if value.Status == executionproposal.StatusConsumed {
		reserved, getErr := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.execution_proposal_id=?`, value.RunID, value.ID))
		return value, reserved, getErr
	}
	if value.Status == executionproposal.StatusRejected {
		return value, run.Run{}, &executionproposal.TransitionError{From: value.Status, To: executionproposal.StatusConsumed}
	}
	if value.Status == executionproposal.StatusPendingConfirmation {
		result, updateErr := tx.ExecContext(ctx, `UPDATE execution_proposals SET status='CONFIRMED',confirmed_at=?,updated_at=? WHERE id=? AND status='PENDING_CONFIRMATION'`, formatTime(now), formatTime(now), value.ID)
		if updateErr != nil {
			return value, run.Run{}, executionProposalConflict(updateErr)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return value, run.Run{}, executionproposal.ErrConflict
		}
		payload, _ := event.Payload(map[string]any{"from": executionproposal.StatusPendingConfirmation, "to": executionproposal.StatusConfirmed, "task_plan_id": value.TaskPlanID})
		if err := appendEvent(ctx, tx, event.Event{Type: event.ExecutionProposalConfirmed, ProjectID: value.ProjectID, TaskID: value.TaskID, EntityType: "execution_proposal", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
			return value, run.Run{}, err
		}
		confirmed := now.UTC()
		value.Status, value.UpdatedAt, value.ConfirmedAt = executionproposal.StatusConfirmed, confirmed, &confirmed
	}
	if value.Status != executionproposal.StatusConfirmed || value.Outcome != executionproposal.OutcomePending {
		return value, run.Run{}, &executionproposal.TransitionError{From: value.Status, To: executionproposal.StatusConsumed}
	}
	var sourceStatus taskplan.Status
	var sourceTaskID string
	if err := tx.QueryRowContext(ctx, `SELECT status,task_id FROM task_plans WHERE id=? AND project_id=?`, value.TaskPlanID, value.ProjectID).Scan(&sourceStatus, &sourceTaskID); err != nil {
		return value, run.Run{}, executionProposalConflict(err)
	}
	if sourceStatus != taskplan.StatusApplied || sourceTaskID != value.TaskID {
		return value, run.Run{}, executionproposal.ErrConflict
	}
	reserved, err := createRunAttempt(ctx, db, tx, value.TaskID, runID, now.UTC(), runMetadata{Purpose: run.PurposeImplementation, ExecutionProposalID: value.ID})
	if err != nil {
		return value, run.Run{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET status='CONSUMED',run_id=?,outcome='RUNNING',updated_at=?,consumed_at=? WHERE id=? AND status='CONFIRMED' AND run_id IS NULL AND outcome='PENDING'`, runID, formatTime(now), formatTime(now), value.ID)
	if err != nil {
		return value, run.Run{}, executionProposalConflict(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return value, run.Run{}, executionproposal.ErrConflict
	}
	consumed := now.UTC()
	value.Status, value.RunID, value.ImplementationRunID, value.Outcome, value.UpdatedAt, value.ConsumedAt = executionproposal.StatusConsumed, runID, runID, executionproposal.OutcomeRunning, consumed, &consumed
	payload, _ := event.Payload(map[string]any{"from": executionproposal.StatusConfirmed, "to": executionproposal.StatusConsumed, "task_plan_id": value.TaskPlanID, "task_id": value.TaskID, "run_id": runID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ExecutionProposalConsumed, ProjectID: value.ProjectID, TaskID: value.TaskID, RunID: runID, EntityType: "execution_proposal", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
		return value, run.Run{}, err
	}
	return value, reserved, nil
}

func executionForTaskPlan(ctx context.Context, tx *sql.Tx, taskPlanID string) (executionproposal.Proposal, bool, error) {
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE task_plan_id=? ORDER BY created_at DESC,id LIMIT 1`, taskPlanID))
	if errors.Is(err, sql.ErrNoRows) {
		return executionproposal.Proposal{}, false, nil
	}
	return value, err == nil, err
}
