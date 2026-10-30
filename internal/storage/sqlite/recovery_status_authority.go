package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/workset"
)

// A diagnostic request receives its own server-derived identity only after
// every immutable preparation/dispatch receipt proves this independent child.
// Normal business requests and historical shared-Thread diagnostics retain
// their existing identity shape.
func independentRecoveryDecisionIDTx(ctx context.Context, tx *sql.Tx, runID string) (string, error) {
	v, err := scanRecovery(tx.QueryRowContext(ctx, `SELECT r.record_json FROM recovery_status_records r JOIN recovery_child_admissions a ON a.recovery_id=r.id WHERE r.recovery_run_id=?`, runID))
	if errors.Is(err, rs.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	_, independent, err := readRecoveryDiagnosticEvidenceTx(ctx, tx, v, false)
	if err != nil || !independent {
		return "", rs.ErrIneligible
	}
	return v.ID, nil
}

// Recovery children borrow the exact source operation's single claim. The
// recovery record is authority only for that child, epoch and conversation.
func (d *DB) recoveryChildAuthorityTx(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var specJSON, sourceRunID, sourceThreadID, sourceWorkspaceID, workspaceID, recoveryID, initialEpoch string
	var worksetJSON sql.NullString
	modernErr := tx.QueryRowContext(ctx, `SELECT rec.spec_json,rec.source_run_id,source.thread_id,admission.source_workspace_id,COALESCE(child.workspace_id,''),child.runtime_workset_json,rec.id,admission.runtime_epoch
 FROM recovery_status_records rec
 JOIN recovery_child_admissions admission ON admission.recovery_id=rec.id AND admission.child_plan_id IS NOT NULL
 JOIN runtime_preparation_attempts attempt ON attempt.plan_id=admission.child_plan_id AND attempt.run_id=rec.recovery_run_id AND attempt.request_id=admission.admission_request_id AND attempt.payload_hash=admission.admission_payload_sha256
 JOIN runs child ON child.id=rec.recovery_run_id AND child.id=? AND child.purpose='RECOVERY_STATUS' AND child.runtime_epoch=rec.runtime_epoch
 JOIN runs source ON source.id=rec.source_run_id
 WHERE rec.runtime_epoch=? AND rec.state IN ('RUN_CREATED','UNKNOWN','RUNNING','REPORT_READY','ASSESSED')
 AND child.project_id=source.project_id AND child.task_id=source.task_id AND child.thread_id IS NOT source.thread_id
	 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE (c.task_id=child.task_id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')) OR (c.run_id IN(child.id,source.id) AND c.state='CANCELED'))`, id, d.epoch).Scan(&specJSON, &sourceRunID, &sourceThreadID, &sourceWorkspaceID, &workspaceID, &worksetJSON, &recoveryID, &initialEpoch)
	if modernErr == nil {
		exactEpoch, err := recoveryDiagnosticEpochChainTx(ctx, tx, recoveryID, id, initialEpoch, d.epoch)
		if err != nil || !exactEpoch {
			return false, err
		}
		var spec rs.Spec
		if json.Unmarshal([]byte(specJSON), &spec) != nil || spec.RunID != sourceRunID || spec.ThreadID != sourceThreadID {
			return false, nil
		}
		if _, _, err := NewRecoveryStatusStore(d).validateSourceTx(ctx, tx, spec.Source); err != nil {
			return false, nil
		}
		if workspaceID != "" && workspaceID == sourceWorkspaceID {
			return false, nil
		}
		if worksetJSON.Valid {
			var value workset.RuntimeWorkset
			if json.Unmarshal([]byte(worksetJSON.String), &value) != nil || value.ProtocolVersion != workset.RuntimeProtocolVersionRecoveryStatus || value.RecoveryStatusID == "" || value.RecoverySourceRunID != sourceRunID || value.WorkspaceID != workspaceID || value.Validate() != nil {
				return false, nil
			}
		}
		return true, nil
	}
	if !errors.Is(modernErr, sql.ErrNoRows) && !strings.Contains(modernErr.Error(), "no such table:") {
		return false, modernErr
	}
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1
 FROM recovery_status_records x
 JOIN runs r ON r.id=x.recovery_run_id
 JOIN runs s ON s.id=x.source_run_id
 JOIN agent_conversations a ON a.id=json_extract(x.spec_json,'$.agentConversationId')
 WHERE r.id=? AND r.purpose='RECOVERY_STATUS' AND r.runtime_epoch=?
   AND x.runtime_epoch=r.runtime_epoch
   AND x.state IN ('RUN_CREATED','UNKNOWN','RUNNING','REPORT_READY','ASSESSED')
   AND s.project_id=r.project_id AND s.task_id=r.task_id
   AND a.ended_at IS NULL AND a.backend_thread_id=r.thread_id
   AND NOT EXISTS(SELECT 1 FROM execution_cancellations q WHERE (q.task_id=r.task_id AND q.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')) OR (q.run_id IN (r.id,s.id) AND q.state='CANCELED'))
   AND (
     (json_extract(x.spec_json,'$.ownerKind')='PROJECT_INSPECTION'
      AND r.feature_area_id IS NULL AND r.operation_kind='INSPECTION'
      AND r.operation_id=json_extract(x.spec_json,'$.ownerId') AND r.context_probe_id IS NULL
      AND EXISTS(SELECT 1 FROM context_probes p JOIN tasks t ON t.id=p.task_id WHERE p.id=json_extract(x.spec_json,'$.ownerId') AND p.run_id=s.id AND p.task_id=s.task_id AND p.project_id=s.project_id AND p.workspace_id=r.workspace_id AND p.runtime_workset_hash=r.runtime_workset_hash AND t.task_scope='PROJECT' AND t.feature_area_id IS NULL AND t.work_owner_id=p.id))
     OR
     (json_extract(x.spec_json,'$.ownerKind') IS NOT 'PROJECT_INSPECTION'
      AND EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=s.id AND c.feature_area_id=r.feature_area_id AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id AND c.runtime_epoch=s.runtime_epoch))
   )
)`, id, d.epoch).Scan(&valid)
	return valid, err
}

// inspectionContinuationAuthorityTx recognizes only the current successor
// named by the immutable partial-resolution receipt.  It deliberately does
// not infer authority from a NULL context_probe_id alone.
func (d *DB) inspectionContinuationAuthorityTx(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM inspection_recovery_continuations k
 JOIN recovery_status_records x ON x.id=k.recovery_id
 JOIN runs r ON r.id=k.successor_run_id
 JOIN context_probes p ON p.id=k.inspection_id
 JOIN project_inspection_agent_claims c ON c.inspection_id=k.inspection_id AND c.run_id=k.successor_run_id
 WHERE r.id=? AND p.recovery_successor_run_id=r.id
   AND r.context_probe_id IS NULL AND r.operation_kind='INSPECTION' AND r.operation_id=p.id
   AND r.project_id=p.project_id AND r.task_id=p.task_id AND r.workspace_id=p.workspace_id
   AND r.runtime_workset_hash=k.runtime_workset_hash AND c.runtime_epoch=r.runtime_epoch
   AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
   AND x.state='RESOLVED' AND json_extract(x.record_json,'$.assessment.consistency')='CONSISTENT'
   AND json_extract(x.record_json,'$.assessment.engineeringDisposition')='RECOVERED_PARTIAL'
)`, id).Scan(&valid)
	return valid, err
}
func (d *DB) sourceRecoveryFencedTx(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var fenced bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_status_records WHERE source_run_id=? AND recovery_run_id IS NOT NULL)`, id).Scan(&fenced)
	return fenced, err
}
