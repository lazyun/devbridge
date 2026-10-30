package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// v93 permits a Recovery child to own a fresh managed Workspace and member
// worktrees. It changes only owner validation; no historical workspace or
// source Run is rewritten.
func migrateRecoveryWorkspaceV93(ctx context.Context, tx *sql.Tx) error {
	for _, item := range []struct{ name, needle string }{
		{"runs_workspace_owner_guard", "NEW.operation_kind<>'INSPECTION'"},
		{"runs_workspace_insert_owner_guard", "NEW.operation_kind<>'INSPECTION'"},
		{"runs_workspace_inspection_owner_guard", "NEW.operation_kind='INSPECTION'"},
		{"runs_workspace_inspection_insert_owner_guard", "NEW.operation_kind='INSPECTION'"},
	} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, item.name).Scan(&ddl); err != nil {
			return err
		}
		if !strings.Contains(ddl, item.needle) {
			return fmt.Errorf("unexpected %s", item.name)
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+item.name+`"`); err != nil {
			return err
		}
		exception := ` AND NOT EXISTS(SELECT 1 FROM recovery_child_admissions a JOIN recovery_status_records rec ON rec.id=a.recovery_id WHERE rec.recovery_run_id=NEW.id)`
		ddl = strings.Replace(ddl, item.needle, item.needle+exception, 1)
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
CREATE TRIGGER runs_workspace_recovery_owner_guard BEFORE UPDATE OF workspace_id ON runs
WHEN NEW.workspace_id IS NOT NULL AND EXISTS(SELECT 1 FROM recovery_child_admissions a JOIN recovery_status_records rec ON rec.id=a.recovery_id WHERE rec.recovery_run_id=NEW.id)
 AND NOT EXISTS(SELECT 1 FROM workspaces w JOIN recovery_status_records rec ON rec.id=w.owner_id JOIN recovery_child_admissions a ON a.recovery_id=rec.id
 WHERE w.id=NEW.workspace_id AND rec.recovery_run_id=NEW.id AND w.owner_kind='RECOVERY_STATUS' AND w.project_id=NEW.project_id AND w.feature_area_id IS NULL AND w.repository_id IS NULL AND w.base_commit IS NULL AND w.runtime_quarantined=0 AND NEW.purpose='RECOVERY_STATUS' AND NEW.runtime_epoch=rec.runtime_epoch)
BEGIN SELECT RAISE(ABORT,'Recovery child Run workspace must match canonical owner'); END;
CREATE TRIGGER runs_workspace_recovery_insert_owner_guard BEFORE INSERT ON runs
WHEN NEW.workspace_id IS NOT NULL AND EXISTS(SELECT 1 FROM recovery_child_admissions a JOIN recovery_status_records rec ON rec.id=a.recovery_id WHERE rec.recovery_run_id=NEW.id)
 AND NOT EXISTS(SELECT 1 FROM workspaces w JOIN recovery_status_records rec ON rec.id=w.owner_id JOIN recovery_child_admissions a ON a.recovery_id=rec.id
 WHERE w.id=NEW.workspace_id AND rec.recovery_run_id=NEW.id AND w.owner_kind='RECOVERY_STATUS' AND w.project_id=NEW.project_id AND w.feature_area_id IS NULL AND w.repository_id IS NULL AND w.base_commit IS NULL AND w.runtime_quarantined=0 AND NEW.purpose='RECOVERY_STATUS' AND NEW.runtime_epoch=rec.runtime_epoch)
