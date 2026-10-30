package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func scanLocalApproveAndStart(row interface{ Scan(...any) error }) (wp.LocalApproveAndStart, error) {
	var v wp.LocalApproveAndStart
	var instructions, runtime string
	var backend sql.NullString
	var created, updated string
	err := row.Scan(&v.ProjectID, &v.TaskID, &v.FeatureAreaID, &v.ReviewID, &v.DecisionID, &v.ActorID, &v.RawRequestID, &v.RequestID, &v.PayloadSHA256, &v.TargetJSON, &v.ApprovalRequestID, &v.StartRequestID, &instructions, &runtime, &backend, &v.RevisionID, &v.ExecutionID, &v.RunID, &v.HandoffState, &v.ErrorCode, &v.State, &created, &updated)
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal([]byte(instructions), &v.ExecutionInstructions); err != nil {
		return v, err
	}
	if err = json.Unmarshal([]byte(runtime), &v.RuntimeConfig); err != nil {
		return v, err
	}
	if v.BackendSelection, err = decodeBackendSelection(backend); err != nil {
		return v, err
	}
	v.CreatedAt, _ = parseTime(created)
	v.UpdatedAt, _ = parseTime(updated)
	return v, nil
}

const selectLocalApproveAndStart = `SELECT project_id,task_id,feature_area_id,review_id,decision_id,actor_id,raw_request_id,request_id,payload_sha256,target_json,approval_request_id,start_request_id,execution_instructions_json,runtime_config_json,backend_selection_json,COALESCE(revision_id,''),COALESCE(execution_id,''),COALESCE(run_id,''),COALESCE(handoff_state,''),COALESCE(error_code,''),state,created_at,updated_at FROM local_approve_and_start_commands`

