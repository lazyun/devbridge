package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/recovery"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestNoRunSuccessorPreparingCancellationContinues(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "immediate-projection"
		if restart {
			name = "restart-before-projection"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, owner, original := qualifiedExecutionFixture(t, "-preparing-cancellation")
			firstInput := readyNoRunSuccessor(t, f, owner, original, "cancel-original")
			first, err := f.store.PrepareSuccessorExecution(ctx, firstInput)
			if err != nil || first.Outcome != wp.NoRunSuccessorCreated {
				t.Fatalf("first successor: %+v %v", first, err)
			}
			beforeCancel := preparingCancellationRows(t, f.db, `SELECT * FROM tasks WHERE id=?`, owner.ID)
			if len(beforeCancel) != 1 || beforeCancel[0]["phase"] != string(task.PhaseImplementationPreparing) || beforeCancel[0]["current_execution_id"] != first.SuccessorExecutionID {
				t.Fatalf("successor command did not establish PREPARING: %+v", beforeCancel)
			}
			cancel := executioncancel.Input{AISessionID: f.session, ProjectID: owner.ProjectID, ExecutionID: first.SuccessorExecutionID, OwnerType: "EXECUTION", OwnerID: first.SuccessorExecutionID, RequestID: "cancel-preparing-successor", Reason: "cancel successor before Run"}
			receipt, interrupt, err := NewExecutionCancellationStore(f.db).Begin(ctx, cancel, false)
			if err != nil || interrupt || receipt.State != "CANCELED" || receipt.RunID != "" || receipt.BackendDisposition != "NO_BACKEND" || receipt.BackendInterruptState != "NOT_SENT" || receipt.RecoveryRequired {
				t.Fatalf("PREPARING cancellation: %+v interrupt=%t err=%v", receipt, interrupt, err)
			}
			if after := preparingCancellationRows(t, f.db, `SELECT * FROM tasks WHERE id=?`, owner.ID); !reflect.DeepEqual(beforeCancel, after) {
				t.Fatalf("cancellation transaction changed Task: before=%+v after=%+v", beforeCancel, after)
			}
			preserved := preparingCancellationHistory(t, f.db, owner.ID, original.ID, first.SuccessorExecutionID)
			assertPreparingCancellationNoRuntime(t, f.db, owner.ID)
			if restart {
				root, epoch, clock := f.db.stateRoot, f.db.RuntimeEpoch(), f.store.clock
				if err := f.db.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := Open(ctx, root)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				f.db, f.store = reopened, NewWorkflowProgressionStore(reopened)
				f.store.clock = clock
				if reopened.RuntimeEpoch() == epoch {
					t.Fatal("reopen reused the old runtime epoch")
				}
				// Exercise the actual boot recovery service, including its generic
				// uncertainty scan after no-Run lifecycle reconciliation.
				if err := recovery.NewService(NewRecoveryStore(reopened), clock).Recover(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				phase, err := NewExecutionCancellationStore(f.db).ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, owner.ID, first.SuccessorExecutionID)
				if err != nil || phase != task.PhaseNoRunSuccessorRequired {
					t.Fatalf("PREPARING projection: phase=%s err=%v", phase, err)
				}
			}
			projected := preparingCancellationRows(t, f.db, `SELECT * FROM tasks WHERE id=?`, owner.ID)
			wantTask := beforeCancel[0]
			wantTask["phase"] = string(task.PhaseNoRunSuccessorRequired)
			if !reflect.DeepEqual([]map[string]any{wantTask}, projected) {
				t.Fatalf("projection must change only phase: want=%+v got=%+v", wantTask, projected)
			}
			eligibility := preparingCancellationRows(t, f.db, `SELECT * FROM no_run_successor_eligibilities WHERE predecessor_execution_id=?`, first.SuccessorExecutionID)
			if len(eligibility) != 1 || eligibility[0]["task_id"] != owner.ID || eligibility[0]["task_definition_revision_id"] != owner.CurrentDefinitionRevisionID || eligibility[0]["effective_cancellation_project_id"] != owner.ProjectID || eligibility[0]["effective_cancellation_request_id"] != cancel.RequestID || eligibility[0]["consumed_successor_execution_id"] != nil {
				t.Fatalf("exact cancellation eligibility: %+v", eligibility)
			}
			if _, err := NewExecutionCancellationStore(f.db).ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, owner.ID, first.SuccessorExecutionID); err != nil {
				t.Fatal(err)
			}
			if err := NewRecoveryStore(f.db).RecoverTaskLifecycles(ctx, f.now); err != nil {
				t.Fatal(err)
			}
			if after := preparingCancellationRows(t, f.db, `SELECT * FROM tasks WHERE id=?`, owner.ID); !reflect.DeepEqual(projected, after) {
				t.Fatalf("repeated projection rewrote Task: before=%+v after=%+v", projected, after)
			}
			if after := preparingCancellationRows(t, f.db, `SELECT * FROM no_run_successor_eligibilities WHERE predecessor_execution_id=?`, first.SuccessorExecutionID); !reflect.DeepEqual(eligibility, after) {
				t.Fatalf("repeated projection rewrote eligibility: before=%+v after=%+v", eligibility, after)
			}
			replayed, interrupt, err := NewExecutionCancellationStore(f.db).Begin(ctx, cancel, false)
			if err != nil || interrupt || !reflect.DeepEqual(receipt, replayed) {
				t.Fatalf("cancellation receipt replay changed: before=%+v after=%+v err=%v", receipt, replayed, err)
			}
			var prepRequest, prepPayload string
			if err := f.db.sql.QueryRow(`SELECT preparation_request_id,preparation_payload_hash FROM runtime_scope_plans WHERE plan_id=?`, first.RuntimeScopePlanID).Scan(&prepRequest, &prepPayload); err != nil {
				t.Fatal(err)
			}
			if _, err := NewRunStore(f.db).CreatePreparationRun(ctx, first.RuntimeScopePlanID, "forbidden-canceled-successor-run", prepRequest, prepPayload, f.now); err == nil {
				t.Fatal("canceled PREPARING successor created a Run")
			}
			assertPreparingCancellationNoRuntime(t, f.db, owner.ID)

			nextInput := firstInput
			nextInput.RequestID = "second-successor-request"
			nextInput.PredecessorExecutionID, nextInput.ExpectedCurrentExecutionID = first.SuccessorExecutionID, first.SuccessorExecutionID
			next, err := f.store.PrepareSuccessorExecution(ctx, nextInput)
			if err != nil || next.Outcome != wp.NoRunSuccessorCreated || next.SuccessorExecutionID == first.SuccessorExecutionID || next.RuntimeScopePlanID == first.RuntimeScopePlanID || next.RuntimePreparationAuthorityID == first.RuntimePreparationAuthorityID {
				t.Fatalf("next successor: %+v err=%v", next, err)
			}
			advanced := preparingCancellationRows(t, f.db, `SELECT * FROM tasks WHERE id=?`, owner.ID)
			if len(advanced) != 1 || advanced[0]["phase"] != string(task.PhaseImplementationPreparing) || advanced[0]["current_execution_id"] != next.SuccessorExecutionID || advanced[0]["current_definition_revision_id"] != owner.CurrentDefinitionRevisionID {
				t.Fatalf("next successor Task identity: %+v", advanced)
			}
			replay, err := f.store.PrepareSuccessorExecution(ctx, nextInput)
			if err != nil || replay != next {
				t.Fatalf("next successor replay: %+v %v", replay, err)
			}
			if after := preparingCancellationRows(t, f.db, `SELECT * FROM tasks WHERE id=?`, owner.ID); !reflect.DeepEqual(advanced, after) {
				t.Fatalf("successor replay changed Task: before=%+v after=%+v", advanced, after)
			}
			if after := preparingCancellationHistory(t, f.db, owner.ID, original.ID, first.SuccessorExecutionID); !reflect.DeepEqual(preserved, after) {
				t.Fatalf("reconciliation/succession rewrote predecessor history: before=%+v after=%+v", preserved, after)
			}
			assertPreparingCancellationNoRuntime(t, f.db, owner.ID)
			for table, want := range map[string]int{"execution_proposals": 3, "no_run_successor_eligibilities": 2, "no_run_successor_effect_authorities": 2, "no_run_successor_command_receipts": 2} {
				var got int
				if err := f.db.sql.QueryRow(`SELECT count(*) FROM `+table+` WHERE task_id=?`, owner.ID).Scan(&got); err != nil || got != want {
					t.Fatalf("%s count=%d want=%d err=%v", table, got, want, err)
				}
			}
		})
	}
}

