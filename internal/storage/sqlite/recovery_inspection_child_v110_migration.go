package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// v110 retains the shared-thread Inspection history rules and admits only the
// exact new child named by an immutable Recovery admission. Its workspace,
// Workset and Thread are intentionally absent until independent preparation.
func migrateRecoveryInspectionChildV110(ctx context.Context, tx *sql.Tx) error {
	for _, name := range []string{"runs_operation_owner_insert", "runs_agent_execution_insert", "recovery_status_inspection_child_v83"} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			return err
		}
		i, j := strings.Index(ddl, "\nWHEN "), strings.Index(ddl, "\nBEGIN ")
		if i < 0 || j <= i || strings.Contains(ddl, "independent_inspection_child") {
			return fmt.Errorf("unexpected v109 Inspection child trigger %s", name)
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+name+`"`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, ddl[:i]+"\nWHEN ("+ddl[i+6:j]+") AND NOT EXISTS("+independentInspectionChildV110+")"+ddl[j:]); err != nil {
			return fmt.Errorf("extend %s for independent Inspection child: %w", name, err)
		}
	}
	return nil
}

const independentInspectionChildV110 = `SELECT 1 AS independent_inspection_child
 FROM recovery_status_records x
 JOIN recovery_child_admissions a ON a.recovery_id=x.id AND a.recovery_generation=x.generation AND a.source_run_id=x.source_run_id AND a.source_turn_id=x.source_turn_id
 JOIN runs source ON source.id=a.source_run_id AND source.thread_id=a.source_thread_id AND source.turn_id=a.source_turn_id AND source.runtime_workset_hash=a.source_runtime_workset_hash AND source.workspace_id=a.source_workspace_id
 JOIN context_probes inspection ON inspection.id=json_extract(x.spec_json,'$.ownerId') AND inspection.project_id=source.project_id AND inspection.task_id=source.task_id
 JOIN tasks t ON t.id=source.task_id AND t.project_id=source.project_id AND t.work_owner_id=inspection.id AND t.task_scope='PROJECT' AND t.task_type='INSPECTION' AND t.feature_area_id IS NULL AND t.legacy_read_only=0 AND t.status<>'CANCELED'
 JOIN runtime_scope_plans p ON p.plan_id=a.child_plan_id AND p.owner_kind='RECOVERY_STATUS' AND p.owner_id=x.id AND p.project_id=source.project_id AND p.task_id=source.task_id AND p.feature_area_id IS NULL AND p.generation=x.generation AND p.preparation_request_id=a.admission_request_id AND p.preparation_payload_hash=a.admission_payload_sha256
 JOIN runtime_preparation_authorities auth ON auth.plan_id=p.plan_id AND auth.owner_kind=p.owner_kind AND auth.owner_id=p.owner_id AND auth.generation=p.generation AND auth.request_id=a.admission_request_id AND auth.payload_hash=a.admission_payload_sha256 AND auth.disposition='CURRENT'
 WHERE x.recovery_run_id=NEW.id AND x.state='PREPARED' AND x.runtime_epoch=NEW.runtime_epoch AND x.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND json_extract(x.spec_json,'$.ownerKind')='PROJECT_INSPECTION'
 AND source.operation_kind='INSPECTION' AND source.operation_id=inspection.id AND source.feature_area_id IS NULL
 AND NEW.purpose='RECOVERY_STATUS' AND NEW.status='CREATED' AND NEW.operation_kind='INSPECTION' AND NEW.operation_id=inspection.id
 AND NEW.project_id=source.project_id AND NEW.task_id=source.task_id AND NEW.feature_area_id IS NULL AND NEW.context_probe_id IS NULL
 AND NEW.agent_execution_id=source.agent_execution_id AND NEW.execution_proposal_id IS NULL AND NEW.review_id IS NULL
 AND NEW.workspace_id IS NULL AND NEW.runtime_workset_hash IS NULL AND NEW.runtime_workset_json IS NULL AND NEW.thread_id IS NULL AND NEW.turn_id IS NULL
 AND EXISTS(SELECT 1 FROM project_inspection_agent_claims claim WHERE claim.inspection_id=inspection.id AND claim.run_id=source.id AND claim.runtime_epoch=source.runtime_epoch)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE (c.task_id=t.id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')) OR (c.run_id=source.id AND c.state='CANCELED'))`
