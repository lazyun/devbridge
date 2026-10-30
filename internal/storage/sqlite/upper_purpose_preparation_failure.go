package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
)

// SettleUpperPreparationFailure records the synchronous preparer's definite
// result. The writer independently proves exact ownership, retained artifacts,
// absence of dispatch/binding, and the original external claim.
func (s *RunStore) SettleUpperPreparationFailure(ctx context.Context, f runtimepreparation.FailureSettlement) error {
	if (f.Stage != "MEMBER_OBSERVATION" && f.Stage != "WORKSET_BINDING") || f.Code != "RUNTIME_WORKSPACE_PREPARATION_FAILED" || f.Message == "" || f.RuntimeEpoch != s.db.epoch || len(f.Artifacts) == 0 {
		return ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var reviewID, taskID, attempt string
	err = tx.QueryRowContext(ctx, `SELECT r.review_id,r.task_id,a.attempt_id FROM runs r JOIN runtime_preparation_attempts a ON a.run_id=r.id WHERE r.id=? AND a.plan_id=?`, f.RunID, f.PlanID).Scan(&reviewID, &taskID, &attempt)
	if err != nil {
		return err
	}
	if _, err = recoveryReviewPreparationMetadataTx(ctx, tx, f.PlanID, reviewID); err != nil {
		return err
	}
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, tx, f.PlanID)
	if err != nil {
		return err
	}
	sort.Slice(f.Artifacts, func(i, j int) bool { return f.Artifacts[i].MemberID < f.Artifacts[j].MemberID })
	seen := map[string]bool{}
	for _, a := range f.Artifacts {
		var exact bool
		for _, target := range plan.RepositoryTargets {
			if target.MemberID == a.MemberID {
				exact = true
			}
		}
		if !exact || seen[a.MemberID] || a.WorktreeID == "" || a.Root == "" {
			return ErrConflict
		}
		seen[a.MemberID] = true
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members m JOIN worktrees w ON w.id=m.worktree_id JOIN reviews v ON v.workspace_id=m.workspace_id WHERE v.id=? AND m.id=? AND w.id=? AND w.path=? AND w.status='READY')`, reviewID, a.MemberID, a.WorktreeID, a.Root).Scan(&exact)
		if err != nil || !exact {
			return ErrConflict
		}
	}
	raw, err := json.Marshal(f.Artifacts)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	var saved string
	err = tx.QueryRowContext(ctx, `SELECT artifact_inventory_hash FROM runtime_preparation_failures WHERE attempt_id=? AND plan_id=? AND run_id=? AND preparation_request_id=? AND preparation_payload_hash=? AND runtime_epoch=? AND failed_stage=? AND failure_code=? AND message=?`, attempt, f.PlanID, f.RunID, f.RequestID, f.PayloadHash, f.RuntimeEpoch, f.Stage, f.Code, f.Message).Scan(&saved)
	if err == nil {
		if saved != digest {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_preparation_failures(attempt_id,plan_id,run_id,preparation_request_id,preparation_payload_hash,owner_kind,owner_id,task_id,runtime_epoch,failed_stage,failure_code,message,external_result,quiescence_proof,artifact_inventory_json,artifact_inventory_hash,recorded_at) VALUES(?,?,?,?,?,'REVIEW',?,?,?,?,?,?,'DEFINITE_FAILURE','SYNCHRONOUS_PREPARER_RETURNED',?,?,?)`, attempt, f.PlanID, f.RunID, f.RequestID, f.PayloadHash, reviewID, taskID, f.RuntimeEpoch, f.Stage, f.Code, f.Message, string(raw), digest, formatTime(f.At))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_preparation_receipts(request_id,payload_hash,plan_id,run_id,outcome,failure_code,recorded_at) VALUES(?,?,?,?,'FAILED',?,?)`, "failed_"+f.RunID, f.PayloadHash, f.PlanID, f.RunID, f.Code, formatTime(f.At))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE review_grants SET preparation_error=? WHERE review_id=? AND run_id=? AND runtime_epoch=? AND state='PREPARING'`, f.Message+"; preparation settled; explicitly cancel the exact Task to abandon this failed attempt", reviewID, f.RunID, f.RuntimeEpoch)
	if err != nil {
		return err
	}
	return tx.Commit()
}

type upperFailedPreparation struct {
	plan, attempt, review, task, run, epoch string
	version                                 agentconversation.UpperVersion
}

