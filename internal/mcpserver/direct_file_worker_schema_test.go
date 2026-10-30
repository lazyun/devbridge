package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDirectFileWorkerClosedSchemaAndMutationIdentity(t *testing.T) {
	claim := resolveSchema(t, fileWorkerClaimSchema())
	input := map[string]any{"aiSessionId": "session", "featureAreaId": "area", "taskId": "task", "taskDefinitionRevisionId": "revision", "decisionId": "approval", "workflowId": "workflow", "protocolVersion": float64(2), "runtimeEpoch": "epoch", "grantGeneration": float64(1), "requestId": "delivery", "workerId": "label"}
	if err := claim.Validate(input); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"taskDefinitionRevisionId", "decisionId", "runtimeEpoch", "grantGeneration", "workerId"} {
		value := input[key]
		delete(input, key)
		if claim.Validate(input) == nil {
			t.Fatalf("missing %s accepted", key)
		}
		input[key] = value
	}
	input["protocolVersion"] = float64(1)
	if claim.Validate(input) == nil {
		t.Fatal("legacy grant enrolled")
	}
	input["protocolVersion"] = float64(2)
	input["callerScope"] = "forged"
	if claim.Validate(input) == nil {
		t.Fatal("client-controlled principal accepted")
	}
	write := resolveSchema(t, json.RawMessage(SurfaceFileWriteInputSchema))
	for _, action := range []string{"CREATE", "REPLACE", "DELETE", "MOVE"} {
		args := map[string]any{"action": action, "aiSessionId": "session", "featureAreaId": "area", "workflowId": "workflow", "protocolVersion": float64(2), "runtimeEpoch": "epoch", "grantGeneration": float64(1), "repositoryId": "repo", "memberId": "member", "memberVersion": float64(1), "requestId": "mutation", "path": "a", "expectedDigest": strings.Repeat("a", 64), "workerClaim": map[string]any{"requestId": "delivery", "workerId": "label"}}
		if action == "CREATE" || action == "REPLACE" {
			args["content"] = "unchanged bytes\n"
		}
		if action == "MOVE" {
			args["destination"] = "b"
		}
		if err := write.Validate(args); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		args["workerClaim"] = map[string]any{"workerId": "label"}
		if write.Validate(args) == nil {
			t.Fatalf("%s accepted incomplete claim", action)
		}
		delete(args, "workerClaim")
		if err := write.Validate(args); err != nil {
			t.Fatalf("legacy shape changed: %v", err)
		}
	}
}

func TestDirectFileWorkerEnvelopeNeverAdoptsCurrentWorkflow(t *testing.T) {
	for _, name := range []string{"file_worker_claim", "file_worker_get"} {
		service := &cancellationGuidanceSpy{}
		value := map[string]any{"state": "OUTCOME_UNKNOWN", "receipt": map[string]any{"workflowId": "old-workflow"}}
		before, _ := json.Marshal(value)
		out := &sdkmcp.CallToolResult{StructuredContent: value}
		_, err := surfaceBusinessResult(context.Background(), service, &sdkmcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(`{"aiSessionId":"session","featureAreaId":"area","taskId":"task","taskDefinitionRevisionId":"old-revision","decisionId":"old-approval","workflowId":"old-workflow","protocolVersion":2,"runtimeEpoch":"old-epoch","grantGeneration":1}`)}, out, nil)
		if err != nil {
			t.Fatal(err)
		}
		envelope := out.StructuredContent.(map[string]any)
		ctx := envelope["context"].(map[string]any)
		if service.scope.AISessionID != "" || ctx["workflowId"] != "old-workflow" || ctx["taskDefinitionRevisionId"] != "old-revision" || ctx["executionId"] != nil || len(envelope["workflow"].(map[string]any)) != 0 {
			t.Fatalf("historical receipt gained unrelated guidance: %+v", envelope)
		}
		after, _ := json.Marshal(envelope["result"])
		if !reflect.DeepEqual(before, after) {
			t.Fatal("receipt rewritten")
		}
	}
}
