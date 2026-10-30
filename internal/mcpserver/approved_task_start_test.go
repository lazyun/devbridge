package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func approvedStartSchemaInput() map[string]any {
	return map[string]any{
		"aiSessionId": "session", "taskId": "task", "requestId": "start-request",
		"expectedTarget": map[string]any{"featureAreaId": "area", "taskDefinitionRevisionId": "revision", "executionId": "", "phase": "IMPLEMENTATION_READY", "primaryMemberId": "member", "approvedWorksetHash": strings.Repeat("a", 64)},
		"runtimeConfig":  map[string]any{"model": "model", "reasoningEffort": "high", "codexFastMode": false},
		"backend":        map[string]any{"configId": "backend", "configFingerprint": strings.Repeat("b", 64)},
	}
}

func TestApprovedTaskStartSchemaClosesSelectionBranch(t *testing.T) {
	schema := resolveSchema(t, json.RawMessage(surfaceExecutionStartInputSchema))
	if err := schema.Validate(approvedStartSchemaInput()); err != nil {
		t.Fatalf("complete explicit start: %v", err)
	}
	for _, field := range []string{"requestId", "expectedTarget", "runtimeConfig", "backend"} {
		t.Run("missing_"+field, func(t *testing.T) {
			v := approvedStartSchemaInput()
			delete(v, field)
			if err := schema.Validate(v); err == nil {
				t.Fatalf("accepted missing %s", field)
			}
		})
	}
	for _, field := range []string{"featureAreaId", "taskDefinitionRevisionId", "executionId", "phase", "primaryMemberId", "approvedWorksetHash"} {
		t.Run("target_missing_"+field, func(t *testing.T) {
			v := approvedStartSchemaInput()
			delete(v["expectedTarget"].(map[string]any), field)
			if err := schema.Validate(v); err == nil {
				t.Fatalf("accepted missing target %s", field)
			}
		})
	}
	for _, field := range []string{"runtimeConfig", "backend", "expectedTarget"} {
		t.Run("null_"+field, func(t *testing.T) {
			v := approvedStartSchemaInput()
			v[field] = nil
			if err := schema.Validate(v); err == nil {
				t.Fatalf("accepted null %s", field)
			}
		})
	}
	for _, object := range []string{"root", "expectedTarget", "runtimeConfig", "backend"} {
		t.Run("unknown_"+object, func(t *testing.T) {
			v := approvedStartSchemaInput()
			target := v
			if object != "root" {
				target = v[object].(map[string]any)
			}
			target["platformQualificationId"] = "caller-authority"
			if err := schema.Validate(v); err == nil {
				t.Fatalf("accepted unknown %s field", object)
			}
		})
	}
	t.Run("verification_selection", func(t *testing.T) {
		v := approvedStartSchemaInput()
		v["expectedTarget"].(map[string]any)["phase"] = "VERIFICATION_READY"
		v["expectedTarget"].(map[string]any)["executionId"] = "existing"
		if err := schema.Validate(v); err == nil {
			t.Fatal("accepted new selection for frozen Verification")
		}
	})
	t.Run("instruction_mix", func(t *testing.T) {
		v := approvedStartSchemaInput()
		v["executionInstructions"] = map[string]any{}
		if err := schema.Validate(v); err == nil {
			t.Fatal("accepted instructions with selection")
		}
	})
	for _, input := range []map[string]any{
		{"aiSessionId": "session", "taskId": "task"},
		{"aiSessionId": "session", "taskId": "task", "expectedTarget": map[string]any{"featureAreaId": "area", "taskDefinitionRevisionId": "revision", "executionId": "existing", "phase": "VERIFICATION_READY", "primaryMemberId": "member", "approvedWorksetHash": strings.Repeat("a", 64)}},
	} {
		if err := schema.Validate(input); err != nil {
			t.Fatalf("historical start rejected: %v", err)
		}
	}
}

// A transport contract probe: the production GPT registration must forward the
// exact v2 target to Core while retaining the old four-field v1 shape.
type verificationTargetProbe struct {
	*surfaceCommandSpy
	target wp.TaskExecutionExpectation
}

