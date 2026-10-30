package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
)

func readRuntimeHandoff(ctx context.Context, q definitionQuerier, projectID, runID string) (executionproposal.Handoff, error) {
	h := executionproposal.Handoff{RunID: runID, OwnerKind: "IN_PROCESS_SUPERVISOR", State: executionproposal.HandoffUnknown, Code: "HANDOFF_EVIDENCE_UNAVAILABLE"}
	var state, epoch, accepted sql.NullString
	var status run.Status
	var current string
	err := q.QueryRowContext(ctx, `SELECT r.task_id,COALESCE(r.execution_proposal_id,''),r.runtime_epoch,r.attempt,r.status,h.state,h.runtime_epoch,h.accepted_at,COALESCE(h.code,''),(SELECT epoch_id FROM runtime_authority WHERE singleton=1) FROM runs r LEFT JOIN runtime_handoffs h ON h.run_id=r.id WHERE r.id=? AND r.project_id=?`, runID, projectID).Scan(&h.TaskID, &h.ExecutionID, &h.RuntimeEpoch, &h.Attempt, &status, &state, &epoch, &accepted, &h.Code, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return h, executionproposal.ErrNotFound
	}
	if err != nil {
		return h, err
	}
	h.RunStatus = status
	h.RunEpochCurrent = current == h.RuntimeEpoch
	if !state.Valid {
		h.Code = "HANDOFF_EVIDENCE_UNAVAILABLE"
	} else {
		h.RecordExists = true
		h.RecordedState = executionproposal.HandoffState(state.String)
		h.RecordedRuntimeEpoch = epoch.String
		h.RecordedCode = h.Code
		h.RecordEpochMatchesRun = h.RecordedRuntimeEpoch == h.RuntimeEpoch
		h.State = h.RecordedState
		h.AcceptedAt, err = parseNullableTime(accepted)
		if err != nil {
			return h, err
		}
		if epoch.String != h.RuntimeEpoch || current != h.RuntimeEpoch {
			h.State = executionproposal.HandoffUnknown
			h.Code = "HANDOFF_EPOCH_NOT_CURRENT"
		}
	}
	switch status {
	case run.StatusSucceeded, run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
		h.State = executionproposal.HandoffTerminal
	}
	return h, nil
}
func (s *ExecutionProposalStore) ReadHandoff(ctx context.Context, projectID, runID string) (executionproposal.Handoff, error) {
	return readRuntimeHandoff(ctx, s.db.sql, projectID, runID)
}

