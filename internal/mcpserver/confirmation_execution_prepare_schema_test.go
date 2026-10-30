package mcpserver

import (
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

func executionPrepareSchemaRequest(action, mode string) map[string]any {
	body := map[string]any{"action": action, "taskId": "task", "featureAreaId": "area", "reviewId": "review", "executionId": "", "taskDefinitionRevisionId": "revision", "decisionId": "REVIEW_APPROVAL:review"}
	if action == "RECOVER_DIRECT_FILE" || action == "CANCEL_DIRECT_FILE" {
		body["workflowId"], body["runtimeEpoch"], body["grantGeneration"] = "workflow", "epoch", float64(2)
	}
	variant := map[string]any{
		"id": "implementation", "label": "Implementation", "summary": "Apply the exact reviewed plan",
		"implementationMode": mode,
		"workSpec":           map[string]any{"implementationMode": mode, "goal": "Update the reviewed files", "constraints": []any{"Only the exact two files"}, "codexInstruction": "Update only the two authorized files; preserve production behavior.", "executionScope": map[string]any{"kind": "PATHS", "paths": []any{"docs/ARCHITECTURE.md", "internal/core/dependency_guard_test.go"}}},
		"verificationSpec":   map[string]any{"mode": "RUN", "scope": "Inspect the exact two files", "checks": []any{map[string]any{"kind": "CUSTOM", "instruction": "Read and compare the authorized files", "required": true}}, "acceptanceCriteria": []any{"No unrelated changes"}, "codexInstruction": "Independently read and verify the two authorized files."},
	}
	return map[string]any{"aiSessionId": "session", "requestId": "prepare", "purpose": "EXECUTION", "action": action, "execution": body, "variants": []any{variant}}
}

func TestExecutionPrepareRejectsDecisionOnlyApproval(t *testing.T) {
	schema := resolveSchema(t, confirmationPrepareSchema(c.Execution))
	for _, action := range []string{"APPROVE", "APPROVE_AND_START", "CONFIRM_IMPLEMENTATION", "REQUEST_CHANGES", "START_APPROVED", "RETRY_IMPLEMENTATION"} {
		req := executionPrepareSchemaRequest(action, "DIRECT_FILE")
		req["variants"] = []any{map[string]any{"id": "approve", "label": "Approve", "summary": "Approve without a complete implementation plan"}}
		if err := schema.Validate(req); err == nil {
			t.Errorf("%s accepted a decision-only variant that Core rejects", action)
		}
	}
}

func TestExecutionPrepareImplementationModesAndDirectFileFences(t *testing.T) {
	schema := resolveSchema(t, confirmationPrepareSchema(c.Execution))
	for _, action := range []string{"APPROVE", "APPROVE_AND_START", "CONFIRM_IMPLEMENTATION", "REQUEST_CHANGES", "START_APPROVED", "RETRY_IMPLEMENTATION", "START_DIRECT_FILE", "RECOVER_DIRECT_FILE", "CANCEL_DIRECT_FILE"} {
		req := executionPrepareSchemaRequest(action, "DIRECT_FILE")
		if err := schema.Validate(req); err != nil {
			t.Errorf("%s rejected Core's complete Direct File variant: %v", action, err)
		}
		if action == "RECOVER_DIRECT_FILE" || action == "CANCEL_DIRECT_FILE" {
			delete(req["execution"].(map[string]any), "grantGeneration")
			if err := schema.Validate(req); err == nil {
				t.Errorf("%s accepted missing recovery fence", action)
			}
		}
		if action == "START_DIRECT_FILE" {
			req["execution"].(map[string]any)["workflowId"] = "stale-workflow"
			if err := schema.Validate(req); err == nil {
				t.Error("initial Direct File start accepted an active workflow fence")
			}
		}
	}
	for _, mode := range []string{"DIRECT_FILE", "CODING_AGENT"} {
		req := executionPrepareSchemaRequest("APPROVE", mode)
		if err := schema.Validate(req); err != nil {
			t.Fatal(err)
		}
		req["execution"].(map[string]any)["implementationMode"] = mode
		if err := schema.Validate(req); err == nil {
			t.Fatal("misplaced execution.implementationMode was accepted")
		}
		delete(req["execution"].(map[string]any), "implementationMode")
		variant := req["variants"].([]any)[0].(map[string]any)
		variant["workSpec"].(map[string]any)["implementationMode"] = map[string]string{"DIRECT_FILE": "CODING_AGENT", "CODING_AGENT": "DIRECT_FILE"}[mode]
		if err := schema.Validate(req); err == nil {
			t.Fatal("variant and workSpec modes disagree")
		}
	}
}

func TestExecutionPrepareAcceptanceRemainsDecisionOnly(t *testing.T) {
	schema := resolveSchema(t, confirmationPrepareSchema(c.Execution))
	for _, action := range []string{"ACCEPT", "REJECT", "REQUEST_REVERIFICATION"} {
		req := executionPrepareSchemaRequest(action, "DIRECT_FILE")
		if err := schema.Validate(req); err == nil {
			t.Fatalf("%s accepted a replacement implementation", action)
		}
		req["variants"] = []any{map[string]any{"id": "decision", "label": "Decision", "summary": "Decide only the existing exact execution"}}
		if err := schema.Validate(req); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExecutionPrepareUnreviewedSingleConfirmation(t *testing.T) {
	schema := resolveSchema(t, confirmationPrepareSchema(c.Execution))
	request := executionPrepareSchemaRequest("CONFIRM_AND_START_IMPLEMENTATION", "CODING_AGENT")
	body := request["execution"].(map[string]any)
	body["reviewId"], body["decisionId"], body["executionId"] = "", "", ""
	if err := schema.Validate(request); err != nil {
		t.Fatal(err)
	}
	request["variants"] = []any{map[string]any{"id": "start", "summary": "No plan"}}
	if err := schema.Validate(request); err == nil {
		t.Fatal("combined confirmation accepted no plan")
	}
	request = executionPrepareSchemaRequest("CONFIRM_AND_START_IMPLEMENTATION", "DIRECT_FILE")
	body = request["execution"].(map[string]any)
	body["reviewId"], body["decisionId"], body["executionId"] = "", "", ""
	if err := schema.Validate(request); err == nil {
		t.Fatal("combined confirmation accepted unsupported file implementation")
	}
}

func TestExecutionPrepareTaskActionGenerationIsTopLevel(t *testing.T) {
	schema := resolveSchema(t, confirmationPrepareSchema(c.Execution))
	for _, action := range []string{"APPROVE", "REQUEST_CHANGES", "CONTINUE_TASK", "REQUEST_REVERIFICATION"} {
		t.Run(action, func(t *testing.T) {
			req := executionPrepareSchemaRequest(action, "CODING_AGENT")
			if action == "CONTINUE_TASK" || action == "REQUEST_REVERIFICATION" {
				req["variants"] = []any{map[string]any{"id": "decision", "label": "Decision", "summary": "Preserve the original Task"}}
			}
			req["taskActionGeneration"] = float64(23)
			if err := schema.Validate(req); err != nil {
				t.Fatalf("top-level generation rejected: %v", err)
			}
			for _, invalid := range []any{float64(0), float64(-1), float64(1.5), "23"} {
				req["taskActionGeneration"] = invalid
				if err := schema.Validate(req); err == nil {
					t.Fatalf("invalid generation accepted: %v", invalid)
				}
			}
			delete(req, "taskActionGeneration")
			req["execution"].(map[string]any)["taskActionGeneration"] = float64(23)
			if err := schema.Validate(req); err == nil {
				t.Fatal("misplaced execution.taskActionGeneration accepted")
			}
		})
	}
}
