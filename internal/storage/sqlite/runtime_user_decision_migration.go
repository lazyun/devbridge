package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Extend the existing cancellation ledger, preserving every historical column,
// actor, row, index and trigger. Inspection uses the same stop protocol.
func migrateRuntimeUserDecisionV78(ctx context.Context, tx *sql.Tx) error {
	var schema string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='execution_cancellations'`).Scan(&schema); err != nil {
		return err
	}
	previous := "owner_type IN ('REVIEW','EXECUTION','TASK')"
	if !strings.Contains(schema, previous) {
		return fmt.Errorf("unexpected cancellation schema for v78")
	}
	schema = strings.Replace(schema, previous, "owner_type IN ('REVIEW','EXECUTION','TASK','INSPECTION')", 1)
	start := strings.Index(schema, "(")
	if start < 0 {
		return fmt.Errorf("invalid cancellation schema")
	}
	schema = "CREATE TABLE execution_cancellations_v78 " + schema[start:]
	type definition struct{ kind, name, sql string }
	var definitions []definition
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL AND (type='trigger' OR (type='index' AND tbl_name='execution_cancellations')) ORDER BY rowid`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var d definition
		if err = rows.Scan(&d.kind, &d.name, &d.sql); err != nil {
			rows.Close()
			return err
		}
		definitions = append(definitions, d)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, d := range definitions {
		if d.kind == "trigger" {
			if _, err = tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(d.name, `"`, `""`)+`"`); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO execution_cancellations_v78 SELECT * FROM execution_cancellations; DROP TABLE execution_cancellations; ALTER TABLE execution_cancellations_v78 RENAME TO execution_cancellations;`); err != nil {
		return err
	}
	for _, d := range definitions {
		if _, err = tx.ExecContext(ctx, d.sql); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, runtimeUserDecisionV78SQL)
	return err
}
