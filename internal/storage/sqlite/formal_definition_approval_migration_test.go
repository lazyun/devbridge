package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/dataroot"
	projectpkg "github.com/lazyun/devbridge/internal/project"
)

// Historical upgrade fixtures must remove every later migration marker and
// its empty schema; retaining a newer marker makes MAX(version) skip the
// historical migration under test.
func dropEmptyFormalApprovalV99Schema(t *testing.T, ctx context.Context, tx *sql.Tx) {
	t.Helper()
	// Keep the historical lower-version fixtures honest: restore only objects
	// changed after v98, and refuse to discard any newer business evidence.
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer reference.Close()
	if err = ApplyMigrations(ctx, reference, migrationsThroughVersion(98)); err != nil {
		t.Fatal(err)
	}
	// Discover all post-v98 tables from the historical schema itself. Later
	// migrations may add guards to existing tables as well as new tables.
	baseTables := map[string]bool{}
	baseRows, e := reference.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if e != nil {
		t.Fatal(e)
	}
	for baseRows.Next() {
		var name string
		if e = baseRows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		baseTables[name] = true
	}
	baseRows.Close()
	extraTables := map[string]bool{}
	currentRows, e := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if e != nil {
		t.Fatal(e)
	}
	for currentRows.Next() {
		var name string
		if e = currentRows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		if !baseTables[name] {
			extraTables[name] = true
		}
	}
	currentRows.Close()
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger'`)
	if err != nil {
		t.Fatal(err)
	}
	type object struct{ name, ddl string }
	var changed []object
	for rows.Next() {
		var o object
		if err = rows.Scan(&o.name, &o.ddl); err != nil {
			t.Fatal(err)
		}
		for name := range extraTables {
			if strings.Contains(o.ddl, name) {
				changed = append(changed, o)
				break
			}
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, o := range changed {
		if _, err = tx.ExecContext(ctx, "DROP TRIGGER "+sqlIdentifier(o.name)); err != nil {
			t.Fatal(err)
		}
		var prior string
		e := reference.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, o.name).Scan(&prior)
		if e == nil {
			if _, err = tx.ExecContext(ctx, prior); err != nil {
				t.Fatal(err)
			}
		} else if e != sql.ErrNoRows {
			t.Fatal(e)
		}
	}
	// Drop children before parents while checking that no new authority is
	// discarded by this synthetic historical fixture.
	for len(extraTables) > 0 {
		progress := false
		for table := range extraTables {
			referenced := false
			for child := range extraTables {
				if child == table {
					continue
				}
				fk, e := tx.QueryContext(ctx, `PRAGMA foreign_key_list(`+sqlIdentifier(child)+`)`)
				if e != nil {
					t.Fatal(e)
				}
				for fk.Next() {
					var id, seq int
					var parent, from, to, onUpdate, onDelete, match string
					if e = fk.Scan(&id, &seq, &parent, &from, &to, &onUpdate, &onDelete, &match); e != nil {
						t.Fatal(e)
					}
					if parent == table {
						referenced = true
					}
				}
				fk.Close()
				if referenced {
					break
				}
			}
			if referenced {
				continue
			}
			var count int
			// The existing synthetic v95 fixture generates Review using the
			// current producer. Remove only its new v103 admission projection;
			// preserve every original Review, revision and business receipt.
			if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+sqlIdentifier(table)).Scan(&count); e != nil || count != 0 && table != "review_definition_admissions" {
				t.Fatalf("cannot downgrade nonempty %s count=%d: %v", table, count, e)
			}
			if _, e = tx.ExecContext(ctx, `DROP TABLE `+sqlIdentifier(table)); e != nil {
				t.Fatal(e)
			}
			delete(extraTables, table)
			progress = true
		}
		if !progress {
			t.Fatalf("post-v98 schema has a dependency cycle: %v", extraTables)
		}
	}

	// v102 removed one FK to represent T2 without preparation authority. The
	// old fixtures contain only complete legacy admissions, so restore that FK.
	var current, prior string
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='execution_proposal_admissions'`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if err = reference.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='execution_proposal_admissions'`).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(current, "runtime_preparation_authority_id TEXT NOT NULL UNIQUE REFERENCES") {
		rows, err = tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger'`)
		if err != nil {
			t.Fatal(err)
		}
		var triggers []object
		for rows.Next() {
			var o object
			if err = rows.Scan(&o.name, &o.ddl); err != nil {
				t.Fatal(err)
			}
			triggers = append(triggers, o)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		for _, o := range triggers {
			if _, err = tx.ExecContext(ctx, "DROP TRIGGER "+sqlIdentifier(o.name)); err != nil {
				t.Fatal(err)
			}
		}
		create := strings.Replace(prior, "CREATE TABLE execution_proposal_admissions", "CREATE TABLE historical_admissions", 1)
		if _, err = tx.ExecContext(ctx, create); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO historical_admissions SELECT * FROM execution_proposal_admissions`); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `DROP TABLE execution_proposal_admissions`); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `ALTER TABLE historical_admissions RENAME TO execution_proposal_admissions`); err != nil {
			t.Fatal(err)
		}
		idx, err := reference.Query(`SELECT sql FROM sqlite_master WHERE type='index' AND tbl_name='execution_proposal_admissions' AND sql IS NOT NULL`)
		if err != nil {
			t.Fatal(err)
		}
		for idx.Next() {
			var ddl string
			if err = idx.Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(ctx, ddl); err != nil {
				t.Fatal(err)
			}
		}
		idx.Close()
		for _, o := range triggers {
			if _, err = tx.ExecContext(ctx, o.ddl); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>=99`); err != nil {
		t.Fatal(err)
	}
}

