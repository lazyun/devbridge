// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// migrateDynamicModels removes only the historical three-model CHECK from
// Tasks and Reviews. Reasoning-effort checks and all ownership objects remain
// intact. The migration framework runs foreign_key_check before commit.
func migrateDynamicModels(ctx context.Context, tx *sql.Tx) error {
	type object struct{ kind, name, ddl string }
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name IN ('tasks','reviews') AND sql IS NOT NULL) ORDER BY type,name`)
	if err != nil {
		return err
	}
	var objects []object
	for rows.Next() {
		var value object
		if err := rows.Scan(&value.kind, &value.name, &value.ddl); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range objects {
		if _, err := tx.ExecContext(ctx, `DROP `+strings.ToUpper(value.kind)+` "`+value.name+`"`); err != nil {
			return err
		}
	}

	closed := " CHECK(model IS NULL OR model IN ('gpt-5.6-luna','gpt-5.6-terra','gpt-5.6-sol'))"
	for _, table := range []string{"tasks", "reviews"} {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return err
		}
		if !strings.Contains(ddl, closed) {
			return fmt.Errorf("%s model constraint not found", table)
		}
		body := strings.Replace(ddl[strings.Index(ddl, "("):], closed, "", 1)
		statement := `CREATE TABLE "` + table + `_v39" ` + body + `; INSERT INTO "` + table + `_v39" SELECT * FROM "` + table + `"; DROP TABLE "` + table + `"; ALTER TABLE "` + table + `_v39" RENAME TO "` + table + `";`
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild %s: %w", table, err)
		}
	}
	for _, value := range objects {
		if _, err := tx.ExecContext(ctx, value.ddl); err != nil {
			return fmt.Errorf("restore %s: %w", value.name, err)
		}
	}
	return nil
}
