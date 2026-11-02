package controlapi_test

import (
	"encoding/json"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Real Local MCP HTTP, Core, SQLite and isolated Git repositories. Qualification
// is explicitly issued by the existing test persistence fixture; no native/model
// is used, and neither predecessor nor successor is allowed to acquire a Run.
func TestHistoricalOperationReadDoesNotBorrowSuccessorWorkflow(t *testing.T) {
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	f.plane.Retirement.Close()
	f.plane.Coordinator.Codex = preparationOnlyCodexFactory{}
	f, id, ref := formalExecutionFixtureForUpper(t, f)
	receipt, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitApproveAndStart(f.ctx, id, ref)
	if err != nil {
		t.Fatal(err)
	}
	commit := receipt.Archive.Commit
	commit.BackendSelection, commit.OriginalRequest, commit.SubmittedHash = nil, nil, ""
	if completed, e := f.facade.CommitConfirmation(f.ctx, commit); e != nil || completed.State != "COMPLETED" {
		t.Fatalf("qualified pre-Run approval: %+v %v", completed, e)
	}
	predecessor := receipt.T2.ExecutionID
	client := newApprovedStartLocalClient(t, f)
	command := map[string]any{"aiSessionId": f.session, "ownerType": "EXECUTION", "ownerId": predecessor, "requestId": "cancel-original-before-run", "reason": "isolated no-Run cancellation"}
	type cancelEnvelope struct {
		Result          executioncancel.Receipt `json:"result"`
		Context         map[string]any          `json:"context"`
		Workflow        map[string]any          `json:"workflow"`
		RefreshRequired bool                    `json:"refreshRequired"`
	}
	read := func() cancelEnvelope {
		t.Helper()
		var out cancelEnvelope
		if err := json.Unmarshal(client.call("operation_cancel", command, false), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	original := read()
	if original.Result.State != "CANCELED" || original.Result.BackendDisposition != "NO_BACKEND" || original.Result.RunID != "" || original.Result.OwnerID != predecessor {
		t.Fatalf("original cancellation: %+v", original.Result)
	}
	if original.RefreshRequired || original.Context["executionId"] != predecessor || original.Workflow["nextAction"].(map[string]any)["code"] != wp.NoRunSuccessorAction {
		t.Fatalf("matching no-Run guidance unavailable: %+v", original)
	}
	in := wp.PrepareSuccessorExecution{RequestID: "explicit-successor-after-cancellation", ProjectID: f.project, TaskID: f.task, PredecessorExecutionID: predecessor, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: predecessor, ExpectedDefinitionRevisionID: receipt.Archive.RevisionID, ActorAuthority: f.session}
	in.PayloadSHA256 = c.ValueHash(in)
	wire := mcpObject(t, in)
	delete(wire, "actorAuthority")
	wire["aiSessionId"] = f.session
	var successor struct {
		Result wp.SuccessorCommandReceipt `json:"result"`
	}
	if err = json.Unmarshal(client.call("successor_execution_prepare", wire, false), &successor); err != nil || successor.Result.SuccessorExecutionID == "" || successor.Result.SuccessorExecutionID == predecessor {
		t.Fatalf("explicit successor: %+v %v", successor, err)
	}
	// Startup reconciliation may prepare authorized successors. Keep that separate
	// process boundary closed for this response-only test, including cold reopen.
	if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER cancel_envelope_no_runtime BEFORE INSERT ON runs WHEN NEW.purpose='IMPLEMENTATION' BEGIN SELECT RAISE(ABORT,'response-only fixture stops before Run'); END`); err != nil {
		t.Fatal(err)
	}
	tables := []string{"tasks", "execution_proposals", "execution_cancellations", "formal_execution_receipts", "runtime_scope_plans", "runtime_preparation_authorities", "runtime_selection_sources", "runtime_selection_bindings", "no_run_successor_command_receipts", "no_run_successor_command_identities", "no_run_successor_effect_authorities", "no_run_successor_eligibilities", "runs"}
	for _, cold := range []bool{false, true} {
		if cold {
			client.close()
			reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
			f.plane.Retirement.Close()
			f.plane.Coordinator.Codex = preparationOnlyCodexFactory{}
			client = newApprovedStartLocalClient(t, f)
		}
		before := definitionFormalSnapshot(t, f, tables)
		readOperation := func(id string) map[string]any {
			t.Helper()
			var out map[string]any
			if err := json.Unmarshal(client.call("operation_get", map[string]any{"aiSessionId": f.session, "target": map[string]any{"kind": "EXECUTION", "operationId": id, "ownerTaskId": f.task}, "include": []string{"RESULT"}}, false), &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		historical := readOperation(predecessor)
		t.Logf("cold=%v historical operation_get: %s", cold, mustAuditJSON(historical))
		w := historical["workflow"].(map[string]any)
		if next, ok := w["nextAction"].(map[string]any); ok && (next["code"] != nil && next["code"] != "" || next["required"] == true) {
			t.Errorf("cold=%v historical canceled predecessor advertised successor action: %+v", cold, next)
		}
		if w["refreshRequired"] != true || w["nextStep"] != nil || w["verificationDecisions"] != nil || len(w["capabilities"].([]any)) != 0 || len(w["pendingDecisions"].([]any)) != 0 {
			t.Errorf("cold=%v historical workflow not isolated: %+v", cold, w)
		}
		op := historical["result"].(map[string]any)["operation"].(map[string]any)
		if op["operationId"] != predecessor || op["status"] != "CANCELED" {
			t.Errorf("history rewritten: %+v", op)
		}
		currentResponse := readOperation(successor.Result.SuccessorExecutionID)
		cw := currentResponse["workflow"].(map[string]any)
		if cw["refreshRequired"] == true || cw["nextAction"].(map[string]any)["code"] != "WAIT_FOR_IMPLEMENTATION_PREPARATION" {
			t.Errorf("matching current operation lost guidance: %+v", cw)
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "exact historical/current operation reads")
		current, e := f.facade.GetTask(f.ctx, f.task)
		if e != nil || current.CurrentExecutionID != successor.Result.SuccessorExecutionID {
			t.Fatalf("replay changed successor pointer: %+v %v", current, e)
		}
		var runs int
		if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id IN (?,?)`, predecessor, successor.Result.SuccessorExecutionID).Scan(&runs); e != nil || runs != 0 {
			t.Fatalf("response-only replay acquired runtime work: %d %v", runs, e)
		}
	}
}

func mustAuditJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
