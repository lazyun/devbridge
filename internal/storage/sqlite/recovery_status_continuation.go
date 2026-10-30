package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/executionproposal"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func validateRecoveryContinuationTx(ctx context.Context, tx *sql.Tx, current task.Task, predecessor executionproposal.Proposal, in wp.TaskAcceptanceInput) (*rs.Record, error) {
	if in.RecoveryStatusID == "" && in.RecoveryContinuationInstruction == "" {
		return nil, nil
	}
	if in.Option != wp.AcceptanceRetryImplementation || in.RecoveryStatusID == "" || in.ExecutionInstructions != nil || len(in.RecoveryContinuationInstruction) == 0 || len(in.RecoveryContinuationInstruction) > task.MaxCodexInstructionBytes || strings.ContainsRune(in.RecoveryContinuationInstruction, 0) {
		return nil, rs.ErrConflict
	}
	if in.NextRunRuntimeConfig != nil {
		a, _ := json.Marshal(in.NextRunRuntimeConfig)
		b, _ := json.Marshal(predecessor.RuntimeConfig)
		if string(a) != string(b) {
			return nil, rs.ErrConflict
		}
	}
	v, err := recoveryReadTx(ctx, tx, in.RecoveryStatusID)
	if err != nil {
		return nil, err
	}
	var a rs.Assessment
	if json.Unmarshal(v.Assessment, &a) != nil {
		return nil, rs.ErrConflict
	}
	if v.State != "RESOLVED" || len(v.Resolution) == 0 || a.Consistency != "CONSISTENT" || a.EngineeringDisposition != "RECOVERED_PARTIAL" || v.Spec.OperationKind != "EXECUTION" || v.Spec.OperationID != predecessor.ID || v.Spec.ProjectID != current.ProjectID || v.Spec.TaskID != current.ID || v.Spec.FeatureAreaID != current.FeatureAreaID || v.Spec.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID || v.Spec.TaskDefinitionRevisionID != predecessor.TaskDefinitionRevisionID || current.CurrentExecutionID != predecessor.ID {
		return nil, rs.ErrConflict
	}
	// Historical partial receipts cannot substitute a Verification attempt for
	// the Implementation source required by this continuation path.
	var implementation bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND purpose='IMPLEMENTATION' AND execution_proposal_id=? AND task_id=?)`, v.Spec.RunID, predecessor.ID, current.ID).Scan(&implementation); err != nil {
		return nil, err
	}
	if !implementation {
		return nil, rs.ErrConflict
	}
	return &v, nil
}
func persistRecoveryContinuationTx(ctx context.Context, tx *sql.Tx, recovery *rs.Record, successor string, in wp.TaskAcceptanceInput, now time.Time) error {
	if recovery == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO recovery_status_continuations(successor_execution_id,recovery_id,source_execution_id,revision_id,workset_hash,instruction,instruction_sha256,request_id,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, successor, recovery.ID, recovery.Spec.OperationID, recovery.Spec.TaskDefinitionRevisionID, recovery.Spec.RuntimeWorksetHash, in.RecoveryContinuationInstruction, rs.Hash([]byte(in.RecoveryContinuationInstruction)), in.RequestID, formatTime(now))
	return err
}
func (s *ExecutionProposalStore) RecoveryContinuationInstruction(ctx context.Context, executionID string) (string, error) {
	var instruction, hash string
	var valid bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT c.instruction,c.instruction_sha256,EXISTS(SELECT 1 FROM recovery_status_records r JOIN execution_proposals e ON e.id=c.successor_execution_id WHERE r.id=c.recovery_id AND r.state='RESOLVED' AND e.task_definition_revision_id=c.revision_id AND e.approved_workset_hash IS (SELECT approved_workset_hash FROM execution_proposals WHERE id=c.source_execution_id)) FROM recovery_status_continuations c WHERE c.successor_execution_id=?`, executionID).Scan(&instruction, &hash, &valid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !valid || rs.Hash([]byte(instruction)) != hash {
		return "", rs.ErrConflict
	}
	return instruction, nil
}

func (s *RunStore) RecoveryContinuationInstruction(ctx context.Context, executionID string) (string, error) {
	return NewExecutionProposalStore(s.db).RecoveryContinuationInstruction(ctx, executionID)
}

