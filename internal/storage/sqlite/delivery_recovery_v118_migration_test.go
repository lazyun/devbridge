package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/git"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type deliveryV116Fixture struct {
	db                        *DB
	project, task, repository string
	payload, hash, epoch, at  string
	commit, tree, resultAt    string
	changed                   bool
}

func deliveryRecoveryV116Fixture(t *testing.T, applied bool) deliveryV116Fixture {
	t.Helper()
	ctx := context.Background()
	f, owner, execution := acceptedExecutionFixture(t, "-delivery-v116")
	execution = terminalExecutionWithReport(t, f, execution)
	_, source, base := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	checkpoint := errors.New("v116 dispatch response unavailable")
	if !applied {
		f.store.SetDeliveryFaultHookForTest(func(stage, _ string) error {
			if stage == "TASK_COMMIT_BEFORE_GIT" {
				return checkpoint
			}
			return nil
		})
	}
	out, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{
		AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID),
		ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "v116-accept",
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied && (out.CommitError != "" || out.Task.CommitSHA == "") {
		t.Fatalf("applied fixture did not commit: %+v", out)
	}
	if !applied && (out.CommitError != checkpoint.Error() || out.Task.CommitSHA != "" || commitTestGit(t, source, "rev-parse", "HEAD") != base) {
		t.Fatalf("unknown fixture did not stop after durable dispatch: %+v", out)
	}
	// Restore only production-produced v116 columns into the historical schema;
	// the current operations/outcomes and migration ledger are not imported.
	v := deliveryV116Fixture{db: restoreHistoricalMigrationFixture(t, f.db, 116), project: f.project, task: owner.ID}
	if err = v.db.sql.QueryRow(`SELECT repository_id,payload_json,payload_sha256,runtime_epoch,created_at FROM task_delivery_dispatch_intents WHERE task_id=?`, v.task).Scan(&v.repository, &v.payload, &v.hash, &v.epoch, &v.at); err != nil {
		t.Fatal(err)
	}
	var resultCount int
	if err = v.db.sql.QueryRow(`SELECT count(*) FROM task_delivery_dispatch_results WHERE task_id=?`, v.task).Scan(&resultCount); err != nil || resultCount != map[bool]int{false: 0, true: 1}[applied] {
		t.Fatalf("historical results=%d applied=%v err=%v", resultCount, applied, err)
	}
	if applied {
		if err = v.db.sql.QueryRow(`SELECT commit_oid,tree_oid,changed,created_at FROM task_delivery_dispatch_results WHERE task_id=?`, v.task).Scan(&v.commit, &v.tree, &v.changed, &v.resultAt); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

// Freeze each historical column list before ALTER TABLE adds terminal_outcome.
// This compares every old business fact, including the complete parent receipt.
func deliveryMigrationHistoricalSnapshot(t *testing.T, db *DB) func() string {
	t.Helper()
	queries := map[string]string{}
	for _, table := range selectionMigrationTables(t, db) {
		rows, err := db.sql.Query(`SELECT * FROM ` + sqlIdentifier(table) + ` LIMIT 0`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		for i := range columns {
			columns[i] = sqlIdentifier(columns[i])
		}
		queries[table] = `SELECT ` + strings.Join(columns, ",") + ` FROM ` + sqlIdentifier(table) + ` ORDER BY 1`
	}
	return func() string {
		t.Helper()
		out := map[string][][]any{}
		for table, query := range queries {
			rows, err := db.sql.Query(query)
			if err != nil {
				t.Fatal(err)
			}
			columns, err := rows.Columns()
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out[table] = [][]any{}
			for rows.Next() {
				values, pointers := make([]any, len(columns)), make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err = rows.Scan(pointers...); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				out[table] = append(out[table], values)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
}

func TestDeliveryRecoveryV118MigrationPreservesKnownAndUnknown(t *testing.T) {
	for _, applied := range []bool{true, false} {
		name := "UNKNOWN"
		if applied {
			name = "APPLIED"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			v := deliveryRecoveryV116Fixture(t, applied)
			history := deliveryMigrationHistoricalSnapshot(t, v.db)
			before := history()
			if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(118)); err != nil {
				t.Fatal(err)
			}
			if history() != before {
				t.Fatal("v118 rewrote historical business facts")
			}
			var operation, project, taskID, repository, request, payload, hash, execution, workset, development, at, epoch, dispatchAt string
			var initial bool
			err := v.db.sql.QueryRow(`SELECT o.operation_id,o.project_id,o.task_id,o.repository_id,o.request_id,o.payload_json,o.payload_sha256,o.execution_id,o.workset_id,o.development_context_id,o.created_at,o.predecessor_operation_id IS NULL,d.runtime_epoch,d.created_at FROM task_delivery_operations o JOIN task_delivery_current_operations c ON c.operation_id=o.operation_id JOIN task_delivery_operation_dispatches d ON d.operation_id=o.operation_id WHERE o.task_id=?`, v.task).Scan(&operation, &project, &taskID, &repository, &request, &payload, &hash, &execution, &workset, &development, &at, &initial, &epoch, &dispatchAt)
			if err != nil {
				t.Fatal(err)
			}
			if project != v.project || taskID != v.task || repository != v.repository || request != operation || payload != v.payload || hash != v.hash || at != v.at || epoch != v.epoch || dispatchAt != v.at || !initial {
				t.Fatal("import did not preserve exact original dispatch identity and payload")
			}
			if !strings.HasPrefix(execution, "legacy-v116/") || !strings.HasPrefix(development, "legacy-v116/") || workset == "" {
				t.Fatalf("migration inferred live execution/development authority: execution=%q development=%q workset=%q", execution, development, workset)
			}
			var outcomes, negatives int
			if err = v.db.sql.QueryRow(`SELECT count(*),COALESCE(sum(outcome='REJECTED_NOT_CONSUMED'),0) FROM task_delivery_operation_outcomes`).Scan(&outcomes, &negatives); err != nil || outcomes != map[bool]int{false: 0, true: 1}[applied] || negatives != 0 {
				t.Fatalf("imported outcomes=%d negatives=%d err=%v", outcomes, negatives, err)
			}
			if applied {
				var outcome, commit, tree, resultAt, detail string
				var beforeEffect, changed bool
				if err = v.db.sql.QueryRow(`SELECT outcome,before_external_effect,commit_oid,tree_oid,changed,error,created_at FROM task_delivery_operation_outcomes WHERE operation_id=?`, operation).Scan(&outcome, &beforeEffect, &commit, &tree, &changed, &detail, &resultAt); err != nil || outcome != "APPLIED" || beforeEffect || commit != v.commit || tree != v.tree || changed != v.changed || detail != "" || resultAt != v.resultAt {
					t.Fatalf("known immutable result changed: outcome=%s commit=%s tree=%s changed=%v error=%s at=%s err=%v", outcome, commit, tree, changed, detail, resultAt, err)
				}
			}
			assertRecoveryForeignKeys(t, v.db)
			stable := selectionMigrationSnapshot(t, v.db)
			if err = ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(118)); err != nil || selectionMigrationSnapshot(t, v.db) != stable {
				t.Fatalf("second migration changed schema/history: %v", err)
			}
			// Current read/command ports require the current schema; v118 assertions above remain pinned.
			if err = ApplyMigrations(ctx, v.db.sql, DefaultMigrations()); err != nil {
				t.Fatal(err)
			}
			stable = selectionMigrationSnapshot(t, v.db)
			store := NewWorkflowProgressionStore(v.db)
			candidates, err := store.TaskDeliveryRetryCandidates(ctx, v.project, v.task)
			if err != nil || len(candidates) != 0 {
				t.Fatalf("historical operation gained retry offer: %+v %v", candidates, err)
			}
			if _, err = store.ReprepareTaskCommit(ctx, v.project, delivery.RetryInput{TaskID: v.task, RepositoryID: v.repository, PredecessorOperationID: operation, RequestID: "legacy-retry"}); !errors.Is(err, git.ErrRecoveryRequired) {
				t.Fatalf("historical operation gained retry authority: %v", err)
			}
			var input taskDeliveryDispatchInput
			if err = json.Unmarshal([]byte(v.payload), &input); err != nil {
				t.Fatal(err)
			}
			effects, observations := 0, 0
			observationErr := &git.BeforeEffectError{Err: errors.New("an observer cannot certify the old dispatch was unconsumed")}
			result, err := store.dispatchTaskDeliveryEffect(ctx, input, func() (taskDeliveryDispatchResult, error) {
				effects++
				return taskDeliveryDispatchResult{}, nil
			}, func() (taskDeliveryDispatchResult, error) {
				observations++
				return taskDeliveryDispatchResult{}, observationErr
			})
			if applied {
				if err != nil || observations != 0 || result != (taskDeliveryDispatchResult{Commit: v.commit, Tree: v.tree, Changed: v.changed}) {
					t.Fatalf("known result did not replay: %+v observations=%d err=%v", result, observations, err)
				}
			} else if !errors.Is(err, observationErr) || observations != 1 {
				t.Fatalf("unknown dispatch was not observed: observations=%d err=%v", observations, err)
			}
			if effects != 0 || selectionMigrationSnapshot(t, v.db) != stable {
				t.Fatalf("historical replay wrote data or dispatched Git: effects=%d", effects)
			}
		})
	}
}

func TestDeliveryRecoveryV118MigrationFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	v := deliveryRecoveryV116Fixture(t, false)
	if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(117)); err != nil {
		t.Fatal(err)
	}
	before := selectionMigrationSnapshot(t, v.db)
	// The final trigger conflicts only after ALTER, old trigger replacement,
	// all five new tables, and all backfill INSERTs have executed.
	if _, err := v.db.sql.Exec(`CREATE TABLE delivery_abandonment_durable(id TEXT); CREATE TRIGGER delivery_abandonment_durable BEFORE INSERT ON delivery_abandonment_durable BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	withConflict := selectionMigrationSnapshot(t, v.db)
	err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(118))
	if err == nil || !strings.Contains(err.Error(), "delivery_abandonment_durable") {
		t.Fatalf("late migration conflict not exercised: %v", err)
	}
	if selectionMigrationSnapshot(t, v.db) != withConflict {
		t.Fatal("failed migration retained partial schema, backfill, or version")
	}
	assertRecoveryForeignKeys(t, v.db)
	if _, err = v.db.sql.Exec(`DROP TABLE delivery_abandonment_durable`); err != nil {
		t.Fatal(err)
	}
	if selectionMigrationSnapshot(t, v.db) != before {
		t.Fatal("failed migration did not restore exact prior schema and facts")
	}
	if err = ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(118)); err != nil {
		t.Fatal(err)
	}
	assertRecoveryForeignKeys(t, v.db)
	var operations, outcomes int
	if err = v.db.sql.QueryRow(`SELECT (SELECT count(*) FROM task_delivery_operations),(SELECT count(*) FROM task_delivery_operation_outcomes)`).Scan(&operations, &outcomes); err != nil || operations != 1 || outcomes != 0 {
		t.Fatalf("recovered migration did not preserve UNKNOWN: operations=%d outcomes=%d err=%v", operations, outcomes, err)
	}
}
