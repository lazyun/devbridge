package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// v82 adds an owner namespace for the pre-existing project Inspection task.
// It deliberately does not infer a canonical runtime for historical probes.
func migrateProjectInspectionV82(ctx context.Context, tx *sql.Tx) error {
	// These additions are nullable so existing v76-v81 rows retain their exact
	// meaning.  The owner fields below are populated only for conversations,
	// where FEATURE_AREA is an explicit representation of the historical owner.
	for _, q := range []string{
		`ALTER TABLE agent_conversations ADD COLUMN owner_kind TEXT`,
		`ALTER TABLE agent_conversations ADD COLUMN owner_id TEXT`,
		`ALTER TABLE workspaces ADD COLUMN owner_kind TEXT NOT NULL DEFAULT 'FEATURE_AREA'`,
		`ALTER TABLE workspaces ADD COLUMN owner_id TEXT`,
		`ALTER TABLE workspaces ADD COLUMN inspection_task_id TEXT REFERENCES tasks(id)`,
		`ALTER TABLE workspaces ADD COLUMN inspection_id TEXT REFERENCES context_probes(id)`,
		`ALTER TABLE run_repository_bindings ADD COLUMN inspection_id TEXT REFERENCES context_probes(id)`,
		`ALTER TABLE context_probes ADD COLUMN canonical_target_receipt_json TEXT CHECK(canonical_target_receipt_json IS NULL OR json_valid(canonical_target_receipt_json))`,
		`ALTER TABLE context_probes ADD COLUMN primary_member_id TEXT`,
		`ALTER TABLE context_probes ADD COLUMN workspace_id TEXT REFERENCES workspaces(id)`,
		`ALTER TABLE context_probes ADD COLUMN runtime_workset_json TEXT CHECK(runtime_workset_json IS NULL OR json_valid(runtime_workset_json))`,
		`ALTER TABLE context_probes ADD COLUMN runtime_workset_hash TEXT`,
		`ALTER TABLE context_probes ADD COLUMN report_hash TEXT`,
		`ALTER TABLE context_probes ADD COLUMN result_hash TEXT`,
		`ALTER TABLE context_probes ADD COLUMN runtime_limitation TEXT`,
		`ALTER TABLE context_probes ADD COLUMN canonical_request_id TEXT`,
		`ALTER TABLE context_probes ADD COLUMN canonical_payload_hash TEXT`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	// The historical conversation table made feature_area_id NOT NULL.  A
	// project Inspection has no FeatureArea by contract, so rebuild only this
	// table while retaining every legacy trigger and index definition.
	if err := makeConversationFeatureAreaNullableV82(ctx, tx); err != nil {
		return err
	}
	if err := makeOwnerFeatureAreaNullableV82(ctx, tx, "workspaces"); err != nil {
		return err
	}
	if err := makeOwnerFeatureAreaNullableV82(ctx, tx, "run_repository_bindings"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS workspaces_feature_area_owner;
DROP TRIGGER IF EXISTS workspaces_worktree_owner_guard;
DROP TRIGGER IF EXISTS workspaces_worktree_insert_guard;
DROP TRIGGER IF EXISTS workspace_member_worktree_owner;
DROP TRIGGER IF EXISTS workspace_member_insert_worktree_owner;
CREATE TRIGGER workspaces_feature_area_owner BEFORE INSERT ON workspaces
WHEN NOT ((NEW.owner_kind='FEATURE_AREA' AND NEW.owner_id=NEW.feature_area_id AND NEW.feature_area_id IS NOT NULL AND EXISTS(SELECT 1 FROM feature_areas f JOIN repositories r ON r.project_id=f.project_id WHERE f.id=NEW.feature_area_id AND f.project_id=NEW.project_id AND (r.id=NEW.repository_id OR NEW.repository_id IS NULL) AND f.status IN ('DISCOVERING','ACTIVE') AND f.legacy_read_only=0 AND f.current_workspace_id IS NULL))
 OR (NEW.owner_kind='PROJECT_INSPECTION' AND NEW.feature_area_id IS NULL AND NEW.repository_id IS NULL AND NEW.base_commit IS NULL AND NEW.owner_id=NEW.inspection_id AND NEW.inspection_id IS NOT NULL AND NEW.inspection_task_id IS NOT NULL AND EXISTS(SELECT 1 FROM context_probes p WHERE p.id=NEW.inspection_id AND p.project_id=NEW.project_id AND p.task_id=NEW.inspection_task_id)))
BEGIN SELECT RAISE(ABORT,'Workspace requires exact owner'); END;
CREATE TRIGGER workspaces_worktree_owner_guard BEFORE UPDATE OF worktree_id ON workspaces
WHEN NEW.worktree_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM worktrees w JOIN runs r ON r.id=w.run_id WHERE w.id=NEW.worktree_id AND w.repository_id=NEW.repository_id AND (NEW.owner_kind='PROJECT_INSPECTION' AND r.context_probe_id=NEW.inspection_id AND r.project_id=NEW.project_id OR NEW.owner_kind='FEATURE_AREA' AND w.base_commit=NEW.base_commit AND r.feature_area_id=NEW.feature_area_id AND r.project_id=NEW.project_id))
BEGIN SELECT RAISE(ABORT,'worktree must belong to exact workspace owner'); END;
CREATE TRIGGER workspaces_worktree_insert_guard BEFORE INSERT ON workspaces
WHEN NEW.worktree_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM worktrees w JOIN runs r ON r.id=w.run_id WHERE w.id=NEW.worktree_id AND w.repository_id=NEW.repository_id AND (NEW.owner_kind='PROJECT_INSPECTION' AND r.context_probe_id=NEW.inspection_id AND r.project_id=NEW.project_id OR NEW.owner_kind='FEATURE_AREA' AND w.base_commit=NEW.base_commit AND r.feature_area_id=NEW.feature_area_id AND r.project_id=NEW.project_id))
BEGIN SELECT RAISE(ABORT,'worktree must belong to exact workspace owner'); END;
CREATE TRIGGER workspace_member_worktree_owner BEFORE UPDATE OF worktree_id ON workspace_members
WHEN NEW.worktree_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM workspaces s JOIN worktrees w ON w.id=NEW.worktree_id JOIN runs r ON r.id=w.run_id WHERE s.id=NEW.workspace_id AND ((s.owner_kind='PROJECT_INSPECTION' AND s.inspection_id=r.context_probe_id AND s.project_id=r.project_id) OR (s.owner_kind='FEATURE_AREA' AND s.feature_area_id=r.feature_area_id AND s.project_id=r.project_id)))
BEGIN SELECT RAISE(ABORT,'member worktree ownership mismatch'); END;
CREATE TRIGGER workspace_member_insert_worktree_owner BEFORE INSERT ON workspace_members
WHEN NEW.worktree_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM workspaces s JOIN worktrees w ON w.id=NEW.worktree_id JOIN runs r ON r.id=w.run_id WHERE s.id=NEW.workspace_id AND ((s.owner_kind='PROJECT_INSPECTION' AND s.inspection_id=r.context_probe_id AND s.project_id=r.project_id) OR (s.owner_kind='FEATURE_AREA' AND s.feature_area_id=r.feature_area_id AND s.project_id=r.project_id)))
BEGIN SELECT RAISE(ABORT,'member worktree ownership mismatch'); END;
DROP TRIGGER IF EXISTS agent_conversations_current_lifecycle;
DROP TRIGGER IF EXISTS agent_conversations_linear_predecessor;
CREATE TRIGGER agent_conversations_current_lifecycle BEFORE INSERT ON agent_conversations
WHEN NEW.ended_at IS NULL AND NOT ((NEW.owner_kind='FEATURE_AREA' AND NEW.owner_id=NEW.feature_area_id AND NEW.feature_area_id IS NOT NULL AND EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND project_id=NEW.project_id AND legacy_read_only=0 AND status IN ('DISCOVERING','ACTIVE')))
 OR (NEW.owner_kind='PROJECT_INSPECTION' AND NEW.feature_area_id IS NULL AND NEW.owner_id IS NOT NULL AND EXISTS(SELECT 1 FROM context_probes p WHERE p.id=NEW.owner_id AND p.project_id=NEW.project_id AND p.task_id IS NOT NULL)))
