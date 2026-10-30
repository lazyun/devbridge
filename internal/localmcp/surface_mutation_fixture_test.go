package localmcp_test

import "strings"

// Existing state-machine scenarios use these explicit test-only migration
// adapters. The production catalog has no aliases: every call below goes
// through the actual new schema and handler. Only command result wrappers are
// unpacked; immutable receipts, IDs, errors and payloads remain unchanged.
func surfaceMutationTestArgs(name string, args map[string]any) (string, map[string]any) {
	in := map[string]any{}
	for k, v := range args {
		in[k] = v
	}
	switch name {
	case "featureArea_create":
		name = "feature_area_create"
	case "featureArea_commit":
		name = "feature_area_submit"
	case "featureArea_activate", "featureArea_retire", "featureArea_decompose":
		in["action"] = strings.ToUpper(strings.TrimPrefix(name, "featureArea_"))
		if name == "featureArea_retire" {
			in["confirmed"] = true
		}
		name = "feature_area_lifecycle"
	case "review_approval_respond":
		in["kind"] = "REVIEW_APPROVAL"
		name = "decision_respond"
	case "task_acceptance_respond":
		in["kind"] = "TASK_ACCEPTANCE"
		name = "decision_respond"
	case "execution_cancel":
		name = "operation_cancel"
	case "codex_request_resolve":
		in["kind"] = "CODEX_REQUEST"
		name = "runtime_request_respond"
	case "file_create", "file_replace", "file_delete", "file_move":
		in["action"] = strings.ToUpper(strings.TrimPrefix(name, "file_"))
		name = "file_write"
	case "decision_respond":
		if _, exists := in["kind"]; !exists {
			decision, _ := in["decisionId"].(string)
			kind, subject, _ := strings.Cut(decision, ":")
			in["kind"] = kind
			if kind == "RUNTIME_APPROVAL" {
				name = "runtime_request_respond"
				in["approvalId"] = subject
				in["decision"] = in["option"]
				delete(in, "decisionId")
				delete(in, "option")
			}
		}
	}
	return name, in
}
func surfaceMutationTestResult(original string, envelope map[string]any) map[string]any {
	out, ok := envelope["result"].(map[string]any)
	if !ok || original == "workspace_get" || original == "operation_get" || original == "work_list" {
		return envelope
	}
	if workflow, ok := envelope["workflow"].(map[string]any); ok {
		out["workflowGuidance"] = workflow
		for _, key := range []string{"currentState", "nextAction"} {
			if value, exists := workflow[key]; exists {
				out[key] = value
			}
		}
	}
	switch original {
	case "featureArea_activate", "featureArea_retire":
		if v, ok := out["featureArea"].(map[string]any); ok {
			return v
		}
	case "featureArea_decompose":
		if v, ok := out["decomposition"].(map[string]any); ok {
			return v
		}
	case "review_approval_respond":
		if v, ok := out["reviewApproval"].(map[string]any); ok {
			return v
		}
	case "task_acceptance_respond":
		if v, ok := out["taskAcceptance"].(map[string]any); ok {
			return v
		}
	}
	return out
}
