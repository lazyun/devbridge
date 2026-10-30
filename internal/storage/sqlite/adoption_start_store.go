package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/confirmation"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/task"
)

// Both explicit-start paths reserve the same caller request. This check runs
// inside the confirmation writer transaction, opposite BindAdoptionStart's
// confirmation check, so neither namespace can win after the other commits.
// Historical consumer rows retain their original request identity; no backfill
// or newly inferred start authority is needed.
func checkAdoptionStartPrepareRequestTx(ctx context.Context, tx *sql.Tx, session, prepareRequest string) error {
	if !strings.HasPrefix(prepareRequest, "approved_task_start_prepare_") {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT request_id FROM ai_admitted_execution_start_requests WHERE ai_session_id=?`, session)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var request string
		if err = rows.Scan(&request); err != nil {
			return err
		}
		if prepareRequest == "approved_task_start_prepare_"+confirmation.ValueHash([]string{"devbridge.approved-task-start.v1", session, request}) {
			return confirmation.ErrRequestConflict
		}
	}
	return rows.Err()
}

func (s *WorkflowProgressionStore) ReplayAdoptionStart(ctx context.Context, in fe.AdoptionStartRequest) (confirmation.Intent, bool, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return confirmation.Intent{}, false, err
	}
	defer tx.Rollback()
	if err = instructionCaller(ctx, tx, in.AISessionID); err != nil {
		return confirmation.Intent{}, false, err
	}
	return replayAdoptionStartTx(ctx, tx, in, s.db.epoch)
}

func replayAdoptionStartTx(ctx context.Context, q definitionQuerier, in fe.AdoptionStartRequest, epoch string) (confirmation.Intent, bool, error) {
	var intentID, executionID, taskID, raw, hash string
	err := q.QueryRowContext(ctx, `SELECT intent_id,execution_id,task_id,request_json,payload_sha256 FROM ai_admitted_execution_start_requests WHERE ai_session_id=? AND request_id=?`, in.AISessionID, in.RequestID).Scan(&intentID, &executionID, &taskID, &raw, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return confirmation.Intent{}, false, nil
	}
	if err != nil {
		return confirmation.Intent{}, false, err
	}
	if confirmation.ValueHash(in) != hash {
		return confirmation.Intent{}, true, confirmation.ErrRequestConflict
	}
	var original fe.AdoptionStartRequest
	// ValueHash uses normalized submitted JSON, whereas formal archive hashes
	// cover the exact serialized bytes. Validate both canonical decoding and the
	// submitted request identity without confusing those two representations.
	if fe.Decode([]byte(raw), fe.HashBytes([]byte(raw)), &original) != nil || original != in || taskID != in.TaskID || executionID != in.ExpectedTarget.ExecutionID {
		return confirmation.Intent{}, true, fe.ErrCorrupt
	}
	intent, _, err := readAdoptionStartIntentTx(ctx, q, in, intentID, epoch)
	return intent, true, err
}

// BindAdoptionStart records only the consumer's exact request identity. The
// original archive and preparation authority remain the sole launch authority.
func (s *WorkflowProgressionStore) BindAdoptionStart(ctx context.Context, in fe.AdoptionStartRequest) (confirmation.Intent, bool, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return confirmation.Intent{}, false, err
	}
	defer tx.Rollback()
	if err = instructionCaller(ctx, tx, in.AISessionID); err != nil {
		return confirmation.Intent{}, false, err
	}
	if intent, found, err := replayAdoptionStartTx(ctx, tx, in, s.db.epoch); err != nil || found {
		return intent, found, err
	}
	if in.ExpectedTarget.ExecutionID == "" {
		return confirmation.Intent{}, false, nil
	}
	var intentID string
	// Only the confirmation that created this admission can supply its archive.
	// Runtime-authorized approvals may have several later locked confirmations;
	// they remain on the original composed-start path, never become this source.
	err = tx.QueryRowContext(ctx, `SELECT b.intent_id FROM formal_adoption_business b JOIN execution_proposal_admissions a ON a.proposal_id=b.execution_id AND a.admission_request_id=b.admission_request_id JOIN confirmation_intents i ON i.ai_session_id=b.ai_session_id AND i.intent_id=b.intent_id AND json_extract(i.intent_json,'$.steps[0].requestId')=a.admission_request_id WHERE b.ai_session_id=? AND b.task_id=? AND b.execution_id=? AND json_type(b.business_json,'$.priorRuntimeApproval') IS NULL`, in.AISessionID, in.TaskID, in.ExpectedTarget.ExecutionID).Scan(&intentID)
	if errors.Is(err, sql.ErrNoRows) {
		return confirmation.Intent{}, false, nil
	}
	if err != nil {
		return confirmation.Intent{}, false, err
	}
	intent, receipt, err := readAdoptionStartIntentTx(ctx, tx, in, intentID, s.db.epoch)
	if err != nil {
		return confirmation.Intent{}, true, err
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=(SELECT project_id FROM ai_sessions WHERE id=?)`, in.TaskID, in.AISessionID))
	if err != nil {
		return confirmation.Intent{}, true, err
	}
	a, expected := receipt.Archive, in.ExpectedTarget
	if t.Status != task.StatusActive || t.Phase != task.PhaseImplementationReady || t.ProjectID != a.ProjectID || t.FeatureAreaID != expected.FeatureAreaID || t.CurrentDefinitionRevisionID != expected.TaskDefinitionRevisionID || t.CurrentExecutionID != expected.ExecutionID || t.PrimaryMemberID != expected.PrimaryMemberID || fe.Hash(t.RepositoryTargets) != expected.ApprovedWorksetHash {
		return confirmation.Intent{}, true, confirmation.ErrRequestConflict
	}
	// An existing composed start request owns its prepare/commit identity even
	// if a caller now supplies an already admitted target under the same key.
	prepareRequest := "approved_task_start_prepare_" + confirmation.ValueHash([]string{"devbridge.approved-task-start.v1", in.AISessionID, in.RequestID})
	var alreadyPrepared bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM confirmation_intents WHERE ai_session_id=? AND prepare_request_id=?)`, in.AISessionID, prepareRequest).Scan(&alreadyPrepared); err != nil {
		return confirmation.Intent{}, true, err
	}
	if alreadyPrepared {
		return confirmation.Intent{}, true, confirmation.ErrRequestConflict
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return confirmation.Intent{}, true, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_admitted_execution_start_requests(ai_session_id,request_id,task_id,execution_id,intent_id,payload_sha256,request_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, in.AISessionID, in.RequestID, in.TaskID, a.ExecutionID, intent.IntentID, confirmation.ValueHash(in), string(raw), formatTime(s.now()))
	if err != nil {
		return confirmation.Intent{}, true, conflict(err)
	}
	return intent, true, tx.Commit()
}

func readAdoptionStartIntentTx(ctx context.Context, q definitionQuerier, in fe.AdoptionStartRequest, intentID, epoch string) (confirmation.Intent, fe.AdoptionReceipt, error) {
	var zero fe.AdoptionReceipt
	intent, err := formalConfirmationTx(ctx, q, in.AISessionID, intentID)
	if err != nil {
		return confirmation.Intent{}, zero, err
	}
	if intent.Purpose != confirmation.Execution || intent.Action != "START_APPROVED" || intent.State != "COMPLETED" || intent.Commit == nil || len(intent.Steps) != 1 || intent.Steps[0].State != "COMPLETED" || intent.Steps[0].Effect != "PREPARATION_READY" || intent.Steps[0].ExecutionID != in.ExpectedTarget.ExecutionID || intent.Steps[0].RunID != "" {
		return confirmation.Intent{}, zero, fe.ErrCorrupt
	}
	var raw, hash string
	err = q.QueryRowContext(ctx, `SELECT receipt_json,receipt_sha256 FROM formal_adoption_receipts WHERE ai_session_id=? AND request_id=?`, in.AISessionID, intent.Commit.RequestID).Scan(&raw, &hash)
	if err != nil {
		return confirmation.Intent{}, zero, err
	}
	var original fe.AdoptionReceipt
	if fe.Decode([]byte(raw), hash, &original) != nil {
		return confirmation.Intent{}, zero, fe.ErrCorrupt
	}
	receipt, err := readFormalAdoptionReceiptTx(ctx, q, original.Archive.Identity)
	if err != nil {
		return confirmation.Intent{}, zero, err
	}
	a := receipt.Archive
	if a.PriorRuntimeApproval != nil || intent.Steps[0].RequestID != a.AdmissionRequestID {
		return confirmation.Intent{}, zero, fe.ErrCorrupt
	}
	expected := a.ExpectedTarget
	expected.ExecutionID = a.ExecutionID
	if a.Identity.IntentID != intent.IntentID || a.TaskID != in.TaskID || a.ProjectID != intent.ProjectID || in.ExpectedTarget != expected || a.Runtime != in.RuntimeConfig || a.Backend.Input() != in.Backend {
		return confirmation.Intent{}, zero, confirmation.ErrRequestConflict
	}
	// Verify the original completed archive and selection source through the
	// public confirmation reader. Launch separately binds that original source.
	intent, err = recoverConfirmationTx(ctx, q, intent, epoch)
	if err != nil {
		return confirmation.Intent{}, zero, err
	}
	return intent, receipt, nil
}
