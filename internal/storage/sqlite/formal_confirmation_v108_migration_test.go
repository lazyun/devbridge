package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
)

func TestFormalConfirmationV108UpgradePreservesRowsAndGuards(t *testing.T) {
	ctx := context.Background()
	db := openSelectionMigrationDB(t, migrationsThroughVersion(106))
	backend := codexconfig.Config{ID: "v108-preserved", Name: "Historical backend", Enabled: true, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewCodexConfigStore(db).Create(ctx, backend); err != nil {
		t.Fatal(err)
	}
	const index = `CREATE INDEX preserved_selection_index ON runtime_selection_sources(task_id,revision_id)`
	const trigger = `CREATE TRIGGER preserved_selection_guard BEFORE INSERT ON runtime_selection_sources WHEN NEW.task_id='' BEGIN SELECT RAISE(ABORT,'preserved guard'); END`
	for _, statement := range []string{index, trigger} {
		if _, err := db.sql.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	before := platformTableSnapshot(t, db, selectionMigrationTables(t, db))
	migrations := append(migrationsThroughVersion(106), Migration{Version: 108, ForeignKeysOff: true, Apply: migrateFormalConfirmationV108})
	if err := ApplyMigrations(ctx, db.sql, migrations); err != nil {
		t.Fatal(err)
	}
	if after := platformTableSnapshot(t, db, selectionMigrationTables(t, db)); after != before {
		t.Fatal("v108 changed existing rows or inferred new authority")
	}
	for name, want := range map[string]string{"preserved_selection_index": index, "preserved_selection_guard": trigger} {
		var actual string
		if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&actual); err != nil || actual != want {
			t.Fatalf("preserve %s: sql=%q err=%v", name, actual, err)
		}
	}
	_, revised, err := formalConfirmationV108Objects()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range revised {
		var actual string
		if err = db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		// SQLite quotes the table name when renaming the temporary copy.
		actual = strings.Replace(actual, `CREATE TABLE "`+name+`"`, "CREATE TABLE "+name, 1)
		if runtimeSelectionSchemaText(actual) != runtimeSelectionSchemaText(want) {
			t.Fatalf("v108 object %s does not match its reviewed definition", name)
		}
	}
	// Reapplying the migration list is a read-only version check.
	stable := selectionMigrationSnapshot(t, db)
	if err = ApplyMigrations(ctx, db.sql, migrations); err != nil {
		t.Fatal(err)
	}
	if selectionMigrationSnapshot(t, db) != stable {
		t.Fatal("v108 repeated migration changed the database")
	}
}

func TestFormalConfirmationV108CopiesOpaqueArchiveBytes(t *testing.T) {
	ctx := context.Background()
	db := openSelectionMigrationDB(t, migrationsThroughVersion(106))
	original, _, err := formalConfirmationV108Objects()
	if err != nil {
		t.Fatal(err)
	}
	// Isolate the copy operation with opaque, deliberately non-authoritative
	// historical rows. Readers must still reject these rows; migration must not
	// decode, repair, upgrade or manufacture receipts from them.
	if _, err = db.sql.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"formal_adoption_business_source", "runtime_selection_adoption_source_identity"} {
		if _, err = db.sql.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
	digest := strings.Repeat("a", 64)
	archive := `{ "historical" : "preserve original bytes", "atomicConfirmation":false }`
	if _, err = db.sql.Exec(`INSERT INTO formal_adoption_business(ai_session_id,request_id,intent_id,project_id,task_id,revision_id,execution_id,admission_request_id,business_sha256,business_json) VALUES('session','command','intent','project','task','revision','execution','admission',?,?)`, digest, archive); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`INSERT INTO runtime_selection_sources(source_id,project_id,task_id,revision_id,runtime_role,entry,ai_session_id,request_id,adoption_request_id,receipt_identity,receipt_sha256,qualification_id,qualification_generation,platform_lineage_id,descriptor_id,source_payload_hash,source_json) VALUES(?,'project','task','revision','IMPLEMENTATION','DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION','session','command','command',?,?,'qualification',1,'lineage','descriptor',?,?)`, digest, digest, digest, digest, archive); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"formal_adoption_business_source", "runtime_selection_adoption_source_identity"} {
		if _, err = db.sql.Exec(original[name]); err != nil {
			t.Fatal(err)
		}
	}
	tables := []string{"formal_adoption_business", "formal_adoption_receipts", "runtime_selection_sources", "runtime_selection_bindings"}
	before := platformTableSnapshot(t, db, tables)
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = migrateFormalConfirmationV108(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if platformTableSnapshot(t, db, tables) != before {
		t.Fatal("v108 rewrote opaque archive bytes or backfilled a historical receipt")
	}
}

func TestFormalConfirmationV108RejectsUnknownSchemaWithoutChanges(t *testing.T) {
	db := openSelectionMigrationDB(t, migrationsThroughVersion(106))
	if _, err := db.sql.Exec(`DROP TRIGGER formal_adoption_fence_source; CREATE TRIGGER formal_adoption_fence_source BEFORE INSERT ON formal_adoption_fences BEGIN SELECT RAISE(ABORT,'unknown guard'); END;`); err != nil {
		t.Fatal(err)
	}
	before := selectionMigrationSnapshot(t, db)
	migrations := append(migrationsThroughVersion(106), Migration{Version: 108, ForeignKeysOff: true, Apply: migrateFormalConfirmationV108})
	if err := ApplyMigrations(context.Background(), db.sql, migrations); err == nil || !strings.Contains(err.Error(), "unsupported schema for formal_adoption_fence_source") {
		t.Fatalf("unknown schema err=%v", err)
	}
	if selectionMigrationSnapshot(t, db) != before {
		t.Fatal("failed v108 migration changed schema, rows or migration version")
	}
}
