package sqlite

import (
	"context"
	"testing"
)

func TestFlowV119MigrationPreservesHistoricalFacts(t *testing.T) {
	v := deliveryRecoveryV116Fixture(t, false)
	ctx := context.Background()
	if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(118)); err != nil {
		t.Fatal(err)
	}
	history := deliveryMigrationHistoricalSnapshot(t, v.db)
	before := history()
	if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(119)); err != nil {
		t.Fatal(err)
	}
	if history() != before {
		t.Fatal("v119 rewrote historical facts")
	}
	for _, table := range []string{"merge_delivery_operations", "merge_delivery_dispatches", "merge_delivery_outcomes", "merge_delivery_current_operations"} {
		var count int
		if err := v.db.sql.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration invented merge authority %s %d %v", table, count, err)
		}
	}
	stable := selectionMigrationSnapshot(t, v.db)
	if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(119)); err != nil || selectionMigrationSnapshot(t, v.db) != stable {
		t.Fatalf("v119 not idempotent %v", err)
	}
}