// Create a real historical DB rather than downgrade the current schema. Its
// journal rows still come through the canonical persistence writers.
func historicalCompletionFixture(t *testing.T, version int) (*ConfirmationStore, context.Context, confirmation.Intent) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyMigrations(ctx, raw, migrationsThroughVersion(version)); err != nil {
		t.Fatal(err)
	}
	writer, err := dataroot.AcquireWriterLock(filepath.Join(root, "core-writer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	db := &DB{sql: raw, stateRoot: root, writer: writer}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.allocateRuntimeEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	const project = "historical-completion-project"
	const session = "historical-completion-session"
	if err = NewProjectStore(db).Create(ctx, projectpkg.Project{ID: project, Name: project, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	seedBoundSession(t, db, aisession.Session{ID: session, ProjectID: project, CreatedAt: testNow, UpdatedAt: testNow})
	ctx = aisession.WithCallerScope(ctx, "storage-fixture-"+session)
	store := NewConfirmationStore(db)
	input := taskPrepareInput(session, "historical-prepare")
	input.SubmittedHash = confirmation.ValueHash(input)
	prepared, err := store.Prepare(ctx, project, input)
	if err != nil {
		t.Fatal(err)
	}
	in := confirmation.CommitInput{AISessionID: session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "historical-commit", ContentHash: prepared.ContentHash, VariantID: "only"}
	in.SubmittedHash = confirmation.ValueHash(in)
	out, fresh, err := store.BeginCommit(ctx, in, []confirmation.Step{{Kind: "FIRST", RequestID: "first", State: "PENDING"}, {Kind: "SECOND", RequestID: "second", State: "PENDING"}})
	if err != nil || !fresh {
		t.Fatalf("historical consumption=%t %v", fresh, err)
	}
	return store, ctx, out
}

func TestFormalApprovalV98UpgradeDoesNotBackfillConfirmationHistory(t *testing.T) {
	for _, complete := range []bool{false, true} {
		name := "pending"
		if complete {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			store, ctx, intent := historicalCompletionFixture(t, 98)
			if complete {
				intent = finishConfirmation(t, store, ctx, intent)
			}
			before := completionSnapshot(t, store)
			root := store.db.stateRoot
			if err := store.db.Close(); err != nil {
				t.Fatal(err)
			}
			for reopen := 0; reopen < 2; reopen++ {
				db, err := Open(ctx, root)
				if err != nil {
					t.Fatal(err)
				}
				store = NewConfirmationStore(db)
				if after := completionSnapshot(t, store); after != before {
					t.Fatal("upgrade rewrote confirmation history")
				}
				for _, table := range []string{"formal_definition_approval_fences", "formal_definition_approval_business", "formal_definition_approval_receipts"} {
					var count int
					if err = db.sql.QueryRow(`SELECT COUNT(*) FROM ` + sqlIdentifier(table)).Scan(&count); err != nil || count != 0 {
						t.Fatalf("backfilled %s: %d %v", table, count, err)
					}
				}
				var applied int
				if err = db.sql.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=99`).Scan(&applied); err != nil || applied != 1 {
					t.Fatalf("migration applied=%d: %v", applied, err)
				}
				read, err := store.Get(ctx, intent.AISessionID, intent.IntentID)
				if err != nil || read.State != intent.State {
					t.Fatalf("legacy read changed: %+v %v", read, err)
				}
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
