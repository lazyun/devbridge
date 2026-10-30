package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed schema_v43.sql
var historicalFinalSchemaV43 string

type historicalSchemaObject struct{ kind, name, sql string }

func historicalFinalSchemaObjects() []historicalSchemaObject {
	var out []historicalSchemaObject
	for _, raw := range strings.Split(historicalFinalSchemaV43, "-- OBJECT ")[1:] {
		header, stmt, _ := strings.Cut(raw, "\n")
		kind, name, _ := strings.Cut(header, " ")
		out = append(out, historicalSchemaObject{kind, name, stmt})
	}
	return out
}
func historicalSQLIdentifier(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func historicalSchemaColumns(ctx context.Context, tx *sql.Tx, name string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+historicalSQLIdentifier(name)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cid, required, pk int
		var n, typ string
		var def sql.NullString
		if err = rows.Scan(&cid, &n, &typ, &required, &def, &pk); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// migrateFinalV43 installs the one v0.1 schema. Unrecognized development
// entities with data require an explicit database recreation. There is no
// legacy domain reader or remapping of retired operation kinds.
func migrateFinalV43(ctx context.Context, tx *sql.Tx) error {
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version != 0 && version != 41 && version != 42 {
		return fmt.Errorf("unreleased development schema %d requires database recreation", version)
	}
	if version == 42 {
		_, err := tx.ExecContext(ctx, schemaV43Upgrade)
		return err
	}
	objects := historicalFinalSchemaObjects()
	wanted := map[string]bool{}
	for _, o := range objects {
		if o.kind == "table" {
			wanted[o.name] = true
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT type,name FROM sqlite_master WHERE type IN ('table','index','trigger') AND name NOT LIKE 'sqlite_%' AND name<>'schema_migrations'`)
	if err != nil {
		return err
	}
	var previous []historicalSchemaObject
	for rows.Next() {
		var o historicalSchemaObject
		if err = rows.Scan(&o.kind, &o.name); err != nil {
			rows.Close()
			return err
		}
		previous = append(previous, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	oldTables := map[string]bool{}
	for _, o := range previous {
		if o.kind != "table" {
			continue
		}
		oldTables[o.name] = true
		if !wanted[o.name] {
			var n int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM "+historicalSQLIdentifier(o.name)).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("unreleased development entity %s contains data; recreate the development database", o.name)
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA legacy_alter_table=OFF`)
	for _, o := range previous {
		if o.kind != "table" {
			if _, err = tx.ExecContext(ctx, "DROP "+strings.ToUpper(o.kind)+" "+historicalSQLIdentifier(o.name)); err != nil {
				return err
			}
		}
	}
	for name := range oldTables {
		if _, err = tx.ExecContext(ctx, "ALTER TABLE "+historicalSQLIdentifier(name)+" RENAME TO "+historicalSQLIdentifier("_source41_"+name)); err != nil {
			return err
		}
	}
	for _, o := range objects {
		if o.kind == "table" {
			if _, err = tx.ExecContext(ctx, o.sql); err != nil {
				return fmt.Errorf("create %s: %w", o.name, err)
			}
		}
	}
	for _, o := range objects {
		if o.kind != "table" || !oldTables[o.name] {
			continue
		}
		old, err := historicalSchemaColumns(ctx, tx, "_source41_"+o.name)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, c := range old {
			have[c] = true
		}
		columns, err := historicalSchemaColumns(ctx, tx, o.name)
		if err != nil {
			return err
		}
		var shared []string
		for _, c := range columns {
			if have[c] {
				shared = append(shared, historicalSQLIdentifier(c))
			}
		}
		names := strings.Join(shared, ",")
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+historicalSQLIdentifier(o.name)+"("+names+") SELECT "+names+" FROM "+historicalSQLIdentifier("_source41_"+o.name)); err != nil {
			return fmt.Errorf("source schema %s cannot migrate; recreate development database: %w", o.name, err)
		}
	}
	if version == 41 {
		if _, err = tx.ExecContext(ctx, executionIdentityBackfill); err != nil {
			return err
		}
	}
	for name := range oldTables {
		if _, err = tx.ExecContext(ctx, "DROP TABLE "+historicalSQLIdentifier("_source41_"+name)); err != nil {
			return err
		}
	}
	for _, o := range objects {
		if o.kind != "table" {
			if _, err = tx.ExecContext(ctx, o.sql); err != nil {
				return fmt.Errorf("create %s: %w", o.name, err)
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO codex_configs(id,name,codex_path,enabled,created_at,updated_at) VALUES('codex_config_default','Default Codex configuration','',1,strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		return err
	}
	return nil
}
