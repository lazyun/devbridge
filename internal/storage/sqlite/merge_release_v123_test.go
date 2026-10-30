package sqlite

import (
	"context"
	"testing"
)

func TestMergeReleaseV123PreservesHistoricalDelivery(t *testing.T) {
	for _, applied := range []bool{false, true} {
		name := "unknown"
		if applied {
			name = "applied"
		}
		t.Run(name, func(t *testing.T) {
			v := deliveryRecoveryV116Fixture(t, applied)
			ctx := context.Background()
			if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(122)); err != nil {
				t.Fatal(err)
			}
			history := deliveryMigrationHistoricalSnapshot(t, v.db)
			before := history()
			for range 2 {
				if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(123)); err != nil {
					t.Fatal(err)
				}
				if history() != before {
					t.Fatal("upgrade rewrote delivery facts or invented merge authority")
				}
			}
			var count int
			if err := v.db.sql.QueryRow(`SELECT count(*) FROM (` + acceptedDeliveryCommitsSQL + `)`).Scan(&count); err != nil || count != map[bool]int{false: 0, true: 1}[applied] {
				t.Fatalf("historical delivery inventory: %d %v", count, err)
			}
		})
	}
}
