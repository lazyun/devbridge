package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// v83 adds durable SQLite admission fences for the PROJECT_INSPECTION owner
// variant.  It neither rebuilds a historical table nor backfills old probes:
// only a recovery record explicitly authored with the v2 owner variant can
// create a recovery child for an existing canonical Inspection.
func migrateRecoveryStatusInspectionV83(ctx context.Context, tx *sql.Tx) error {
	if err := rebuildInspectionRuntimeRecordsV83(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `

ALTER TABLE context_probes ADD COLUMN recovery_successor_run_id TEXT REFERENCES runs(id);
CREATE TABLE inspection_recovery_continuations(
	successor_run_id TEXT PRIMARY KEY REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 recovery_id TEXT NOT NULL UNIQUE REFERENCES recovery_status_records(id),
 inspection_id TEXT NOT NULL REFERENCES context_probes(id),
 source_run_id TEXT NOT NULL REFERENCES runs(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 runtime_workset_hash TEXT NOT NULL CHECK(length(runtime_workset_hash)=64),
 instruction TEXT NOT NULL,
 instruction_sha256 TEXT NOT NULL CHECK(length(instruction_sha256)=64),
 request_id TEXT NOT NULL UNIQUE,
 actor_id TEXT NOT NULL,
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 created_at TEXT NOT NULL
);
CREATE TABLE inspection_recovery_resolutions(
 recovery_id TEXT PRIMARY KEY REFERENCES recovery_status_records(id),
 inspection_id TEXT NOT NULL REFERENCES context_probes(id),
 source_run_id TEXT NOT NULL REFERENCES runs(id),
 source_turn_id TEXT NOT NULL,
 recovery_run_id TEXT NOT NULL REFERENCES runs(id),
 recovery_turn_id TEXT NOT NULL,
 report_hash TEXT NOT NULL CHECK(length(report_hash)=64),
 assessment_hash TEXT NOT NULL CHECK(length(assessment_hash)=64),
 owner_facts_hash TEXT NOT NULL CHECK(length(owner_facts_hash)=64),
	inspection_result_hash TEXT NOT NULL CHECK(length(inspection_result_hash)=64),
 result_source TEXT NOT NULL CHECK(result_source='RECOVERY_STATUS'),
 disposition TEXT NOT NULL CHECK(disposition IN ('RECOVERED_COMPLETE','RECOVERED_PARTIAL','REVISION_REQUIRED')),
 created_at TEXT NOT NULL
);
CREATE TRIGGER inspection_recovery_resolution_immutable BEFORE UPDATE ON inspection_recovery_resolutions BEGIN SELECT RAISE(ABORT,'Inspection recovery resolution is immutable'); END;
CREATE TRIGGER inspection_recovery_resolution_no_delete BEFORE DELETE ON inspection_recovery_resolutions BEGIN SELECT RAISE(ABORT,'Inspection recovery resolution is durable'); END;
CREATE TRIGGER inspection_recovery_continuation_immutable BEFORE UPDATE ON inspection_recovery_continuations BEGIN SELECT RAISE(ABORT,'Inspection recovery continuation is immutable'); END;
CREATE TRIGGER inspection_recovery_continuation_no_delete BEFORE DELETE ON inspection_recovery_continuations BEGIN SELECT RAISE(ABORT,'Inspection recovery continuation is durable'); END;
CREATE TRIGGER context_probe_recovery_successor_immutable_v83 BEFORE UPDATE OF recovery_successor_run_id ON context_probes
WHEN OLD.recovery_successor_run_id IS NOT NULL AND NEW.recovery_successor_run_id IS NOT OLD.recovery_successor_run_id
BEGIN SELECT RAISE(ABORT,'Inspection recovery successor is immutable'); END;
CREATE TRIGGER context_probe_recovery_successor_owner_v83 BEFORE UPDATE OF recovery_successor_run_id ON context_probes
WHEN NEW.recovery_successor_run_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM inspection_recovery_continuations c JOIN runs r ON r.id=c.successor_run_id
 WHERE c.successor_run_id=NEW.recovery_successor_run_id AND c.inspection_id=NEW.id
   AND c.source_run_id=OLD.run_id AND c.task_id=NEW.task_id AND c.project_id=NEW.project_id
   AND c.workspace_id=NEW.workspace_id AND c.runtime_workset_hash=NEW.runtime_workset_hash
   AND r.context_probe_id IS NULL AND r.operation_kind='INSPECTION' AND r.operation_id=NEW.id
)
BEGIN SELECT RAISE(ABORT,'Inspection recovery successor must retain canonical owner'); END;

CREATE TRIGGER recovery_status_inspection_source_v83 BEFORE INSERT ON recovery_status_records
WHEN json_extract(NEW.spec_json,'$.ownerKind')='PROJECT_INSPECTION' AND NOT EXISTS(
 SELECT 1 FROM context_probes p
 JOIN runs r ON r.id=NEW.source_run_id
 JOIN tasks t ON t.id=p.task_id
 WHERE p.id=json_extract(NEW.spec_json,'$.ownerId')
   AND p.id=json_extract(NEW.spec_json,'$.sourceOperationId')
   AND p.project_id=json_extract(NEW.spec_json,'$.projectId')
   AND p.task_id=json_extract(NEW.spec_json,'$.taskId')
   AND ((p.run_id=r.id AND r.context_probe_id=p.id)
     OR EXISTS(SELECT 1 FROM inspection_recovery_continuations c JOIN recovery_status_records x ON x.id=c.recovery_id JOIN project_inspection_agent_claims claim ON claim.inspection_id=c.inspection_id AND claim.run_id=c.successor_run_id WHERE c.successor_run_id=r.id AND c.inspection_id=p.id AND p.recovery_successor_run_id=r.id AND r.context_probe_id IS NULL AND claim.runtime_epoch=r.runtime_epoch AND x.state='RESOLVED' AND json_extract(x.record_json,'$.assessment.consistency')='CONSISTENT' AND json_extract(x.record_json,'$.assessment.engineeringDisposition')='RECOVERED_PARTIAL'))
   AND r.operation_kind='INSPECTION' AND r.project_id=p.project_id
   AND r.task_id=p.task_id AND r.feature_area_id IS NULL
   AND t.task_scope='PROJECT' AND t.feature_area_id IS NULL
   AND t.work_owner_id=p.id
   AND p.canonical_target_receipt_json IS NOT NULL
   AND p.workspace_id=json_extract(NEW.spec_json,'$.sourceWorkspaceId') AND p.runtime_workset_hash IS NOT NULL
)
BEGIN SELECT RAISE(ABORT,'Recovery Inspection source must be canonical exact owner'); END;

CREATE TRIGGER recovery_status_inspection_child_v83 BEFORE INSERT ON runs
WHEN NEW.purpose='RECOVERY_STATUS' AND NEW.operation_kind='INSPECTION' AND NOT EXISTS(
 SELECT 1 FROM recovery_status_records x
 JOIN runs s ON s.id=x.source_run_id
 JOIN context_probes p ON p.id=s.context_probe_id
 WHERE x.recovery_run_id=NEW.id AND x.state='PREPARED'
   AND json_extract(x.spec_json,'$.ownerKind')='PROJECT_INSPECTION'
   AND s.id=x.source_run_id
   AND NEW.project_id=s.project_id AND NEW.task_id=s.task_id
   AND NEW.feature_area_id IS NULL AND NEW.context_probe_id IS NULL AND NEW.workspace_id=p.workspace_id
   AND NEW.runtime_workset_hash=p.runtime_workset_hash
   AND NEW.thread_id=s.thread_id AND NEW.runtime_epoch=x.runtime_epoch
)
BEGIN SELECT RAISE(ABORT,'Recovery Inspection child must retain exact source authority'); END;`)
	if err != nil {
		return err
	}
	if err := extendInspectionRecoveryWorkspaceTriggersV83(ctx, tx); err != nil {
		return err
	}
	if err := extendInspectionRecoveryBindingTriggerV83(ctx, tx); err != nil {
		return err
	}
	if err := extendInspectionRecoveryClaimTriggersV83(ctx, tx); err != nil {
		return err
	}
	if err := extendInspectionSuccessorStatusTriggerV83(ctx, tx); err != nil {
		return err
	}
	return extendInspectionRecoveryRunOwnerTriggersV83(ctx, tx)
}

