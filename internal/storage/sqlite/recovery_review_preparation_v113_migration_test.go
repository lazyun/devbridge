package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
)

func TestRecoveryReviewPreparationV113MigrationAtomicHistory(t *testing.T) {
	ctx := context.Background()
	db := openSelectionMigrationDB(t, migrationsThroughVersion(111))
	if err := NewCodexConfigStore(db).Create(ctx, codexconfig.Config{ID: "review-v113-history", Name: "Retained backend", Enabled: true, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	tables := selectionMigrationTables(t, db)
	before := platformTableSnapshot(t, db, tables)
	var original string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='claims_runtime_epoch_immutable'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TRIGGER claims_runtime_epoch_immutable`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(113)); err == nil {
		t.Fatal("migration accepted an absent epoch guard")
	}
	var count, version int
	if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='review_purpose_preparation_adoptions'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration retained adoption authority: count=%d err=%v", count, err)
	}
	var ddl string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='runs_runtime_epoch_immutable'`).Scan(&ddl); err != nil || strings.Contains(ddl, "review_purpose_preparation_adoptions") {
		t.Fatalf("failed migration retained partial epoch exception: %v", err)
	}
	if err := db.sql.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 111 {
		t.Fatalf("failed migration version=%d err=%v", version, err)
	}
	if _, err := db.sql.Exec(original); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(113)); err != nil {
			t.Fatal(err)
		}
	}
	if after := platformTableSnapshot(t, db, tables); before != after {
		t.Fatal("migration changed historical business facts")
	}
	if err := db.sql.QueryRow(`SELECT count(*) FROM review_purpose_preparation_adoptions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration invented preparation adoption: count=%d err=%v", count, err)
	}
	rows, err := db.sql.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("Review migration broke foreign keys")
	}
}
