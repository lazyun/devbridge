package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// v75 preserves historical rows and creation provenance while introducing
// explicit per-operation use bindings. No historical definition is recoded.
func migrateMultiRepositoryUpperV75(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name IN ('worktrees','workspaces','review_grants') AND sql IS NOT NULL)`)
	if err != nil {
		return err
	}
	var objects []schemaObject
	for rows.Next() {
		var o schemaObject
		if err = rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range objects {
		if _, err = tx.ExecContext(ctx, "DROP "+strings.ToUpper(o.kind)+" "+sqlIdentifier(o.name)); err != nil {
			return err
		}
	}
	replacements := map[string][][2]string{
		"worktrees":     {{"run_id TEXT NOT NULL UNIQUE REFERENCES runs(id)", "run_id TEXT NOT NULL REFERENCES runs(id)"}},
		"workspaces":    {{"repository_id TEXT NOT NULL REFERENCES repositories(id)", "repository_id TEXT REFERENCES repositories(id)"}, {"base_commit TEXT NOT NULL CHECK(length(base_commit)=40)", "base_commit TEXT CHECK(base_commit IS NULL OR length(base_commit) IN (40,64))"}},
		"review_grants": {{"repository_id TEXT NOT NULL", "repository_id TEXT"}},
	}
	for _, table := range []string{"worktrees", "workspaces", "review_grants"} {
		var ddl string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return err
		}
		for _, pair := range replacements[table] {
			if strings.Count(ddl, pair[0]) != 1 {
				return fmt.Errorf("unexpected v74 %s schema", table)
			}
			ddl = strings.Replace(ddl, pair[0], pair[1], 1)
		}
		pos := strings.Index(ddl, "(")
		ddl = "CREATE TABLE " + sqlIdentifier("_v75_"+table) + " " + ddl[pos:]
		if _, err = tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+sqlIdentifier("_v75_"+table)+" SELECT * FROM "+sqlIdentifier(table)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DROP TABLE "+sqlIdentifier(table)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "ALTER TABLE "+sqlIdentifier("_v75_"+table)+" RENAME TO "+sqlIdentifier(table)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, multiRepositoryUpperSchema); err != nil {
		return err
	}
	for _, o := range objects {
		switch o.name {
		case "reviews_current_owner":
			o.sql = strings.Replace(o.sql, "WHEN NEW.repository_id IS NULL", "WHEN (json_array_length(NEW.repository_targets_json)=0 AND (NEW.repository_id IS NULL", 1)
			o.sql = strings.Replace(o.sql, "t.repository_id=NEW.repository_id AND t.task_type", "t.repository_id IS NEW.repository_id AND t.task_type", 1)
			o.sql = strings.Replace(o.sql, "BEGIN SELECT", ")) OR (json_array_length(NEW.repository_targets_json)>0 AND (NEW.executor_kind<>'UPPER_LLM' OR NEW.repository_id IS NOT NULL OR COALESCE(NEW.base_commit,'')<>'' OR COALESCE(NEW.workspace_id,'')='' OR NOT EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND t.repository_id IS NULL AND t.task_type='DEVELOPMENT' AND t.status='ACTIVE' AND t.phase IN ('REVIEW_REQUIRED','REVIEWING') AND f.status IN ('DISCOVERING','ACTIVE') AND f.legacy_read_only=0))) BEGIN SELECT", 1)
		case "task_current_review_guard":
			o.sql = strings.ReplaceAll(o.sql, "r.repository_id=NEW.repository_id", "r.repository_id IS NEW.repository_id")
		case "review_grants_exact_owner":
			o.sql = strings.ReplaceAll(o.sql, "repository_id=NEW.repository_id", "repository_id IS NEW.repository_id")
		case "workspaces_feature_area_owner":
			o.sql = strings.ReplaceAll(o.sql, "JOIN repositories r ON r.project_id=f.project_id", "LEFT JOIN repositories r ON r.project_id=f.project_id")
			o.sql = strings.ReplaceAll(o.sql, "r.id=NEW.repository_id", "(r.id=NEW.repository_id OR NEW.repository_id IS NULL)")
		case "runs_workspace_insert_owner_guard", "runs_workspace_owner_guard":
			o.sql = strings.ReplaceAll(o.sql, "w.repository_id=(SELECT repository_id FROM tasks WHERE id=NEW.task_id)", "(w.repository_id=(SELECT repository_id FROM tasks WHERE id=NEW.task_id) OR EXISTS(SELECT 1 FROM reviews v WHERE v.id=NEW.review_id AND v.workspace_id=w.id AND json_array_length(v.repository_targets_json)>0))")
		}
		if _, err = tx.ExecContext(ctx, o.sql); err != nil {
			return fmt.Errorf("v75 recreate %s: %w", o.name, err)
		}
	}
	return nil
}

const multiRepositoryUpperSchema = `
ALTER TABLE reviews ADD COLUMN workspace_id TEXT;
ALTER TABLE reviews ADD COLUMN repository_targets_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(repository_targets_json) AND json_type(repository_targets_json)='array');
ALTER TABLE review_grants ADD COLUMN protocol_version INTEGER NOT NULL DEFAULT 1 CHECK(protocol_version IN (1,2));
CREATE TABLE run_repository_bindings (
 run_id TEXT NOT NULL REFERENCES runs(id),review_id TEXT NOT NULL REFERENCES reviews(id),
 project_id TEXT NOT NULL REFERENCES projects(id),feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 workspace_id TEXT NOT NULL,repository_id TEXT NOT NULL REFERENCES repositories(id),member_id TEXT NOT NULL,member_version INTEGER NOT NULL CHECK(member_version>0),
 origin_commit TEXT NOT NULL CHECK(length(origin_commit) IN (40,64)),worktree_id TEXT REFERENCES worktrees(id),
 preparation TEXT NOT NULL CHECK(preparation IN ('PENDING','READY','FAILED')),failure TEXT NOT NULL DEFAULT '',
 start_snapshot TEXT,root_path TEXT,root_identity TEXT,last_observed_at TEXT,
 PRIMARY KEY(run_id,repository_id),UNIQUE(run_id,member_id),CHECK(preparation<>'READY' OR worktree_id IS NOT NULL)
);
CREATE INDEX run_repository_bindings_worktree ON run_repository_bindings(worktree_id);
CREATE TRIGGER run_repository_binding_owner BEFORE INSERT ON run_repository_bindings
WHEN NOT EXISTS(SELECT 1 FROM runs r JOIN reviews v ON v.id=r.review_id JOIN json_each(v.repository_targets_json) target JOIN repositories repo ON repo.id=NEW.repository_id
 WHERE r.id=NEW.run_id AND v.id=NEW.review_id AND v.project_id=NEW.project_id AND v.feature_area_id=NEW.feature_area_id AND v.workspace_id=NEW.workspace_id AND repo.project_id=NEW.project_id
 AND json_extract(target.value,'$.repositoryId')=NEW.repository_id AND json_extract(target.value,'$.memberId')=NEW.member_id AND json_extract(target.value,'$.memberVersion')=NEW.member_version AND json_extract(target.value,'$.originCommit')=NEW.origin_commit)