BEGIN SELECT RAISE(ABORT,'Agent owner cannot own current context'); END;
CREATE TRIGGER agent_conversations_linear_predecessor BEFORE INSERT ON agent_conversations
WHEN NEW.predecessor_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM agent_conversations p WHERE p.id=NEW.predecessor_id AND p.owner_kind=NEW.owner_kind AND p.owner_id=NEW.owner_id AND p.ended_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'agent conversation predecessor must be historical in the same owner'); END;
DROP INDEX IF EXISTS context_probes_one_running_per_project;
CREATE UNIQUE INDEX context_probes_one_legacy_running_per_project_v82 ON context_probes(project_id) WHERE status='RUNNING' AND canonical_target_receipt_json IS NULL;
UPDATE agent_conversations SET owner_kind='FEATURE_AREA',owner_id=feature_area_id WHERE owner_kind IS NULL AND feature_area_id IS NOT NULL;
UPDATE workspaces SET owner_id=feature_area_id WHERE owner_kind='FEATURE_AREA' AND owner_id IS NULL;
CREATE UNIQUE INDEX agent_conversations_one_current_owner_v82 ON agent_conversations(owner_kind,owner_id) WHERE ended_at IS NULL;
CREATE UNIQUE INDEX workspaces_one_current_inspection_v82 ON workspaces(inspection_id) WHERE inspection_id IS NOT NULL AND status<>'RETIRED';
CREATE UNIQUE INDEX context_probes_canonical_request_v82 ON context_probes(project_id,canonical_request_id) WHERE canonical_request_id IS NOT NULL;
	CREATE TABLE project_inspection_agent_claims(
 inspection_id TEXT PRIMARY KEY REFERENCES context_probes(id),task_id TEXT NOT NULL REFERENCES tasks(id),run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),project_id TEXT NOT NULL REFERENCES projects(id),runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),created_at TEXT NOT NULL,
 UNIQUE(task_id,run_id)
);

