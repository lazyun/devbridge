package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryStatusInspectionV83MigrationRollbackRepeatAndFK(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "v82.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var prior []Migration
	for _, migration := range DefaultMigrations() {
		if migration.Version < 83 {
			prior = append(prior, migration)
		}
	}
	if err = ApplyMigrations(ctx, db, prior); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('v83-history','unchanged','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err = db.QueryRow(`SELECT json_array(id,name,created_at,updated_at) FROM projects WHERE id='v83-history'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	broken := append(append([]Migration{}, prior...), Migration{Version: 83, Apply: func(ctx context.Context, tx *sql.Tx) error {
		if err := migrateRecoveryStatusInspectionV83(ctx, tx); err != nil {
			return err
		}
		return errors.New("v83 injected rollback")
	}})
	if err = ApplyMigrations(ctx, db, broken); err == nil {
		t.Fatal("v83 rollback unexpectedly committed")
	}
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='inspection_recovery_continuations'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback leaked v83 table: %d %v", n, err)
	}
	for i := 0; i < 2; i++ {
		if err = ApplyMigrations(ctx, db, DefaultMigrations()); err != nil {
			t.Fatal(err)
		}
		var after string
		if err = db.QueryRow(`SELECT json_array(id,name,created_at,updated_at) FROM projects WHERE id='v83-history'`).Scan(&after); err != nil || after != before {
			t.Fatalf("v83 rewrote historical project: %q %v", after, err)
		}
	}
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name='recovery_status_inspection_child_v83'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("v83 inspection recovery trigger missing: %d %v", n, err)
	}
	var ddl string
	if err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='inspection_recovery_resolutions'`).Scan(&ddl); err != nil || !strings.Contains(ddl, "inspection_result_hash TEXT NOT NULL") {
		t.Fatalf("v83 immutable inspection result receipt missing: %q %v", ddl, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v83 FK violation")
	}
}
