package sqlite

import (
	"context"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"testing"
)

func TestTaskDeliveryDispatchV116MigrationPreservesHistoryAtomically(t *testing.T) {
	ctx := context.Background()
	db := openSelectionMigrationDB(t, migrationsThroughVersion(115))
	if err := NewCodexConfigStore(db).Create(ctx, codexconfig.Config{ID: "delivery-history", Name: "Retained config", Enabled: true, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	tables := selectionMigrationTables(t, db)
	before := platformTableSnapshot(t, db, tables)
	// A conflict late in the DDL must roll back even the newly-created tables.
	if _, err := db.sql.Exec(`CREATE TABLE task_delivery_result_no_delete(id TEXT); CREATE TRIGGER task_delivery_result_no_delete BEFORE INSERT ON task_delivery_result_no_delete BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(116)); err == nil {
		t.Fatal("migration accepted conflicting trigger")
	}
	var count, version int
	if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('task_delivery_dispatch_intents','task_delivery_dispatch_results')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial schema survived: %d %v", count, err)
	}
	if err := db.sql.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 115 {
		t.Fatalf("partial version=%d %v", version, err)
	}
	if _, err := db.sql.Exec(`DROP TABLE task_delivery_result_no_delete`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := ApplyMigrations(ctx, db.sql, migrationsThroughVersion(116)); err != nil {
			t.Fatal(err)
		}
	}
	if after := platformTableSnapshot(t, db, tables); after != before {
		t.Fatal("migration altered historical business facts")
	}
	if err := db.sql.QueryRow(`SELECT (SELECT count(*) FROM task_delivery_dispatch_intents)+(SELECT count(*) FROM task_delivery_dispatch_results)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration invented dispatch/result: %d %v", count, err)
	}
	if _, err := db.sql.Exec(`INSERT INTO task_delivery_dispatch_intents VALUES('missing-task','missing-repo','{}',printf('%064d',0),'missing-epoch','now')`); err == nil {
		t.Fatal("unadmitted dispatch accepted")
	}
	if _, err := db.sql.Exec(`INSERT INTO task_delivery_dispatch_results VALUES('missing-task','missing-repo',printf('%064d',0),'commit','tree',1,'now')`); err == nil {
		t.Fatal("unclaimed result accepted")
	}
	rows, err := db.sql.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("migration broke foreign keys")
	}
}