CREATE TABLE inspection_runtime_records(
 run_id TEXT PRIMARY KEY REFERENCES runs(id),inspection_id TEXT NOT NULL UNIQUE REFERENCES context_probes(id),runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),state TEXT NOT NULL CHECK(state IN ('PREPARED','RUN_CREATED','UNKNOWN','RUNNING','REPORT_READY','FINALIZED','FAILED','RECOVERY_REQUIRED')),record_json TEXT NOT NULL CHECK(json_valid(record_json)),created_at TEXT NOT NULL,updated_at TEXT NOT NULL
);
CREATE TABLE inspection_runtime_adoptions(run_id TEXT NOT NULL REFERENCES runs(id),inspection_id TEXT NOT NULL REFERENCES context_probes(id),previous_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),current_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),created_at TEXT NOT NULL,PRIMARY KEY(run_id,current_epoch));
CREATE TRIGGER inspection_runtime_adoption_immutable BEFORE UPDATE ON inspection_runtime_adoptions BEGIN SELECT RAISE(ABORT,'Inspection adoption immutable'); END;
CREATE TRIGGER inspection_runtime_adoption_no_delete BEFORE DELETE ON inspection_runtime_adoptions BEGIN SELECT RAISE(ABORT,'Inspection adoption durable'); END;
CREATE TRIGGER inspection_runtime_identity_immutable BEFORE UPDATE OF run_id,inspection_id,runtime_epoch ON inspection_runtime_records
WHEN NEW.run_id IS NOT OLD.run_id OR NEW.inspection_id IS NOT OLD.inspection_id OR (NEW.runtime_epoch IS NOT OLD.runtime_epoch AND NOT EXISTS(SELECT 1 FROM inspection_runtime_adoptions a WHERE a.run_id=OLD.run_id AND a.inspection_id=OLD.inspection_id AND a.previous_epoch=OLD.runtime_epoch AND a.current_epoch=NEW.runtime_epoch))
BEGIN SELECT RAISE(ABORT,'Inspection runtime identity immutable'); END;
CREATE TRIGGER inspection_runtime_no_delete BEFORE DELETE ON inspection_runtime_records BEGIN SELECT RAISE(ABORT,'Inspection runtime evidence durable'); END;
CREATE TRIGGER inspection_runtime_artifacts_immutable BEFORE UPDATE OF record_json ON inspection_runtime_records
WHEN (json_extract(OLD.record_json,'$._before') IS NOT NULL AND json_extract(OLD.record_json,'$._before') IS NOT json_extract(NEW.record_json,'$._before'))
 OR (json_extract(OLD.record_json,'$._report') IS NOT NULL AND json_extract(OLD.record_json,'$._report') IS NOT json_extract(NEW.record_json,'$._report'))
 OR (json_extract(OLD.record_json,'$._schema') IS NOT NULL AND json_extract(OLD.record_json,'$._schema') IS NOT json_extract(NEW.record_json,'$._schema'))
 OR (json_extract(OLD.record_json,'$._after') IS NOT NULL AND json_extract(OLD.record_json,'$._after') IS NOT json_extract(NEW.record_json,'$._after'))
 OR (json_extract(OLD.record_json,'$._finalization') IS NOT NULL AND json_extract(OLD.record_json,'$._finalization') IS NOT json_extract(NEW.record_json,'$._finalization'))