func extendInspectionSuccessorStatusTriggerV83(ctx context.Context, tx *sql.Tx) error {
	const name = "context_probes_status_transition"
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
		return err
	}
	needle := "\nBEGIN"
	at := strings.LastIndex(ddl, needle)
	if at < 0 {
		return fmt.Errorf("unexpected %s", name)
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+name); err != nil {
		return err
	}
	// The canonical probe is FAILED after the verified BLOCKED source attempt.
	// Only its current partial-continuation receipt may reactivate it; ordinary
	// historical FAILED probes retain the original state machine.
	exception := ` AND NOT (OLD.status='FAILED' AND NEW.status IN ('RUNNING','SUCCEEDED','FAILED','CANCELED') AND EXISTS(
 SELECT 1 FROM inspection_recovery_continuations c
 JOIN recovery_status_records x ON x.id=c.recovery_id
 JOIN runs successor ON successor.id=c.successor_run_id
 JOIN project_inspection_agent_claims claim ON claim.inspection_id=c.inspection_id AND claim.run_id=c.successor_run_id
 WHERE c.inspection_id=NEW.id AND NEW.recovery_successor_run_id=c.successor_run_id
   AND successor.context_probe_id IS NULL AND successor.operation_kind='INSPECTION' AND successor.operation_id=NEW.id
   AND successor.project_id=NEW.project_id AND successor.task_id=NEW.task_id AND successor.workspace_id=c.workspace_id
   AND successor.runtime_workset_hash=c.runtime_workset_hash AND claim.runtime_epoch=successor.runtime_epoch
   AND x.state='RESOLVED' AND json_extract(x.record_json,'$.assessment.consistency')='CONSISTENT'
   AND json_extract(x.record_json,'$.assessment.engineeringDisposition')='RECOVERED_PARTIAL'
))`
	_, err := tx.ExecContext(ctx, ddl[:at]+exception+ddl[at:])
	return err
}