// ClaimHandoff never backfills or retries an old pending preparation. A crashed
// or uncertain attempt stays under the existing epoch/recovery authority.
func (s *ExecutionProposalStore) ClaimHandoff(ctx context.Context, projectID, runID string, now time.Time) (executionproposal.Handoff, bool, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Handoff{}, false, err
	}
	defer tx.Rollback()
	h, err := readRuntimeHandoff(ctx, tx, projectID, runID)
	if err != nil {
		return h, false, err
	}
	if h.State != executionproposal.HandoffRegistered {
		return h, false, nil
	}
	if err = s.db.checkRunTx(ctx, tx, runID); err != nil {
		return h, false, err
	}
	// A stale foreground replay can reach an already-reserved Run without
	// passing today's candidate reservation branch. Recheck the same immutable
	// effect at the last durable transition that can still cause dispatch.
	var executionID sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT execution_proposal_id FROM runs WHERE id=? AND project_id=?`, runID, projectID).Scan(&executionID); err != nil {
		return h, false, err
	}
	if executionID.Valid && executionID.String != "" {
		value, readErr := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=? AND project_id=?`, executionID.String, projectID))
		if readErr != nil {
			return h, false, readErr
		}
		if readErr = requireExecutionRunStartAuthorizationTx(ctx, tx, value, runID); readErr != nil {
			return h, false, readErr
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE runtime_handoffs SET state='PREPARING',code='HANDOFF_IN_PROGRESS',updated_at=? WHERE run_id=? AND runtime_epoch=? AND state='REGISTERED' AND EXISTS(SELECT 1 FROM runs r WHERE r.id=runtime_handoffs.run_id AND r.project_id=? AND r.status='CREATED')`, formatTime(now), runID, s.db.epoch, projectID)
	if err != nil {
		return h, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return h, false, err
	}
	if n != 1 {
		return h, false, executionproposal.ErrConflict
	}
	h.State = executionproposal.HandoffPreparing
	h.Code = "HANDOFF_IN_PROGRESS"
	return h, true, tx.Commit()
}
func (s *ExecutionProposalStore) RecordHandoff(ctx context.Context, projectID, runID string, state executionproposal.HandoffState, code string, now time.Time) (executionproposal.Handoff, error) {
	if state != executionproposal.HandoffAccepted && state != executionproposal.HandoffRejected && state != executionproposal.HandoffUnknown {
		return executionproposal.Handoff{}, executionproposal.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Handoff{}, err
	}
	defer tx.Rollback()
	// The execution may finish before this receipt write; check immutable epoch
	// and Project rather than requiring its released FeatureArea claim.
	result, err := tx.ExecContext(ctx, `UPDATE runtime_handoffs SET state=?,code=?,accepted_at=CASE WHEN ?='ACCEPTED' THEN ? ELSE accepted_at END,updated_at=? WHERE run_id=? AND runtime_epoch=? AND state='PREPARING' AND EXISTS(SELECT 1 FROM runs r WHERE r.id=runtime_handoffs.run_id AND r.project_id=? AND r.runtime_epoch=runtime_handoffs.runtime_epoch)`, string(state), code, string(state), formatTime(now), formatTime(now), runID, s.db.epoch, projectID)
	if err != nil {
		return executionproposal.Handoff{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return executionproposal.Handoff{}, err
	}
	if n != 1 {
		return executionproposal.Handoff{}, executionproposal.ErrConflict
	}
	h, err := readRuntimeHandoff(ctx, tx, projectID, runID)
	if err != nil {
		return h, err
	}
	return h, tx.Commit()
}

// ReserveExecution is shared by explicit Start and the trusted continuation.
// The worker must prove a durable execute effect; creation provenance is never
// installed into the caller context as an AI Session identity.
func (s *ExecutionProposalStore) ReserveExecution(ctx context.Context, id string, worker bool, now time.Time) (executionproposal.Proposal, run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	defer tx.Rollback()
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, id))
	if err != nil {
		return value, run.Run{}, err
	}
	if value.Status == executionproposal.StatusAdmitted {
		if err = tx.Commit(); err != nil {
			return value, run.Run{}, err
		}
		return value, run.Run{}, nil
	}
	// Caller identity and candidate status never imply delivery authority. Both
	// the background worker and any legacy foreground proposal entry must prove
	// the same persisted START_AUTHORIZED command.
	targetRunID := value.RunID
	if value.VerificationRunID != "" {
		targetRunID = value.VerificationRunID
	}
	if err = requireExecutionRunStartAuthorizationTx(ctx, tx, value, targetRunID); err != nil {
		return value, run.Run{}, err
	}
	if value.Status == executionproposal.StatusConsumed {
		r, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.execution_proposal_id=?`, targetRunID, value.ID))
		return value, r, err
	}
	if value.Status != executionproposal.StatusConfirmed {
		return value, run.Run{}, executionproposal.ErrConflict
	}
	var verification bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM decision_resolution_receipts WHERE decision_kind='TASK_ACCEPTANCE' AND subject_kind='EXECUTION_PROPOSAL' AND option='REQUEST_REVERIFICATION' AND result_kind='EXECUTION_PROPOSAL' AND result_id=?)`, id).Scan(&verification)
	if err != nil {
		return value, run.Run{}, err
	}
	var reserved run.Run
	if verification {
		value, reserved, err = consumeAndCreateVerificationRunTx(ctx, s.db, tx, id, "run_verify_"+id, now)
	} else {
		var requestID, payload string
		if err = tx.QueryRowContext(ctx, `SELECT request_id,payload_sha256 FROM execution_start_effects WHERE execution_id=? AND effect='START_AUTHORIZED' AND expected_phase='IMPLEMENTATION_READY' ORDER BY created_at,request_id LIMIT 1`, id).Scan(&requestID, &payload); err != nil {
			return value, run.Run{}, executionProposalConflict(err)
		}
		admission, admissionErr := admitExecutionTx(ctx, tx, id, requestID, payload, now)
		if admissionErr != nil {
			return value, run.Run{}, admissionErr
		}
		if err = tx.Commit(); err != nil {
			return value, run.Run{}, err
		}
		return admission.Proposal, run.Run{}, nil
	}
	if err != nil {
		return value, reserved, err
	}
	return value, reserved, tx.Commit()
}

// PrepareAdmittedExecution is the post-commit Core step for an already
// admitted Execution. It reuses the immutable admission request and plan.
func (s *ExecutionProposalStore) PrepareAdmittedExecution(ctx context.Context, id string, now time.Time) (executionproposal.Proposal, run.Run, error) {
	return continueAdmittedExecution(ctx, s.db, id, now)
}
