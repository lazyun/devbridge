package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
)

// A returned observation failure is durable evidence that the owned preparer
// has stopped. An absent receipt or a failed external Create is not evidence.
func (s *RunStore) SettleRecoveryPreparationFailure(ctx context.Context, recoveryID string, generation int, f runtimepreparation.FailureSettlement) error {
	if f.Stage != "MEMBER_OBSERVATION" || f.Code != "RECOVERY_PREPARATION_FAILED" || f.Message == "" || f.RuntimeEpoch != s.db.epoch || f.At.IsZero() || len(f.Artifacts) == 0 {
		return rs.ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := recoveryReadTx(ctx, tx, recoveryID)
	if err != nil {
		return err
	}
	if v.Spec.Generation != generation || v.RecoveryRunID != f.RunID || v.RuntimeEpoch != f.RuntimeEpoch || v.RecoveryTurnID != "" {
		return rs.ErrConflict
	}
	artifacts := append([]runtimepreparation.FailureArtifact(nil), f.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].MemberID < artifacts[j].MemberID })
	raw, err := json.Marshal(artifacts)
	if err != nil {
		return err
	}
	admission, err := json.Marshal(v.Admission)
	if err != nil {
		return err
	}
	var saved, savedAdmission string
	err = tx.QueryRowContext(ctx, `SELECT artifact_inventory_hash,source_admission_hash FROM recovery_child_preparation_failures WHERE recovery_id=? AND recovery_generation=? AND plan_id=? AND run_id=? AND preparation_request_id=? AND preparation_payload_hash=? AND runtime_epoch=? AND failed_stage=? AND failure_code=? AND message=?`, recoveryID, generation, f.PlanID, f.RunID, f.RequestID, f.PayloadHash, f.RuntimeEpoch, f.Stage, f.Code, f.Message).Scan(&saved, &savedAdmission)
	if err == nil {
		if saved != rs.Hash(raw) || savedAdmission != rs.Hash(admission) {
			return rs.ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if v.State != "RUN_CREATED" || !frozenRecoveryPreparationAdmission(v.Admission) {
		return rs.ErrIneligible
	}
	if _, _, err = NewRecoveryStatusStore(s.db).validateSourceTx(ctx, tx, v.Spec.Source); err != nil {
		return err
	}
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, tx, f.PlanID)
	if err != nil {
		return err
	}
	if plan.OwnerID != recoveryID || plan.OwnerKind != runtimepreparation.OwnerRecoveryStatus || plan.Generation != generation {
		return rs.ErrConflict
	}
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		if seen[artifact.MemberID] || artifact.Root == "" || artifact.WorktreeID == "" {
			return rs.ErrConflict
		}
		seen[artifact.MemberID] = true
		var exact bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members m JOIN workspaces w ON w.id=m.workspace_id JOIN worktrees tree ON tree.id=m.worktree_id
 WHERE w.owner_kind='RECOVERY_STATUS' AND w.owner_id=? AND m.id=? AND tree.id=? AND tree.path=? AND tree.run_id=? AND tree.status='READY'
 AND EXISTS(SELECT 1 FROM runtime_scope_plans p,json_each(p.repository_targets_json) target WHERE p.plan_id=? AND json_extract(target.value,'$.memberId')=m.id AND json_extract(target.value,'$.repositoryId')=m.repository_id))`, recoveryID, artifact.MemberID, artifact.WorktreeID, artifact.Root, f.RunID, f.PlanID).Scan(&exact)
		if err != nil {
			return err
		}
		if !exact {
			return rs.ErrConflict
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM worktrees WHERE run_id=?`, f.RunID).Scan(&count); err != nil {
		return err
	}
	if count != len(artifacts) {
		return rs.ErrConflict
	}
	var attempt string
	if err = tx.QueryRowContext(ctx, `SELECT attempt_id FROM runtime_preparation_attempts WHERE plan_id=? AND run_id=?`, f.PlanID, f.RunID).Scan(&attempt); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_child_preparation_failures(recovery_id,recovery_generation,plan_id,run_id,attempt_id,preparation_request_id,preparation_payload_hash,runtime_epoch,failed_stage,failure_code,message,external_result,quiescence_proof,artifact_inventory_json,artifact_inventory_hash,source_admission_hash,recorded_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,'DEFINITE_FAILURE','SYNCHRONOUS_PREPARER_RETURNED',?,?,?,?)`, recoveryID, generation, f.PlanID, f.RunID, attempt, f.RequestID, f.PayloadHash, f.RuntimeEpoch, f.Stage, f.Code, f.Message, string(raw), rs.Hash(raw), rs.Hash(admission), formatTime(f.At))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET status='INFRASTRUCTURE_FAILED',failure_class='INFRASTRUCTURE',failure_code=?,failure_message=?,finished_at=?,updated_at=? WHERE id=?`, f.Code, f.Message, formatTime(f.At), formatTime(f.At), f.RunID)
	if err != nil {
		return err
	}
	v.State, v.FailureReason, v.UpdatedAt = "FAILED", f.Code, f.At.UTC()
	v.CompletedAt = &v.UpdatedAt
	if err = saveRecoveryTx(ctx, tx, v); err != nil {
		return err
	}
	return tx.Commit()
}