func upperFailedPreparationTx(ctx context.Context, tx *sql.Tx, projectID, taskID, runID, currentEpoch string) (upperFailedPreparation, error) {
	var f upperFailedPreparation
	err := tx.QueryRowContext(ctx, `SELECT p.plan_id,f.attempt_id,v.id,t.id,r.id,r.runtime_epoch,g.runtime_epoch,g.generation,a.epoch_id
 FROM runtime_preparation_failures f JOIN runtime_scope_plans p ON p.plan_id=f.plan_id JOIN runs r ON r.id=f.run_id
 JOIN reviews v ON v.id=f.owner_id JOIN tasks t ON t.id=f.task_id JOIN review_grants g ON g.review_id=v.id JOIN runtime_authority a ON a.singleton=1
 JOIN feature_area_agent_claims claim ON claim.run_id=r.id AND claim.operation_kind='REVIEW' AND claim.operation_id=v.id AND claim.runtime_epoch=r.runtime_epoch
 WHERE r.project_id=? AND t.id=? AND (?='' OR r.id=?) AND p.source_command='APPLY_CONFIRMED_REVIEW_PURPOSE'
 AND v.executor_kind='UPPER_LLM' AND v.linked_run_id=r.id AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING'
 AND f.runtime_epoch=r.runtime_epoch AND g.run_id=r.id AND g.runtime_epoch=r.runtime_epoch AND g.state IN('PREPARING','RECOVERY_REQUIRED')
 AND r.status IN('CREATED','PREPARING') AND r.thread_id IS NULL AND r.turn_id IS NULL AND r.runtime_workset_json IS NULL
 AND NOT EXISTS(SELECT 1 FROM runtime_preparation_revocations x WHERE x.plan_id=p.plan_id)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM app_server_run_bindings b WHERE b.run_id=r.id)

 AND NOT EXISTS(SELECT 1 FROM runtime_workset_bindings b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_start_authorities b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM runs other WHERE other.task_id=t.id AND other.id<>r.id AND other.status NOT IN('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED'))
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(t.id,v.id) AND c.state IN('CANCEL_REQUESTED','RECOVERY_REQUIRED'))`, projectID, taskID, runID, runID).Scan(&f.plan, &f.attempt, &f.review, &f.task, &f.run, &f.epoch, &f.version.GrantEpoch, &f.version.GrantGeneration, &f.version.AuthorityEpoch)
	if err != nil {
		return f, err
	}
	if currentEpoch != f.version.AuthorityEpoch {
		return f, ErrConflict
	}
	if _, err = recoveryReviewPreparationMetadataTx(ctx, tx, f.plan, f.review); err != nil {
		return f, err
	}
	var inventory, digest string
	if err = tx.QueryRowContext(ctx, `SELECT artifact_inventory_json,artifact_inventory_hash FROM runtime_preparation_failures WHERE attempt_id=?`, f.attempt).Scan(&inventory, &digest); err != nil {
		return f, err
	}
	sum := sha256.Sum256([]byte(inventory))
	if hex.EncodeToString(sum[:]) != digest {
		return f, ErrConflict
	}
	return f, nil
}