// RecoveryContinuationBinding returns only the original process authorized by an
// immutable partial-recovery continuation receipt. No receipt means an ordinary run.
func (s *AgentRuntimeStore) RecoveryContinuationBinding(ctx context.Context, runID string) (appserver.Binding, error) {
	var b appserver.Binding
	var executionID, recoveryID, sourceRunID, raw string
	err := s.db.sql.QueryRowContext(ctx, `SELECT c.successor_execution_id,c.recovery_id,x.source_run_id FROM runs r JOIN recovery_status_continuations c ON c.successor_execution_id=r.execution_proposal_id JOIN recovery_status_records x ON x.id=c.recovery_id WHERE r.id=? AND r.purpose IN ('IMPLEMENTATION','VERIFICATION')`, runID).Scan(&executionID, &recoveryID, &sourceRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return s.completedRecoveryBinding(ctx, runID)
	}
	if err != nil {
		return b, err
	}
	if _, err = NewExecutionProposalStore(s.db).RecoveryContinuationInstruction(ctx, executionID); err != nil {
		return b, err
	}
	rec, err := NewRecoveryStatusStore(s.db).Read(ctx, recoveryID)
	if err != nil {
		return b, err
	}
	var a rs.Assessment
	if json.Unmarshal(rec.Assessment, &a) != nil || rec.State != "RESOLVED" || a.Consistency != "CONSISTENT" || a.EngineeringDisposition != "RECOVERED_PARTIAL" || rec.Spec.RunID != sourceRunID {
		return b, rs.ErrConflict
	}
	_, independent, evidenceErr := readRecoveryDiagnosticEvidenceTx(ctx, s.db.sql, rec, true)
	if evidenceErr != nil {
		return b, evidenceErr
	}
	if independent {
		if err = validateRecoveryResolutionEvidence(rec); err != nil {
			return b, err
		}
		return b, sql.ErrNoRows
	}
	var valid bool
	err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs n JOIN runs old ON old.id=? JOIN execution_proposals e ON e.id=n.execution_proposal_id JOIN recovery_status_continuations c ON c.successor_execution_id=e.id
 JOIN recovery_status_records x ON x.id=c.recovery_id JOIN runs child ON child.id=CASE WHEN n.purpose='VERIFICATION' THEN e.run_id ELSE x.recovery_run_id END
 JOIN app_server_run_bindings childbinding ON childbinding.run_id=child.id AND json_extract(childbinding.binding_json,'$.id')=json_extract(x.spec_json,'$.appServerInstanceId')
 JOIN agent_operation_boundaries cb ON cb.run_id=child.id JOIN agent_operation_boundaries nb ON nb.run_id=n.id AND nb.agent_conversation_id=cb.agent_conversation_id
 JOIN agent_conversations a ON a.id=cb.agent_conversation_id JOIN feature_area_agent_claims claim ON claim.run_id=n.id AND claim.runtime_epoch=n.runtime_epoch AND claim.operation_id=n.operation_id AND claim.operation_kind=n.operation_kind AND claim.feature_area_id=n.feature_area_id JOIN tasks currenttask ON currenttask.id=n.task_id AND currenttask.current_execution_id=e.id AND currenttask.current_definition_revision_id=e.task_definition_revision_id
 WHERE n.id=? AND n.runtime_epoch=? AND n.status='PREPARING' AND (n.purpose='IMPLEMENTATION' OR (n.purpose='VERIFICATION' AND e.verification_run_id=n.id AND child.execution_proposal_id=e.id)) AND child.status='SUCCEEDED' AND cb.quiesced=1 AND cb.advance_state='KNOWN_ADVANCE'
 AND nb.entry_boundary_turn_id=cb.exit_boundary_turn_id AND nb.advance_state='NO_ADVANCE' AND nb.quiesced=0 AND a.ended_at IS NULL AND a.current_boundary_turn_id=cb.exit_boundary_turn_id
 AND n.task_id=old.task_id AND n.feature_area_id=old.feature_area_id AND n.project_id=old.project_id AND e.task_definition_revision_id=c.revision_id AND c.source_execution_id=old.execution_proposal_id AND c.workset_hash=?)`, sourceRunID, runID, s.db.epoch, rec.Spec.RuntimeWorksetHash).Scan(&valid)
	if err != nil {
		return b, err
	}
	if !valid {
		return b, rs.ErrConflict
	}
	err = s.db.sql.QueryRowContext(ctx, `SELECT binding_json FROM app_server_run_bindings WHERE run_id=?`, sourceRunID).Scan(&raw)
	if err != nil {
		return b, rs.ErrConflict
	}
	if json.Unmarshal([]byte(raw), &b) != nil || b.ID != rec.Spec.AppServerInstanceID {
		return appserver.Binding{}, rs.ErrConflict
	}
	return b, nil
}

// completedRecoveryBinding follows a canonical successor's exact conversation
// entry boundary back to the resolved recovery receipt. Task identity alone is
// insufficient: the new Run must own the current claim and formal next phase.
func (s *AgentRuntimeStore) completedRecoveryBinding(ctx context.Context, runID string) (appserver.Binding, error) {
	var out appserver.Binding
	var recoveryID string
	err := s.db.sql.QueryRowContext(ctx, `SELECT x.id FROM runs n JOIN agent_operation_boundaries nb ON nb.run_id=n.id JOIN agent_conversations a ON a.id=nb.agent_conversation_id JOIN recovery_status_records x ON json_extract(x.spec_json,'$.agentConversationId')=a.id AND (json_extract(x.record_json,'$.recoveryTurnId')=nb.entry_boundary_turn_id OR (n.purpose='VERIFICATION' AND EXISTS(SELECT 1 FROM runs reviewsource JOIN tasks t ON t.id=n.task_id JOIN review_approval_decisions d ON d.decision_id=t.review_approval_id WHERE reviewsource.id=x.source_run_id AND reviewsource.operation_kind='REVIEW' AND t.current_review_id=reviewsource.review_id AND d.review_id=reviewsource.review_id))) WHERE n.id=? AND a.ended_at IS NULL AND a.current_boundary_turn_id=nb.entry_boundary_turn_id`, runID).Scan(&recoveryID)
	if err != nil {
		return out, err
	}
	rec, err := NewRecoveryStatusStore(s.db).Read(ctx, recoveryID)
	if err != nil {
		return out, err
	}
	var assessment rs.Assessment
	if rec.State != "RESOLVED" || len(rec.Resolution) == 0 || json.Unmarshal(rec.Assessment, &assessment) != nil || assessment.Consistency != "CONSISTENT" || assessment.EngineeringDisposition != "RECOVERED_COMPLETE" {
		return out, rs.ErrConflict
	}
	var valid bool
	err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM runs n JOIN runs source ON source.id=? JOIN runs child ON child.id=CASE WHEN n.purpose='VERIFICATION' AND source.operation_kind='REVIEW' THEN (SELECT run_id FROM execution_proposals WHERE id=n.execution_proposal_id) ELSE ? END
 JOIN app_server_run_bindings childbinding ON childbinding.run_id=child.id
 JOIN agent_operation_boundaries cb ON cb.run_id=child.id JOIN agent_operation_boundaries nb ON nb.run_id=n.id AND nb.agent_conversation_id=cb.agent_conversation_id
 JOIN agent_conversations a ON a.id=cb.agent_conversation_id
 JOIN feature_area_agent_claims claim ON claim.run_id=n.id AND claim.runtime_epoch=n.runtime_epoch AND claim.operation_id=n.operation_id AND claim.operation_kind=n.operation_kind AND claim.feature_area_id=n.feature_area_id
 JOIN execution_proposals e ON e.id=n.execution_proposal_id JOIN tasks t ON t.id=n.task_id
 WHERE n.id=? AND n.runtime_epoch=? AND n.status='PREPARING' AND source.status='SUCCEEDED' AND child.status='SUCCEEDED'
 AND cb.quiesced=1 AND cb.advance_state='KNOWN_ADVANCE' AND (cb.exit_boundary_turn_id=? OR (source.operation_kind='REVIEW' AND n.purpose='VERIFICATION' AND cb.exit_boundary_turn_id=child.turn_id)) AND json_extract(childbinding.binding_json,'$.id')=? AND nb.entry_boundary_turn_id=cb.exit_boundary_turn_id AND nb.advance_state='NO_ADVANCE' AND nb.quiesced=0
 AND a.ended_at IS NULL AND a.backend_thread_id=? AND a.current_boundary_turn_id=cb.exit_boundary_turn_id
 AND n.task_id=source.task_id AND n.project_id=source.project_id AND n.feature_area_id=source.feature_area_id
 AND t.current_execution_id=e.id AND t.current_definition_revision_id=e.task_definition_revision_id AND t.status='ACTIVE'
 AND ((source.purpose='IMPLEMENTATION' AND n.purpose='VERIFICATION' AND e.id=source.execution_proposal_id AND e.run_id=source.id AND e.verification_run_id=n.id)
 OR (source.operation_kind='REVIEW' AND ((n.purpose='IMPLEMENTATION' AND e.run_id=n.id) OR (n.purpose='VERIFICATION' AND e.run_id=child.id AND child.execution_proposal_id=e.id AND e.verification_run_id=n.id)) AND t.current_review_id=source.review_id
 AND EXISTS(SELECT 1 FROM review_approval_decisions d JOIN decision_resolution_receipts receipt ON receipt.decision_id=d.decision_id WHERE d.decision_id=t.review_approval_id AND d.review_id=source.review_id AND d.task_id=t.id AND receipt.option='APPROVE' AND receipt.result_id=t.id))))`, rec.Spec.RunID, rec.RecoveryRunID, runID, s.db.epoch, rec.RecoveryTurnID, rec.Spec.AppServerInstanceID, rec.Spec.ThreadID).Scan(&valid)
	if err != nil {
		return out, err
	}
	if !valid {
		return out, rs.ErrConflict
	}
	out, err = s.AppServerBinding(ctx, rec.Spec.RunID)
	if err != nil {
		return out, rs.ErrConflict
	}
	if out.ID != rec.Spec.AppServerInstanceID {
		return appserver.Binding{}, rs.ErrConflict
	}
	return out, nil
}

