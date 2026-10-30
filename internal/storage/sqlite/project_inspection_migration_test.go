package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestProjectInspectionV82MigrationRollbackRepeatAndFK(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "v81.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var prior []Migration
	for _, m := range DefaultMigrations() {
		if m.Version < 82 {
			prior = append(prior, m)
		}
	}
	if err = ApplyMigrations(ctx, raw, prior); err != nil {
		t.Fatal(err)
	}
	broken := append(append([]Migration{}, prior...), Migration{Version: 82, ForeignKeysOff: true, Apply: func(ctx context.Context, tx *sql.Tx) error {
		if err := migrateProjectInspectionV82(ctx, tx); err != nil {
			return err
		}
		return errors.New("v82 injected rollback")
	}})
	if err = ApplyMigrations(ctx, raw, broken); err == nil {
		t.Fatal("v82 rollback unexpectedly committed")
	}
	var n int
	if err = raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='project_inspection_agent_claims'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback leaked v82 tables: %d %v", n, err)
	}
	for i := 0; i < 2; i++ {
		if err = ApplyMigrations(ctx, raw, DefaultMigrations()); err != nil {
			t.Fatal(err)
		}
	}
	if err = raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='agent_conversations_one_current_owner_v82'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("owner index missing: %d %v", n, err)
	}
	rows, err := raw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v82 FK violation")
	}
}
