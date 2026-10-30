package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestNoRunStopRevisionApprovalPreservesCanceledPredecessor(t *testing.T) {
	ctx := context.Background()
	f, owner, predecessor := acceptedExecutionFixture(t, "-stop-revision")
	ctx = retryCallerCtx(t, f, ctx)
	if _, _, err := f.store.ReserveTaskExecution(ctx, f.session, owner.ID); err != nil {
		t.Fatal(err)
	}
	cancel := executioncancel.Input{AISessionID: f.session, ProjectID: owner.ProjectID, ExecutionID: predecessor.ID, OwnerType: "EXECUTION", OwnerID: predecessor.ID, RequestID: "cancel-stop-revision"}
	if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, cancel, false); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecutionCancellationStore(f.db).ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, owner.ID, predecessor.ID); err != nil {
		t.Fatal(err)
	}
	before, err := NewExecutionProposalStore(f.db).Get(ctx, predecessor.ID)
	if err != nil {
		t.Fatal(err)
	}
	history := noRunRevisionHistory(t, f.db, predecessor.ID)
	in := wp.StopAndReviseNoRunTask{ProjectID: owner.ProjectID, TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: owner.CurrentDefinitionRevisionID, ActorAuthority: f.session}
	stopped, err := f.store.StopAndReviseNoRunTask(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Phase != task.PhaseReviewRequired || stopped.CurrentExecutionID != predecessor.ID || stopped.CurrentDefinitionRevisionID != owner.CurrentDefinitionRevisionID {
		t.Fatalf("stop: %+v", stopped)
	}
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	approved, err := f.store.Resolve(ctx, f.input(a, "APPROVE"))
	if err != nil {
		t.Fatal(err)
	}
	if approved.Task.CurrentDefinitionRevisionID == owner.CurrentDefinitionRevisionID || approved.Task.CurrentExecutionID == predecessor.ID || approved.Task.CurrentExecutionID == "" {
		t.Fatalf("new approval did not advance identity: %+v", approved.Task)
	}
	newCandidate, err := NewExecutionProposalStore(f.db).Get(ctx, approved.Task.CurrentExecutionID)
	if err != nil || newCandidate.ReviewApprovalID != approved.Task.ReviewApprovalID {
		t.Fatalf("new candidate approval: %+v %v", newCandidate, err)
	}
	counts := map[string]int{}
	for _, table := range []string{"no_run_successor_command_receipts", "no_run_successor_effect_authorities", "execution_proposals", "runtime_scope_plans", "runtime_preparation_authorities", "runs"} {
		counts[table] = noRunCount(t, f, table)
	}
	stale := wp.PrepareSuccessorExecution{RequestID: "old-predecessor-after-revision", PayloadSHA256: strings.Repeat("c", 64), ProjectID: owner.ProjectID, TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: owner.CurrentDefinitionRevisionID, ActorAuthority: f.session}
	// Even an unavailable live backend cannot displace the expected-identity
	// rejection or turn this old command into a runtime preparation attempt.
	if _, err = f.db.sql.Exec(`UPDATE codex_configs SET enabled=0 WHERE id=?`, before.BackendSelection.ConfigID); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.PrepareSuccessorExecution(ctx, stale)
	if _, enableErr := f.db.sql.Exec(`UPDATE codex_configs SET enabled=1 WHERE id=?`, before.BackendSelection.ConfigID); enableErr != nil {
		t.Fatal(enableErr)
	}
	var rejected *wp.Error
	if !errors.As(err, &rejected) || rejected.Code != "TASK_NOT_CURRENT" {
		t.Fatalf("old successor identity after revision: %v", err)
	}
	for table, want := range counts {
		if got := noRunCount(t, f, table); got != want {
			t.Fatalf("stale successor changed %s: %d -> %d", table, want, got)
		}
	}
	after, err := NewExecutionProposalStore(f.db).Get(ctx, predecessor.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("predecessor mutated: before=%+v after=%+v err=%v", before, after, err)
	}
	if got := noRunRevisionHistory(t, f.db, predecessor.ID); got != history {
		t.Fatal("revision changed canceled predecessor history")
	}
	var consumed any
	if err = f.db.sql.QueryRow(`SELECT consumed_successor_execution_id FROM no_run_successor_eligibilities WHERE predecessor_execution_id=?`, predecessor.ID).Scan(&consumed); err != nil || consumed != nil {
		t.Fatalf("old eligibility consumed: %v %v", consumed, err)
	}
	replay, err := f.store.Resolve(ctx, f.input(a, "APPROVE"))
	if err != nil || !replay.Replay || !reflect.DeepEqual(approved.Task, replay.Task) {
		t.Fatalf("approval replay: %+v %v", replay, err)
	}
	stateRoot := f.db.stateRoot
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = Open(ctx, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Close() })
	f.store = NewWorkflowProgressionStore(f.db)
	replay, err = f.store.Resolve(ctx, f.input(a, "APPROVE"))
	if err != nil || !replay.Replay || !reflect.DeepEqual(approved.Task, replay.Task) {
		t.Fatalf("cold approval replay: %+v %v", replay, err)
	}
	if _, err = NewExecutionCancellationStore(f.db).ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, owner.ID, predecessor.ID); err != nil {
		t.Fatalf("old cancellation projection after revision: %v", err)
	}
	if got := noRunRevisionHistory(t, f.db, predecessor.ID); got != history {
		t.Fatal("replay changed canceled predecessor history")
	}
}

