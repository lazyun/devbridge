package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/dataroot"
)

type earlySelectionSchema struct {
	Objects []struct{ Kind, Name, SQL string }
}

func earlySelectionV104(t *testing.T, variant string) Migration {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "runtime_selection_v104_"+variant+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var source earlySelectionSchema
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	var statements []string
	for _, o := range source.Objects {
		if o.Name == "formal_definition_fence_source" || o.Name == "no_run_successor_effect_owner" {
			statements = append(statements, "DROP TRIGGER "+o.Name+";")
		}
		statements = append(statements, o.SQL+";")
	}
	return Migration{Version: 104, SQL: strings.Join(statements, "\n")}
}

func openSelectionMigrationDB(t *testing.T, migrations []Migration) *DB {
	t.Helper()
	ctx, root := context.Background(), t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if err = ApplyMigrations(ctx, raw, migrations); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	writer, err := dataroot.AcquireWriterLock(filepath.Join(root, "core-writer.lock"))
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	db := &DB{sql: raw, stateRoot: root, writer: writer}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.allocateRuntimeEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func selectionMigrationTables(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.sql.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT IN ('schema_migrations','runtime_epochs','runtime_authority') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

func selectionMigrationSnapshot(t *testing.T, db *DB) string {
	t.Helper()
	var version int
	if err := db.sql.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	rows, err := db.sql.Query(`SELECT type,name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	var schema [][3]string
	for rows.Next() {
		var r [3]string
		if err = rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		schema = append(schema, r)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	b, _ := json.Marshal(struct {
		Version int
		Schema  [][3]string
		Rows    string
	}{version, schema, platformTableSnapshot(t, db, selectionMigrationTables(t, db))})
	return string(b)
}

func assertSelectionV106Objects(t *testing.T, db *DB) {
	t.Helper()
	var version int
	if err := db.sql.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	replacements := map[string]string{}
	if version >= 108 {
		_, next, err := formalConfirmationV108Objects()
		if err != nil {
			t.Fatal(err)
		}
		replacements = next
	}
	for _, o := range runtimeSelectionV104Objects() {
		if replacement, ok := replacements[o.name]; ok {
			o.statement = replacement
		}
		var kind, actual string
		err := db.sql.QueryRow(`SELECT type,sql FROM sqlite_master WHERE name=?`, o.name).Scan(&kind, &actual)
		if version >= 108 {
			actual = strings.Replace(actual, `CREATE TABLE "`+o.name+`"`, "CREATE TABLE "+o.name, 1)
		}
		if err != nil || kind != o.kind || runtimeSelectionSchemaText(actual) != runtimeSelectionSchemaText(o.statement) {
			t.Fatalf("canonical %s: kind=%s err=%v", o.name, kind, err)
		}
	}
}

func TestRuntimeSelectionV106RepairsRecognizedEarly104(t *testing.T) {
	for _, variant := range []string{"invalid-column", "pre-fence"} {
		t.Run(variant, func(t *testing.T) {
			migrations := append(migrationsThroughVersion(103), earlySelectionV104(t, variant))
			db := openSelectionMigrationDB(t, migrations)
			ctx := context.Background()
			backend := codexconfig.Config{ID: "preserved-backend", Name: "Historical backend", Enabled: true, CreatedAt: testNow, UpdatedAt: testNow}
			if err := NewCodexConfigStore(db).Create(ctx, backend); err != nil {
				t.Fatal(err)
			}
			tables := selectionMigrationTables(t, db)
			before := platformTableSnapshot(t, db, tables)
			root := db.StateRoot()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded, err := Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer upgraded.Close()
			assertSelectionV106Objects(t, upgraded)
			if platformTableSnapshot(t, upgraded, tables) != before {
				t.Fatal("migration rewrote existing rows")
			}
			for _, table := range []string{"formal_adoption_fences", "no_run_successor_command_identities", "backend_platform_mapping_heads"} {
				var n int
				if err = upgraded.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
					t.Fatalf("migration inferred %s rows=%d err=%v", table, n, err)
				}
			}
			stable := platformTableSnapshot(t, upgraded, selectionMigrationTables(t, upgraded))
			if err = upgraded.Close(); err != nil {
				t.Fatal(err)
			}
			cold, err := Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer cold.Close()
			assertSelectionV106Objects(t, cold)
			if platformTableSnapshot(t, cold, selectionMigrationTables(t, cold)) != stable {
				t.Fatal("cold reopen changed business rows")
			}
		})
	}
}

func TestRuntimeSelectionV106PreservesCanonicalRows(t *testing.T) {
	db := openSelectionMigrationDB(t, migrationsThroughVersion(105))
	ctx := context.Background()
	template := newPlatformStoreFixture(t)
	f := &platformStoreFixture{t: t, ctx: ctx, db: db, store: NewRuntimePlatformStore(db), descriptor: template.descriptor, evidence: template.evidence}
	f.register()
	_, q := f.qualify()
	m := backendTestMapping(t, f, "canonical-backend", "canonical-map", q)
	if err := NewRuntimePlatformStore(db).SaveBackendMapping(ctx, m); err != nil {
		t.Fatal(err)
	}
	tables := selectionMigrationTables(t, db)
	before := platformTableSnapshot(t, db, tables)
	root := db.StateRoot()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	assertSelectionV106Objects(t, upgraded)
	if platformTableSnapshot(t, upgraded, tables) != before {
		t.Fatal("canonical upgrade changed data")
	}
	if got, err := NewRuntimePlatformStore(upgraded).ReadBackendMappingForRequest(ctx, m.RequestID); err != nil || got != m {
		t.Fatalf("canonical receipt=%+v err=%v", got, err)
	}
}

func TestRuntimeSelectionV106RepairsAlreadyAppliedSQLOnly105(t *testing.T) {
	// Recreate the real historical path: pre-fence 104 followed by the original
	// SQL-only 105. The new 105 preflight must not participate in this fixture.
	migrations := append(migrationsThroughVersion(103), earlySelectionV104(t, "pre-fence"), Migration{Version: 105, SQL: backendPlatformV105SQL})
	db := openSelectionMigrationDB(t, migrations)
	ctx := context.Background()
	var version, missing int
	if err := db.sql.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 105 {
		t.Fatalf("historical SQL-only schema version=%d error=%v", version, err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('formal_adoption_fences','no_run_successor_command_identities')`).Scan(&missing); err != nil || missing != 0 {
		t.Fatalf("historical 105 unexpectedly repaired its 104 prerequisites: count=%d error=%v", missing, err)
	}
	template := newPlatformStoreFixture(t)
	f := &platformStoreFixture{t: t, ctx: ctx, db: db, store: NewRuntimePlatformStore(db), descriptor: template.descriptor, evidence: template.evidence}
	f.register()
	_, q := f.qualify()
	mapping := backendTestMapping(t, f, "sql-only-105-backend", "sql-only-105-mapping", q)
	if err := NewRuntimePlatformStore(db).SaveBackendMapping(ctx, mapping); err != nil {
		t.Fatal(err)
	}
	tables := selectionMigrationTables(t, db)
	before := platformTableSnapshot(t, db, tables)
	root := db.StateRoot()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	assertSelectionV106Objects(t, upgraded)
	var applied int
	if err = upgraded.sql.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=106`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("independent 106 was not applied: count=%d error=%v", applied, err)
	}
	if platformTableSnapshot(t, upgraded, tables) != before {
		t.Fatal("independent 106 rewrote original 105 rows or mapping head")
	}
	for _, name := range []string{"formal_adoption_fences", "no_run_successor_command_identities"} {
		var count int
		if err = upgraded.sql.QueryRow("SELECT COUNT(*) FROM " + name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("106 inferred authority in %s: count=%d error=%v", name, count, err)
		}
	}
	stable := platformTableSnapshot(t, upgraded, selectionMigrationTables(t, upgraded))
	if err = upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	cold, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	store := NewRuntimePlatformStore(cold)
	if got, err := store.ReadBackendMappingForRequest(ctx, mapping.RequestID); err != nil || got != mapping {
		t.Fatalf("cold original receipt=%+v error=%v", got, err)
	}
	if got, err := store.ReadBackendMapping(ctx, mapping.BackendID, mapping.BackendFingerprint, mapping.RuntimeRole, mapping.Platform.DescriptorID); err != nil || got != mapping {
		t.Fatalf("cold exact head=%+v error=%v", got, err)
	}
	if err = store.SaveBackendMapping(ctx, mapping); err != nil {
		t.Fatal(err)
	}
	if platformTableSnapshot(t, cold, selectionMigrationTables(t, cold)) != stable {
		t.Fatal("cold original 105 receipt replay changed facts")
	}
}

func TestRuntimeSelectionV106RejectsUnprovedBusinessAndUnknownSchema(t *testing.T) {
	for _, variant := range []string{"unproved-business", "unknown-trigger", "extra-index", "partial-current"} {
		t.Run(variant, func(t *testing.T) {
			migrations := append(migrationsThroughVersion(103), earlySelectionV104(t, "pre-fence"))
			if variant == "partial-current" {
				migrations = migrationsThroughVersion(105)
			}
			db := openSelectionMigrationDB(t, migrations)
			if variant == "unproved-business" {
				// Deliberately invalid historical archive sentinel: this negative
				// fixture grants no business authority. Any surviving row must block
				// reconstruction, even before its receipt can be decoded.
				var original string
				if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='formal_adoption_business_source'`).Scan(&original); err != nil {
					t.Fatal(err)
				}
				for _, s := range []string{`PRAGMA foreign_keys=OFF`, `DROP TRIGGER formal_adoption_business_source`, `INSERT INTO formal_adoption_business(ai_session_id,request_id,intent_id,project_id,task_id,revision_id,execution_id,admission_request_id,business_sha256,business_json) VALUES('unproved-session','unproved-request','unproved-intent','unproved-project','unproved-task','unproved-revision','unproved-execution','unproved-admission','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','{}')`, original, `PRAGMA foreign_keys=ON`} {
					if _, err := db.sql.Exec(s); err != nil {
						t.Fatal(err)
					}
				}
			} else if variant == "unknown-trigger" {
				if _, err := db.sql.Exec(`DROP TRIGGER formal_adoption_business_source; CREATE TRIGGER formal_adoption_business_source BEFORE INSERT ON formal_adoption_business BEGIN SELECT RAISE(ABORT,'unknown schema'); END`); err != nil {
					t.Fatal(err)
				}
			} else if variant == "extra-index" {
				if _, err := db.sql.Exec(`CREATE INDEX unknown_legacy_index ON formal_adoption_business(task_id)`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.sql.Exec(`DROP TRIGGER formal_adoption_confirmation_completion`); err != nil {
					t.Fatal(err)
				}
			}
			before := selectionMigrationSnapshot(t, db)
			root := db.StateRoot()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			unexpected, err := Open(context.Background(), root)
			if err == nil {
				unexpected.Close()
				t.Fatal("unsupported schema accepted")
			}
			if !strings.Contains(err.Error(), "v106:") {
				t.Fatalf("missing migration diagnosis: %v", err)
			}
			if variant == "unproved-business" && !strings.Contains(err.Error(), "historical rows without original prepare fences") {
				t.Fatalf("wrong unsupported history error: %v", err)
			}
			raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db")+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if selectionMigrationSnapshot(t, &DB{sql: raw}) != before {
				t.Fatal("rejected upgrade changed original schema or data")
			}
		})
	}
}