// RecoveryAppServerNeedsContinuity retains a recovered process only while the
// same canonical owner has a concrete next implementation/verification phase.
// It grants no execution authority: normal confirmation and claim acquisition
// remain necessary when the next Run starts.
func (s *AgentRuntimeStore) RecoveryAppServerNeedsContinuity(ctx context.Context, instanceID string) (bool, error) {
	var keep bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM recovery_status_records x JOIN runs source ON source.id=x.source_run_id
 JOIN tasks t ON t.id=source.task_id JOIN agent_conversations a ON a.id=json_extract(x.spec_json,'$.agentConversationId')
 JOIN agent_operation_boundaries boundary ON boundary.agent_conversation_id=a.id AND boundary.exit_boundary_turn_id=a.current_boundary_turn_id
 JOIN runs predecessor ON predecessor.id=boundary.run_id
 JOIN app_server_run_bindings binding ON binding.run_id=predecessor.id
 WHERE x.state='RESOLVED' AND json_extract(x.record_json,'$.resolution') IS NOT NULL
 AND json_extract(x.spec_json,'$.appServerInstanceId')=? AND json_extract(binding.binding_json,'$.id')=?
 AND json_extract(x.record_json,'$.assessment.consistency')='CONSISTENT'
 AND json_extract(x.record_json,'$.assessment.engineeringDisposition') IN ('RECOVERED_COMPLETE','RECOVERED_PARTIAL')
 AND a.ended_at IS NULL AND a.backend_thread_id=json_extract(x.spec_json,'$.threadId')
 AND a.feature_area_id=t.feature_area_id AND t.project_id=source.project_id AND t.status='ACTIVE'
 AND boundary.quiesced=1 AND boundary.advance_state='KNOWN_ADVANCE' AND predecessor.status='SUCCEEDED'
 AND predecessor.task_id=t.id AND predecessor.feature_area_id=t.feature_area_id
 AND ( (source.operation_kind='REVIEW' AND t.current_review_id=source.review_id AND
   ((t.phase='REVIEW_APPROVAL_PENDING' AND predecessor.id=x.recovery_run_id)
    OR (t.phase IN ('IMPLEMENTATION_READY','VERIFICATION_READY') AND EXISTS(
       SELECT 1 FROM execution_proposals e JOIN review_approval_decisions d ON d.decision_id=t.review_approval_id
       JOIN decision_resolution_receipts receipt ON receipt.decision_id=d.decision_id AND receipt.option='APPROVE'
       WHERE e.id=t.current_execution_id AND e.task_id=t.id AND e.task_definition_revision_id=t.current_definition_revision_id
       AND d.review_id=source.review_id AND receipt.result_id=t.id AND (predecessor.id=x.recovery_run_id OR predecessor.id=e.run_id)))))
 OR (source.operation_kind='EXECUTION' AND t.phase IN ('IMPLEMENTATION_READY','VERIFICATION_READY') AND EXISTS(
   SELECT 1 FROM execution_proposals e WHERE e.id=t.current_execution_id AND e.task_id=t.id AND e.task_definition_revision_id=t.current_definition_revision_id
   AND (e.id=source.execution_proposal_id OR EXISTS(SELECT 1 FROM recovery_status_continuations c WHERE c.recovery_id=x.id AND c.successor_execution_id=e.id AND c.revision_id=e.task_definition_revision_id))
   AND (predecessor.id=x.recovery_run_id OR predecessor.id=e.run_id))))
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations cancel WHERE cancel.task_id=t.id AND cancel.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))
 )`, instanceID, instanceID).Scan(&keep)
	return keep, err
}
