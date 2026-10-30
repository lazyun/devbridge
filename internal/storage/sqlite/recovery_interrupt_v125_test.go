package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestRecoveryInterruptV125PreservesHistoryAndRollsBack(t *testing.T) {
	v := deliveryRecoveryV116Fixture(t, false)
	ctx := context.Background()
	if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(124)); err != nil {
		t.Fatal(err)
	}
	before := selectionMigrationSnapshot(t, v.db)
	failure := errors.New("after replacing cancellation view")
	migrations := append(migrationsThroughVersion(124), Migration{Version: 125, Apply: func(ctx context.Context, tx *sql.Tx) error {
		if err := migrateRecoveryInterruptV125(ctx, tx); err != nil {
			return err
		}
		return failure
	}})
	if err := ApplyMigrations(ctx, v.db.sql, migrations); !errors.Is(err, failure) {
		t.Fatalf("upgrade failure not exercised: %v", err)
	}
	if selectionMigrationSnapshot(t, v.db) != before {
		t.Fatal("failed upgrade changed schema or historical facts")
	}
	history := deliveryMigrationHistoricalSnapshot(t, v.db)
	prior := history()
	for range 2 {
		if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(125)); err != nil {
			t.Fatal(err)
		}
		if history() != prior {
			t.Fatal("upgrade rewrote delivery history")
		}
	}
	assertRecoveryForeignKeys(t, v.db)
}