func extendInspectionRecoveryClaimTriggersV83(ctx context.Context, tx *sql.Tx) error {
	for _, item := range []struct{ name, needle, exception string }{
		{"project_inspection_claim_no_delete", "WHEN NOT EXISTS(", `WHEN NOT EXISTS(SELECT 1 FROM inspection_recovery_continuations c JOIN recovery_status_records x ON x.id=c.recovery_id WHERE c.source_run_id=OLD.run_id AND c.inspection_id=OLD.inspection_id AND c.task_id=OLD.task_id AND c.project_id=OLD.project_id AND x.state='RESOLVED' AND json_extract(x.record_json,'$.assessment.consistency')='CONSISTENT' AND json_extract(x.record_json,'$.assessment.engineeringDisposition')='RECOVERED_PARTIAL') AND NOT EXISTS(SELECT 1 FROM recovery_status_records x JOIN execution_cancellations q ON q.run_id=x.recovery_run_id WHERE x.source_run_id=OLD.run_id AND json_extract(x.spec_json,'$.ownerKind')='PROJECT_INSPECTION' AND json_extract(x.spec_json,'$.ownerId')=OLD.inspection_id AND x.state='FAILED' AND json_extract(x.record_json,'$.failureReason')='RECOVERY_CANCELED' AND q.state='CANCELED' AND q.owner_type='INSPECTION' AND q.owner_id=OLD.inspection_id) AND NOT EXISTS(`},
		{"project_inspection_claim_owner", "WHEN NOT EXISTS(", `WHEN NOT EXISTS(SELECT 1 FROM inspection_recovery_continuations c JOIN recovery_status_records x ON x.id=c.recovery_id JOIN runs r ON r.id=NEW.run_id WHERE c.successor_run_id=NEW.run_id AND c.inspection_id=NEW.inspection_id AND c.task_id=NEW.task_id AND c.project_id=NEW.project_id AND x.state='RESOLVED' AND json_extract(x.record_json,'$.assessment.consistency')='CONSISTENT' AND json_extract(x.record_json,'$.assessment.engineeringDisposition')='RECOVERED_PARTIAL' AND r.context_probe_id IS NULL) AND NOT EXISTS(`},
	} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, item.name).Scan(&ddl); err != nil {
			return err
		}
		at := strings.Index(ddl, item.needle)
		if at < 0 {
			return fmt.Errorf("unexpected %s", item.name)
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+item.name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, ddl[:at]+item.exception+ddl[at+len(item.needle):]); err != nil {
			return err
		}
	}
	return nil
}

