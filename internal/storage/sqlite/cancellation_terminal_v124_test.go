package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestCancellationTerminalV124PreservesHistoryAndRollsBack(t *testing.T) {
	ctx := context.Background()
	v := deliveryRecoveryV116Fixture(t, false)
	if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(123)); err != nil {
		t.Fatal(err)
	}
	history := deliveryMigrationHistoricalSnapshot(t, v.db)
	before := history()
	var original string
	if err := v.db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='delivery_operation_dispatch_exact'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	// Fail after creating the view and replacing the first dispatch trigger.
	if _, err := v.db.sql.Exec(`DROP TRIGGER delivery_operation_dispatch_exact`); err != nil {
		t.Fatal(err)
	}
	conflict := selectionMigrationSnapshot(t, v.db)
	if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(124)); err == nil {
		t.Fatal("missing dispatch guard accepted")
	}
	if selectionMigrationSnapshot(t, v.db) != conflict {
		t.Fatal("failed upgrade left partial schema or business facts")
	}
	if _, err := v.db.sql.Exec(original); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(124)); err != nil {
			t.Fatal(err)
		}
		if history() != before {
			t.Fatal("upgrade changed historical delivery facts")
		}
	}
	for _, name := range []string{"task_delivery_dispatch_exact", "delivery_operation_dispatch_exact"} {
		var sql string
		if err := v.db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&sql); err != nil || !strings.Contains(sql, "unresolved_cancellation_interrupts") {
			t.Fatalf("dispatch guard %s not upgraded: %v", name, err)
		}
	}
	assertRecoveryForeignKeys(t, v.db)
}

func TestSettledUnknownCancellationAllowsTaskDeliveryAndReplay(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-settled-interrupt")
	execution = terminalExecutionWithReport(t, f, execution)
	ctx := context.Background()
	// Historical readiness timeout followed by an applied NO_ADVANCE terminal.
	// Seed the transport history without inventing an interrupt acknowledgement.
	_, err := f.db.sql.Exec(`INSERT INTO execution_cancellations(project_id,request_id,execution_id,run_id,requested_by_ai_session_id,reason,requested_at,runtime_epoch,state,interrupt_state,thread_id,turn_id,resolved_at,owner_type,owner_id,task_id,backend_disposition)
 SELECT project_id,'settled-interrupt',execution_proposal_id,id,?,'readiness timeout',?,runtime_epoch,'CANCEL_NOT_APPLIED','OUTCOME_UNKNOWN',thread_id,turn_id,?,'EXECUTION',operation_id,task_id,'TERMINATED' FROM runs WHERE id=?`, f.session, formatTime(testNow), formatTime(testNow), execution.RunID)
	if err != nil {
		t.Fatal(err)
	}
	attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	in := wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "accept-settled-interrupt"}
	accepted, err := f.store.ResolveTaskAcceptance(ctx, in)
	if err != nil || accepted.CommitError != "" || accepted.Task.Status != task.StatusSucceeded || accepted.Task.CommitSHA == "" {
		t.Fatalf("settled cancellation blocked delivery: %+v err=%v", accepted, err)
	}
	root := f.db.stateRoot
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db = openTestDB(t, root)
	t.Cleanup(func() { f.db.Close() })
	f.store = NewWorkflowProgressionStore(f.db)
	replayed, err := f.store.ResolveTaskAcceptance(ctx, in)
	if err != nil || !replayed.Replay || replayed.Task.CommitSHA != accepted.Task.CommitSHA {
		t.Fatalf("cold replay changed completed delivery: %+v err=%v", replayed, err)
	}
	var interrupt string
	if err = f.db.sql.QueryRow(`SELECT interrupt_state FROM execution_cancellations WHERE request_id='settled-interrupt'`).Scan(&interrupt); err != nil || interrupt != "OUTCOME_UNKNOWN" {
		t.Fatalf("delivery rewrote interrupt history: %s %v", interrupt, err)
	}
}
