package sqlite

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
)

func TestAdoptionStartV109UpgradePreservesFacts(t *testing.T) {
	ctx := context.Background()
	db := openSelectionMigrationDB(t, migrationsThroughVersion(108))
	config := codexconfig.Config{ID: "v109-preserved", Name: "Historical backend", Enabled: true, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewCodexConfigStore(db).Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	tables := selectionMigrationTables(t, db)
	before := platformTableSnapshot(t, db, tables)
	if err := ApplyMigrations(ctx, db.sql, DefaultMigrations()); err != nil {
		t.Fatal(err)
	}
	if after := platformTableSnapshot(t, db, tables); before != after {
		t.Fatal("v109 changed prior facts")
	}
	var requests int
	if err := db.sql.QueryRow(`SELECT count(*) FROM ai_admitted_execution_start_requests`).Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("migration inferred historical start requests: count=%d err=%v", requests, err)
	}
	for _, name := range []string{"ai_admitted_execution_start_source", "ai_admitted_execution_start_immutable", "ai_admitted_execution_start_durable"} {
		var found int
		if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&found); err != nil || found != 1 {
			t.Fatalf("missing %s: %d %v", name, found, err)
		}
	}
	if err := ApplyMigrations(ctx, db.sql, DefaultMigrations()); err != nil {
		t.Fatal(err)
	}
	if after := platformTableSnapshot(t, db, tables); before != after {
		t.Fatal("replaying migration changed prior facts")
	}
}