func (s *WorkflowProgressionStore) ReadLocalApproveAndStart(ctx context.Context, projectID, actorID, requestID string) (wp.LocalApproveAndStart, bool, error) {
	value, err := scanLocalApproveAndStart(s.db.sql.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND actor_id=? AND request_id=?`, projectID, actorID, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		return wp.LocalApproveAndStart{}, false, nil
	}
	return value, err == nil, err
}

// ReadLocalApproveAndStartRun is deliberately narrower than an execution
// lookup: it can return only the Run recorded by this parent's stable start
// child receipt. It never selects a Task's latest Run and never reserves one.
func (s *WorkflowProgressionStore) ReadLocalApproveAndStartRun(ctx context.Context, projectID, actorID, requestID string) (string, bool, error) {
	var runID string
	err := s.db.sql.QueryRowContext(ctx, `SELECT o.run_id
		FROM local_approve_and_start_commands c
		JOIN operator_task_execution_start_receipts o
		  ON o.project_id=c.project_id AND o.actor_kind='LOCAL_OPERATOR' AND o.actor_id=c.actor_id
		 AND o.request_id=c.start_request_id AND o.action='START_EXECUTION'
		 AND o.task_id=c.task_id AND o.feature_area_id=c.feature_area_id
		 AND o.task_definition_revision_id=c.revision_id AND o.expected_execution_id=c.execution_id
		WHERE c.project_id=? AND c.actor_id=? AND c.request_id=? AND c.execution_id<>''`, projectID, actorID, requestID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return runID, true, nil
}

func (s *WorkflowProgressionStore) BeginLocalApproveAndStart(ctx context.Context, in wp.LocalApproveAndStart) (wp.LocalApproveAndStart, bool, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return wp.LocalApproveAndStart{}, false, err
	}
	defer tx.Rollback()
	var prior wp.LocalApproveAndStart
	prior, err = scanLocalApproveAndStart(tx.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND actor_id=? AND request_id=?`, in.ProjectID, in.ActorID, in.RequestID))
	if err == nil {
		priorBackend, _ := json.Marshal(prior.BackendSelection)
		inBackend, _ := json.Marshal(in.BackendSelection)
		if prior.TaskID != in.TaskID || prior.FeatureAreaID != in.FeatureAreaID || prior.ReviewID != in.ReviewID || prior.DecisionID != in.DecisionID || prior.RawRequestID != in.RawRequestID || prior.PayloadSHA256 != in.PayloadSHA256 || prior.TargetJSON != in.TargetJSON || prior.ApprovalRequestID != in.ApprovalRequestID || prior.StartRequestID != in.StartRequestID || prior.RuntimeConfig != in.RuntimeConfig || string(priorBackend) != string(inBackend) || !sameExecutionInstructions(prior.ExecutionInstructions, in.ExecutionInstructions) {
			return prior, true, wp.Reject("LOCAL_APPROVE_AND_START_REQUEST_CONFLICT", wp.Guidance{})
		}
		if err = tx.Commit(); err != nil {
			return prior, true, err
		}
		return prior, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return wp.LocalApproveAndStart{}, false, err
	}
	if err = taskActionAdmissionTx(ctx, tx, in.TaskID); err != nil {
		return wp.LocalApproveAndStart{}, false, err
	}
	instructions, err := json.Marshal(in.ExecutionInstructions)
	if err != nil {
		return wp.LocalApproveAndStart{}, false, err
	}
	runtime, err := json.Marshal(in.RuntimeConfig)
	if err != nil {
		return wp.LocalApproveAndStart{}, false, err
	}
	backend, err := encodeBackendSelection(in.BackendSelection)
	if err != nil {
		return wp.LocalApproveAndStart{}, false, err
	}
	now := formatTime(s.now())
	_, err = tx.ExecContext(ctx, `INSERT INTO local_approve_and_start_commands(project_id,task_id,feature_area_id,review_id,decision_id,actor_id,raw_request_id,request_id,payload_sha256,target_json,approval_request_id,start_request_id,execution_instructions_json,runtime_config_json,backend_selection_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'PENDING_APPROVAL',?,?)`, in.ProjectID, in.TaskID, in.FeatureAreaID, in.ReviewID, in.DecisionID, in.ActorID, in.RawRequestID, in.RequestID, in.PayloadSHA256, in.TargetJSON, in.ApprovalRequestID, in.StartRequestID, string(instructions), string(runtime), backend, now, now)
	if err != nil {
		return wp.LocalApproveAndStart{}, false, err
	}
	out, err := scanLocalApproveAndStart(tx.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND actor_id=? AND request_id=?`, in.ProjectID, in.ActorID, in.RequestID))
	if err != nil {
		return out, false, err
	}
	if err = tx.Commit(); err != nil {
		// The INSERT may or may not have committed; do not return its staged
		// row as a durable receipt.
		return wp.LocalApproveAndStart{}, false, err
	}
	return out, false, nil
}

func sameExecutionInstructions(a, b any) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}

func (s *WorkflowProgressionStore) RecordLocalApproveAndStartApproval(ctx context.Context, projectID, requestID, revisionID, executionID string) (wp.LocalApproveAndStart, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return wp.LocalApproveAndStart{}, err
	}
	defer tx.Rollback()
	var v wp.LocalApproveAndStart
	v, err = scanLocalApproveAndStart(tx.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND request_id=?`, projectID, requestID))
	if err != nil {
		return v, err
	}
	before := v
	if v.State == "PENDING_APPROVAL" {
		_, err = tx.ExecContext(ctx, `UPDATE local_approve_and_start_commands SET revision_id=?,execution_id=?,state='APPROVED',error_code=NULL,updated_at=? WHERE project_id=? AND actor_id=? AND request_id=?`, revisionID, executionID, formatTime(s.now()), projectID, v.ActorID, requestID)
		if err != nil {
			return v, err
		}
	}
	if v.RevisionID != "" && (v.RevisionID != revisionID || v.ExecutionID != executionID) {
		return v, wp.Reject("LOCAL_APPROVE_AND_START_REVISION_CONFLICT", wp.Guidance{})
	}
	v, err = scanLocalApproveAndStart(tx.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND actor_id=? AND request_id=?`, projectID, v.ActorID, requestID))
	if err != nil {
		return v, err
	}
	if err = tx.Commit(); err != nil {
		return before, err
	}
	return v, nil
}

func (s *WorkflowProgressionStore) RecordLocalApproveAndStartApprovalFailure(ctx context.Context, projectID, requestID, code string, rejected bool) (wp.LocalApproveAndStart, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return wp.LocalApproveAndStart{}, err
	}
	defer tx.Rollback()
	v, err := scanLocalApproveAndStart(tx.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND request_id=?`, projectID, requestID))
	if err != nil {
		return v, err
	}
	before := v
	if v.State != "PENDING_APPROVAL" {
		if err = tx.Commit(); err != nil {
			return before, err
		}
		return v, nil
	}
	state := "PENDING_APPROVAL"
	if rejected {
		state = "REJECTED"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE local_approve_and_start_commands SET state=?,error_code=?,updated_at=? WHERE project_id=? AND actor_id=? AND request_id=? AND state='PENDING_APPROVAL'`, state, code, formatTime(s.now()), projectID, v.ActorID, requestID); err != nil {
		return v, err
	}
	v, err = scanLocalApproveAndStart(tx.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND actor_id=? AND request_id=?`, projectID, v.ActorID, requestID))
	if err != nil {
		return v, err
	}
	if err = tx.Commit(); err != nil {
		return before, err
	}
	return v, nil
}

