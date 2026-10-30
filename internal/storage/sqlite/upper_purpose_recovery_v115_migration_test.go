package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
)

func TestUpperPurposeRecoveryV115MigrationAtomicHistory(t *testing.T) {
	ctx := context.Background()
	db := openSelectionMigrationDB(t, migrationsThroughVersion(113))
	if err := NewCodexConfigStore(db).Create(ctx, codexconfig.Config{ID: "v115-history", Name: "Retained", Enabled: true, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	tables := selectionMigrationTables(t, db)
	before := platformTableSnapshot(t, db, tables)
	var original string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='upper_review_grant_transition'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TRIGGER upper_review_grant_transition`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(115)); err == nil {
		t.Fatal("migration accepted missing predecessor grant guard")
	}
	var count int
	if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN('runtime_preparation_failures','runtime_preparation_revocations','runtime_preparation_abandonments')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration retained tables=%d err=%v", count, err)
	}
	if _, err := db.sql.Exec(original); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(115)); err != nil {
			t.Fatal(err)
		}
	}
	if after := platformTableSnapshot(t, db, tables); after != before {
		t.Fatal("migration changed historical rows")
	}
	var ddl string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='upper_review_grant_transition'`).Scan(&ddl); err != nil || !strings.Contains(ddl, "runtime_preparation_revocations") {
		t.Fatalf("missing exact abandonment guard err=%v", err)
	}
	for _, table := range []string{"runtime_preparation_failures", "runtime_preparation_revocations", "runtime_preparation_abandonments"} {
		if err := db.sql.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration invented %s rows=%d err=%v", table, count, err)
		}
	}
	rows, err := db.sql.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("migration foreign keys invalid")
	}
}
