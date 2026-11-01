package controlapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Real Local MCP HTTP, Core, SQLite and disposable Git repositories; only the
// explicit TEST_ONLY_PROTOCOL_DEPENDENCY inventory replaces native measurement.
// No native executable or model is launched by this test.
func TestApprovedTaskStartMissingQualificationIsRecoverableThroughLocalMCP(t *testing.T) {
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	f.plane.Retirement.Close()
	installProtocolFixturePlatform(t, f)
	f.plane.Coordinator.Codex = preparationOnlyCodexFactory{}
	// Adding the backend after fixture qualification leaves a genuinely absent
	// mapping, rather than manufacturing an error or removing immutable records.
	backend, err := f.plane.CodexConfigs.Create(f.ctx, codexconfig.CreateInput{Name: "Unqualified isolated backend", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, approve := prepareDefinitionFormalApproval(t, f, "unqualified-start-definition")
	if approved, e := f.facade.CommitConfirmation(f.ctx, approve); e != nil || approved.State != "COMPLETED" {
		t.Fatalf("definition approval: %+v %v", approved, e)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	proposal, options := definitionOnlyStart(t, f, current)
	runtime := differentDefinitionRuntime(t, options, codexruntime.NextRunRuntimeConfig{})
	var selected codexconfig.BackendSelectionInput
	for _, candidate := range options.BackendCandidates {
		if candidate.ConfigID == backend.ID {
			selected = candidate.Input()
		}
	}
	if selected.ConfigID == "" {
		t.Fatal("unqualified enabled backend absent from options")
	}
	if _, err = f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(proposal)); err != nil {
		t.Fatal(err)
	}
	input := controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: f.task, RequestID: "missing-qualification-start", ExpectedTarget: *proposal.Content.Execution.ExpectedTarget, RuntimeConfig: runtime, Backend: selected}
	wire := func(in controlapi.ApprovedTaskStartInput) map[string]any {
		out := mcpObject(t, in)
		delete(out["runtimeConfig"].(map[string]any), "serviceTier")
		return out
	}
	client := newApprovedStartLocalClient(t, f)
	before := definitionRuntimeSnapshot(t, f, false)
	failed, callErr := client.session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "execution_start", Arguments: wire(input)})
	if callErr != nil || failed == nil || !failed.IsError {
		t.Fatalf("missing qualification should be a business refusal: %+v %v", failed, callErr)
	}
	raw, err := json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), runtimeplatform.ErrQualificationMissing.Error()) {
		t.Fatalf("missing actionable qualification error: %s", raw)
	}
	if after := definitionRuntimeSnapshot(t, f, false); after != before {
		t.Fatalf("unqualified start changed business facts: %s", definitionRuntimeSnapshotDiff(t, f, before, after))
	}
	key := c.ValueHash([]string{"devbridge.approved-task-start.v1", input.AISessionID, input.RequestID})
	readOriginal := func() c.Intent {
		t.Helper()
		original, e := f.plane.Confirmations.ReplayPrepare(f.ctx, f.session, "approved_task_start_prepare_"+key, c.ValueHash(input))
		if e != nil {
			t.Fatal(e)
		}
		return original
	}
	original := readOriginal()
	if original.State != "REJECTED" || len(original.Steps) != 1 || original.Steps[0].State != "REJECTED" || original.Steps[0].Effect != "NOT_STARTED" || original.Steps[0].Code != runtimeplatform.ErrQualificationMissing.Error() {
		t.Errorf("proven no-effect qualification refusal became a blocking ambiguous action: state=%s steps=%+v", original.State, original.Steps)
	}
	// The public projection must not leave the Task gated by an unresumable
	// UNKNOWN action; do not rely solely on a private confirmation row assertion.
	assertUnblocked := func() {
		t.Helper()
		raw := client.call("workspace_get", map[string]any{"aiSessionId": f.session, "taskId": f.task, "include": []string{"TASK"}}, false)
		var envelope map[string]any
		if e := json.Unmarshal(raw, &envelope); e != nil {
			t.Fatal(e)
		}
		view := envelope["result"].(map[string]any)["taskView"].(map[string]any)
		if node, ok := view["actionNode"].(map[string]any); ok && node["blocking"] == true {
			t.Errorf("qualification refusal still blocks Task: %+v", node)
		}
		workflow := envelope["workflow"].(map[string]any)
		available := false
		for _, value := range workflow["capabilities"].([]any) {
			capability := value.(map[string]any)
			if capability["action"] == "START_EXECUTION" && capability["available"] == true {
				available = true
			}
		}
		if !available {
			t.Errorf("public workflow does not offer a fresh explicit start: %+v", workflow["nextAction"])
		}
	}
	assertUnblocked()
	client.close()
	reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
	f.plane.Retirement.Close()
	f.plane.Coordinator.Codex = preparationOnlyCodexFactory{}
	// Explicit test-only operator repair, never qualification by execution_start.
	installProtocolFixturePlatform(t, f)
	client = newApprovedStartLocalClient(t, f)
	before = definitionRuntimeSnapshot(t, f, false)
	client.call("execution_start", wire(input), true)
	if after := definitionRuntimeSnapshot(t, f, false); after != before {
		t.Fatal("qualification repair caused old rejected request to execute")
	}
	if replay := readOriginal(); c.ValueHash(replay) != c.ValueHash(original) {
		t.Errorf("cold replay reclassified original rejection: state=%s steps=%+v", replay.State, replay.Steps)
	}
	assertUnblocked()
	fresh := input
	fresh.RequestID = "new-explicit-start-after-qualification"
	started := decodeApprovedLocalStart(t, client.call("execution_start", wire(fresh), false))
	if !started.Handoff.Accepted() || started.Proposal.BackendSelection == nil || started.Proposal.BackendSelection.Input() != selected {
		t.Fatalf("new explicit start lost selection: %+v", started)
	}
	replay := decodeApprovedLocalStart(t, client.call("execution_start", wire(fresh), false))
	if replay.Proposal.ID != started.Proposal.ID || replay.Run.ID != started.Run.ID {
		t.Fatal("new request replay duplicated start")
	}
	var implementations int
	if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE purpose='IMPLEMENTATION'`).Scan(&implementations); e != nil || implementations != 1 {
		t.Fatalf("implementation count: %d %v", implementations, e)
	}
}

// A writer failure is not proof of a safe rejection, even if its message names
// the same qualification sentinel and the returned receipt has no IDs. Existing
// UNKNOWN requests must remain read-only after the apparent fault is removed.
func TestApprovedTaskStartQualificationLookalikeWriterFailureStaysUnknown(t *testing.T) {
	f, proposal, _, input := definitionAdoptionStartFixture(t)
	if _, err := f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(proposal)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER adoption_qualification_lookalike BEFORE INSERT ON formal_adoption_business BEGIN SELECT RAISE(ABORT,'RUNTIME_PLATFORM_QUALIFICATION_MISSING'); END`); err != nil {
		t.Fatal(err)
	}
	before := definitionRuntimeSnapshot(t, f, false)
	if result, err := f.facade.StartApprovedTaskExecution(f.ctx, input); err == nil || result.Proposal.ID != "" || result.Run.ID != "" {
		t.Fatalf("writer fault did not reach intended boundary: %+v %v", result, err)
	}
	key := c.ValueHash([]string{"devbridge.approved-task-start.v1", input.AISessionID, input.RequestID})
	original, err := f.plane.Confirmations.ReplayPrepare(f.ctx, f.session, "approved_task_start_prepare_"+key, c.ValueHash(input))
	if err != nil || original.State != "UNKNOWN" || len(original.Steps) != 1 || original.Steps[0].Effect != "UNKNOWN" {
		t.Fatalf("writer uncertainty became a definite rejection: %+v %v", original, err)
	}
	if after := definitionRuntimeSnapshot(t, f, false); after != before {
		t.Fatal("fault fixture unexpectedly retained business effects")
	}
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER adoption_qualification_lookalike`); err != nil {
		t.Fatal(err)
	}
	reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
	f.plane.Retirement.Close()
	f.plane.Coordinator.Codex = preparationOnlyCodexFactory{}
	installProtocolFixturePlatform(t, f)
	before = definitionRuntimeSnapshot(t, f, false)
	if result, e := f.facade.StartApprovedTaskExecution(f.ctx, input); e == nil || result.Proposal.ID != "" || result.Run.ID != "" {
		t.Fatalf("cold UNKNOWN replay executed: %+v %v", result, e)
	}
	if after := definitionRuntimeSnapshot(t, f, false); after != before {
		t.Fatal("cold UNKNOWN replay rewrote business facts")
	}
	fresh := input
	fresh.RequestID = "new-start-must-not-bypass-unknown"
	if _, e := f.facade.StartApprovedTaskExecution(f.ctx, fresh); e == nil || !strings.Contains(e.Error(), "TASK_ACTION_PENDING") {
		t.Fatalf("fresh request bypassed uncertain action: %v", e)
	}
}
