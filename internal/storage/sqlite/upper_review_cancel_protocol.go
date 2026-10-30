package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

// cancellationPayload excludes the row key and authenticated caller, while
// preserving every normalized business input and its exact observed version.
func cancellationPayload(in executioncancel.Input) executioncancel.Input {
	in = in.Normalized()
	in.ProjectID = ""
	in.AISessionID = ""
	in.OperatorPrincipalID = ""
	in.RequestID = ""
	return in
}
func cancellationPayloadMatches(raw string, in executioncancel.Input) bool {
	var stored executioncancel.Input
	return json.Unmarshal([]byte(raw), &stored) == nil && reflect.DeepEqual(cancellationPayload(stored), cancellationPayload(in))
}

func upperReviewExecutorTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input) (bool, error) {
	if in.OwnerType != "REVIEW" {
		return false, nil
	}
	var executor string
	err := tx.QueryRowContext(ctx, `SELECT executor_kind FROM reviews WHERE id=?`, in.OwnerID).Scan(&executor)
	if err != nil {
		return false, err
	}
	if executor != "UPPER_LLM" && executor != "CODING_AGENT" {
		return false, executioncancel.ErrRunExecutionMismatch
	}
	return executor == "UPPER_LLM", nil
}

// completedCancellationReplay is strictly read-only. Pending records only
// select the later writer path, where their original admission is rechecked.
func (s *ExecutionCancellationStore) completedCancellationReplay(ctx context.Context, in executioncancel.Input) (executioncancel.Receipt, bool, error) {
	empty := executioncancel.Receipt{}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	if err = cancellationActorTx(ctx, tx, in); err != nil {
		return empty, false, err
	}
	var kind, owner, runID, reason, state, payload, result, receiptSession, receiptOperator string
	err = tx.QueryRowContext(ctx, `SELECT owner_type,owner_id,COALESCE(run_id,''),reason,state,COALESCE(request_payload_json,''),COALESCE(result_json,''),COALESCE(requested_by_ai_session_id,''),COALESCE(requested_by_operator_id,'') FROM execution_cancellations WHERE project_id=? AND request_id=?`, in.ProjectID, in.RequestID).Scan(&kind, &owner, &runID, &reason, &state, &payload, &result, &receiptSession, &receiptOperator)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	if receiptOperator != in.OperatorPrincipalID || (in.OperatorPrincipalID == "" && receiptSession != in.AISessionID) || kind != in.OwnerType || owner != in.OwnerID || runID != in.RunID || reason != in.Reason || (payload != "" && !cancellationPayloadMatches(payload, in)) || (payload == "" && in.ExpectedUpperVersion != nil) {
		return empty, false, executioncancel.ErrRequestConflict
	}
	if state == "CANCEL_REQUESTED" || state == "RECOVERY_REQUIRED" {
		return empty, false, nil
	}
	if result != "" {
		var value executioncancel.Receipt
		err = json.Unmarshal([]byte(result), &value)
		return value, true, err
	}
	value, err := scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	return value, true, err
}

