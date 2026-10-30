package controlapi_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func fileWorkerHTTPFixture(t *testing.T) (*upperLifecycleFixture, task.Task, directfile.State, directfile.WorkerClaimInput) {
	t.Helper()
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	current := approveDirectOnFixture(t, f, "http-file-worker")
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("start: %+v %v", started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || !state.Enabled || state.ProtocolVersion != 2 {
		t.Fatalf("authority: %+v %v", state, err)
	}
	in := directfile.WorkerClaimInput{WorkerTarget: directfile.WorkerTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: state.DecisionID, WorkflowID: state.WorkflowID, ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, WorkerClaimIdentity: directfile.WorkerClaimIdentity{RequestID: "http-file-delivery", WorkerID: "external-assistant-label"}}
	return f, current, state, in
}

func TestDirectFileWorkerHTTPRealMultiMemberImplementation(t *testing.T) {
	f, current, state, in := fileWorkerHTTPFixture(t)
	ctx, session, _ := workerHTTPClient(t, f)
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*sdkmcp.Tool{}
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"file_worker_claim", "file_worker_get", "file_write", "file_implementation_result_submit"} {
		if tools[name] == nil {
			t.Fatalf("missing actual HTTP tool %s", name)
		}
	}
	bad := mcpObject(t, in)
	bad["callerScope"] = "forged-principal"
	wantFileWorkerMCPError(t, ctx, session, "file_worker_claim", bad)
	claim := callUpperMCP(t, ctx, session, "file_worker_claim", mcpObject(t, in))
	validateFileWorkerHTTPOutput(t, tools["file_worker_claim"], claim)
	first := claim["result"].(map[string]any)
	payload := first["executionPayload"].(map[string]any)
	if first["state"] != directfile.WorkerDispatchIntent || payload["instruction"] != current.WorkSpec.CodexInstruction || payload["verification"].(map[string]any)["mode"] != string(task.VerificationModeNone) {
		t.Fatalf("altered frozen delivery: %+v", first)
	}
	definition := payload["definition"].(map[string]any)
	work := definition["workSpec"].(map[string]any)
	if work["codexInstruction"] != current.WorkSpec.CodexInstruction || work["goal"] != current.WorkSpec.Goal {
		t.Fatalf("missing frozen engineering context: %+v", definition)
	}
	for _, name := range []string{"file_worker_claim", "file_worker_get"} {
		got := callUpperMCP(t, ctx, session, name, mcpObject(t, in))["result"].(map[string]any)
		if got["state"] != directfile.WorkerOutcomeUnknown || got["executionPayload"] != nil || !reflect.DeepEqual(got["receipt"], first["receipt"]) {
			t.Fatalf("%s redispatched: %+v", name, got)
		}
	}
	for _, target := range current.RepositoryTargets {
		args := mcpObject(t, directInput(f, state, target))
		before := callUpperMCP(t, ctx, session, "file_read", args)["result"].(map[string]any)
		args["action"], args["protocolVersion"] = "REPLACE", float64(2)
		args["requestId"], args["expectedDigest"] = "http-write-"+target.RepositoryID, before["digest"]
		args["content"] = "actual worker file bytes for " + target.RepositoryID + "\n"
		wantFileWorkerMCPError(t, ctx, session, "file_write", args)
		args["workerClaim"] = map[string]any{"requestId": in.RequestID, "workerId": "wrong-label"}
		wantFileWorkerMCPError(t, ctx, session, "file_write", args)
		args["workerClaim"] = mcpObject(t, in.WorkerClaimIdentity)
		written := callUpperMCP(t, ctx, session, "file_write", args)["result"]
		replay := callUpperMCP(t, ctx, session, "file_write", args)["result"]
		if !reflect.DeepEqual(written, replay) {
			t.Fatal("file mutation replay changed durable result")
		}
	}
	resultInput := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "http-file-result", Summary: "Changed both approved member files through Core", VerificationNotes: "Fixture has an explicit user verification waiver; no command execution claimed", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}
	wantFileWorkerMCPError(t, ctx, session, "file_implementation_result_submit", mcpObject(t, resultInput))
	resultInput.WorkerClaim = &in.WorkerClaimIdentity
	result := callUpperMCP(t, ctx, session, "file_implementation_result_submit", mcpObject(t, resultInput))["result"].(map[string]any)
	if result["accepted"] != false || result["verificationStatus"] != "NOT_RUN" || result["executionId"] != nil || len(result["members"].([]any)) != 2 {
		t.Fatalf("file report invented execution/acceptance: %+v", result)
	}
	readback := callUpperMCP(t, ctx, session, "file_worker_get", mcpObject(t, in))
	validateFileWorkerHTTPOutput(t, tools["file_worker_get"], readback)
	recorded := readback["result"].(map[string]any)
	if recorded["state"] != directfile.WorkerResultRecorded || recorded["executionPayload"] != nil || recorded["resultReceipt"].(map[string]any)["id"] != result["id"] {
		t.Fatalf("worker completion not linked to result: %+v", recorded)
	}
	if readback["context"].(map[string]any)["workflowId"] != state.WorkflowID || len(readback["workflow"].(map[string]any)) != 0 {
		t.Fatalf("receipt acquired unrelated workflow actions: %+v", readback)
	}
	var implementations, executions, acceptances int
	for query, out := range map[string]*int{"SELECT count(*) FROM runs WHERE purpose='IMPLEMENTATION'": &implementations, "SELECT count(*) FROM execution_proposals": &executions, "SELECT count(*) FROM direct_file_implementation_acceptances": &acceptances} {
		if err := f.plane.DB.SQL().QueryRow(query).Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	if implementations != 0 || executions != 0 || acceptances != 0 {
		t.Fatalf("invented lifecycle effects: %d %d %d", implementations, executions, acceptances)
	}
	owner, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || owner.Status != task.StatusActive || owner.Phase != task.PhaseAcceptancePending {
		t.Fatalf("report accepted task: %+v %v", owner, err)
	}
}

