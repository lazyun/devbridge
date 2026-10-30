package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
)

// Historical evidence is checked against its recorded epoch, never the current
// writer epoch. An independent diagnostic cannot make its source Thread safe.
func readRecoveryDiagnosticEvidenceTx(ctx context.Context, q definitionQuerier, v rs.Record, finished bool) (child run.Run, independent bool, err error) {
	status := ""
	if finished {
		status = string(run.StatusSucceeded)
	}
	return readRecoveryDiagnosticStateEvidenceTx(ctx, q, v, status)
}

func readRecoveryDiagnosticStateEvidenceTx(ctx context.Context, q definitionQuerier, v rs.Record, terminalStatus string) (child run.Run, independent bool, err error) {
	if v.RecoveryRunID == "" {
		return child, false, rs.ErrIneligible
	}
	child, err = scanRun(q.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, v.RecoveryRunID))
	if err != nil {
		return
	}
	var initialEpoch string
	if err = q.QueryRowContext(ctx, `SELECT COALESCE((SELECT runtime_epoch FROM recovery_child_admissions WHERE recovery_id=?),'')`, v.ID).Scan(&initialEpoch); err != nil {
		return
	}
	admitted := initialEpoch != ""
	independent = admitted || child.ThreadID != v.Spec.ThreadID || child.RuntimeWorkset != nil && child.RuntimeWorkset.ProtocolVersion == workset.RuntimeProtocolVersionRecoveryStatus
	reject := func() (run.Run, bool, error) { return child, independent, rs.ErrIneligible }
	if child.ID == v.Spec.RunID || child.Purpose != run.PurposeRecoveryStatus || child.ProjectID != v.Spec.ProjectID || child.FeatureAreaID != v.Spec.FeatureAreaID || child.TaskID != v.Spec.TaskID || string(child.OperationKind) != v.Spec.OperationKind || child.OperationID != v.Spec.OperationID || child.RuntimeEpoch != v.RuntimeEpoch || child.ThreadID == "" || child.TurnID == "" || child.TurnID != v.RecoveryTurnID {
		return reject()
	}
	if terminalStatus != "" && (string(child.Status) != terminalStatus || child.FinishedAt == nil) {
		return reject()
	}
	if terminalStatus == "" && child.Status != run.StatusRunning && child.Status != run.StatusCollecting {
		return reject()
	}
	if !independent {
		var exact bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN agent_conversations a ON a.id=b.agent_conversation_id JOIN runs source ON source.id=? WHERE b.run_id=? AND b.agent_conversation_id=? AND a.backend_thread_id=? AND a.project_id=source.project_id AND a.feature_area_id=source.feature_area_id AND source.thread_id=a.backend_thread_id AND source.turn_id=? AND source.operation_id=? AND source.operation_kind=? AND source.runtime_workset_hash=?)`, v.Spec.RunID, child.ID, v.Spec.AgentConversationID, child.ThreadID, v.Spec.TurnID, v.Spec.OperationID, v.Spec.OperationKind, v.Spec.RuntimeWorksetHash).Scan(&exact)
		if err != nil {
			return
		}
		if !exact {
			return reject()
		}
		return
	}
	if !admitted || child.ThreadID == v.Spec.ThreadID || v.Spec.PayloadHash != v.Spec.ComputePayloadHash() || child.RuntimeWorkset == nil || child.RuntimeWorkset.Validate() != nil {
		return reject()
	}
	exactEpoch, epochErr := recoveryDiagnosticEpochChainTx(ctx, q, v.ID, child.ID, initialEpoch, v.RuntimeEpoch)
	if epochErr != nil {
		err = epochErr
		return
	}
	if !exactEpoch {
		return reject()
	}
	w := child.RuntimeWorkset
	if w.ProtocolVersion != workset.RuntimeProtocolVersionRecoveryStatus || w.RunID != child.ID || w.ProjectID != child.ProjectID || w.FeatureAreaID != child.FeatureAreaID || w.TaskID != child.TaskID || w.RecoveryStatusID != v.ID || w.RecoverySourceRunID != v.Spec.RunID || w.WorkspaceID != child.WorkspaceID || w.WorkspaceID == "" {
		return reject()
	}
	source, e := scanRun(q.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, v.Spec.RunID))
	if e != nil {
		err = e
		return
	}
	if source.ProjectID != v.Spec.ProjectID || source.FeatureAreaID != v.Spec.FeatureAreaID || source.TaskID != v.Spec.TaskID || source.OperationID != v.Spec.OperationID || string(source.OperationKind) != v.Spec.OperationKind || source.RuntimeEpoch != v.Spec.RuntimeEpoch || source.ThreadID != v.Spec.ThreadID || source.TurnID != v.Spec.TurnID || source.RuntimeWorkset == nil || source.RuntimeWorkset.Validate() != nil || source.RuntimeWorkset.ContentHash != v.Spec.RuntimeWorksetHash || source.WorkspaceID != source.RuntimeWorkset.WorkspaceID || child.WorkspaceID == source.WorkspaceID {
		return reject()
	}
	if source.RuntimeWorkset.RunID != source.ID || source.RuntimeWorkset.ProjectID != source.ProjectID || source.RuntimeWorkset.FeatureAreaID != source.FeatureAreaID {
		return reject()
	}
	if v.Spec.AgentConversationID != "" {
		var sourceContext bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN agent_conversations a ON a.id=b.agent_conversation_id WHERE b.run_id=? AND a.id=? AND a.project_id=? AND a.backend_thread_id=?)`, source.ID, v.Spec.AgentConversationID, source.ProjectID, source.ThreadID).Scan(&sourceContext)
		if err != nil {
			return
		}
		if !sourceContext {
			return reject()
		}
	}
	var planID, boundJSON, bindingEpoch, threadEpoch string
	err = q.QueryRowContext(ctx, `SELECT a.child_plan_id,b.workset_json,b.runtime_epoch,ta.runtime_epoch FROM recovery_child_admissions a
 JOIN recovery_status_records x ON x.id=a.recovery_id AND x.source_run_id=a.source_run_id AND x.source_turn_id=a.source_turn_id AND x.generation=a.recovery_generation AND x.runtime_epoch=? AND x.recovery_run_id=?
 JOIN runtime_scope_plans p ON p.plan_id=a.child_plan_id AND p.owner_kind='RECOVERY_STATUS' AND p.owner_id=a.recovery_id AND p.generation=a.recovery_generation AND p.preparation_request_id=a.admission_request_id AND p.preparation_payload_hash=a.admission_payload_sha256
 JOIN runtime_preparation_authorities auth ON auth.plan_id=p.plan_id AND auth.owner_kind=p.owner_kind AND auth.owner_id=p.owner_id AND auth.generation=p.generation AND auth.request_id=a.admission_request_id AND auth.payload_hash=a.admission_payload_sha256
 JOIN runtime_preparation_attempts attempt ON attempt.plan_id=p.plan_id AND attempt.run_id=? AND attempt.request_id=a.admission_request_id AND attempt.payload_hash=a.admission_payload_sha256
 JOIN runtime_preparation_receipts created ON created.plan_id=p.plan_id AND created.run_id=attempt.run_id AND created.request_id=attempt.request_id AND created.payload_hash=attempt.payload_hash AND created.outcome='RUN_CREATED'
 JOIN runtime_workset_bindings b ON b.plan_id=p.plan_id AND b.run_id=attempt.run_id AND b.owner_kind=p.owner_kind AND b.owner_id=p.owner_id AND b.workset_content_hash=?
 JOIN runtime_start_authorities start ON start.authority_id=b.start_authority_id AND start.plan_id=b.plan_id AND start.run_id=b.run_id AND start.owner_kind=b.owner_kind AND start.owner_id=b.owner_id AND start.runtime_epoch=b.runtime_epoch AND start.workset_content_hash=b.workset_content_hash AND start.observation_set_hash=b.observation_set_hash
 JOIN runtime_preparation_receipts bound ON bound.plan_id=b.plan_id AND bound.run_id=b.run_id AND bound.outcome='BINDING_COMMITTED' AND bound.workset_content_hash=b.workset_content_hash AND bound.start_authority_id=start.authority_id AND bound.observation_set_hash=b.observation_set_hash
 JOIN runtime_preparation_receipts observed ON observed.plan_id=b.plan_id AND observed.run_id=b.run_id AND observed.outcome='OBSERVATION_RECORDED' AND observed.observation_set_hash=b.observation_set_hash
 JOIN recovery_child_thread_attempts ta ON ta.recovery_id=a.recovery_id AND ta.child_run_id=b.run_id AND ta.child_runtime_workset_hash=b.workset_content_hash AND ta.child_workspace_id=?
 JOIN recovery_child_thread_receipts tr ON tr.request_id=ta.request_id AND tr.payload_sha256=ta.payload_sha256 AND tr.outcome='CREATED' AND tr.child_thread_id=?
 JOIN recovery_child_turn_attempts ua ON ua.recovery_id=a.recovery_id AND ua.thread_request_id=ta.request_id AND ua.child_thread_id=tr.child_thread_id
 JOIN recovery_child_turn_receipts ur ON ur.request_id=ua.request_id AND ur.payload_sha256=ua.payload_sha256 AND ur.outcome='DISPATCHED' AND ur.child_thread_id=ua.child_thread_id AND ur.child_turn_id=? AND ur.dispatch_attempt_id=ua.request_id
 WHERE a.recovery_id=? AND a.recovery_generation=? AND a.source_run_id=? AND a.source_turn_id=? AND a.source_thread_id=? AND a.source_runtime_workset_hash=? AND a.source_workspace_id=? AND a.source_app_server_instance_id=? AND a.runtime_epoch=?`, v.RuntimeEpoch, child.ID, child.ID, w.ContentHash, w.WorkspaceID, child.ThreadID, child.TurnID, v.ID, v.Spec.Generation, source.ID, source.TurnID, source.ThreadID, v.Spec.RuntimeWorksetHash, source.WorkspaceID, v.Spec.AppServerInstanceID, initialEpoch).Scan(&planID, &boundJSON, &bindingEpoch, &threadEpoch)
	if err != nil {
		return
	}
	for _, epoch := range []string{bindingEpoch, threadEpoch} {
		valid, e := recoveryDiagnosticEpochChainTx(ctx, q, v.ID, child.ID, epoch, v.RuntimeEpoch)
		if e != nil {
			err = e
			return
		}
		if !valid {
			return reject()
		}
	}
	var archived workset.RuntimeWorkset
	if json.Unmarshal([]byte(boundJSON), &archived) != nil || !reflect.DeepEqual(archived, *w) {
		return reject()
	}
	p, e := readArchivedRuntimeSelectionPlanTx(ctx, q, planID)
	if e != nil {
		err = e
		return
	}
	if p.OwnerKind != runtimepreparation.OwnerRecoveryStatus || p.OwnerID != v.ID || p.ProjectID != source.ProjectID || p.FeatureAreaID != source.FeatureAreaID || p.TaskID != source.TaskID || p.Generation != v.Spec.Generation || p.SourceCommand != "RECOVERY_STATUS_CHILD" || p.SourceReceiptID != p.PreparationRequestID || p.ProposalHash != v.Spec.PayloadHash || p.TaskDefinitionRevisionID != v.Spec.TaskDefinitionRevisionID || p.ID != "runtime_scope_recovery_child_"+workflowHash(v.ID + "\x00" + p.PreparationRequestID)[:32] || p.PrimaryMemberID != w.PrimaryMemberID || p.BackendIdentity != w.BackendConfigID || p.AgentID != w.AgentID || p.AgentConfigFingerprint != w.AgentConfigFingerprint || p.ExecutionPolicy != w.ExecutionPolicy || p.Model != w.Model || p.ReasoningEffort != w.ReasoningEffort || p.RuntimeAdapterIdentity != w.RuntimeAdapterIdentity || len(p.RepositoryTargets) != len(w.Members) || len(w.Members) != len(source.RuntimeWorkset.Members) {
		return reject()
	}
	sourceMembers := map[string]workset.RuntimeMemberBinding{}
	for _, m := range source.RuntimeWorkset.Members {
		sourceMembers[m.RepositoryID] = m
	}
	for _, m := range w.Members {
		sm, ok := sourceMembers[m.RepositoryID]
		if !ok || m.MemberID != "member_recovery_"+workflowHash(v.ID + "\x00" + m.RepositoryID)[:32] || m.MemberID == sm.MemberID || m.CanonicalRoot == sm.CanonicalRoot || m.WorktreeID == sm.WorktreeID || m.OriginCommit != sm.OriginCommit {
			return reject()
		}
		matched := false
		for _, target := range p.RepositoryTargets {
			if target.RepositoryID == m.RepositoryID && target.MemberID == m.MemberID && target.MemberVersion == m.MemberVersion && target.OriginCommit == m.OriginCommit && target.Read && target.Write.Kind == "NONE" {
				matched = true
			}
		}
		if !matched {
			return reject()
		}
		var observed bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_workspace_observations WHERE plan_id=? AND run_id=? AND workspace_id=? AND repository_id=? AND member_id=? AND worktree_id=? AND canonical_root=? AND root_identity=? AND start_head=? AND start_snapshot=?)`, p.ID, child.ID, w.WorkspaceID, m.RepositoryID, m.MemberID, m.WorktreeID, m.CanonicalRoot, m.RootFileIdentity, m.StartHEAD, m.StartSnapshot).Scan(&observed)
		if err != nil {
			return
		}
		if !observed || m.RuntimeRootIdentity != m.RootFileIdentity {
			return reject()
		}
	}
	return
}

func finishRecoveryDiagnosticTx(ctx context.Context, tx *sql.Tx, v *rs.Record, now time.Time) error {
	if child, e := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, v.RecoveryRunID)); e == nil && child.ResultProtocol == run.NaturalResultProtocol && child.Status == run.StatusSucceeded {
		_, _, e = readRecoveryDiagnosticEvidenceTx(ctx, tx, *v, true)
		return e
	}
	child, independent, err := readRecoveryDiagnosticEvidenceTx(ctx, tx, *v, false)
	if err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runs SET status='SUCCEEDED',finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND status IN ('RUNNING','COLLECTING') AND thread_id=? AND turn_id=?`, formatTime(now), formatTime(now), child.ID, child.ThreadID, child.TurnID)
	if err != nil {
		return err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return rs.ErrIneligible
	}
	if !independent {
		if _, err = tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET advance_state='KNOWN_ADVANCE',exit_boundary_turn_id=?,quiesced=1 WHERE run_id=? AND agent_conversation_id=?`, child.TurnID, child.ID, v.Spec.AgentConversationID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET current_boundary_turn_id=?,continuation_safe=1 WHERE id=? AND ended_at IS NULL AND backend_thread_id=?`, child.TurnID, v.Spec.AgentConversationID, child.ThreadID); err != nil {
			return err
		}
	}
	return finalizeRecoveryUsageTx(ctx, tx, child.ID, now)
}