BEGIN SELECT RAISE(ABORT,'Inspection runtime artifact immutable'); END;
CREATE TRIGGER project_inspection_claim_immutable BEFORE UPDATE ON project_inspection_agent_claims BEGIN SELECT RAISE(ABORT,'Inspection claim immutable'); END;
CREATE TRIGGER project_inspection_claim_no_delete BEFORE DELETE ON project_inspection_agent_claims
WHEN NOT EXISTS(SELECT 1 FROM inspection_runtime_records r WHERE r.run_id=OLD.run_id AND r.inspection_id=OLD.inspection_id AND r.state IN ('FINALIZED','FAILED','RECOVERY_REQUIRED') AND json_extract(r.record_json,'$._finalization') IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'Inspection claim release must finalize'); END;
CREATE TRIGGER project_inspection_claim_owner BEFORE INSERT ON project_inspection_agent_claims
WHEN NOT EXISTS(SELECT 1 FROM context_probes p JOIN runs r ON r.id=NEW.run_id JOIN tasks t ON t.id=NEW.task_id WHERE p.id=NEW.inspection_id AND p.project_id=NEW.project_id AND p.task_id=NEW.task_id AND r.project_id=NEW.project_id AND r.task_id=NEW.task_id AND r.context_probe_id=NEW.inspection_id AND r.runtime_epoch=NEW.runtime_epoch AND r.operation_kind='INSPECTION')
BEGIN SELECT RAISE(ABORT,'Inspection claim requires exact probe Run owner'); END;
CREATE TRIGGER agent_conversation_owner_immutable_v82 BEFORE UPDATE OF owner_kind,owner_id ON agent_conversations
BEGIN SELECT RAISE(ABORT,'Agent conversation owner immutable'); END;
CREATE TRIGGER agent_conversation_owner_insert_v82 BEFORE INSERT ON agent_conversations
WHEN (NEW.owner_kind='FEATURE_AREA' AND (NEW.feature_area_id IS NULL OR NEW.owner_id<>NEW.feature_area_id)) OR (NEW.owner_kind='PROJECT_INSPECTION' AND (NEW.feature_area_id IS NOT NULL OR NEW.owner_id IS NULL)) OR NEW.owner_kind NOT IN ('FEATURE_AREA','PROJECT_INSPECTION')
BEGIN SELECT RAISE(ABORT,'Agent conversation owner invalid'); END;
CREATE TRIGGER context_probe_canonical_identity_immutable_v82 BEFORE UPDATE OF canonical_target_receipt_json,primary_member_id,workspace_id,runtime_workset_json,runtime_workset_hash,canonical_request_id,canonical_payload_hash ON context_probes
WHEN (OLD.canonical_target_receipt_json IS NOT NULL AND NEW.canonical_target_receipt_json IS NOT OLD.canonical_target_receipt_json)
 OR (OLD.primary_member_id IS NOT NULL AND NEW.primary_member_id IS NOT OLD.primary_member_id)
 OR (OLD.workspace_id IS NOT NULL AND NEW.workspace_id IS NOT OLD.workspace_id)
 OR (OLD.runtime_workset_json IS NOT NULL AND NEW.runtime_workset_json IS NOT OLD.runtime_workset_json)
 OR (OLD.runtime_workset_hash IS NOT NULL AND NEW.runtime_workset_hash IS NOT OLD.runtime_workset_hash)
 OR (OLD.canonical_request_id IS NOT NULL AND NEW.canonical_request_id IS NOT OLD.canonical_request_id)
 OR (OLD.canonical_payload_hash IS NOT NULL AND NEW.canonical_payload_hash IS NOT OLD.canonical_payload_hash)
	BEGIN SELECT RAISE(ABORT,'Inspection canonical receipt immutable'); END;`); err != nil {
		return err
	}
	if err := extendInspectionEpochTriggersV82(ctx, tx); err != nil {
		return err
	}
	if err := extendInspectionBindingTriggerV82(ctx, tx); err != nil {
		return err
	}
	if err := extendInspectionWorkspaceTriggersV82(ctx, tx); err != nil {
		return err
	}
	return nil
}

func extendInspectionWorkspaceTriggersV82(ctx context.Context, tx *sql.Tx) error {
	for _, name := range []string{"runs_workspace_owner_guard", "runs_workspace_insert_owner_guard"} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			return err
		}
		needle := "WHEN NEW.workspace_id IS NOT NULL AND NOT EXISTS("
		at := strings.Index(ddl, needle)
		if at < 0 {
			return fmt.Errorf("unexpected %s", name)
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+name+`"`); err != nil {
			return err
		}
		ddl = ddl[:at] + "WHEN NEW.workspace_id IS NOT NULL AND NEW.operation_kind<>'INSPECTION' AND NOT EXISTS(" + ddl[at+len(needle):]
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `CREATE TRIGGER runs_workspace_inspection_owner_guard BEFORE UPDATE OF workspace_id ON runs
WHEN NEW.workspace_id IS NOT NULL AND NEW.operation_kind='INSPECTION' AND NOT EXISTS(SELECT 1 FROM workspaces w JOIN context_probes p ON p.id=NEW.context_probe_id WHERE w.id=NEW.workspace_id AND w.project_id=NEW.project_id AND w.owner_kind='PROJECT_INSPECTION' AND w.owner_id=p.id AND w.inspection_id=p.id AND w.inspection_task_id=NEW.task_id AND w.feature_area_id IS NULL AND w.repository_id IS NULL AND w.base_commit IS NULL AND w.runtime_quarantined=0 AND p.project_id=NEW.project_id AND p.task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT,'Inspection Run workspace must match canonical operation environment'); END;
CREATE TRIGGER runs_workspace_inspection_insert_owner_guard BEFORE INSERT ON runs
WHEN NEW.workspace_id IS NOT NULL AND NEW.operation_kind='INSPECTION' AND NOT EXISTS(SELECT 1 FROM workspaces w JOIN context_probes p ON p.id=NEW.context_probe_id WHERE w.id=NEW.workspace_id AND w.project_id=NEW.project_id AND w.owner_kind='PROJECT_INSPECTION' AND w.owner_id=p.id AND w.inspection_id=p.id AND w.inspection_task_id=NEW.task_id AND w.feature_area_id IS NULL AND w.repository_id IS NULL AND w.base_commit IS NULL AND w.runtime_quarantined=0 AND p.project_id=NEW.project_id AND p.task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT,'Inspection Run workspace must match canonical operation environment'); END;`)
	return err
}