func (s *verificationTargetProbe) StartTaskExecutionExpected(_ context.Context, _ string, _ string, target wp.TaskExecutionExpectation) (executionproposal.StartResult, error) {
	s.target = target
	return executionproposal.StartResult{}, nil
}
func TestGPTVerificationContinueForwardsCompleteV2Target(t *testing.T) {
	ctx := context.Background()
	service := &verificationTargetProbe{surfaceCommandSpy: &surfaceCommandSpy{}}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "verification-contract", Version: "1"}, nil)
	RegisterSurfaceBusinessTools(server, service)
	registerChatGPTDecisionContinuation(server, service)
	client := connectPresentationTest(t, ctx, server)
	listed, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "execution_start" {
			t.Fatal("GPT exposed Local start selection command")
		}
	}
	target := wp.TaskExecutionExpectation{FeatureAreaID: "area", TaskDefinitionRevisionID: "revision", ExecutionID: "execution", Phase: "VERIFICATION_READY", PrimaryMemberID: "member", ApprovedWorksetHash: strings.Repeat("a", 64)}
	input := map[string]any{"aiSessionId": "session", "taskId": "task", "expectedTarget": target}
	result, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "verification_continue", Arguments: input})
	if err != nil || result.IsError || !reflect.DeepEqual(service.target, target) {
		t.Fatalf("v2 target lost at GPT boundary: result=%+v target=%+v error=%v", result, service.target, err)
	}
	input["runtimeConfig"] = approvedStartSchemaInput()["runtimeConfig"]
	result, err = client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "verification_continue", Arguments: input})
	if err == nil && !result.IsError {
		t.Fatal("GPT Verification accepted new runtime selection")
	}
	delete(input, "runtimeConfig")
	target.PrimaryMemberID = ""
	target.ApprovedWorksetHash = ""
	input["expectedTarget"] = target
	result, err = client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "verification_continue", Arguments: input})
	if err != nil || result.IsError || !reflect.DeepEqual(service.target, target) {
		t.Fatalf("legacy Verification shape rejected: %+v %v", result, err)
	}
}

func TestConfirmationOptionsSelectionModeMountGate(t *testing.T) {
	options := map[string]any{"eligibility": map[string]any{"available": true, "reprepareAllowed": false, "attemptState": "NOT_SUBMITTED", "receiptState": "NOT_OBSERVED"}, "runtimeInventorySource": "CORE", "runtimeInventoryRevision": float64(1), "projects": []any{}, "repositories": []any{}, "branches": []any{}, "variants": []any{}, "runtimeOptions": []any{}, "backendInventoryComplete": true}
	if code, path := validateOptionsAbsolute(options); code != "" {
		t.Fatalf("baseline options rejected: %s %s", code, path)
	}
	for _, mode := range []any{"FROZEN_EXECUTION", "EXPLICIT_SELECTION", nil, "", "INHERIT_REVIEW", true} {
		options["runtimeSelectionMode"] = mode
		code, path := validateOptionsAbsolute(options)
		if mode == "FROZEN_EXECUTION" || mode == "EXPLICIT_SELECTION" {
			if code != "" {
				t.Fatalf("valid mode %v rejected: %s %s", mode, code, path)
			}
		} else if code != "RUNTIME_SELECTION_MODE_INVALID" || path != "options.runtimeSelectionMode" {
			t.Fatalf("invalid mode %v accepted: %s %s", mode, code, path)
		}
	}
}

type approvedStartReceiptProbe struct {
	*surfaceCommandSpy
	receipt *executionproposal.StartReceiptIdentity
}

func (s *approvedStartReceiptProbe) StartApprovedTaskExecution(context.Context, controlapi.ApprovedTaskStartInput) (executionproposal.StartResult, error) {
	return executionproposal.StartResult{Receipt: s.receipt}, errors.New("original child read temporarily unavailable")
}
func TestApprovedTaskStartSurfacePreservesReceiptOnReadFailure(t *testing.T) {
	receipt := &executionproposal.StartReceiptIdentity{ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", OriginalExecutionID: "original-execution", OriginalRunID: "original-run"}
	service := &approvedStartReceiptProbe{surfaceCommandSpy: &surfaceCommandSpy{}, receipt: receipt}
	session, _ := surfaceTestSession(t, service)
	result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "execution_start", Arguments: approvedStartSchemaInput()})
	if err != nil || !result.IsError {
		t.Fatalf("read failure became success or transport-only error: %+v %v", result, err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var envelope struct {
		Result SurfaceExecutionStartOutput `json:"result"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	out := envelope.Result
	if !out.ReservationCommitted || !out.RefreshRequired || out.DispatchError == "" || !reflect.DeepEqual(out.Receipt, receipt) {
		t.Fatalf("Surface discarded original receipt: %s", raw)
	}
	if out.Proposal.ID != "" || out.Run.ID != "" {
		t.Fatal("Surface fabricated complete child objects")
	}
}