// Only a completed engineering recovery or exact completed owner cancellation
// can release its historical source fence. Pending revised-purpose
// confirmations remain fenced until their consumption authority is complete.
func recoveryIndependentResolvedSourceRunsTx(ctx context.Context, q definitionQuerier, areaID string) ([]string, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_group_array(x.id),'[]') FROM recovery_status_records x JOIN recovery_child_admissions a ON a.recovery_id=x.id JOIN runs source ON source.id=x.source_run_id WHERE source.feature_area_id=? AND ((x.state='RESOLVED' AND json_extract(x.record_json,'$.assessment.engineeringDisposition') IN ('RECOVERED_COMPLETE','RECOVERED_PARTIAL')) OR (x.state='FAILED' AND json_extract(x.record_json,'$.failureReason')='RECOVERY_CANCELED'))`, areaID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var ids, out []string
	if err = json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	for _, id := range ids {
		v, err := scanRecovery(q.QueryRowContext(ctx, `SELECT record_json FROM recovery_status_records WHERE id=?`, id))
		if err != nil {
			return nil, err
		}
		if v.State == "FAILED" && v.FailureReason == "RECOVERY_CANCELED" {
			if err = validateIndependentRecoveryCancellationTx(ctx, q, v); err != nil {
				return nil, err
			}
			out = append(out, v.Spec.RunID)
			continue
		}
		if err = validateRecoveryResolutionEvidence(v); err != nil {
			return nil, err
		}
		if _, independent, err := readRecoveryDiagnosticEvidenceTx(ctx, q, v, true); err != nil {
			return nil, err
		} else if !independent {
			return nil, rs.ErrIneligible
		}
		var assessment rs.Assessment
		if err = json.Unmarshal(v.Assessment, &assessment); err != nil {
			return nil, err
		}
		status := "SUCCEEDED"
		if assessment.EngineeringDisposition == "RECOVERED_PARTIAL" {
			status = "FAILED"
		}
		var sourceClosed bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r WHERE r.id=? AND r.status=? AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id))`, v.Spec.RunID, status).Scan(&sourceClosed)
		if err != nil {
			return nil, err
		}
		if !sourceClosed {
			return nil, rs.ErrIneligible
		}
		out = append(out, v.Spec.RunID)
	}
	return out, nil
}

