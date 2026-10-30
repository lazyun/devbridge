package mcpserver

import (
	"context"
	"encoding/json"
	"strings"

	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Bootstrap contracts are complete before Project workflow enrichment. The
// binding getter owns its Project-only envelope and must never read capabilities.
func isBootstrapTool(name string) bool {
	return name == "bridge_diagnostics" || name == "session_initialize" || name == "project_open" || name == "session_binding_get" || name == "project_selection_show" || name == "panel_show" || name == "dev_bridge_panel_get"
}

func isSurfaceBusinessTool(name string) bool {
	if isBootstrapTool(name) {
		return false
	}
	for _, n := range SurfaceBusinessToolNames {
		if n == name {
			return true
		}
	}
	return false
}
func surfaceOwnsEnvelope(name string) bool {
	return name == "workspace_get" || name == "work_list" || name == "operation_get"
}

// The transport envelope is created after the Core handler returned. Its
// refresh failure cannot erase a committed command result or its receipt.
func surfaceBusinessResult(ctx context.Context, service BridgeService, params *sdkmcp.CallToolParamsRaw, result sdkmcp.Result, err error) (sdkmcp.Result, error) {
	out, ok := result.(*sdkmcp.CallToolResult)
	if isBootstrapTool(params.Name) || !ok || out == nil || err != nil || (out.IsError && params.Name != "execution_start" && params.Name != "task_commit_reprepare" && params.Name != "feature_area_merge_reprepare") || surfaceOwnsEnvelope(params.Name) {
		return result, err
	}
	var input, value map[string]any
	if json.Unmarshal(params.Arguments, &input) != nil {
		return result, err
	}
	raw, e := json.Marshal(out.StructuredContent)
	if e != nil || json.Unmarshal(raw, &value) != nil || value == nil {
		return result, err
	}
	if out.IsError && value["reservationCommitted"] != true && !((params.Name == "task_commit_reprepare" || params.Name == "feature_area_merge_reprepare") && stringValue(value, "operationId") != "") {
		return result, err
	}
	session := stringValue(input, "aiSessionId")
	if session == "" {
		session = stringValue(value, "aiSessionId")
	}
	contextValue := map[string]any{}
	for _, k := range []string{"aiSessionId", "projectId", "featureAreaId", "taskId", "reviewId", "executionId", "taskDefinitionRevisionId", "runId", "decisionId"} {
		v := stringValue(input, k)
		if v == "" {
			v = stringValue(value, k)
		}
		if v != "" {
			contextValue[k] = v
		}
	}
	if session != "" {
		contextValue["aiSessionId"] = session
	}
	scope := wp.ResourceScope{AISessionID: session, FeatureAreaID: stringValue(contextValue, "featureAreaId"), TaskID: stringValue(contextValue, "taskId"), ReviewID: stringValue(contextValue, "reviewId"), ExecutionID: stringValue(contextValue, "executionId"), RunID: stringValue(contextValue, "runId"), OwnerType: stringValue(input, "ownerType"), OwnerID: stringValue(input, "ownerId")}
	if params.Name == "task_create" {
		scope.TaskID = stringValue(value, "id")
		contextValue["taskId"] = scope.TaskID
	}
	if params.Name == "feature_area_create" {
		scope.FeatureAreaID = stringValue(value, "id")
		contextValue["featureAreaId"] = scope.FeatureAreaID
		// CREATE_AND_REVIEW returns durable identities from the composition
		// receipt. Scope the post-action readback to the most specific object
		// actually created; never infer one from a later workspace selection.
		if startup, ok := value["startup"].(map[string]any); ok {
			for _, field := range []struct {
				key    string
				target *string
			}{
				{"taskId", &scope.TaskID}, {"reviewId", &scope.ReviewID}, {"runId", &scope.RunID},
			} {
				if id := stringValue(startup, field.key); id != "" {
					*field.target = id
					contextValue[field.key] = id
				}
			}
		}
	}
	if v, ok := value["review"].(map[string]any); ok {
		scope.ReviewID = stringValue(v, "id")
		if scope.ReviewID != "" {
			contextValue["reviewId"] = scope.ReviewID
		}
	}
	workflow := map[string]any{}
	hint := surfaceHint()
	refreshRequired, _ := value["refreshRequired"].(bool)
	// Branch/File/Document commands have their own transaction and retry
	// contracts. Their exact result needs no unrelated workflow readback.
	needsWorkflow := session != "" && (scope.FeatureAreaID != "" || scope.TaskID != "" || scope.ReviewID != "" || scope.ExecutionID != "" || scope.OwnerID != "")
	switch params.Name {
	case "direct_file_enable", "direct_file_disable", "file_list", "file_read", "file_write", "document_create", "repository_search", "development_branch_create", "development_branch_select":
		needsWorkflow = false
	}
	if needsWorkflow {
		if reader, available := service.(interface {
			WorkflowGuidanceForResource(context.Context, wp.ResourceScope) (wp.Guidance, error)
		}); available {
			g, readErr := reader.WorkflowGuidanceForResource(ctx, scope)
			if readErr != nil {
				refreshRequired = true
				workflow["readError"] = publicToolError(readErr).Error()
			} else {
				workflow = compactSurfaceGuidance(g)
				hint = surfaceHintForGuidance(SurfaceContext{AISessionID: session, ProjectID: g.ProjectID, FeatureAreaID: g.FeatureAreaID, TaskID: g.TaskID}, g)
				for k, v := range map[string]string{"projectId": g.ProjectID, "featureAreaId": g.FeatureAreaID, "taskId": g.TaskID, "reviewId": g.ReviewID, "executionId": g.ExecutionID, "taskDefinitionRevisionId": g.TaskDefinitionRevisionID, "runId": g.RunID, "decisionId": g.DecisionID} {
					if v != "" {
						contextValue[k] = v
					}
				}
				// A durable Task result previously marked unavailable becomes fresh only
				// when this exact resource read succeeds.
				refreshRequired = g.RefreshRequired || stringValue(value, "dispatchError") != ""
			}
		} else {
			refreshRequired = true
			workflow["readError"] = "Exact workflow readback unavailable"
		}
	}
	if params.Name == "feature_area_create" {
		if startup, ok := value["startup"].(map[string]any); ok {
			if required, _ := startup["refreshRequired"].(bool); required {
				refreshRequired = true
			}
		}
	}
	if refreshRequired {
		workflow["refreshRequired"] = true
		workflow["stale"] = true
		hint = surfaceHint()
		hint.Reason = "READ_UNAVAILABLE"
	}
	envelope := map[string]any{"result": value, "context": contextValue, "workflow": workflow, "presentationHint": hint}
	if refreshRequired {
		envelope["refreshRequired"] = true
	}
	out.StructuredContent = envelope
	// Replace the original Core text representation with the one envelope,
	// avoiding full proposal/guidance duplicates in model context.
	encoded, e := json.Marshal(envelope)
	if e == nil {
		out.Content = []sdkmcp.Content{&sdkmcp.TextContent{Text: string(encoded)}}
	}
	return out, nil
}

// Pending decisions require conversation handling; showing their read-only Card
// remains optional and does not change Core nextAction.required.
func surfaceHintForGuidance(ctx SurfaceContext, g wp.Guidance) SurfacePresentationHint {
	hint := surfaceHint()
	if g.RefreshRequired {
		hint.Reason = "READ_UNAVAILABLE"
		return hint
	}
	if g.Approval != nil && g.Approval.Status == "PENDING" && (ctx.TaskID == "" || g.Approval.TaskID == ctx.TaskID) {
		hint.Required, hint.Reason = false, "PENDING_USER_DECISION"
		hint.Arguments = &SurfacePresentationArgs{AISessionID: ctx.AISessionID, ProjectID: ctx.ProjectID, FeatureAreaID: ctx.FeatureAreaID, TaskID: g.Approval.TaskID, DecisionID: g.Approval.DecisionID}
		return hint
	}
	if strings.HasPrefix(g.DecisionID, "TASK_ACCEPTANCE:") && g.NextAction.Required && g.NextAction.Code == "RESOLVE_TASK_ACCEPTANCE" && (ctx.TaskID == "" || g.TaskID == ctx.TaskID) {
		hint.Required, hint.Reason = false, "PENDING_USER_DECISION"
		hint.Arguments = &SurfacePresentationArgs{AISessionID: ctx.AISessionID, ProjectID: ctx.ProjectID, FeatureAreaID: ctx.FeatureAreaID, TaskID: g.TaskID, DecisionID: g.DecisionID}
	}
	if g.RuntimeApprovalID != "" && g.NextAction.Required && g.NextAction.Code == "RESOLVE_RUNTIME_APPROVAL" && (ctx.TaskID == "" || g.TaskID == ctx.TaskID) {
		hint.Required, hint.Reason = false, "PENDING_RUNTIME_REQUEST"
		decisionID := g.RuntimeApprovalID
		if !strings.HasPrefix(decisionID, "RUNTIME_APPROVAL:") {
			decisionID = "RUNTIME_APPROVAL:" + decisionID
		}
		hint.Arguments = &SurfacePresentationArgs{AISessionID: ctx.AISessionID, ProjectID: ctx.ProjectID, FeatureAreaID: ctx.FeatureAreaID, TaskID: g.TaskID, DecisionID: decisionID}
	}
	return hint
}

func compactSurfaceGuidance(g wp.Guidance) map[string]any {
	out := map[string]any{"generation": g.Generation, "sourceSequence": g.SourceSequence, "currentState": g.CurrentState, "phase": g.TaskPhase, "nextAction": g.NextAction, "refreshRequired": g.RefreshRequired}
	if g.Approval != nil {
		out["pendingDecisions"] = []any{map[string]any{"kind": "REVIEW_APPROVAL", "decisionId": g.Approval.DecisionID, "reviewId": g.Approval.ReviewID, "taskId": g.Approval.TaskID, "allowedActions": g.Approval.AllowedActions}}
	}
	if g.DecisionID != "" && g.Approval == nil {
		out["pendingDecisions"] = []any{map[string]any{"decisionId": g.DecisionID, "taskId": g.TaskID, "executionId": g.ExecutionID, "taskDefinitionRevisionId": g.TaskDefinitionRevisionID}}
	}
	if g.RuntimeApprovalID != "" {
		out["runtimeApprovalId"] = g.RuntimeApprovalID
	}
	return out
}
func surfaceEnvelopeOutputSchema(original any) any {
	// Preserve the original exact result schema and its root definitions. The
	// envelope's workflow is deliberately compact and independent of result.
	contextProperties := map[string]any{}
	for _, key := range []string{"aiSessionId", "projectId", "featureAreaId", "taskId", "reviewId", "executionId", "taskDefinitionRevisionId", "runId", "decisionId"} {
		contextProperties[key] = map[string]any{"type": "string"}
	}
	workflowProperties := map[string]any{"generation": map[string]any{"type": "string"}, "sourceSequence": map[string]any{"type": "integer"}, "currentState": map[string]any{"type": "string"}, "phase": map[string]any{"type": "string"}, "nextAction": json.RawMessage(wp.ActionJSONSchema), "refreshRequired": map[string]any{"type": "boolean"}, "stale": map[string]any{"type": "boolean"}, "readError": map[string]any{"type": "string"}, "runtimeApprovalId": map[string]any{"type": "string"}}
	var originalWorkflow map[string]any
	_ = json.Unmarshal([]byte(externalWorkflowOutputSchema), &originalWorkflow)
	for _, key := range []string{"state", "capabilities", "pendingDecision", "blockers"} {
		workflowProperties[key] = originalWorkflow["properties"].(map[string]any)[key]
	}
	decisionProperties := map[string]any{}
	for _, key := range []string{"kind", "decisionId", "taskId", "reviewId", "executionId", "taskDefinitionRevisionId"} {
		decisionProperties[key] = map[string]any{"type": "string"}
	}
	decisionProperties["allowedActions"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	workflowProperties["pendingDecisions"] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": decisionProperties, "required": []string{"decisionId"}, "additionalProperties": false}}
	var hintSchema map[string]any
	if err := json.Unmarshal([]byte(surfacePresentationHintSchema), &hintSchema); err != nil {
		panic(err)
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"result": original, "context": map[string]any{"type": "object", "properties": contextProperties, "additionalProperties": false}, "workflow": map[string]any{"type": "object", "properties": workflowProperties, "additionalProperties": false}, "presentationHint": hintSchema, "refreshRequired": map[string]any{"type": "boolean"}}, "required": []string{"result", "context", "workflow", "presentationHint"}, "additionalProperties": false}
	if original == nil {
		schema["properties"].(map[string]any)["result"] = map[string]any{"type": "object"}
	}
	raw, _ := json.Marshal(original)
	var result map[string]any
	if json.Unmarshal(raw, &result) == nil {
		if defs, ok := result["$defs"]; ok {
			schema["$defs"] = defs
		}
	}
	return schema
}