// cancelUpperReviewTx continues an admitted intent or atomically admits and
// completes a fresh request. Pending work keeps the original grant epoch and
// generation even after a writer restart; it never reauthorizes an old Run.
func cancelUpperReviewTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input, currentEpoch string, pending bool, storedEpoch string) (executioncancel.Receipt, error) {
	empty := executioncancel.Receipt{}
	if !pending {
		if !in.ExpectedUpperVersion.Valid() {
			return empty, executioncancel.ErrInvalidArgument
		}
		if value, completed, e := admitCompletedUpperCancellationTx(ctx, tx, in, currentEpoch); e != nil || completed {
			return value, e
		}
	}
	if pending {
		if value, completed, e := finishUpperCancellationAfterReportTx(ctx, tx, in, storedEpoch); e != nil || completed {
			return value, e
		}
	}
	f, err := upperCancellationFactsTx(ctx, tx, in.OwnerID, currentEpoch)
	if err != nil {
		return empty, err
	}
	if f.reason != "" {
		return empty, executioncancel.ErrRunNotActive
	}
	if f.run.ProjectID != in.ProjectID {
		return empty, executioncancel.ErrProjectMismatch
	}
	if f.run.ID != in.RunID {
		return empty, executioncancel.ErrRunExecutionMismatch
	}
	if pending {
		if storedEpoch != f.version.GrantEpoch {
			return empty, executioncancel.ErrRequestConflict
		}
		if in.ExpectedUpperVersion != nil && (!in.ExpectedUpperVersion.Valid() || in.ExpectedUpperVersion.GrantEpoch != f.version.GrantEpoch || in.ExpectedUpperVersion.GrantGeneration != f.version.GrantGeneration) {
			return empty, executioncancel.ErrRequestConflict
		}
	} else {
		if !in.ExpectedUpperVersion.Valid() {
			return empty, executioncancel.ErrInvalidArgument
		}
		if !in.ExpectedUpperVersion.Equal(&f.version) {
			return empty, executioncancel.ErrRequestConflict
		}
		if !f.currentOwner {
			return empty, executioncancel.ErrRunNotActive
		}
		occupied, e := pendingCancellationOwnerTx(ctx, tx, in)
		if e != nil {
			return empty, e
		}
		if occupied {
			return empty, executioncancel.ErrRequestConflict
		}
	}
	now := time.Now().UTC()
	if !pending {
		payload, e := json.Marshal(cancellationPayload(in))
		if e != nil {
			return empty, e
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO execution_cancellations(project_id,request_id,execution_id,run_id,requested_by_ai_session_id,requested_by_operator_id,reason,requested_at,runtime_epoch,state,interrupt_state,owner_type,owner_id,task_id,backend_disposition,request_payload_json) VALUES(?,?,NULL,?,?,?,?,?,?,'CANCEL_REQUESTED','NOT_SENT','REVIEW',?,?,'NO_BACKEND',?)`, in.ProjectID, in.RequestID, in.RunID, nullString(in.AISessionID), nullString(in.OperatorPrincipalID), in.Reason, formatTime(now), f.version.GrantEpoch, in.OwnerID, f.run.TaskID, string(payload))
		if err != nil {
			return empty, executioncancel.ErrRequestConflict
		}
	}
	if err = terminalizeUpperCancellationTx(ctx, tx, in, f, now); err != nil {
		return empty, err
	}
	value, err := scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	if err != nil {
		return empty, err
	}
	value.ExpectedUpperVersion = in.ExpectedUpperVersion
	// Legacy pending intents retain their original no-version contract. Freeze
	// only new-format outcomes; never fabricate historical observed versions.
	if in.ExpectedUpperVersion != nil {
		encoded, e := json.Marshal(value)
		if e != nil {
			return empty, e
		}
		_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET result_json=? WHERE project_id=? AND request_id=? AND result_json IS NULL AND request_payload_json IS NOT NULL AND state='CANCELED'`, string(encoded), in.ProjectID, in.RequestID)
		if err != nil {
			return empty, err
		}
	}
	return value, nil
}

// A fresh exact request may record ALREADY_TERMINAL, but this is still a new
// Upper command: both observed epochs and the original grant generation must
// match. It never repairs or rewrites the completed grant, Run or report.
func admitCompletedUpperCancellationTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input, currentEpoch string) (executioncancel.Receipt, bool, error) {
	empty := executioncancel.Receipt{}
	var taskID, epoch, authority string
	var generation int64
	var terminal bool
	err := tx.QueryRowContext(ctx, `SELECT v.task_id,COALESCE(g.runtime_epoch,''),g.generation,a.epoch_id,
 r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED') AND g.state IN ('CLOSED','CANCELED')
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id)
 FROM reviews v JOIN runs r ON r.id=v.linked_run_id JOIN review_grants g ON g.review_id=v.id JOIN runtime_authority a ON a.singleton=1
 WHERE v.id=? AND v.executor_kind='UPPER_LLM' AND v.project_id=? AND r.id=? AND r.review_id=v.id
 AND r.operation_kind='REVIEW' AND r.operation_id=v.id AND r.project_id=v.project_id AND r.task_id=v.task_id
 AND r.feature_area_id=v.feature_area_id AND g.project_id=v.project_id AND g.feature_area_id=v.feature_area_id
 AND g.run_id=r.id AND g.runtime_epoch=r.runtime_epoch`, in.OwnerID, in.ProjectID, in.RunID).Scan(&taskID, &epoch, &generation, &authority, &terminal)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	if !terminal {
		return empty, false, nil
	}
	if authority != currentEpoch || in.ExpectedUpperVersion.AuthorityEpoch != authority || in.ExpectedUpperVersion.GrantEpoch != epoch || in.ExpectedUpperVersion.GrantGeneration != generation {
		return empty, false, executioncancel.ErrRequestConflict
	}
	occupied, e := pendingCancellationOwnerTx(ctx, tx, in)
	if e != nil {
		return empty, false, e
	}
	if occupied {
		return empty, false, executioncancel.ErrRequestConflict
	}
	payload, e := json.Marshal(cancellationPayload(in))
	if e != nil {
		return empty, false, e
	}
	now := formatTime(time.Now().UTC())
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_cancellations(project_id,request_id,execution_id,run_id,requested_by_ai_session_id,requested_by_operator_id,reason,requested_at,runtime_epoch,state,interrupt_state,resolved_at,owner_type,owner_id,task_id,backend_disposition,request_payload_json) VALUES(?,?,NULL,?,?,?,?,?,?,'ALREADY_TERMINAL','NOT_SENT',?,'REVIEW',?,?,'NO_BACKEND',?)`, in.ProjectID, in.RequestID, in.RunID, nullString(in.AISessionID), nullString(in.OperatorPrincipalID), in.Reason, now, epoch, now, in.OwnerID, taskID, string(payload))
	if err != nil {
		return empty, false, executioncancel.ErrRequestConflict
	}
	value, err := scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	if err != nil {
		return empty, false, err
	}
	value.ExpectedUpperVersion = in.ExpectedUpperVersion
	result, e := json.Marshal(value)
	if e != nil {
		return empty, false, e
	}
	_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET result_json=? WHERE project_id=? AND request_id=? AND result_json IS NULL AND state='ALREADY_TERMINAL'`, string(result), in.ProjectID, in.RequestID)
	return value, true, err
}