// Explicit cancellation closes the owner without declaring its unknown source
// conversation safe. Only the exact completed child and frozen cancellation
// receipt release the historical source fence; unrelated uncertainty remains.
func validateIndependentRecoveryCancellationTx(ctx context.Context, q definitionQuerier, v rs.Record) error {
	if v.RecoveryTurnID == "" {
		return validateRecoveryPreparationAbandonment(ctx, q, v)
	}
	child, independent, err := readRecoveryDiagnosticStateEvidenceTx(ctx, q, v, string(run.StatusCanceled))
	if err != nil || !independent {
		return rs.ErrIneligible
	}
	var receipt executioncancel.Receipt
	if v.CompletedAt == nil || json.Unmarshal(v.Resolution, &receipt) != nil || receipt.State != "CANCELED" || receipt.QuiescenceState != "QUIESCED" || receipt.BackendDisposition != "TERMINATED" || receipt.ProjectID != v.Spec.ProjectID || receipt.TaskID != v.Spec.TaskID || receipt.OwnerType != v.Spec.OperationKind || receipt.OwnerID != v.Spec.OperationID || receipt.RunID != child.ID || receipt.RequestID == "" {
		return rs.ErrIneligible
	}
	if receipt.BackendInterruptState != "REQUESTED" && receipt.BackendInterruptState != "ACKNOWLEDGED" && receipt.BackendInterruptState != "OUTCOME_UNKNOWN" {
		return rs.ErrIneligible
	}
	var exact bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations c JOIN runs source ON source.id=?
 WHERE c.project_id=? AND c.request_id=? AND c.run_id=? AND c.owner_type=? AND c.owner_id=? AND c.task_id=?
 AND c.state='CANCELED' AND c.interrupt_state=? AND c.terminal_observed=1 AND c.backend_disposition='TERMINATED'
 AND c.thread_id=? AND c.turn_id=? AND c.result_json=? AND source.status='CANCELED'
 AND COALESCE(c.requested_by_ai_session_id,'')=? AND COALESCE(c.requested_by_operator_id,'')=?
 AND EXISTS(SELECT 1 FROM runtime_epochs e WHERE e.id=c.runtime_epoch)
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims claim WHERE claim.run_id=source.id))`, v.Spec.RunID, receipt.ProjectID, receipt.RequestID, child.ID, receipt.OwnerType, receipt.OwnerID, receipt.TaskID, receipt.BackendInterruptState, child.ThreadID, child.TurnID, string(v.Resolution), receipt.RequestedByAISessionID, receipt.RequestedByOperatorID).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return rs.ErrIneligible
	}
	return nil
}

func validateRecoveryResolutionEvidence(v rs.Record) error {
	var a rs.Assessment
	var receipt struct{ RequestID, ActorID, PayloadHash, AssessmentHash, ReportHash string }
	if v.State != "RESOLVED" || v.CompletedAt == nil || json.Unmarshal(v.Assessment, &a) != nil || a.Consistency != "CONSISTENT" || (a.EngineeringDisposition != "RECOVERED_COMPLETE" && a.EngineeringDisposition != "RECOVERED_PARTIAL") || rs.Hash(v.Assessment) != v.AssessmentHash || rs.Hash(v.Report) != v.ReportHash || json.Unmarshal(v.Resolution, &receipt) != nil || receipt.RequestID == "" || receipt.ActorID == "" || receipt.AssessmentHash != v.AssessmentHash || receipt.ReportHash != v.ReportHash {
		return rs.ErrIneligible
	}
	in := rs.ResolveInput{ID: v.ID, RequestID: receipt.RequestID, ActorID: receipt.ActorID, RuntimeEpoch: v.RuntimeEpoch, AssessmentHash: v.AssessmentHash, ReportHash: v.ReportHash}
	if receipt.PayloadHash != in.ComputePayloadHash() {
		return rs.ErrIneligible
	}
	return nil
}

// Only recorded adoptions for this exact Recovery and Run may bridge the
// immutable preparation epoch. UNION bounds corrupted cyclic archives.
func recoveryDiagnosticEpochChainTx(ctx context.Context, q definitionQuerier, recoveryID, runID, initial, current string) (bool, error) {
	var valid bool
	err := q.QueryRowContext(ctx, `WITH RECURSIVE epochs(epoch) AS (
 SELECT ?
 UNION SELECT a.previous_epoch FROM recovery_status_adoptions a JOIN epochs e ON e.epoch=a.current_epoch WHERE a.recovery_id=? AND a.run_id=?
 UNION SELECT a.previous_epoch FROM recovery_child_preparation_adoptions a JOIN epochs e ON e.epoch=a.current_epoch JOIN recovery_child_admissions admitted ON admitted.recovery_id=a.recovery_id AND admitted.child_plan_id=a.child_plan_id WHERE a.recovery_id=?
 ) SELECT EXISTS(SELECT 1 FROM epochs WHERE epoch=?)`, current, recoveryID, runID, recoveryID, initial).Scan(&valid)
	return valid, err
}
