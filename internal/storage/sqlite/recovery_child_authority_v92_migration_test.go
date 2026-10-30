package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestRecoveryChildAuthorityV92UpgradeAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "devbridge.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	if err := ApplyMigrations(ctx, db, DefaultMigrations()[:len(DefaultMigrations())-3]); err != nil {
		t.Fatal(err)
	}
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
	for _, table := range []string{"recovery_child_admissions", "recovery_child_thread_attempts", "recovery_child_thread_receipts", "recovery_child_turn_attempts", "recovery_child_turn_receipts"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN (?,?)`, table+"_immutable_UPDATE", table+"_immutable_DELETE").Scan(&count); err != nil || count != 2 {
			t.Fatalf("immutability %s count=%d err=%v", table, count, err)
		}
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