// A legacy pending intent can have lost to a report before report admission
// learned the pending guard. Resolve only that intent; completed report/grant
// history and any released claim remain untouched.
func finishUpperCancellationAfterReportTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input, storedEpoch string) (executioncancel.Receipt, bool, error) {
	empty := executioncancel.Receipt{}
	var epoch string
	var generation int64
	var completed bool
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(g.runtime_epoch,''),g.generation,
 r.status='SUCCEEDED' AND g.state='CLOSED' AND EXISTS(SELECT 1 FROM review_reports p WHERE p.review_id=v.id AND p.run_id=r.id)
 FROM reviews v JOIN runs r ON r.id=v.linked_run_id JOIN review_grants g ON g.review_id=v.id
 WHERE v.id=? AND v.executor_kind='UPPER_LLM' AND v.project_id=? AND r.id=? AND r.review_id=v.id
 AND r.operation_kind='REVIEW' AND r.operation_id=v.id AND r.project_id=v.project_id AND r.task_id=v.task_id
 AND g.run_id=r.id AND g.runtime_epoch=r.runtime_epoch`, in.OwnerID, in.ProjectID, in.RunID).Scan(&epoch, &generation, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	if !completed {
		return empty, false, nil
	}
	if storedEpoch != epoch || (in.ExpectedUpperVersion != nil && (in.ExpectedUpperVersion.GrantEpoch != epoch || in.ExpectedUpperVersion.GrantGeneration != generation)) {
		return empty, false, executioncancel.ErrRequestConflict
	}
	changed, err := tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCEL_NOT_APPLIED',backend_disposition='NO_BACKEND',resolved_at=? WHERE project_id=? AND request_id=? AND owner_type='REVIEW' AND owner_id=? AND run_id=? AND runtime_epoch=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')`, formatTime(time.Now().UTC()), in.ProjectID, in.RequestID, in.OwnerID, in.RunID, epoch)
	if err != nil {
		return empty, false, err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return empty, false, executioncancel.ErrRequestConflict
	}
	value, err := scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	if err != nil {
		return empty, false, err
	}
	if in.ExpectedUpperVersion != nil {
		value.ExpectedUpperVersion = in.ExpectedUpperVersion
		encoded, e := json.Marshal(value)
		if e != nil {
			return empty, false, e
		}
		_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET result_json=? WHERE project_id=? AND request_id=? AND result_json IS NULL AND request_payload_json IS NOT NULL AND state='CANCEL_NOT_APPLIED'`, string(encoded), in.ProjectID, in.RequestID)
	}
	return value, true, err
}

func terminalizeUpperCancellationTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input, f upperCancellationFacts, now time.Time) error {
	r := f.run
	var admitted bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE project_id=? AND request_id=? AND owner_type='REVIEW' AND owner_id=? AND run_id=? AND task_id=? AND runtime_epoch=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))`, in.ProjectID, in.RequestID, in.OwnerID, r.ID, r.TaskID, f.version.GrantEpoch).Scan(&admitted)
	if err != nil {
		return err
	}
	if !admitted {
		return executioncancel.ErrRequestConflict
	}
	// Preserve terminal failures as evidence. A successful report/Run was already
	// rejected by the shared facts and is repeated in the mutation guard.
	if !terminalAgentRun(r.Status) {
		changed, e := tx.ExecContext(ctx, `UPDATE runs SET status='CANCELED',finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND project_id=? AND feature_area_id=? AND task_id=? AND review_id=? AND operation_kind='REVIEW' AND operation_id=? AND runtime_epoch=? AND status IN ('CREATED','PREPARING','RUNNING','COLLECTING') AND NOT EXISTS(SELECT 1 FROM review_reports WHERE review_id=? OR run_id=?)`, formatTime(now), formatTime(now), r.ID, r.ProjectID, r.FeatureAreaID, r.TaskID, r.ReviewID, r.OperationID, f.version.GrantEpoch, r.ReviewID, r.ID)
		if e != nil {
			return e
		}
		if n, _ := changed.RowsAffected(); n != 1 {
			return executioncancel.ErrRequestConflict
		}
	}
	changed, err := tx.ExecContext(ctx, `UPDATE review_grants SET state='CANCELED' WHERE review_id=? AND project_id=? AND feature_area_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND state=? AND NOT EXISTS(SELECT 1 FROM review_reports WHERE review_id=? OR run_id=?)`, r.ReviewID, r.ProjectID, r.FeatureAreaID, r.ID, f.version.GrantEpoch, f.version.GrantGeneration, f.grantState, r.ReviewID, r.ID)
	if err != nil {
		return err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return executioncancel.ErrRequestConflict
	}
	changed, err = tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE feature_area_id=? AND operation_kind='REVIEW' AND operation_id=? AND run_id=? AND runtime_epoch=?`, r.FeatureAreaID, r.ReviewID, r.ID, f.version.GrantEpoch)
	if err != nil {
		return err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return executioncancel.ErrClaimMismatch
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED',updated_at=? WHERE id=? AND project_id=? AND feature_area_id=? AND current_review_id=? AND status='ACTIVE' AND phase='REVIEWING'`, formatTime(now), r.TaskID, r.ProjectID, r.FeatureAreaID, r.ReviewID); err != nil {
		return err
	}
	changed, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCELED',backend_disposition='NO_BACKEND',resolved_at=? WHERE project_id=? AND request_id=? AND owner_type='REVIEW' AND owner_id=? AND run_id=? AND task_id=? AND runtime_epoch=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')`, formatTime(now), in.ProjectID, in.RequestID, r.ReviewID, r.ID, r.TaskID, f.version.GrantEpoch)
	if err != nil {
		return err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return executioncancel.ErrRequestConflict
	}
	return nil
}

// convergePendingUpperCancellationTx is also used by startup's existing orphan
// convergence seam, so no Upper intent can fall through generic Run-only writes.
func convergePendingUpperCancellationTx(ctx context.Context, tx *sql.Tx, r run.Run, currentEpoch string) (bool, error) {
	if r.OperationKind != run.OperationReview {
		return false, nil
	}
	upper, err := upperReviewExecutorTx(ctx, tx, executioncancel.Input{OwnerType: "REVIEW", OwnerID: r.OperationID})
	if err != nil || !upper {
		return upper, err
	}
	var in executioncancel.Input
	var raw, epoch string
	err = tx.QueryRowContext(ctx, `SELECT project_id,request_id,COALESCE(requested_by_ai_session_id,''),COALESCE(requested_by_operator_id,''),reason,runtime_epoch,COALESCE(request_payload_json,'') FROM execution_cancellations WHERE owner_type='REVIEW' AND owner_id=? AND run_id=? AND task_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')`, r.OperationID, r.ID, r.TaskID).Scan(&in.ProjectID, &in.RequestID, &in.AISessionID, &in.OperatorPrincipalID, &in.Reason, &epoch, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return true, executioncancel.ErrRequestConflict
	}
	if err != nil {
		return true, err
	}
	in.OwnerType, in.OwnerID, in.RunID = "REVIEW", r.OperationID, r.ID
	if raw != "" {
		var stored executioncancel.Input
		if err = json.Unmarshal([]byte(raw), &stored); err != nil {
			return true, err
		}
		in.ExpectedUpperVersion = stored.ExpectedUpperVersion
		if !cancellationPayloadMatches(raw, in) {
			return true, executioncancel.ErrRequestConflict
		}
	}
	_, err = cancelUpperReviewTx(ctx, tx, in, currentEpoch, true, epoch)
	return true, err
}
