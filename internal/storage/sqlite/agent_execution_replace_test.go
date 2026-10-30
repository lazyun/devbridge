package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/run"
)

func TestAgentExecutionReplaceCannotRewriteIdentity(t *testing.T) {
	for _, recursive := range []int{0, 1} {
		for _, tc := range []struct {
			name    string
			kind    agentconversation.OperationKind
			withRun bool
		}{
			{"review-with-run", agentconversation.Review, true},
			{"development-with-run", agentconversation.Execution, true},
			{"development-no-run", agentconversation.Execution, false},
		} {
			t.Run(fmt.Sprintf("recursive=%d/%s", recursive, tc.name), func(t *testing.T) {
				f := newAgentStorageFixture(t, "replace")
				ctx := context.Background()
				op := prepareAgentOperation(t, f, tc.kind, "replace")
				id := op.id
				var referenced run.Run
				if tc.withRun {
					var err error
					referenced, err = op.make(ctx, "replace-run")
					if err != nil {
						t.Fatal(err)
					}
					id = referenced.AgentExecutionID
					// Compare the same hydrated read model before and after mutation.
					referenced, err = NewRunStore(f.db).Get(ctx, referenced.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				store := NewAgentExecutionStore(f.db)
				before, err := store.ListByProject(ctx, f.project.ID)
				if err != nil {
					t.Fatal(err)
				}
				conn, err := f.db.sql.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA recursive_triggers=%d", recursive)); err != nil {
					t.Fatal(err)
				}
				assertUnchanged := func(t *testing.T) {
					t.Helper()
					after, err := store.ListByProject(ctx, f.project.ID)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("Execution count/identity/provenance changed: before=%+v after=%+v err=%v", before, after, err)
					}
					var count int
					if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE agent_execution_id=?`, id).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if tc.withRun {
						persisted, err := NewRunStore(f.db).Get(ctx, referenced.ID)
						if err != nil || count != 1 || !reflect.DeepEqual(referenced, persisted) {
							t.Fatalf("referenced Run changed: count=%d Run=%+v err=%v", count, persisted, err)
						}
					} else if count != 0 {
						t.Fatalf("no-Run fixture acquired %d Runs", count)
					}
				}
				assertUnchanged(t)
				columns := []string{"id", "project_id", "task_id", "feature_area_id", "kind", "semantic_owner_kind", "semantic_owner_id", "execution_proposal_id", "provenance_kind", "created_at"}
				for _, algorithm := range []string{"INSERT", "INSERT OR REPLACE", "INSERT OR IGNORE", "UPSERT"} {
					for _, field := range append([]string{"same"}, columns...) {
						t.Run(algorithm+"/"+field, func(t *testing.T) {
							expressions := append([]string(nil), columns...)
							for i, col := range columns {
								if col == field {
									expressions[i] = "'different'"
								}
							}
							verb := algorithm
							if verb == "UPSERT" {
								verb = "INSERT"
							}
							q := verb + " INTO agent_executions(" + strings.Join(columns, ",") + ") SELECT " + strings.Join(expressions, ",") + " FROM agent_executions WHERE id=?"
							if algorithm == "UPSERT" {
								q += " ON CONFLICT(id) DO UPDATE SET task_id=excluded.task_id"
							}
							result, err := conn.ExecContext(ctx, q, id)
							if field == "same" {
								if err != nil {
									t.Fatalf("safe replay: %v", err)
								}
								if n, err := result.RowsAffected(); err != nil || n != 0 {
									t.Fatalf("replay must be no-op, not delete/recreate: affected=%d err=%v", n, err)
								}
							} else if err == nil || !strings.Contains(err.Error(), "Agent Execution identity is immutable") {
								t.Fatalf("identity guard must reject before CHECK/FK/conflict handling: %v", err)
							}
							assertUnchanged(t)
						})
					}
				}
				for _, q := range []string{`UPDATE agent_executions SET task_id='different' WHERE id=?`, `DELETE FROM agent_executions WHERE id=?`} {
					if _, err := conn.ExecContext(ctx, q, id); err == nil || (!strings.Contains(err.Error(), "Agent Execution identity is immutable") && !strings.Contains(err.Error(), "Agent Execution is durable history")) {
						t.Fatalf("direct mutation must fail through durable identity guard: %v", err)
					}
					assertUnchanged(t)
				}
			})
		}
	}
}

func TestAgentExecutionWithoutRowidRejectsAliasReplacement(t *testing.T) {
	for _, recursive := range []int{0, 1} {
		t.Run(fmt.Sprintf("recursive=%d", recursive), func(t *testing.T) {
			ctx := context.Background()
			f := newAgentStorageFixture(t, "rowid")
			a := prepareAgentOperation(t, f, agentconversation.Execution, "a-no-run")
			b := prepareAgentOperation(t, f, agentconversation.Review, "b-no-execution")
			store := NewAgentExecutionStore(f.db)
			before, err := store.ListByProject(ctx, f.project.ID)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := f.db.sql.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA recursive_triggers=%d", recursive)); err != nil {
				t.Fatal(err)
			}
			var wr, mode, count int
			if err = conn.QueryRowContext(ctx, `SELECT wr FROM pragma_table_list WHERE schema='main' AND name='agent_executions'`).Scan(&wr); err != nil || wr != 1 {
				t.Fatalf("table must be WITHOUT ROWID: wr=%d err=%v", wr, err)
			}
			if err = conn.QueryRowContext(ctx, `PRAGMA recursive_triggers`).Scan(&mode); err != nil || mode != recursive {
				t.Fatalf("recursive_triggers=%d err=%v", mode, err)
			}
			if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_executions WHERE semantic_owner_kind='REVIEW' AND semantic_owner_id=?`, b.id).Scan(&count); err != nil || count != 0 {
				t.Fatalf("B must initially have no Execution: count=%d err=%v", count, err)
			}
			assertNoRunA := func() {
				t.Helper()
				if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE agent_execution_id=?`, a.id).Scan(&count); err != nil || count != 0 {
					t.Fatalf("A must have no Run references: count=%d err=%v", count, err)
				}
			}
			assertNoRunA()
			columns := "id,project_id,task_id,feature_area_id,kind,semantic_owner_kind,semantic_owner_id,execution_proposal_id,provenance_kind,created_at"
			values := "'review:'||id,project_id,task_id,feature_area_id,'REVIEW','REVIEW',id,NULL,'NATIVE',created_at FROM reviews WHERE id=?"
			for _, alias := range []string{"rowid", "_rowid_", "oid"} {
				t.Run(alias, func(t *testing.T) {
					// A has no implicit row identifier to read or collide with. Even
					// supplying an arbitrary integer cannot form a replacement.
					var hidden int64
					if err := conn.QueryRowContext(ctx, "SELECT "+alias+" FROM agent_executions WHERE id=?", a.id).Scan(&hidden); err == nil || !strings.Contains(err.Error(), "no such column: "+alias) {
						t.Fatalf("implicit alias must not exist: %v", err)
					}
					q := "INSERT OR REPLACE INTO agent_executions(" + alias + "," + columns + ") SELECT 1," + values
					if _, err := conn.ExecContext(ctx, q, b.id); err == nil || !strings.Contains(err.Error(), "has no column named "+alias) {
						t.Fatalf("alias replacement must be impossible: %v", err)
					}
					after, err := store.ListByProject(ctx, f.project.ID)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("alias replacement changed durable inventory: %+v %v", after, err)
					}
					assertNoRunA()
				})
			}
			// Prove B is otherwise a legal new identity, not a malformed owner
			// rejected incidentally by a CHECK, foreign key or admission guard.
			result, err := conn.ExecContext(ctx, "INSERT INTO agent_executions("+columns+") SELECT "+values, b.id)
			if err != nil {
				t.Fatalf("valid new identity must admit through plain INSERT: %v", err)
			}
			if n, err := result.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("new identity insert: affected=%d err=%v", n, err)
			}
			bBefore, err := store.Get(ctx, "review:"+b.id)
			if err != nil {
				t.Fatal(err)
			}
			r, err := b.make(ctx, "b-run")
			if err != nil || r.AgentExecutionID != bBefore.ID {
				t.Fatalf("production admission must replay the exact envelope: %+v %v", r, err)
			}
			bAfter, err := store.Get(ctx, bBefore.ID)
			if err != nil || !reflect.DeepEqual(bBefore, bAfter) {
				t.Fatalf("production replay rewrote identity: %+v %v", bAfter, err)
			}
			after, err := store.ListByProject(ctx, f.project.ID)
			if err != nil || len(after) != len(before)+1 {
				t.Fatalf("new identity/replay inventory: %+v %v", after, err)
			}
			for _, original := range before {
				persisted, err := store.Get(ctx, original.ID)
				if err != nil || !reflect.DeepEqual(original, persisted) {
					t.Fatalf("existing identity changed: %+v %v", persisted, err)
				}
			}
			assertNoRunA()
		})
	}
}