func extendInspectionRecoveryBindingTriggerV83(ctx context.Context, tx *sql.Tx) error {
	const name = "run_repository_binding_inspection_owner_v82"
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
		return err
	}
	needle := "WHEN NEW.inspection_id IS NOT NULL AND NOT EXISTS("
	at := strings.Index(ddl, needle)
	if at < 0 {
		return fmt.Errorf("unexpected %s", name)
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+name); err != nil {
		return err
	}
	exception := `WHEN NEW.inspection_id IS NOT NULL
AND NOT EXISTS(SELECT 1 FROM recovery_status_records x JOIN runs s ON s.id=x.source_run_id WHERE x.recovery_run_id=NEW.run_id AND x.state='PREPARED' AND NEW.feature_area_id IS NULL AND NEW.review_id IS NULL AND NEW.execution_id IS NULL AND NEW.workspace_id=s.workspace_id AND NEW.inspection_id=s.context_probe_id AND EXISTS(SELECT 1 FROM runs child WHERE child.id=NEW.run_id AND child.context_probe_id IS NULL AND child.operation_kind='INSPECTION' AND child.operation_id=s.context_probe_id))
AND NOT EXISTS(SELECT 1 FROM inspection_recovery_continuations c JOIN recovery_status_records x ON x.id=c.recovery_id JOIN runs successor ON successor.id=c.successor_run_id JOIN run_repository_bindings source ON source.run_id=c.source_run_id WHERE c.successor_run_id=NEW.run_id AND c.inspection_id=NEW.inspection_id AND c.project_id=NEW.project_id AND c.workspace_id=NEW.workspace_id AND c.runtime_workset_hash=successor.runtime_workset_hash AND successor.context_probe_id IS NULL AND successor.operation_kind='INSPECTION' AND successor.operation_id=c.inspection_id AND x.state='RESOLVED' AND json_extract(x.record_json,'$.assessment.consistency')='CONSISTENT' AND json_extract(x.record_json,'$.assessment.engineeringDisposition')='RECOVERED_PARTIAL' AND source.inspection_id=NEW.inspection_id AND source.project_id=NEW.project_id AND source.workspace_id=NEW.workspace_id AND source.repository_id=NEW.repository_id AND source.member_id=NEW.member_id AND source.member_version=NEW.member_version AND source.origin_commit=NEW.origin_commit AND source.worktree_id=NEW.worktree_id AND source.repository_alias=NEW.repository_alias AND source.start_head=NEW.start_head AND source.start_snapshot=NEW.start_snapshot AND source.intended_read=NEW.intended_read AND source.intended_write_scope_json=NEW.intended_write_scope_json AND source.runtime_root_identity=NEW.runtime_root_identity AND source.root_path=NEW.root_path AND source.root_identity=NEW.root_identity)
AND NOT EXISTS(`
	_, err := tx.ExecContext(ctx, ddl[:at]+exception+ddl[at+len(needle):])
	return err
}

func extendInspectionRecoveryRunOwnerTriggersV83(ctx context.Context, tx *sql.Tx) error {
	for _, name := range []string{"runs_operation_owner_insert", "runs_agent_execution_insert"} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			return err
		}
		i, j := strings.Index(ddl, "\nWHEN "), strings.Index(ddl, "\nBEGIN ")
		if i < 0 || j < 0 {
			return fmt.Errorf("unexpected %s", name)
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+name+`"`); err != nil {
			return err
		}
		// v81's generic recovery exception compares context_probe_id to the
		// source. A Project Inspection child deliberately keeps it NULL, so its
		// exact recovery record is an additional, narrow exception.
		receipt := `NOT EXISTS(SELECT 1 FROM inspection_recovery_continuations c WHERE c.successor_run_id=NEW.id AND c.inspection_id=NEW.operation_id AND c.project_id=NEW.project_id AND c.task_id=NEW.task_id AND c.workspace_id=NEW.workspace_id AND c.runtime_workset_hash=NEW.runtime_workset_hash AND NEW.context_probe_id IS NULL)
AND NOT EXISTS(SELECT 1 FROM recovery_status_records x JOIN runs s ON s.id=x.source_run_id WHERE x.recovery_run_id=NEW.id AND x.state='PREPARED' AND json_extract(x.spec_json,'$.ownerKind')='PROJECT_INSPECTION' AND NEW.purpose='RECOVERY_STATUS' AND NEW.context_probe_id IS NULL AND NEW.operation_kind='INSPECTION' AND NEW.operation_id=json_extract(x.spec_json,'$.ownerId') AND NEW.project_id=s.project_id AND NEW.task_id=s.task_id AND NEW.workspace_id=json_extract(x.spec_json,'$.sourceWorkspaceId') AND NEW.runtime_workset_hash=json_extract(x.spec_json,'$.expectedRuntimeWorksetHash'))`
		if _, err := tx.ExecContext(ctx, ddl[:i]+"\nWHEN ("+ddl[i+6:j]+") AND "+receipt+ddl[j:]); err != nil {
			return err
		}
	}
	return nil
}

