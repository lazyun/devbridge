package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type presentationFlowFixture struct {
	*exactPresentationService
	runtime []p.PendingDecision
}

func (s *presentationFlowFixture) CompactWorkspaceForSession(context.Context, string, string, string) (p.Projection, error) {
	return s.taskProjection, nil
}
func (s *presentationFlowFixture) DevelopmentWorkspaceForSession(context.Context, string, string, string) (p.WorkspaceProjection, error) {
	return p.WorkspaceProjection{SchemaVersion: "1", PresentationID: "workspace:session:project", RefreshedAt: time.Now().UTC(), DevelopmentContext: s.taskProjection.DevelopmentContext, SelectedFeatureAreaID: "area", SelectedTaskID: "task", Presentation: &s.taskProjection, PendingDecisions: append(projectSurfaceWorkflow(s.taskProjection).PendingDecisions, s.runtime...)}, nil
}

func TestPresentationFlowHintToActualShowWireContract(t *testing.T) {
	ctx := context.Background()
	svc := &presentationFlowFixture{exactPresentationService: exactPresentationFixture()}
	svc.taskProjection.CurrentState = "REVIEW_APPROVAL_PENDING"
	svc.taskProjection.PendingDecision = &p.Decision{ID: "REVIEW_APPROVAL:old-review", Kind: "REVIEW_APPROVAL", SubjectID: "task", Summary: "Review complete; user approval required."}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "flow-contract", Version: "1"}, nil)
	server.AddReceivingMiddleware(presentationResultMiddleware(svc))
	RegisterSurfaceBusinessTools(server, svc)
	RegisterSurfacePresentationTools(server, svc)
	client := connectPresentationTest(t, ctx, server)
	listed, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]any{}
	for _, tool := range listed.Tools {
		schemas[tool.Name] = tool.OutputSchema
	}
	read, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_get", Arguments: map[string]any{"aiSessionId": "session", "taskId": "task"}})
	if err != nil || read.IsError {
		t.Fatalf("read: %+v %v", read, err)
	}
	wire := jsonValue(t, read.StructuredContent)
	if err := resolveSchema(t, schemas["workspace_get"]).Validate(wire); err != nil {
		t.Fatal(err)
	}
	hint := wire["presentationHint"].(map[string]any)
	if hint["required"] != false || hint["reason"] != "PENDING_USER_DECISION" || read.Meta["openai/outputTemplate"] != nil {
		t.Fatalf("wrong silent hint: %#v", hint)
	}
	args := hint["arguments"].(map[string]any)
	if args["presentationRequestId"] != nil || args["decisionId"] != "REVIEW_APPROVAL:old-review" {
		t.Fatal("hint invented intent or lost exact decision", args)
	}
	showArgs := map[string]any{}
	for k, v := range args {
		showArgs[k] = v
	}
	showArgs["presentationRequestId"] = "flow-intent"
	shown, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_show", Arguments: showArgs})
	if err != nil || shown.IsError {
		t.Fatalf("show: %+v %v", shown, err)
	}
	show := jsonValue(t, shown.StructuredContent)
	if err := resolveSchema(t, schemas["workspace_show"]).Validate(show); err != nil {
		t.Fatal(err)
	}
	if err := resolveSchema(t, mustWorkspaceProjectionOutputSchema()).Validate(jsonValue(t, shown.Meta["workspace"])); err != nil {
		t.Fatal("component projection schema", err)
	}
	result := show["result"].(map[string]any)
	workflow := show["workflow"].(map[string]any)
	if result["taskId"] != shown.Meta["taskId"] || workflow["phase"] != "REVIEW_APPROVAL_PENDING" || len(workflow["pendingDecisions"].([]any)) != 1 || workflow["capabilities"] != nil {
		t.Fatal("show scope/compact summary mismatch", show)
	}
	if show["presentationHint"].(map[string]any)["reason"] != "PRESENTATION_RETURNED" {
		t.Fatal("recursive show hint")
	}
	for name, schema := range schemas {
		if name != "workspace_get" && name != "workspace_show" && name != "work_list" && name != "operation_get" && name != "project_selection_show" {
			continue
		}
		props := jsonValue(t, schema)["properties"].(map[string]any)
		validator := resolveSchema(t, props["presentationHint"])
		for field, value := range map[string]any{"reason": "INVENTED_INSTRUCTION", "tool": "execution_start", "display": "TEXT", "hostVisibility": "VISIBLE"} {
			invalid := jsonValue(t, hint)
			invalid[field] = value
			if validator.Validate(invalid) == nil {
				t.Fatalf("%s schema accepts invalid hint %s", name, field)
			}
		}
	}
	if dir := os.Getenv("DEVBRIDGE_FLOW_EVIDENCE_DIR"); dir != "" {
		for name, value := range map[string]any{"pending-read-fixture-response.json": read, "workspace-show-fixture-response.json": shown} {
			data, e := json.MarshalIndent(map[string]any{"fixture": "presentationFlowFixture; actual SDK tools/call with test identities only", "response": value}, "", "  ")
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(dir, name), append(data, '\n'), 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func (s *presentationFlowFixture) WorkspaceRuntimeDecisionsForSession(context.Context, string, string) ([]p.PendingDecision, error) {
	return s.runtime, nil
}

func TestPresentationFlowRuntimeHintKeepsExactScope(t *testing.T) {
	ctx := context.Background()
	svc := &presentationFlowFixture{exactPresentationService: exactPresentationFixture()}
	request := runtimePayloadRequest()
	target := &p.ActionTarget{TaskID: "task", FeatureAreaID: "area", RunID: request.RunID, SubjectID: "request-approval", AgentConversationID: "conversation"}
	svc.runtime = []p.PendingDecision{{Kind: "RUNTIME_REQUEST", Owner: "TASK", TaskID: "task", FeatureAreaID: "area", SemanticState: "WAITING_FOR_PERMISSION", RuntimeRequest: &request, Options: []p.Capability{{Action: "APPROVE_ONCE", Available: true, Target: target}}}}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "runtime-flow", Version: "1"}, nil)
	RegisterSurfaceBusinessTools(server, svc)
	RegisterSurfacePresentationTools(server, svc)
	client := connectPresentationTest(t, ctx, server)
	read, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_get", Arguments: map[string]any{"aiSessionId": "session", "taskId": "task"}})
	if err != nil || read.IsError {
		t.Fatalf("runtime read: %+v %v", read, err)
	}
	hint := jsonValue(t, read.StructuredContent)["presentationHint"].(map[string]any)
	args := hint["arguments"].(map[string]any)
	if hint["required"] != false || hint["reason"] != "PENDING_RUNTIME_REQUEST" || args["decisionId"] != nil || args["actionTarget"] == nil {
		t.Fatal("runtime target coerced/lost", hint)
	}
	args["presentationRequestId"] = "runtime-intent"
	shown, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_show", Arguments: args})
	if err != nil || shown.IsError {
		t.Fatalf("runtime show: %+v %v", shown, err)
	}
	if err := resolveSchema(t, workspaceShowOutputSchema()).Validate(jsonValue(t, shown.StructuredContent)); err != nil {
		t.Fatal(err)
	}
	if err := resolveSchema(t, mustWorkspaceProjectionOutputSchema()).Validate(jsonValue(t, shown.Meta["workspace"])); err != nil {
		t.Fatal(err)
	}
	// An unrelated project's inventory entry cannot authorize this selected Task.
	foreign := *target
	foreign.TaskID = "foreign-task"
	if workspaceAuthorizesActionTarget(p.WorkspaceProjection{SelectedTaskID: "task", SelectedFeatureAreaID: "area", PendingDecisions: []p.PendingDecision{{Kind: "RUNTIME_REQUEST", TaskID: "foreign-task", FeatureAreaID: "area", Options: []p.Capability{{Available: true, Target: &foreign}}}}}, &foreign) {
		t.Fatal("foreign capability contaminated scope")
	}
	svc.runtime = nil
	resolvedShow, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_show", Arguments: args})
	if err != nil || resolvedShow.IsError {
		t.Fatalf("resolved runtime entry cannot refresh: %+v %v", resolvedShow, err)
	}
	resolvedWire := jsonValue(t, resolvedShow.StructuredContent)
	if resolvedWire["result"].(map[string]any)["actionTargetStatus"] != "UNAVAILABLE" || len(resolvedWire["workflow"].(map[string]any)["pendingDecisions"].([]any)) != 0 {
		t.Fatal("old request was replaced with another approval", resolvedWire)
	}
	svc.taskProjection.CurrentState = "REVIEWING"
	svc.taskProjection.Capabilities = []p.Capability{{Action: "RETIRE", Available: true}, {Action: "CANCEL_EXECUTION", Available: true}}
	silent, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_get", Arguments: map[string]any{"aiSessionId": "session", "taskId": "task"}})
	if err != nil || silent.IsError {
		t.Fatalf("management read: %+v %v", silent, err)
	}
	if jsonValue(t, silent.StructuredContent)["presentationHint"].(map[string]any)["required"] != false {
		t.Fatal("running or management capabilities manufactured approval")
	}
}