BEGIN SELECT RAISE(ABORT,'Run binding requires frozen Review target'); END;
CREATE TRIGGER run_repository_binding_identity BEFORE UPDATE ON run_repository_bindings
WHEN NEW.run_id IS NOT OLD.run_id OR NEW.review_id IS NOT OLD.review_id OR NEW.project_id IS NOT OLD.project_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.member_id IS NOT OLD.member_id OR NEW.member_version IS NOT OLD.member_version OR NEW.origin_commit IS NOT OLD.origin_commit OR (OLD.worktree_id IS NOT NULL AND NEW.worktree_id IS NOT OLD.worktree_id)
BEGIN SELECT RAISE(ABORT,'Run member binding is immutable'); END;
CREATE TRIGGER run_repository_binding_no_delete BEFORE DELETE ON run_repository_bindings BEGIN SELECT RAISE(ABORT,'Run member binding is durable'); END;
CREATE TRIGGER run_repository_binding_worktree BEFORE UPDATE OF worktree_id,preparation ON run_repository_bindings
WHEN NEW.preparation='READY' AND NOT EXISTS(SELECT 1 FROM workspace_members m JOIN workspaces w ON w.id=m.workspace_id JOIN worktrees wt ON wt.id=m.worktree_id
 WHERE m.id=NEW.member_id AND m.identity_version=NEW.member_version AND m.repository_id=NEW.repository_id AND m.origin_commit=NEW.origin_commit AND w.id=NEW.workspace_id AND w.project_id=NEW.project_id AND w.feature_area_id=NEW.feature_area_id AND m.worktree_id=NEW.worktree_id AND m.preparation='READY' AND wt.status='READY')
BEGIN SELECT RAISE(ABORT,'Run binding requires exact ready member'); END;
CREATE TRIGGER reviews_targets_immutable BEFORE UPDATE OF workspace_id,repository_targets_json ON reviews BEGIN SELECT RAISE(ABORT,'Review target set is immutable'); END;
CREATE TRIGGER review_grant_protocol_immutable BEFORE UPDATE OF protocol_version ON review_grants BEGIN SELECT RAISE(ABORT,'Review protocol is immutable'); END;
`