// Immutable failure facts prove the exact no-dispatch child in both the live
// cancellation writer and historical progression reads. They never establish
// consumption of an old source answer or continuation safety.
func recoveryPreparationFailureEvidence(ctx context.Context, q definitionQuerier, v rs.Record) (string, error) {
	var plan, inventory, hash, admissionHash string
	err := q.QueryRowContext(ctx, `SELECT f.plan_id,f.artifact_inventory_json,f.artifact_inventory_hash,f.source_admission_hash
 FROM recovery_child_preparation_failures f JOIN recovery_child_admissions a ON a.recovery_id=f.recovery_id AND a.child_plan_id=f.plan_id
 JOIN runtime_scope_plans p ON p.plan_id=f.plan_id JOIN runtime_preparation_attempts attempt ON attempt.attempt_id=f.attempt_id AND attempt.plan_id=p.plan_id AND attempt.run_id=f.run_id
 JOIN runtime_workspace_preparation_claims claim ON claim.request_id=f.preparation_request_id AND claim.payload_hash=f.preparation_payload_hash AND claim.plan_id=f.plan_id AND claim.run_id=f.run_id
 JOIN runs child ON child.id=f.run_id JOIN runs source ON source.id=a.source_run_id
 WHERE f.recovery_id=? AND f.recovery_generation=? AND a.recovery_generation=f.recovery_generation AND f.run_id=? AND f.runtime_epoch=? AND child.runtime_epoch=f.runtime_epoch
 AND p.owner_kind='RECOVERY_STATUS' AND p.owner_id=f.recovery_id AND p.generation=f.recovery_generation AND p.source_command='RECOVERY_STATUS_CHILD'
 AND a.source_run_id=? AND a.source_turn_id=? AND a.source_thread_id=? AND a.source_runtime_workset_hash=? AND a.source_workspace_id=source.workspace_id
 AND source.project_id=? AND source.task_id=? AND source.operation_kind=? AND source.operation_id=? AND source.thread_id=a.source_thread_id AND source.turn_id=a.source_turn_id
 AND child.purpose='RECOVERY_STATUS' AND child.project_id=source.project_id AND child.task_id=source.task_id AND child.operation_kind=source.operation_kind AND child.operation_id=source.operation_id
 AND child.thread_id IS NULL AND child.turn_id IS NULL AND child.runtime_workset_json IS NULL
 AND NOT EXISTS(SELECT 1 FROM recovery_child_thread_attempts x WHERE x.recovery_id=f.recovery_id)
 AND NOT EXISTS(SELECT 1 FROM recovery_child_turn_attempts x WHERE x.recovery_id=f.recovery_id)
 AND NOT EXISTS(SELECT 1 FROM runtime_workset_bindings x WHERE x.run_id=child.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_start_authorities x WHERE x.run_id=child.id)
 AND NOT EXISTS(SELECT 1 FROM app_server_run_bindings x WHERE x.run_id=child.id)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries x WHERE x.run_id=child.id)`, v.ID, v.Spec.Generation, v.RecoveryRunID, v.RuntimeEpoch, v.Spec.RunID, v.Spec.TurnID, v.Spec.ThreadID, v.Spec.RuntimeWorksetHash, v.Spec.ProjectID, v.Spec.TaskID, v.Spec.OperationKind, v.Spec.OperationID).Scan(&plan, &inventory, &hash, &admissionHash)
	if err != nil {
		return "", err
	}
	admission, err := json.Marshal(v.Admission)
	if err != nil {
		return "", err
	}
	if rs.Hash([]byte(inventory)) != hash || rs.Hash(admission) != admissionHash || v.RecoveryTurnID != "" || !frozenRecoveryPreparationAdmission(v.Admission) {
		return "", rs.ErrConflict
	}
	return plan, nil
}

