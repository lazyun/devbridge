package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// restoreHistoricalMigrationFixture imports production-produced row values
// into a genuine historical schema, without importing the source schema or its
// migration ledger. Only target columns are copied. Import-time triggers and
// foreign keys are restored and checked before the migration under test runs.
func restoreHistoricalMigrationFixture(t *testing.T, source *DB, version int) *DB {
	t.Helper()
	var db *DB
	if version < 44 {
		root := t.TempDir()
		raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db")+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		raw.SetMaxOpenConns(1)
		if err = ApplyMigrations(context.Background(), raw, historicalMigrations()[:version]); err != nil {
			t.Fatal(err)
		}
		db = &DB{sql: raw, stateRoot: root}
		t.Cleanup(func() { raw.Close() })
	} else {
		db = openSelectionMigrationDB(t, migrationsThroughVersion(version))
	}
	ctx := context.Background()
	rows, err := db.sql.Query(`SELECT name,sql FROM sqlite_master WHERE type='trigger' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var triggers []schemaObject
	for rows.Next() {
		var object schemaObject
		if err = rows.Scan(&object.name, &object.sql); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		triggers = append(triggers, object)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	// openSelectionMigrationDB pins one connection, so ATTACH and this pragma
	// cover every import statement and are not lost to a pool handoff.
	if _, err = db.sql.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`ATTACH DATABASE ? AS historical_fixture`, filepath.Join(source.stateRoot, "devbridge.db")); err != nil {
		t.Fatal(err)
	}
	defer db.sql.Exec(`DETACH DATABASE historical_fixture`)
	tables := selectionMigrationTables(t, db)
	var epochs int
	if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='runtime_epochs'`).Scan(&epochs); err != nil {
		t.Fatal(err)
	}
	if epochs != 0 {
		tables = append(tables, "runtime_epochs", "runtime_authority")
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, trigger := range triggers {
		if _, err = tx.ExecContext(ctx, `DROP TRIGGER `+sqlIdentifier(trigger.name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range tables {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM historical_fixture.sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			continue
		}
		columns, err := schemaColumns(ctx, tx, table)
		if err != nil {
			t.Fatal(err)
		}
		for i := range columns {
			columns[i] = sqlIdentifier(columns[i])
		}
		name, list := sqlIdentifier(table), strings.Join(columns, ",")
		if _, err = tx.ExecContext(ctx, `DELETE FROM main.`+name); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO main.`+name+`(`+list+`) SELECT `+list+` FROM historical_fixture.`+name); err != nil {
			t.Fatalf("restore v%d table %s: %v", version, table, err)
		}
	}
	for _, trigger := range triggers {
		if _, err = tx.ExecContext(ctx, trigger.sql); err != nil {
			t.Fatalf("restore v%d trigger %s: %v", version, trigger.name, err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	var enabled int
	if err = db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("historical fixture foreign keys not restored: %d %v", enabled, err)
	}
	assertRecoveryForeignKeys(t, db)
	db.epoch = source.epoch
	return db
}