func extendInspectionBindingTriggerV82(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='run_repository_binding_owner'`).Scan(&ddl); err != nil {
		return err
	}
	needle := "WHEN NOT ("
	at := strings.Index(ddl, needle)
	if at < 0 {
		return fmt.Errorf("unexpected run binding owner trigger")
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER run_repository_binding_owner`); err != nil {
		return err
	}
	ddl = ddl[:at] + "WHEN NEW.inspection_id IS NULL AND NOT (" + ddl[at+len(needle):]
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE TRIGGER run_repository_binding_inspection_owner_v82 BEFORE INSERT ON run_repository_bindings
WHEN NEW.inspection_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM context_probes p JOIN runs r ON r.id=NEW.run_id JOIN workspaces w ON w.id=NEW.workspace_id JOIN json_each(p.canonical_target_receipt_json,'$.targets') target
 WHERE p.id=NEW.inspection_id AND r.context_probe_id=p.id AND r.project_id=NEW.project_id AND r.task_id=p.task_id
 AND w.project_id=p.project_id AND w.owner_kind='PROJECT_INSPECTION' AND w.owner_id=p.id AND w.inspection_id=p.id AND w.inspection_task_id=p.task_id
 AND NEW.feature_area_id IS NULL AND NEW.review_id IS NULL AND NEW.execution_id IS NULL
 AND json_extract(target.value,'$.repositoryId')=NEW.repository_id AND json_extract(target.value,'$.memberId')=NEW.member_id AND json_extract(target.value,'$.memberVersion')=NEW.member_version AND json_extract(target.value,'$.originCommit')=NEW.origin_commit
)
BEGIN SELECT RAISE(ABORT,'Inspection Run binding requires frozen canonical target'); END;`)
	return err
}

func extendInspectionEpochTriggersV82(ctx context.Context, tx *sql.Tx) error {
	for _, item := range []struct{ name, needle string }{
		{"runs_runtime_epoch_immutable", "run_id=OLD.id"},
		{"probe_work_binding_immutable", "run_id=OLD.run_id"},
	} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, item.name).Scan(&ddl); err != nil {
			return err
		}
		at := strings.Index(ddl, "\nBEGIN")
		if at < 0 {
			at = strings.Index(ddl, " BEGIN")
		}
		if at < 0 {
			return fmt.Errorf("unexpected %s", item.name)
		}
		exception := ` AND NOT EXISTS(SELECT 1 FROM inspection_runtime_adoptions a WHERE a.` + item.needle + ` AND a.previous_epoch=OLD.runtime_epoch AND a.current_epoch=NEW.runtime_epoch)`
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+item.name+`"`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, ddl[:at]+exception+ddl[at:]); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER project_inspection_claim_immutable;
CREATE TRIGGER project_inspection_claim_immutable BEFORE UPDATE ON project_inspection_agent_claims
WHEN NEW.inspection_id IS NOT OLD.inspection_id OR NEW.task_id IS NOT OLD.task_id OR NEW.run_id IS NOT OLD.run_id OR NEW.project_id IS NOT OLD.project_id OR (NEW.runtime_epoch IS NOT OLD.runtime_epoch AND NOT EXISTS(SELECT 1 FROM inspection_runtime_adoptions a WHERE a.run_id=OLD.run_id AND a.inspection_id=OLD.inspection_id AND a.previous_epoch=OLD.runtime_epoch AND a.current_epoch=NEW.runtime_epoch)) OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT,'Inspection claim immutable'); END;`); err != nil {
		return err
	}
	return nil
}

func makeConversationFeatureAreaNullableV82(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='agent_conversations'`).Scan(&ddl); err != nil {
		return err
	}
	if !strings.Contains(ddl, "feature_area_id TEXT NOT NULL") {
		return fmt.Errorf("unexpected agent conversation feature area constraint")
	}
	ddl = strings.Replace(ddl, "feature_area_id TEXT NOT NULL", "feature_area_id TEXT", 1)
	ddl = "CREATE TABLE _v82_agent_conversations " + ddl[strings.Index(ddl, "("):]
	type definition struct{ kind, name, sql string }
	// SQLite drops a table before the rename swap.  Capture every trigger that
	// references it, including lifecycle triggers owned by feature_areas.
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL AND ((type='trigger' AND sql LIKE '%agent_conversations%') OR (type='index' AND tbl_name='agent_conversations')) ORDER BY rowid`)
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
	oldRows, err := tx.QueryContext(ctx, `PRAGMA table_info(agent_conversations)`)
	if err != nil {
		return err
	}
	var cols []string
	for oldRows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var def any
		if err = oldRows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			oldRows.Close()
			return err
		}
		cols = append(cols, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
	}
	if err = oldRows.Err(); err != nil {
		oldRows.Close()
		return err
	}
	if err = oldRows.Close(); err != nil {
		return err
	}
	list := strings.Join(cols, ",")
	if _, err = tx.ExecContext(ctx, `INSERT INTO _v82_agent_conversations(`+list+`) SELECT `+list+` FROM agent_conversations; DROP TABLE agent_conversations; ALTER TABLE _v82_agent_conversations RENAME TO agent_conversations;`); err != nil {
		return err
	}
	for _, d := range defs {
		if _, err = tx.ExecContext(ctx, d.sql); err != nil {
			return fmt.Errorf("restore %s: %w", d.name, err)
		}
	}
	return nil
}

func makeOwnerFeatureAreaNullableV82(ctx context.Context, tx *sql.Tx, table string) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
		return err
	}
	if !strings.Contains(ddl, "feature_area_id TEXT NOT NULL") {
		return fmt.Errorf("unexpected %s feature area constraint", table)
	}
	ddl = "CREATE TABLE _v82_" + table + " " + strings.Replace(ddl[strings.Index(ddl, "("):], "feature_area_id TEXT NOT NULL", "feature_area_id TEXT", 1)
	if table == "run_repository_bindings" {
		old := "CHECK((review_id IS NOT NULL)+(execution_id IS NOT NULL)=1)"
		if !strings.Contains(ddl, old) {
			return fmt.Errorf("unexpected run binding owner check")
		}
		ddl = strings.Replace(ddl, old, "CHECK((review_id IS NOT NULL)+(execution_id IS NOT NULL)+(inspection_id IS NOT NULL)=1)", 1)
	}
	type definition struct{ kind, name, sql string }
	var defs []definition
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL AND ((type='trigger' AND (tbl_name=? OR sql LIKE ?)) OR (type='index' AND tbl_name=?)) ORDER BY rowid`, table, "%"+table+"%", table)
	if err != nil {
		return err
	}
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
	info, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	var cols []string
	for info.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = info.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			info.Close()
			return err
		}
		cols = append(cols, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
	}
	if err = info.Err(); err != nil {
		info.Close()
		return err
	}
	if err = info.Close(); err != nil {
		return err
	}
	list := strings.Join(cols, ",")
	if _, err = tx.ExecContext(ctx, `INSERT INTO _v82_`+table+`(`+list+`) SELECT `+list+` FROM `+table+`; DROP TABLE `+table+`; ALTER TABLE _v82_`+table+` RENAME TO `+table+`;`); err != nil {
		return err
	}
	for _, d := range defs {
		if _, err = tx.ExecContext(ctx, d.sql); err != nil {
			return fmt.Errorf("restore %s: %w", d.name, err)
		}
	}
	return nil
}

// inspectionClaimTx is intentionally narrow: runtime allocation has already
// established the exact probe-owned Run.  It is the only new claim namespace.
func inspectionClaimTx(ctx context.Context, tx *sql.Tx, inspectionID, taskID, runID, projectID, epoch string, now string) error {
	if strings.TrimSpace(inspectionID) == "" || strings.TrimSpace(taskID) == "" || strings.TrimSpace(runID) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(epoch) == "" {
		return fmt.Errorf("invalid inspection claim")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO project_inspection_agent_claims(inspection_id,task_id,run_id,project_id,runtime_epoch,created_at) VALUES(?,?,?,?,?,?)`, inspectionID, taskID, runID, projectID, epoch, now)
	return err
}

func releaseInspectionClaimTx(ctx context.Context, tx *sql.Tx, inspectionID, runID, epoch string) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM project_inspection_agent_claims WHERE inspection_id=? AND run_id=? AND runtime_epoch=?`, inspectionID, runID, epoch)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("inspection claim conflict")
	}
	return nil
}