// v82 made inspection_id unique because a canonical inspection initially had
// exactly one Run.  A v83 continuation is a new Run for that same immutable
// owner. Preserve every historical row and trigger while replacing that table
// constraint with a partial current-record uniqueness fence.
func rebuildInspectionRuntimeRecordsV83(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='inspection_runtime_records'`).Scan(&ddl); err != nil {
		return err
	}
	needle := "inspection_id TEXT NOT NULL UNIQUE"
	if !strings.Contains(ddl, needle) {
		return fmt.Errorf("unexpected inspection runtime v82 constraint")
	}
	ddl = strings.Replace(ddl, needle, "inspection_id TEXT NOT NULL", 1)
	ddl = "CREATE TABLE _v83_inspection_runtime_records " + ddl[strings.Index(ddl, "("):]
	type definition struct{ kind, name, sql string }
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL AND ((type='trigger' AND sql LIKE '%inspection_runtime_records%') OR (type='index' AND tbl_name='inspection_runtime_records')) ORDER BY rowid`)
	if err != nil {
		return err
	}
	var defs []definition
	for rows.Next() {
		var d definition
		if err = rows.Scan(&d.kind, &d.name, &d.sql); err != nil {
			rows.Close()
			return err
		}
		defs = append(defs, d)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, d := range defs {
		if d.kind == "trigger" {
			if _, err = tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(d.name, `"`, `""`)+`"`); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO _v83_inspection_runtime_records SELECT * FROM inspection_runtime_records; DROP TABLE inspection_runtime_records; ALTER TABLE _v83_inspection_runtime_records RENAME TO inspection_runtime_records;`); err != nil {
		return err
	}
	for _, d := range defs {
		if d.kind == "index" {
			if _, err = tx.ExecContext(ctx, d.sql); err != nil {
				return fmt.Errorf("restore %s: %w", d.name, err)
			}
		}
		if d.kind == "trigger" {
			if _, err = tx.ExecContext(ctx, d.sql); err != nil {
				return fmt.Errorf("restore %s: %w", d.name, err)
			}
		}
	}
	_, err = tx.ExecContext(ctx, `CREATE UNIQUE INDEX inspection_runtime_records_one_current_v83 ON inspection_runtime_records(inspection_id) WHERE state NOT IN ('FINALIZED','FAILED','RECOVERY_REQUIRED')`)
	return err
}

// Successors intentionally retain the original workspace/workset but have a
// NULL context_probe_id: the latter is unique to the historical source Run.
// The immutable continuation receipt is the only permitted owner bridge.
func extendInspectionRecoveryWorkspaceTriggersV83(ctx context.Context, tx *sql.Tx) error {
	for _, name := range []string{"runs_workspace_inspection_owner_guard", "runs_workspace_inspection_insert_owner_guard"} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			return err
		}
		needle := "WHEN NEW.workspace_id IS NOT NULL AND NEW.operation_kind='INSPECTION' AND NOT EXISTS("
		at := strings.Index(ddl, needle)
		if at < 0 {
			return fmt.Errorf("unexpected %s", name)
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+name+`"`); err != nil {
			return err
		}
		receipt := `WHEN NEW.workspace_id IS NOT NULL AND NEW.operation_kind='INSPECTION'
AND NOT EXISTS(SELECT 1 FROM inspection_recovery_continuations c WHERE c.successor_run_id=NEW.id AND c.workspace_id=NEW.workspace_id AND c.inspection_id=NEW.operation_id AND c.project_id=NEW.project_id AND c.task_id=NEW.task_id AND c.runtime_workset_hash=NEW.runtime_workset_hash)
AND NOT EXISTS(SELECT 1 FROM recovery_status_records x JOIN runs s ON s.id=x.source_run_id JOIN context_probes p ON p.id=s.context_probe_id WHERE x.recovery_run_id=NEW.id AND x.state='PREPARED' AND NEW.purpose='RECOVERY_STATUS' AND NEW.context_probe_id IS NULL AND NEW.operation_kind='INSPECTION' AND NEW.operation_id=p.id AND NEW.project_id=s.project_id AND NEW.task_id=s.task_id AND NEW.workspace_id=p.workspace_id AND NEW.runtime_workset_hash=p.runtime_workset_hash)
AND NOT EXISTS(`
		if _, err := tx.ExecContext(ctx, ddl[:at]+receipt+ddl[at+len(needle):]); err != nil {
			return err
		}
	}
	return nil
}
