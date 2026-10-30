package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// migrateExecutionRuntimeConfigV46 adds a runtime-only immutable snapshot to
// each newly authorized Execution. NULL is retained for historical rows whose
// approvals predate this capability; new decision paths always write a value.
func migrateExecutionRuntimeConfigV46(ctx context.Context, tx *sql.Tx) error {
	if err := widenReasoningEffortEnumV46(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE execution_proposals ADD COLUMN runtime_config_json TEXT`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE review_approval_materializations ADD COLUMN runtime_config_json TEXT`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER execution_proposals_runtime_config_immutable BEFORE UPDATE OF runtime_config_json ON execution_proposals WHEN OLD.runtime_config_json IS NOT NEW.runtime_config_json BEGIN SELECT RAISE(ABORT,'Execution runtime config is immutable'); END`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE TRIGGER review_approval_materializations_runtime_config_immutable BEFORE UPDATE OF runtime_config_json ON review_approval_materializations WHEN OLD.runtime_config_json IS NOT NEW.runtime_config_json BEGIN SELECT RAISE(ABORT,'Review approval runtime config is immutable'); END`)
	return err
}

// widenReasoningEffortEnumV46 keeps the persisted Review/Task selection
// contract aligned with the installed Codex capability catalog. SQLite cannot
// alter a CHECK constraint in place, so rebuild only these two tables while
// retaining every row and recreating the existing indexes and triggers.
func widenReasoningEffortEnumV46(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA legacy_alter_table=OFF`)

	tableSQL := make(map[string]string, 2)
	for _, table := range []string{"tasks", "reviews"} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return err
		}
		old := "reasoning_effort IN ('low','medium','high','xhigh')"
		updated := "reasoning_effort IN ('low','medium','high','xhigh','max','ultra')"
		if !strings.Contains(ddl, old) {
			return fmt.Errorf("%s reasoning effort constraint is not the v45 contract", table)
		}
		tableSQL[table] = strings.Replace(ddl, old, updated, 1)
	}

	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name IN ('tasks','reviews') AND sql IS NOT NULL)`)
	if err != nil {
		return err
	}
	var retained []schemaObject
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.sql); err != nil {
			rows.Close()
			return err
		}
		retained = append(retained, object)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, object := range retained {
		if _, err := tx.ExecContext(ctx, "DROP "+strings.ToUpper(object.kind)+" "+sqlIdentifier(object.name)); err != nil {
			return err
		}
	}
	for _, table := range []string{"tasks", "reviews"} {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+sqlIdentifier(table)+" RENAME TO "+sqlIdentifier("_source45_"+table)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, tableSQL[table]); err != nil {
			return err
		}
		columns, err := schemaColumns(ctx, tx, "_source45_"+table)
		if err != nil {
			return err
		}
		for i := range columns {
			columns[i] = sqlIdentifier(columns[i])
		}
		shared := strings.Join(columns, ",")
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+sqlIdentifier(table)+"("+shared+") SELECT "+shared+" FROM "+sqlIdentifier("_source45_"+table)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DROP TABLE "+sqlIdentifier("_source45_"+table)); err != nil {
			return err
		}
	}
	for _, object := range retained {
		if _, err := tx.ExecContext(ctx, object.sql); err != nil {
			return err
		}
	}
	return nil
}
