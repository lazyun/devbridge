package controlapi_test

import (
	"encoding/json"
	"reflect"
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
func TestSuccessorPreparationReplayKeepsOriginalSuccessorAfterThirdGeneration(t *testing.T) {
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
	if original.Result.State != "CANCELED" || original.Result.RunID != "" {
		t.Fatalf("first cancel: %+v", original)
	}
	// This test intentionally stops before native/runtime dispatch, including restart.
	if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER successor_envelope_no_runtime BEFORE INSERT ON runs WHEN NEW.purpose='IMPLEMENTATION' BEGIN SELECT RAISE(ABORT,'response-only fixture stops before Run'); END`); err != nil {
		t.Fatal(err)
	}
	type successorEnvelope struct {
		Result          wp.SuccessorCommandReceipt `json:"result"`
		Context         map[string]any             `json:"context"`
		Workflow        map[string]any             `json:"workflow"`
		RefreshRequired bool                       `json:"refreshRequired"`
	}
	prepare := func(predecessor, request string) (map[string]any, successorEnvelope) {
		t.Helper()
		in := wp.PrepareSuccessorExecution{RequestID: request, ProjectID: f.project, TaskID: f.task, PredecessorExecutionID: predecessor, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: predecessor, ExpectedDefinitionRevisionID: receipt.Archive.RevisionID, ActorAuthority: f.session}
		in.PayloadSHA256 = c.ValueHash(in)
		wire := mcpObject(t, in)
		delete(wire, "actorAuthority")
		wire["aiSessionId"] = f.session
		var out successorEnvelope
		if err := json.Unmarshal(client.call("successor_execution_prepare", wire, false), &out); err != nil {
			t.Fatal(err)
		}
		if out.Result.SuccessorExecutionID == "" || out.Result.SuccessorExecutionID == predecessor {
			t.Fatalf("successor: %+v", out)
		}
		return wire, out
	}
	firstWire, first := prepare(predecessor, "first-successor-envelope")
	if first.Context["executionId"] != first.Result.SuccessorExecutionID || first.RefreshRequired {
		t.Fatalf("initial successor context: %+v", first)
	}
	client.call("operation_cancel", map[string]any{"aiSessionId": f.session, "ownerType": "EXECUTION", "ownerId": first.Result.SuccessorExecutionID, "requestId": "cancel-second-generation"}, false)
	_, third := prepare(first.Result.SuccessorExecutionID, "second-successor-envelope")
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
		var replay successorEnvelope
		if err := json.Unmarshal(client.call("successor_execution_prepare", firstWire, false), &replay); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(replay.Result, first.Result) {
			t.Errorf("cold=%v original preparation receipt changed: first=%+v replay=%+v", cold, first.Result, replay.Result)
		}
		if replay.Context["executionId"] != first.Result.SuccessorExecutionID || replay.Context["runId"] != nil || replay.Context["decisionId"] != nil {
			t.Errorf("cold=%v original successor preparation borrowed third-generation identity: first=%s third=%s context=%+v", cold, first.Result.SuccessorExecutionID, third.Result.SuccessorExecutionID, replay.Context)
		}
		if !replay.RefreshRequired || replay.Workflow["stale"] != true || replay.Workflow["nextAction"] != nil || replay.Workflow["pendingDecisions"] != nil {
			t.Errorf("cold=%v old successor preparation inherited fresh third-generation actions: %+v", cold, replay.Workflow)
		}
		var changed wp.PrepareSuccessorExecution
		raw, err := json.Marshal(firstWire)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &changed); err != nil {
			t.Fatal(err)
		}
		changed.ActorAuthority = f.session
		changed.ExpectedCurrentExecutionID = third.Result.SuccessorExecutionID
		changed.PayloadSHA256 = ""
		changed.PayloadSHA256 = c.ValueHash(changed)
		changedWire := mcpObject(t, changed)
		delete(changedWire, "actorAuthority")
		changedWire["aiSessionId"] = f.session
		client.call("successor_execution_prepare", changedWire, true)
		assertDefinitionFormalSnapshot(t, f, tables, before, "old successor preparation replay and changed-payload refusal")
		current, e := f.facade.GetTask(f.ctx, f.task)
		if e != nil || current.CurrentExecutionID != third.Result.SuccessorExecutionID {
			t.Fatalf("replay changed current pointer: %+v %v", current, e)
		}
		var runs int
		if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id IN (?,?,?)`, predecessor, first.Result.SuccessorExecutionID, third.Result.SuccessorExecutionID).Scan(&runs); e != nil || runs != 0 {
			t.Fatalf("response-only preparation replay acquired a Run: %d %v", runs, e)
		}
	}
}
