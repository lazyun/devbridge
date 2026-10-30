package controlapi_test

import (
	"errors"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

func TestFormalExecutionConfirmationRequiresFinalArchiveAndBinding(t *testing.T) {
	for _, stage := range []string{"formal_execution_t2", "formal_execution_receipts", "runtime_selection_bindings", "confirmation_completion"} {
		t.Run(stage, func(t *testing.T) {
			f, id, ref := formalExecutionFixture(t)
			journal, err := f.plane.Confirmations.Get(f.ctx, id.AISessionID, id.IntentID)
			if err != nil || journal.Commit == nil {
				t.Fatalf("original journal: %v", err)
			}
			input := *journal.Commit
			input.SubmittedHash = id.SubmittedHash
			initial := formalExecutionCounts(t, f)
			trigger := `CREATE TRIGGER formal_confirmation_fault BEFORE INSERT ON ` + stage + ` BEGIN SELECT RAISE(ABORT,'formal-confirmation-fault'); END`
			if stage == "confirmation_completion" {
				trigger = `CREATE TRIGGER formal_confirmation_fault BEFORE UPDATE OF intent_json ON confirmation_intents WHEN json_extract(NEW.intent_json,'$.state')='COMPLETED' BEGIN SELECT RAISE(ABORT,'formal-confirmation-fault'); END`
			}
			if _, err = f.plane.DB.SQL().Exec(trigger); err != nil {
				t.Fatal(err)
			}
			store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
			_, err = store.CommitApproveAndStart(f.ctx, id, ref)
			if stage == "runtime_selection_bindings" || stage == "confirmation_completion" {
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.facade.CommitConfirmation(f.ctx, input)
			}
			if err == nil || !strings.Contains(err.Error(), "formal-confirmation-fault") {
				t.Fatalf("expected real %s fault: %v", stage, err)
			}
			reopenDirectDeliveryFixture(t, f)
			before := definitionFormalSnapshot(t, f, formalExecutionTables())
			read := c.ReadInput{AISessionID: id.AISessionID, IntentID: id.IntentID, Version: id.IntentVersion, Purpose: c.Execution, ContentHash: id.ContentHash, RequestID: id.RequestID}
			got, err := f.facade.ReadConfirmation(f.ctx, read)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "confirmation_completion" {
				if got.State != "COMPLETED" || got.Steps[0].Effect != "PREPARATION_READY" || got.Steps[0].ExecutionID == "" {
					t.Fatalf("durable final result not recovered: %+v", got)
				}
			} else if got.State == "COMPLETED" || got.Steps[0].State == "COMPLETED" {
				t.Fatalf("incomplete %s was projected as completed: state=%s step=%+v", stage, got.State, got.Steps[0])
			}
			if stage != "confirmation_completion" && (!got.CanResume || got.Steps[0].TaskID != f.task || got.Steps[0].RevisionID == "" || (got.Steps[0].ExecutionID != "") != (stage != "formal_execution_t2")) {
				t.Fatalf("original durable prefix was not resumable: state=%s resume=%t step=%+v", got.State, got.CanResume, got.Steps[0])
			}
			assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "cold public confirmation read")
			if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER formal_confirmation_fault`); err != nil {
				t.Fatal(err)
			}
			completed, err := f.facade.CommitConfirmation(f.ctx, input)
			if err != nil || completed.State != "COMPLETED" || len(completed.Steps) != 1 || completed.Steps[0].ExecutionID == "" || completed.Steps[0].Effect != "PREPARATION_READY" || completed.Steps[0].RunID != "" {
				t.Fatalf("exact public continuation: %+v %v", completed, err)
			}
			counts := formalExecutionCounts(t, f)
			if counts["formal_execution_t1"] != 1 || counts["formal_execution_t2"] != 1 || counts["formal_execution_receipts"] != 1 || counts["runtime_selection_bindings"] != 1 || counts["runs"] != initial["runs"] || counts["execution_start_effects"] != initial["execution_start_effects"] {
				t.Fatalf("continuation duplicated effects or started a Run: %v", counts)
			}
		})
	}
}

// Older journals can contain a completed step while their overall confirmation
// is still CONFIRMING. Existing SQL permits this exact T3-backed shape without
// a binding. Recovery must verify the formal chain before promoting its state.
func TestFormalExecutionCompletedStepWithoutBindingCannotRecoverCompleted(t *testing.T) {
	f, id, ref := formalExecutionFixture(t)
	receipt, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitApproveAndStart(f.ctx, id, ref)
	if err != nil {
		t.Fatal(err)
	}
	counts := formalExecutionCounts(t, f)
	if counts["formal_execution_receipts"] != 1 || counts["runtime_selection_sources"] != 1 || counts["runtime_selection_bindings"] != 0 {
		t.Fatalf("expected durable T3/source without binding: %v", counts)
	}
	// Do not disable any trigger: this update itself proves the historical
	// partial-journal shape is accepted by the real current SQL constraints.
	result, err := f.plane.DB.SQL().ExecContext(f.ctx, `UPDATE confirmation_intents SET intent_json=json_set(intent_json,
 '$.state','CONFIRMING','$.steps[0].state','COMPLETED','$.steps[0].effect','PREPARATION_READY',
 '$.steps[0].taskId',?,'$.steps[0].revisionId',?,'$.steps[0].executionId',?) WHERE intent_id=? AND ai_session_id=?`, receipt.Archive.Fence.TaskID, receipt.Archive.RevisionID, receipt.T2.ExecutionID, id.IntentID, id.AISessionID)
	if err != nil {
		t.Fatalf("historical journal shape is not SQL-valid: %v", err)
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		t.Fatalf("journal update affected=%d error=%v", n, e)
	}
	var state, stepState, effect, execution string
	if err = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT json_extract(intent_json,'$.state'),json_extract(intent_json,'$.steps[0].state'),json_extract(intent_json,'$.steps[0].effect'),json_extract(intent_json,'$.steps[0].executionId') FROM confirmation_intents WHERE intent_id=?`, id.IntentID).Scan(&state, &stepState, &effect, &execution); err != nil {
		t.Fatal(err)
	}
	if state != "CONFIRMING" || stepState != "COMPLETED" || effect != "PREPARATION_READY" || execution != receipt.T2.ExecutionID {
		t.Fatalf("unexpected persisted journal %s/%s/%s/%s", state, stepState, effect, execution)
	}
	reopenDirectDeliveryFixture(t, f)
	before := definitionFormalSnapshot(t, f, formalExecutionTables())
	got, err := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: id.AISessionID, IntentID: id.IntentID, Version: id.IntentVersion, Purpose: c.Execution, ContentHash: id.ContentHash, RequestID: id.RequestID})
	if !errors.Is(err, runtimeselection.ErrBindingMissing) {
		t.Fatalf("missing binding did not refuse historical completed-step recovery: state=%s error=%v", got.State, err)
	}
	if got.IntentID != id.IntentID || got.State != "CONFIRMING" || len(got.Steps) != 1 || got.Steps[0].State != "COMPLETED" || got.Steps[0].ExecutionID != receipt.T2.ExecutionID {
		t.Fatalf("read failure changed original journal identity/state: %+v", got)
	}
	assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "cold partial journal with completed step")
	if after := formalExecutionCounts(t, f); after["runtime_selection_bindings"] != 0 {
		t.Fatalf("read repaired missing binding: %v", after)
	}
}
