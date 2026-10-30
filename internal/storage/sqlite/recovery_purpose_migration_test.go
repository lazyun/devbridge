package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestRecoveryPurposeMigrationV89ColdReopenAndImmutableSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devbridge.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	ctx := context.Background()
	db := open()
	if err := ApplyMigrations(ctx, db, DefaultMigrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	if err := ApplyMigrations(ctx, db, DefaultMigrations()); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=89`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	for _, table := range []string{"recovery_purpose_confirmation_links", "recovery_purpose_confirmation_receipts", "recovery_purpose_rejection_receipts", "recovery_purpose_confirmation_consumptions", "recovery_purpose_repair_receipts"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing %s: %v", table, err)
		}
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		rows.Close()
		t.Fatal("foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
}