func (s *WorkflowProgressionStore) RecordLocalApproveAndStartReservation(ctx context.Context, projectID, requestID, executionID, runID string) (wp.LocalApproveAndStart, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return wp.LocalApproveAndStart{}, err
	}
	defer tx.Rollback()
	var v wp.LocalApproveAndStart
	v, err = scanLocalApproveAndStart(tx.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND request_id=?`, projectID, requestID))
	if err != nil {
		return v, err
	}
	before := v
	if v.State == "APPROVED" || v.State == "UNKNOWN" && v.RunID == "" {
		_, err = tx.ExecContext(ctx, `UPDATE local_approve_and_start_commands SET execution_id=?,run_id=?,state='RUN_RESERVED',handoff_state='REGISTERED',error_code=NULL,updated_at=? WHERE project_id=? AND actor_id=? AND request_id=?`, executionID, runID, formatTime(s.now()), projectID, v.ActorID, requestID)
		if err != nil {
			return v, err
		}
	}
	if v.ExecutionID != "" && (v.ExecutionID != executionID || v.RunID != "" && v.RunID != runID) {
		return v, wp.Reject("LOCAL_APPROVE_AND_START_EXECUTION_CONFLICT", wp.Guidance{})
	}
	v, err = scanLocalApproveAndStart(tx.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND actor_id=? AND request_id=?`, projectID, v.ActorID, requestID))
	if err != nil {
		return v, err
	}
	if err = tx.Commit(); err != nil {
		return before, err
	}
	return v, nil
}