func TestNoRunRevisionTriggersRejectUnrelatedWrites(t *testing.T) {
	ctx := context.Background()
	f, owner, predecessor := acceptedExecutionFixture(t, "-revision-guards")
	ctx = retryCallerCtx(t, f, ctx)
	readyNoRunSuccessor(t, f, owner, predecessor, "guard-cancel")
	history := noRunRevisionHistory(t, f.db, predecessor.ID)
	for _, tc := range []struct{ name, assignment string }{
		{"timestamp", `updated_at='2099-01-01T00:00:00Z'`},
		{"pointer", `current_execution_id=NULL`},
		{"revision", `current_definition_revision_id=NULL`},
		{"semantics", `goal='unapproved goal'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.db.sql.Exec(`UPDATE tasks SET phase='REVIEW_REQUIRED',`+tc.assignment+` WHERE id=?`, owner.ID); err == nil {
				t.Fatal("STOP accepted unrelated mutation")
			}
		})
	}
	t.Run("consumed", func(t *testing.T) {
		tx, err := f.db.beginWrite(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `UPDATE no_run_successor_eligibilities SET consumed_successor_execution_id=? WHERE predecessor_execution_id=?`, predecessor.ID, predecessor.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED' WHERE id=?`, owner.ID); err == nil {
			t.Fatal("consumed eligibility allowed STOP")
		}
	})
	if _, err := f.store.StopAndReviseNoRunTask(ctx, wp.StopAndReviseNoRunTask{ProjectID: owner.ProjectID, TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: owner.CurrentDefinitionRevisionID, ActorAuthority: f.session}); err != nil {
		t.Fatal(err)
	}
	t.Run("no-new-review", func(t *testing.T) {
		tx, err := f.db.beginWrite(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='IMPLEMENTATION_READY' WHERE id=?`, owner.ID); err != nil {
			t.Fatal(err)
		}
		current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, owner.ID))
		if err != nil {
			t.Fatal(err)
		}
		if eligible, err := noRunRevisionApprovedTx(ctx, tx, owner.ID); err != nil || eligible {
			t.Fatalf("old approval authorized replacement: %v %v", eligible, err)
		}
		if err = createApprovedExecutionTx(ctx, tx, current, f.session, f.now); err == nil {
			t.Fatal("old approval created candidate")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET current_execution_id=NULL WHERE id=?`, owner.ID); err == nil {
			t.Fatal("cleared retained predecessor")
		}
	})
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	if _, err := f.db.sql.Exec(`UPDATE tasks SET review_approval_id=? WHERE id=?`, wp.DecisionID(a.Review.ID), owner.ID); err == nil {
		t.Fatal("new decision without receipt changed approval")
	}
	before := ownedTask(t, f)
	for _, table := range []string{"decision_resolution_receipts", "review_approval_materializations"} {
		t.Run("missing-"+table, func(t *testing.T) {
			if _, err := f.db.sql.Exec(`CREATE TRIGGER no_run_missing_authority BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.Resolve(ctx, f.input(a, "APPROVE")); err == nil {
				t.Fatal("approved without required durable authority")
			}
			if _, err := f.db.sql.Exec(`DROP TRIGGER no_run_missing_authority`); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, ownedTask(t, f)) {
				t.Fatal("failed approval leaked Task changes")
			}
			if got := noRunRevisionHistory(t, f.db, predecessor.ID); got != history {
				t.Fatal("failed approval changed canceled history")
			}
		})
	}
	if _, err := f.store.Resolve(ctx, f.input(a, "APPROVE")); err != nil {
		t.Fatalf("valid approval after rolled-back failures: %v", err)
	}
}

func noRunRevisionHistory(t *testing.T, db *DB, predecessor string) string {
	t.Helper()
	queries := []string{
		`SELECT * FROM execution_proposals WHERE id=?`,
		`SELECT * FROM execution_cancellations WHERE owner_id=? ORDER BY request_id`,
		`SELECT * FROM no_run_successor_eligibilities WHERE predecessor_execution_id=?`,
		`SELECT * FROM task_definition_revisions WHERE id=(SELECT task_definition_revision_id FROM execution_proposals WHERE id=?)`,
		`SELECT * FROM runtime_scope_plans WHERE owner_kind='EXECUTION' AND owner_id=? ORDER BY plan_id`,
		`SELECT * FROM runtime_preparation_authorities WHERE owner_kind='EXECUTION' AND owner_id=? ORDER BY plan_id`,
	}
	var history [][][]any
	for _, query := range queries {
		rows, err := db.sql.Query(query, predecessor)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var values [][]any
		for rows.Next() {
			value := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range value {
				targets[i] = &value[i]
			}
			if err = rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		history = append(history, values)
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestNoRunRevisionV95UpgradeRetainsPopulatedHistory(t *testing.T) {
	ctx := context.Background()
	f, owner, predecessor := acceptedExecutionFixture(t, "-v95-upgrade")
	ctx = retryCallerCtx(t, f, ctx)
	readyNoRunSuccessor(t, f, owner, predecessor, "v95-upgrade-cancel")
	history := noRunRevisionHistory(t, f.db, predecessor.ID)
	f.db = restoreHistoricalMigrationFixture(t, f.db, 95)
	all := DefaultMigrations()
	var err error
	if _, err = f.db.sql.Exec(`UPDATE tasks SET phase='REVIEW_REQUIRED' WHERE id=?`, owner.ID); err == nil {
		t.Fatal("v95 unexpectedly allowed STOP")
	}
	stateRoot := f.db.stateRoot
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		f.db, err = Open(ctx, stateRoot)
		if err != nil {
			t.Fatal(err)
		}
		var version, v96Applied int
		latestVersion := all[len(all)-1].Version
		if err = f.db.sql.QueryRow(`SELECT MAX(version),COUNT(CASE WHEN version=96 THEN 1 END) FROM schema_migrations`).Scan(&version, &v96Applied); err != nil || version != latestVersion || v96Applied != 1 {
			t.Fatalf("version=%d want=%d v96Applied=%d err=%v", version, latestVersion, v96Applied, err)
		}
		if noRunRevisionHistory(t, f.db, predecessor.ID) != history {
			t.Fatal("migration or repeated open rewrote history")
		}
		if i == 0 {
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() { f.db.Close() })
	in := wp.StopAndReviseNoRunTask{ProjectID: owner.ProjectID, TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: owner.CurrentDefinitionRevisionID, ActorAuthority: f.session}
	if _, err = NewWorkflowProgressionStore(f.db).StopAndReviseNoRunTask(ctx, in); err != nil {
		t.Fatal(err)
	}
	if noRunRevisionHistory(t, f.db, predecessor.ID) != history {
		t.Fatal("STOP rewrote upgraded history")
	}
}