// FailedUpperPreparationTaskCancellation exposes exactly the existing public
// TASK cancellation command; the command rechecks this observation atomically.
func (s *ExecutionCancellationStore) FailedUpperPreparationTaskCancellation(ctx context.Context, projectID, taskID string) (agentconversation.Eligibility, error) {
	out := agentconversation.Eligibility{}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	f, err := upperFailedPreparationTx(ctx, tx, projectID, taskID, "", s.db.epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err = guardTaskDeliveryCancellationTx(ctx, tx, executioncancel.Input{OwnerType: "TASK", OwnerID: taskID}); err != nil {
		return out, nil
	}
	out.Available = true
	out.Reason = "The exact failed workspace attempt is settled; cancel this Task to abandon it without repeating preparation."
	var area string
	if err = tx.QueryRowContext(ctx, `SELECT feature_area_id FROM tasks WHERE id=?`, taskID).Scan(&area); err != nil {
		return out, err
	}
	out.Target = &agentconversation.ActionTarget{FeatureAreaID: area, OperationKind: "TASK", OperationID: taskID, RunID: f.run, ExpectedUpperVersion: &f.version}
	return out, nil
}

func abandonUpperPurposeTaskPreparationTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input, currentEpoch string) (executioncancel.Receipt, bool, error) {
	empty := executioncancel.Receipt{}
	if in.OwnerType != "TASK" || in.RunID == "" {
		return empty, false, nil
	}
	var relevant bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_preparation_attempts a JOIN runtime_scope_plans p ON p.plan_id=a.plan_id JOIN reviews v ON v.id=p.owner_id WHERE a.run_id=? AND p.source_command='APPLY_CONFIRMED_REVIEW_PURPOSE' AND v.executor_kind='UPPER_LLM')`, in.RunID).Scan(&relevant); err != nil {
		return empty, true, err
	}
	if !relevant {
		return empty, false, nil
	}
	if !in.ExpectedUpperVersion.Valid() {
		return empty, true, executioncancel.ErrInvalidArgument
	}
	if err := guardTaskDeliveryCancellationTx(ctx, tx, in); err != nil {
		return empty, true, err
	}
	f, err := upperFailedPreparationTx(ctx, tx, in.ProjectID, in.OwnerID, in.RunID, currentEpoch)
	if err != nil {
		return empty, true, executioncancel.ErrRecoveryRequired
	}
	if !in.ExpectedUpperVersion.Equal(&f.version) {
		return empty, true, executioncancel.ErrRequestConflict
	}
	now := time.Now().UTC()
	at := formatTime(now)
	payload, err := json.Marshal(cancellationPayload(in))
	if err != nil {
		return empty, true, err
	}
	sum := sha256.Sum256(payload)
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_cancellations(project_id,request_id,run_id,requested_by_ai_session_id,requested_by_operator_id,reason,requested_at,runtime_epoch,state,interrupt_state,owner_type,owner_id,task_id,backend_disposition,request_payload_json) VALUES(?,?,?,?,?,?,?,?,'CANCEL_REQUESTED','NOT_SENT','TASK',?,?,'NO_BACKEND',?)`, in.ProjectID, in.RequestID, in.RunID, nullString(in.AISessionID), nullString(in.OperatorPrincipalID), in.Reason, at, f.epoch, in.OwnerID, f.task, string(payload))
	if err != nil {
		return empty, true, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_preparation_revocations(plan_id,run_id,attempt_id,project_id,request_id,recorded_at) VALUES(?,?,?,?,?,?)`, f.plan, f.run, f.attempt, in.ProjectID, in.RequestID, at)
	if err != nil {
		return empty, true, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET status='INFRASTRUCTURE_FAILED',failure_class='INFRASTRUCTURE',failure_code='RUNTIME_PREPARATION_ABANDONED',failure_message='Settled workspace preparation explicitly abandoned with its Task',finished_at=?,updated_at=? WHERE id=?`, at, at, f.run)
	if err != nil {
		return empty, true, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE review_grants SET state='CANCELED' WHERE review_id=? AND run_id=?`, f.review, f.run)
	if err != nil {
		return empty, true, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=? AND operation_id=? AND runtime_epoch=?`, f.run, f.review, f.epoch)
	if err != nil {
		return empty, true, err
	}
	if err = terminalizeCancellationOwnerTx(ctx, tx, "TASK", f.task, f.task, now); err != nil {
		return empty, true, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCELED',resolved_at=? WHERE project_id=? AND request_id=?`, at, in.ProjectID, in.RequestID)
	if err != nil {
		return empty, true, err
	}
	receipt, err := scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	if err != nil {
		return empty, true, err
	}
	receipt.ExpectedUpperVersion = in.ExpectedUpperVersion
	result, err := json.Marshal(receipt)
	if err != nil {
		return empty, true, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET result_json=? WHERE project_id=? AND request_id=?`, string(result), in.ProjectID, in.RequestID)
	if err != nil {
		return empty, true, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_preparation_abandonments(plan_id,run_id,attempt_id,project_id,request_id,payload_hash,owner_id,task_id,result_json,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, f.plan, f.run, f.attempt, in.ProjectID, in.RequestID, hex.EncodeToString(sum[:]), f.review, f.task, string(result), at)
	return receipt, true, err
}

// ReadSettledPreparationFailure reads the failure receipt independently of the
// successful earlier observation receipt. Neither historical slot is rewritten.
func (s *RunStore) ReadSettledPreparationFailure(ctx context.Context, runID string) (string, error) {
	var message string
	err := s.db.sql.QueryRowContext(ctx, `SELECT message FROM runtime_preparation_failures WHERE run_id=?`, runID).Scan(&message)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return message, err
}