func TestDirectFileWorkerHTTPRestartRequiresExplicitCancellation(t *testing.T) {
	f, current, state, in := fileWorkerHTTPFixture(t)
	ctx, session, closeHTTP := workerHTTPClient(t, f)
	first := callUpperMCP(t, ctx, session, "file_worker_claim", mcpObject(t, in))["result"].(map[string]any)
	closeHTTP()
	reopenDirectDeliveryFixture(t, f)
	ctx, session, _ = workerHTTPClient(t, f)
	got := callUpperMCP(t, ctx, session, "file_worker_get", mcpObject(t, in))["result"].(map[string]any)
	if got["state"] != directfile.WorkerOutcomeUnknown || got["executionPayload"] != nil || !reflect.DeepEqual(got["receipt"], first["receipt"]) {
		t.Fatalf("restart redispatched: %+v", got)
	}
	workspace := callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
	workflow := workspace["workflow"].(map[string]any)
	if action, ok := workflow["nextAction"].(map[string]any); ok && (action["code"] == "RECOVER" || action["code"] == "RECOVER_DIRECT_FILE") {
		t.Fatalf("advertised unsupported worker continuation: %+v", workspace)
	}
	if recovered, err := directConfirmation(t, f, current, "RECOVER_DIRECT_FILE", state); err == nil && recovered.State == "COMPLETED" {
		t.Fatal("unknown delivery recovered into another usable generation")
	}
	var epoch string
	var generation int64
	if err := f.plane.DB.SQL().QueryRow(`SELECT runtime_epoch,generation FROM direct_file_task_authorizations WHERE workflow_id=?`, state.WorkflowID).Scan(&epoch, &generation); err != nil {
		t.Fatal(err)
	}
	if epoch != state.RuntimeEpoch || generation != state.GrantGeneration {
		t.Fatal("read/recovery refusal refreshed authority")
	}
	canceled, err := directConfirmation(t, f, current, "CANCEL_DIRECT_FILE", state)
	if err != nil || canceled.State != "COMPLETED" {
		t.Fatalf("existing cancellation unavailable: %+v %v", canceled, err)
	}
	get := callUpperMCP(t, ctx, session, "file_worker_get", mcpObject(t, in))["result"].(map[string]any)
	if get["state"] != directfile.WorkerOutcomeUnknown || get["executionPayload"] != nil {
		t.Fatalf("cancellation fabricated external worker termination: %+v", get)
	}
}

func wantFileWorkerMCPError(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, name string, args map[string]any) {
	t.Helper()
	if result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args}); err == nil && !result.IsError {
		t.Fatalf("%s unexpectedly succeeded: %+v", name, result)
	}
}

func validateFileWorkerHTTPOutput(t *testing.T, tool *sdkmcp.Tool, output map[string]any) {
	t.Helper()
	raw, err := json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = resolved.Validate(output); err != nil {
		t.Fatalf("actual %s HTTP output violates discovered schema: %v", tool.Name, err)
	}
}