func TestPresentationHistoricalReplacementRequiresTaskSourceEvidence(t *testing.T) {
	svc := exactPresentationFixture()
	workspace := p.WorkspaceProjection{DevelopmentContext: svc.taskProjection.DevelopmentContext}
	if got := presentationDecisionStatus(context.Background(), svc, "session", workspace, "REVIEW_APPROVAL:old-review"); got != "HISTORICAL_STATUS_UNAVAILABLE" {
		t.Fatal(got)
	}
	svc.tasks[0].CurrentReviewID = "new-review"
	if got := presentationDecisionStatus(context.Background(), svc, "session", workspace, "REVIEW_APPROVAL:old-review"); got != "SUPERSEDED" {
		t.Fatal(got)
	}
	svc.tasks[0].CurrentReviewID = "old-review"
	if got := presentationDecisionStatus(context.Background(), svc, "session", workspace, "REVIEW_APPROVAL:old-review"); got != "HISTORICAL_STATUS_UNAVAILABLE" {
		t.Fatal("absence alone was treated as resolution", got)
	}
}

func TestSurfaceMutationHintsUseCanonicalPendingActions(t *testing.T) {
	ctx := SurfaceContext{AISessionID: "session", ProjectID: "project", FeatureAreaID: "area", TaskID: "task"}
	for _, code := range []string{"RESOLVE_TASK_ACCEPTANCE", "RESOLVE_RUNTIME_APPROVAL"} {
		g := wp.Guidance{TaskID: "task", ExecutionID: "execution", DecisionID: "TASK_ACCEPTANCE:execution"}
		g.Set("PENDING", code, "Exact current pending gate")
		expected := "TASK_ACCEPTANCE:execution"
		if code == "RESOLVE_RUNTIME_APPROVAL" {
			g.RuntimeApprovalID = "approval"
			expected = "RUNTIME_APPROVAL:approval"
		}
		hint := surfaceHintForGuidance(ctx, g)
		if hint.Required || hint.Arguments == nil || hint.Arguments.DecisionID != expected || hint.Arguments.ActionTarget != nil {
			t.Fatalf("canonical %s not presented accurately: %+v", code, hint)
		}
		g.Set("RUNNING", "CANCEL_EXECUTION", "Management action")
		if surfaceHintForGuidance(ctx, g).Required {
			t.Fatal("management manufactured an interaction")
		}
	}
}