BEGIN SELECT RAISE(ABORT,'Recovery child Run workspace must match canonical owner'); END;
DROP TRIGGER IF EXISTS workspaces_feature_area_owner;
CREATE TRIGGER workspaces_feature_area_owner BEFORE INSERT ON workspaces
WHEN NOT (
 (NEW.owner_kind='FEATURE_AREA' AND NEW.owner_id=NEW.feature_area_id AND NEW.feature_area_id IS NOT NULL AND EXISTS(SELECT 1 FROM feature_areas f JOIN repositories r ON r.project_id=f.project_id WHERE f.id=NEW.feature_area_id AND f.project_id=NEW.project_id AND (r.id=NEW.repository_id OR NEW.repository_id IS NULL) AND f.status IN ('DISCOVERING','ACTIVE') AND f.legacy_read_only=0 AND f.current_workspace_id IS NULL))
 OR (NEW.owner_kind='PROJECT_INSPECTION' AND NEW.feature_area_id IS NULL AND NEW.repository_id IS NULL AND NEW.base_commit IS NULL AND NEW.owner_id=NEW.inspection_id AND NEW.inspection_id IS NOT NULL AND NEW.inspection_task_id IS NOT NULL AND EXISTS(SELECT 1 FROM context_probes p WHERE p.id=NEW.inspection_id AND p.project_id=NEW.project_id AND p.task_id=NEW.inspection_task_id))
 OR (NEW.owner_kind='RECOVERY_STATUS' AND NEW.owner_id IS NOT NULL AND NEW.feature_area_id IS NULL AND NEW.repository_id IS NULL AND NEW.base_commit IS NULL AND NEW.inspection_id IS NULL AND NEW.inspection_task_id IS NULL AND EXISTS(SELECT 1 FROM recovery_status_records rec JOIN runs source ON source.id=rec.source_run_id WHERE rec.id=NEW.owner_id AND source.project_id=NEW.project_id AND rec.recovery_run_id IS NOT NULL))
)
BEGIN SELECT RAISE(ABORT,'Workspace requires exact owner'); END;

DROP TRIGGER IF EXISTS workspace_member_worktree_owner;
CREATE TRIGGER workspace_member_worktree_owner BEFORE UPDATE OF worktree_id ON workspace_members
WHEN NEW.worktree_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM workspaces s JOIN worktrees w ON w.id=NEW.worktree_id JOIN runs r ON r.id=w.run_id WHERE s.id=NEW.workspace_id AND (
 (s.owner_kind='PROJECT_INSPECTION' AND s.inspection_id=r.context_probe_id AND s.project_id=r.project_id)
 OR (s.owner_kind='FEATURE_AREA' AND s.feature_area_id=r.feature_area_id AND s.project_id=r.project_id)
 OR (s.owner_kind='RECOVERY_STATUS' AND r.purpose='RECOVERY_STATUS' AND s.project_id=r.project_id AND s.feature_area_id IS NULL AND EXISTS(SELECT 1 FROM recovery_status_records rec WHERE rec.id=s.owner_id AND rec.recovery_run_id=r.id))
))
BEGIN SELECT RAISE(ABORT,'member worktree ownership mismatch'); END;

DROP TRIGGER IF EXISTS workspace_member_insert_worktree_owner;
CREATE TRIGGER workspace_member_insert_worktree_owner BEFORE INSERT ON workspace_members
WHEN NEW.worktree_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM workspaces s JOIN worktrees w ON w.id=NEW.worktree_id JOIN runs r ON r.id=w.run_id WHERE s.id=NEW.workspace_id AND (
 (s.owner_kind='PROJECT_INSPECTION' AND s.inspection_id=r.context_probe_id AND s.project_id=r.project_id)
 OR (s.owner_kind='FEATURE_AREA' AND s.feature_area_id=r.feature_area_id AND s.project_id=r.project_id)
 OR (s.owner_kind='RECOVERY_STATUS' AND r.purpose='RECOVERY_STATUS' AND s.project_id=r.project_id AND s.feature_area_id IS NULL AND EXISTS(SELECT 1 FROM recovery_status_records rec WHERE rec.id=s.owner_id AND rec.recovery_run_id=r.id))
))
BEGIN SELECT RAISE(ABORT,'member worktree ownership mismatch'); END;
`)
	return err
}
