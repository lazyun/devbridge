package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

// Restore the original columns of a production-produced fixture into a real
// v106 schema. Disabling import-time triggers allows archived rows to be loaded
// in table order; all original triggers and foreign keys are restored and
// checked before the migration itself runs.
func restoreRecoveryV106Fixture(t *testing.T, source *DB) (*DB, map[string][]string) {
	t.Helper()
	db := openSelectionMigrationDB(t, migrationsThroughVersion(106))
	rows, err := db.sql.Query(`SELECT name,sql FROM sqlite_master WHERE type='trigger' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	triggers := map[string]string{}
	for rows.Next() {
		var name, ddl string
		if err = rows.Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		triggers[name] = ddl
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for name := range triggers {
		if _, err = db.sql.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.sql.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`ATTACH DATABASE ? AS fixture`, filepath.Join(source.stateRoot, "devbridge.db")); err != nil {
		t.Fatal(err)
	}
	defer db.sql.Exec(`DETACH DATABASE fixture`)
	tables := append(selectionMigrationTables(t, db), "runtime_epochs", "runtime_authority")
	columns := make(map[string][]string, len(tables))
	for _, table := range tables {
		rows, err := db.sql.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			columns[table] = append(columns[table], `"`+name+`"`)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if _, err = db.sql.Exec(`DELETE FROM main.` + table); err != nil {
			t.Fatal(err)
		}
		list := strings.Join(columns[table], ",")
		if _, err = db.sql.Exec(`INSERT INTO main.` + table + `(` + list + `) SELECT ` + list + ` FROM fixture.` + table); err != nil {
			t.Fatalf("restore %s: %v", table, err)
		}
	}
	for _, ddl := range triggers {
		if _, err = db.sql.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.sql.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	assertRecoveryForeignKeys(t, db)
	return db, columns
}

func assertRecoveryForeignKeys(t *testing.T, db *DB) {
	t.Helper()
	rows, err := db.sql.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var row, constraint int
		if err = rows.Scan(&table, &row, &parent, &constraint); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign key violation %s row %d -> %s constraint %d", table, row, parent, constraint)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func recoveryLegacyRows(t *testing.T, db *DB, columns map[string][]string) string {
	t.Helper()
	snapshot := map[string][][]any{}
	for table, names := range columns {
		rows, err := db.sql.Query(`SELECT ` + strings.Join(names, ",") + ` FROM ` + table + ` ORDER BY ` + strings.Join(names, ","))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, targets := make([]any, len(names)), make([]any, len(names))
			for i := range values {
				targets[i] = &values[i]
			}
			if err = rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			snapshot[table] = append(snapshot[table], values)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRecoveryPurposeV107UpgradePreservesExistingLinksAndReceipts(t *testing.T) {
	runPurposeBeforeCommit(t, "REVIEW", func(t *testing.T, source *DB, ctx context.Context, _ *RecoveryStatusStore, _ rs.Spec, record rs.Record, intent confirmation.Intent) confirmation.Intent {
		intent = commitPurposeForTest(t, source, ctx, intent, "legacy-commit")
		consume := rs.PurposeConsumeInput{AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version, ContentHash: intent.ContentHash, Action: rs.ApplyConfirmedReviewPurpose, RequestID: "legacy-consume"}
		consume.PayloadHash = consume.ComputePayloadHash()
		if _, err := NewRecoveryPurposeStore(source).ApplyConfirmedReviewPurpose(ctx, consume); err != nil {
			t.Fatal(err)
		}
		db, columns := restoreRecoveryV106Fixture(t, source)
		before := recoveryLegacyRows(t, db, columns)
		migrations := append(migrationsThroughVersion(106), Migration{Version: 107, Apply: migrateRecoveryPurposeV107})
		if err := ApplyMigrations(ctx, db.sql, migrations); err != nil {
			t.Fatal(err)
		}
		if after := recoveryLegacyRows(t, db, columns); after != before {
			t.Fatal("v107 changed an existing row or receipt byte")
		}
		assertRecoveryForeignKeys(t, db)
		p, err := NewRecoveryStatusStore(db).ReadPurposeConfirmation(ctx, record.ID)
		if err != nil || p.IntentID != intent.IntentID || p.Historical || !p.Consumed || !p.ReceiptCommitted || p.LinkOrdinal != 1 {
			t.Fatalf("migrated current projection=%+v err=%v", p, err)
		}
		for _, table := range []string{"recovery_purpose_confirmation_receipts", "recovery_purpose_confirmation_consumptions"} {
			var linkID string
			var ordinal int
			if err = db.sql.QueryRow(`SELECT purpose_link_id,purpose_link_ordinal FROM `+table+` WHERE intent_id=?`, intent.IntentID).Scan(&linkID, &ordinal); err != nil || linkID != p.LinkID || ordinal != 1 {
				t.Fatalf("backfilled %s link=%s/%d err=%v", table, linkID, ordinal, err)
			}
		}
		for _, table := range []string{"recovery_purpose_confirmation_links", "recovery_purpose_link_history", "recovery_purpose_confirmation_receipts", "recovery_purpose_confirmation_consumptions"} {
			if _, err = db.sql.Exec(`UPDATE ` + table + ` SET intent_id=intent_id`); err == nil {
				t.Fatalf("%s lost immutable guard", table)
			}
			if _, err = db.sql.Exec(`DELETE FROM ` + table); err == nil {
				t.Fatalf("%s lost deletion guard", table)
			}
		}
		var guard string
		if err = db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='approved_task_revision_immutable'`).Scan(&guard); err != nil || !strings.Contains(guard, "recovery_purpose_link_history") {
			t.Fatalf("replacement Task guard not migrated: %v", err)
		}
		// Migration replay must not append another link or produce fresh authority.
		stable := selectionMigrationSnapshot(t, db)
		if err = ApplyMigrations(ctx, db.sql, migrations); err != nil {
			t.Fatal(err)
		}
		if stable != selectionMigrationSnapshot(t, db) {
			t.Fatal("migration replay changed history or selectors")
		}
		return confirmation.Intent{}
	})
}