func (s *WorkflowProgressionStore) RecordLocalApproveAndStartOutcome(ctx context.Context, projectID, requestID, runID, handoffState, code string, _ bool) (wp.LocalApproveAndStart, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return wp.LocalApproveAndStart{}, err
	}
	defer tx.Rollback()
	v, err := scanLocalApproveAndStart(tx.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND request_id=?`, projectID, requestID))
	if err != nil {
		return v, err
	}
	before := v
	if runID != "" && v.RunID != "" && v.RunID != runID {
		return v, wp.Reject("LOCAL_APPROVE_AND_START_RUN_CONFLICT", wp.Guidance{})
	}
	// The caller's handoff snapshot can race the Supervisor's accepted_at
	// receipt. Re-read the exact Run and its handoff row inside this writer
	// transaction; caller state/bool are never authority for a terminal Run.
	actualRunID := v.RunID
	if actualRunID == "" {
		actualRunID = runID
	}
	state, recordedHandoffState, recordedCode := "UNKNOWN", handoffState, code
	if recordedHandoffState == "DIRECT_FILE_READY" {
		state = "DIRECT_FILE_READY"
	} else if actualRunID != "" {
		handoff, readErr := readRuntimeHandoff(ctx, tx, projectID, actualRunID)
		if readErr != nil {
			return v, readErr
		}
		if handoff.TaskID != v.TaskID || handoff.ExecutionID != v.ExecutionID || handoff.RuntimeEpoch == "" || handoff.RecordExists && handoff.RecordedRuntimeEpoch != handoff.RuntimeEpoch {
			return v, wp.Reject("LOCAL_APPROVE_AND_START_OUTCOME_CONFLICT", wp.Guidance{})
		}
		recordedHandoffState, recordedCode = string(handoff.RecordedState), handoff.RecordedCode
		switch {
		case handoff.RecordedAccepted():
			state, recordedHandoffState = "STARTED", string(executionproposal.HandoffAccepted)
		case handoff.RecordExists && handoff.RecordedState == executionproposal.HandoffRejected:
			state = "PARTIAL"
		case handoff.RunStatus == run.StatusSucceeded || handoff.RunStatus == run.StatusFailed || handoff.RunStatus == run.StatusInfrastructureFailed || handoff.RunStatus == run.StatusCanceled:
			// A terminal Run with a preparing/missing/unknown receipt is an
			// observation, not proof that the Supervisor never accepted it.
			// Keep this parent reopen-free but evidence-refreshable as UNKNOWN.
			state, recordedHandoffState = "UNKNOWN", string(executionproposal.HandoffUnknown)
			if recordedCode == "" || recordedCode == "HANDOFF_IN_PROGRESS" || recordedCode == "HANDOFF_EVIDENCE_UNAVAILABLE" {
				recordedCode = "HANDOFF_ACCEPTANCE_UNCONFIRMED"
			}
		default:
			state = "UNKNOWN"
		}
	}
	if v.State == "STARTED" || v.State == "DIRECT_FILE_READY" || v.State == "PARTIAL" || v.State == "REJECTED" {
		// Later accepted_at for the same immutable Run complements a historical
		// PARTIAL/STARTED receipt. Do not rewrite the parent; the pure reader
		// projects the newer fact independently.
		if state == "STARTED" && actualRunID != "" {
			if err = tx.Commit(); err != nil {
				return before, err
			}
			return v, nil
		}
		// Unconfirmed/missing evidence is not a contradictory outcome. Keep the
		// historical parent intact so a later same-Run ACCEPTED receipt can be
		// read independently; identity and explicit rejection conflicts below
		// remain strict.
		if state == "UNKNOWN" && actualRunID != "" {
			if err = tx.Commit(); err != nil {
				return before, err
			}
			return v, nil
		}
		if v.HandoffState != recordedHandoffState || v.ErrorCode != recordedCode {
			return v, wp.Reject("LOCAL_APPROVE_AND_START_OUTCOME_CONFLICT", wp.Guidance{})
		}
		if err = tx.Commit(); err != nil {
			return before, err
		}
		return v, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE local_approve_and_start_commands SET state=?,handoff_state=?,error_code=?,updated_at=? WHERE project_id=? AND actor_id=? AND request_id=? AND state IN ('APPROVED','RUN_RESERVED','UNKNOWN')`, state, recordedHandoffState, nullString(recordedCode), formatTime(s.now()), projectID, v.ActorID, requestID)
	if err != nil {
		return v, err
	}
	v, err = scanLocalApproveAndStart(tx.QueryRowContext(ctx, selectLocalApproveAndStart+` WHERE project_id=? AND actor_id=? AND request_id=?`, projectID, v.ActorID, requestID))
	if err != nil {
		return v, err
	}
	if err = tx.Commit(); err != nil {
		return before, err
	}
	return v, nil
}
