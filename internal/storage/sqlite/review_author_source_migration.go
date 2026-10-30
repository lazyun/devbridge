package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// V69 records new Review authors as an immutable union. The legacy Session
// column remains historical evidence only; old rows deliberately stay source
// unknown rather than being relabelled as an active Session author.
const reviewAuthorSourceMigrationSQL = `
ALTER TABLE reviews ADD COLUMN author_kind TEXT;
ALTER TABLE reviews ADD COLUMN author_id TEXT;
CREATE UNIQUE INDEX review_author_start_request ON reviews(project_id,author_kind,author_id,id) WHERE author_kind IS NOT NULL;
CREATE TRIGGER review_author_source_insert BEFORE INSERT ON reviews
WHEN (NEW.author_kind IS NULL) <> (NEW.author_id IS NULL)
 OR (NEW.author_kind IS NOT NULL AND NEW.author_kind NOT IN ('AI_SESSION','LOCAL_OPERATOR'))
 OR (NEW.author_kind='AI_SESSION' AND (NEW.created_by_ai_session_id IS NULL OR NEW.author_id<>NEW.created_by_ai_session_id))
 OR (NEW.author_kind='LOCAL_OPERATOR' AND NEW.created_by_ai_session_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'Review author source is invalid'); END;
CREATE TRIGGER review_author_source_immutable BEFORE UPDATE OF author_kind,author_id,created_by_ai_session_id ON reviews
WHEN OLD.author_kind IS NOT NEW.author_kind OR OLD.author_id IS NOT NEW.author_id OR OLD.created_by_ai_session_id IS NOT NEW.created_by_ai_session_id
BEGIN SELECT RAISE(ABORT,'Review author source is immutable'); END;
`

// V69 also removes the last mandatory Session columns from local Coding
// Review approval and its initial candidate. Existing Session values remain
// immutable provenance; new actor_kind/id columns identify the actual author.
func migrateReviewAuthorSourceV69(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, reviewAuthorSourceMigrationSQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA legacy_alter_table=OFF`)
	for _, source := range []struct{ table, declaration string }{
		{"reviews", "created_by_ai_session_id TEXT NOT NULL"},
		{"review_approval_materializations", "ai_session_id TEXT NOT NULL"},
		{"execution_instruction_sources", "ai_session_id TEXT NOT NULL"},
		{"task_plans", "ai_session_id TEXT NOT NULL"},
		{"execution_proposals", "ai_session_id TEXT NOT NULL"},
	} {
		if err := relaxSessionEvidenceV69(ctx, tx, source.table, source.declaration); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
ALTER TABLE review_approval_materializations ADD COLUMN actor_kind TEXT;
ALTER TABLE review_approval_materializations ADD COLUMN actor_id TEXT;
ALTER TABLE execution_instruction_sources ADD COLUMN actor_kind TEXT;
ALTER TABLE execution_instruction_sources ADD COLUMN actor_id TEXT;
CREATE TRIGGER review_approval_materialization_actor_pair BEFORE INSERT ON review_approval_materializations
 WHEN (NEW.actor_kind IS NULL) <> (NEW.actor_id IS NULL) OR (NEW.actor_kind IS NOT NULL AND NEW.actor_kind NOT IN ('AI_SESSION','LOCAL_OPERATOR'))
 BEGIN SELECT RAISE(ABORT,'Review approval materialization actor is invalid'); END;
CREATE TRIGGER execution_instruction_source_actor_pair BEFORE INSERT ON execution_instruction_sources
 WHEN (NEW.actor_kind IS NULL) <> (NEW.actor_id IS NULL) OR (NEW.actor_kind IS NOT NULL AND NEW.actor_kind NOT IN ('AI_SESSION','LOCAL_OPERATOR'))
 BEGIN SELECT RAISE(ABORT,'Execution instruction source actor is invalid'); END;
CREATE TRIGGER execution_instruction_source_actor_immutable BEFORE UPDATE OF actor_kind,actor_id ON execution_instruction_sources
 WHEN OLD.actor_kind IS NOT NEW.actor_kind OR OLD.actor_id IS NOT NEW.actor_id
 BEGIN SELECT RAISE(ABORT,'Execution instruction source actor is immutable'); END;
`)
	return err
}

func relaxSessionEvidenceV69(ctx context.Context, tx *sql.Tx, table, declaration string) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
		return err
	}
	if !strings.Contains(ddl, declaration) {
		return fmt.Errorf("unexpected %s Session evidence schema", table)
	}
	ddl = strings.Replace(ddl, declaration, strings.Replace(declaration, " TEXT NOT NULL", " TEXT", 1), 1)
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE tbl_name=? AND sql IS NOT NULL AND type IN ('index','trigger')`, table)
	if err != nil {
		return err
	}
	var retained []schemaObject
	for rows.Next() {
		var o schemaObject
		if err = rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			rows.Close()
			return err
		}
		retained = append(retained, o)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, o := range retained {
		if _, err = tx.ExecContext(ctx, "DROP "+strings.ToUpper(o.kind)+" "+sqlIdentifier(o.name)); err != nil {
			return err
		}
	}
	backup := "_source68_" + table
	if _, err = tx.ExecContext(ctx, "ALTER TABLE "+sqlIdentifier(table)+" RENAME TO "+sqlIdentifier(backup)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+sqlIdentifier(table)+" SELECT * FROM "+sqlIdentifier(backup)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DROP TABLE "+sqlIdentifier(backup)); err != nil {
		return err
	}
	for _, o := range retained {
		if _, err = tx.ExecContext(ctx, o.sql); err != nil {
			return err
		}
	}
	return nil
}