// The source was admitted before this fenced child preparation. Once the
// synchronous failure is settled, historical evidence never expires by clock;
// its exact admission hash and absence of child dispatch remain mandatory.
func frozenRecoveryPreparationAdmission(a rs.Admission) bool {
	return a.SourceTurnTerminal && a.ThreadIdle && a.NoPendingRequests && a.Quiesced && json.Valid(a.Before) && !a.ObservedAt.IsZero()
}

func (s *RecoveryStatusStore) abandonFailedPreparationTx(ctx context.Context, tx *sql.Tx, v *rs.Record, in executioncancel.Input) (executioncancel.Receipt, error) {
	var empty executioncancel.Receipt
	if v.State != "FAILED" || v.FailureReason != "RECOVERY_PREPARATION_FAILED" {
		return empty, executioncancel.ErrOutcomeUnknown
	}
	plan, err := recoveryPreparationFailureEvidence(ctx, tx, *v)
	if err != nil {
		return empty, err
	}
	if _, _, err = s.validateSourceTx(ctx, tx, v.Spec.Source); err != nil {
		return empty, err
	}
	var current bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_current_generations WHERE recovery_id=? AND generation=?)`, v.ID, v.Spec.Generation).Scan(&current); err != nil {
		return empty, err
	}
	if !current {
		return empty, rs.ErrConflict
	}
	now := time.Now().UTC()
	payload, err := json.Marshal(in)
	if err != nil {
		return empty, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_cancellations(project_id,request_id,execution_id,run_id,requested_by_ai_session_id,requested_by_operator_id,reason,requested_at,runtime_epoch,state,interrupt_state,owner_type,owner_id,task_id,backend_disposition,request_payload_json)
 VALUES(?,?,?,?,?,?,?,?,?,'CANCEL_REQUESTED','NOT_SENT',?,?,?,'NO_BACKEND',?)`, in.ProjectID, in.RequestID, nullString(in.ExecutionID), v.RecoveryRunID, nullString(in.AISessionID), nullString(in.OperatorPrincipalID), in.Reason, formatTime(now), s.db.epoch, in.OwnerType, in.OwnerID, v.Spec.TaskID, string(payload))
	if err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET status='CANCELED',finished_at=COALESCE(finished_at,?),updated_at=? WHERE id IN (?,?)`, formatTime(now), formatTime(now), v.Spec.RunID, v.RecoveryRunID); err != nil {
		return empty, err
	}
	if err = terminalizeCancellationOwnerTx(ctx, tx, v.Spec.OperationKind, v.Spec.OperationID, v.Spec.TaskID, now); err != nil {
		return empty, err
	}
	if v.Spec.OwnerKind == "PROJECT_INSPECTION" {
		if _, err = tx.ExecContext(ctx, `UPDATE context_probes SET status='CANCELED',runtime_limitation='CANCELED',finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND status='RUNNING'`, formatTime(now), formatTime(now), v.Spec.OperationID); err != nil {
			return empty, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='CANCELED',updated_at=? WHERE id=? AND work_owner_id=? AND status='ACTIVE'`, formatTime(now), v.Spec.TaskID, v.Spec.OperationID); err != nil {
			return empty, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCELED',resolved_at=? WHERE project_id=? AND request_id=?`, formatTime(now), in.ProjectID, in.RequestID); err != nil {
		return empty, err
	}
	receipt, err := scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	if err != nil {
		return empty, err
	}
	receipt.QuiescenceState, receipt.RecoveryRequired = "QUIESCED", false
	raw, err := json.Marshal(receipt)
	if err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET result_json=? WHERE project_id=? AND request_id=?`, string(raw), in.ProjectID, in.RequestID); err != nil {
		return empty, err
	}
	v.State, v.FailureReason, v.Resolution, v.UpdatedAt = "FAILED", "RECOVERY_CANCELED", raw, now
	v.CompletedAt = &now
	if err = saveRecoveryTx(ctx, tx, *v); err != nil {
		return empty, err
	}
	if v.Spec.OwnerKind == "PROJECT_INSPECTION" {
		_, err = tx.ExecContext(ctx, `DELETE FROM project_inspection_agent_claims WHERE inspection_id=? AND run_id=? AND runtime_epoch=?`, v.Spec.OperationID, v.Spec.RunID, v.Spec.RuntimeEpoch)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=? AND operation_id=?`, v.Spec.RunID, v.Spec.OperationID)
	}
	if err != nil {
		return empty, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_child_preparation_abandonments(recovery_id,plan_id,run_id,project_id,request_id,payload_hash,result_json,recorded_at) VALUES(?,?,?,?,?,?,?,?)`, v.ID, plan, v.RecoveryRunID, in.ProjectID, in.RequestID, rs.Hash(payload), string(raw), formatTime(now))
	if err == nil {
		err = reconcileCanceledRecoveryTaskTx(ctx, tx, v.Spec.TaskID, now)
	}
	return receipt, err
}

func validateRecoveryPreparationAbandonment(ctx context.Context, q definitionQuerier, v rs.Record) error {
	plan, err := recoveryPreparationFailureEvidence(ctx, q, v)
	if err != nil {
		return err
	}
	var receipt executioncancel.Receipt
	if v.State != "FAILED" || v.FailureReason != "RECOVERY_CANCELED" || v.CompletedAt == nil || json.Unmarshal(v.Resolution, &receipt) != nil || receipt.State != "CANCELED" || receipt.BackendDisposition != "NO_BACKEND" || receipt.BackendInterruptState != "NOT_SENT" || receipt.QuiescenceState != "QUIESCED" || receipt.RecoveryRequired || receipt.ProjectID != v.Spec.ProjectID || receipt.OwnerType != v.Spec.OperationKind || receipt.OwnerID != v.Spec.OperationID || receipt.RunID != v.RecoveryRunID || receipt.TaskID != v.Spec.TaskID {
		return rs.ErrConflict
	}
	var payload, hash string
	err = q.QueryRowContext(ctx, `SELECT c.request_payload_json,a.payload_hash FROM recovery_child_preparation_abandonments a
 JOIN execution_cancellations c ON c.project_id=a.project_id AND c.request_id=a.request_id
 JOIN runs child ON child.id=a.run_id JOIN runs source ON source.id=?
 WHERE a.recovery_id=? AND a.plan_id=? AND a.run_id=? AND a.project_id=? AND a.request_id=? AND a.result_json=? AND c.result_json=a.result_json
 AND c.owner_type=? AND c.owner_id=? AND c.task_id=? AND c.run_id=child.id AND c.state='CANCELED' AND c.backend_disposition='NO_BACKEND' AND c.interrupt_state='NOT_SENT'
 AND c.thread_id IS NULL AND c.turn_id IS NULL AND child.status='CANCELED' AND source.status='CANCELED'
 AND COALESCE(c.requested_by_ai_session_id,'')=? AND COALESCE(c.requested_by_operator_id,'')=?
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims claim WHERE claim.run_id=source.id)
 AND NOT EXISTS(SELECT 1 FROM project_inspection_agent_claims claim WHERE claim.run_id=source.id)`, v.Spec.RunID, v.ID, plan, v.RecoveryRunID, receipt.ProjectID, receipt.RequestID, string(v.Resolution), receipt.OwnerType, receipt.OwnerID, receipt.TaskID, receipt.RequestedByAISessionID, receipt.RequestedByOperatorID).Scan(&payload, &hash)
	if err != nil {
		return err
	}
	if rs.Hash([]byte(payload)) != hash {
		return rs.ErrConflict
	}
	return nil
}