func preparingCancellationRows(t *testing.T, db *DB, query string, args ...any) []map[string]any {
	t.Helper()
	rows, err := db.sql.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for rows.Next() {
		values, pointers := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = values[i]
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func preparingCancellationHistory(t *testing.T, db *DB, taskID, originalID, canceledID string) map[string][]map[string]any {
	t.Helper()
	return map[string][]map[string]any{
		"cancellations":          preparingCancellationRows(t, db, `SELECT * FROM execution_cancellations WHERE task_id=? ORDER BY request_id`, taskID),
		"executions":             preparingCancellationRows(t, db, `SELECT * FROM execution_proposals WHERE id IN (?,?) ORDER BY id`, originalID, canceledID),
		"plans":                  preparingCancellationRows(t, db, `SELECT * FROM runtime_scope_plans WHERE owner_kind='EXECUTION' AND owner_id IN (?,?) ORDER BY plan_id`, originalID, canceledID),
		"preparationAuthorities": preparingCancellationRows(t, db, `SELECT * FROM runtime_preparation_authorities WHERE owner_kind='EXECUTION' AND owner_id IN (?,?) ORDER BY plan_id`, originalID, canceledID),
	}
}

func assertPreparingCancellationNoRuntime(t *testing.T, db *DB, taskID string) {
	t.Helper()
	queries := map[string]string{
		"runs":             `SELECT count(*) FROM runs WHERE task_id=? AND (execution_proposal_id IS NOT NULL OR operation_kind='EXECUTION')`,
		"attempts":         `SELECT count(*) FROM runtime_preparation_attempts a JOIN runtime_scope_plans p ON p.plan_id=a.plan_id WHERE p.task_id=? AND p.owner_kind IN ('IMPLEMENTATION','EXECUTION')`,
		"bindings":         `SELECT count(*) FROM runtime_workset_bindings b JOIN runtime_scope_plans p ON p.plan_id=b.plan_id WHERE p.task_id=? AND p.owner_kind IN ('IMPLEMENTATION','EXECUTION')`,
		"startAuthorities": `SELECT count(*) FROM runtime_start_authorities a JOIN runtime_scope_plans p ON p.plan_id=a.plan_id WHERE p.task_id=? AND p.owner_kind IN ('IMPLEMENTATION','EXECUTION')`,
		"recoveryStatuses": `SELECT count(*) FROM recovery_status_records s JOIN runs r ON r.id=s.source_run_id WHERE r.task_id=?`,
	}
	for name, query := range queries {
		var count int
		if err := db.sql.QueryRow(query, taskID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("no-Run chain has %s count=%d err=%v", name, count, err)
		}
	}
}
