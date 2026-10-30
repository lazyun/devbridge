package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWorkspaceCardResourcesDeclarePiPAndKeepInlineCompatibility(t *testing.T) {
	ctx := context.Background()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "pip-resource-test", Version: "1"}, nil)
	RegisterDevelopmentContextCard(server)
	client := connectPresentationTest(t, ctx, server)
	if devBridgeCardResourceURI == previousStickyViewDevBridgeCardResourceURI {
		t.Fatal("terminal status update must use a new resource identity for cached hosts")
	}
	for _, uri := range []string{devBridgeCardResourceURI, "ui://devbridge/card/task-panel-v43", "ui://devbridge/card/task-panel-v33", "ui://devbridge/card/task-panel-v32", "ui://devbridge/card/task-panel-v31", "ui://devbridge/card/task-panel-v30", "ui://devbridge/card/task-panel-v29", "ui://devbridge/card/task-panel-v28", "ui://devbridge/card/task-panel-v27", "ui://devbridge/card/task-panel-v26", "ui://devbridge/card/task-panel-v25", "ui://devbridge/card/task-panel-v24", "ui://devbridge/card/task-panel-v23", previousScopedTaskPanelResourceURI, previousSessionTaskPanelResourceURI, previousTabbedTaskPanelResourceURI, previousPrioritizedTaskPanelResourceURI, previousUnifiedTaskPanelResourceURI, previousVerificationDecisionsDevBridgeCardResourceURI, previousRunResultRecoveryDevBridgeCardResourceURI, previousOneNextStepDevBridgeCardResourceURI, previousCurrentRunAnalysisDevBridgeCardResourceURI, previousBusinessStageDevBridgeCardResourceURI, previousAutoStartDevBridgeCardResourceURI, previousTerminalStatusDevBridgeCardResourceURI, previousStickyViewDevBridgeCardResourceURI, previousReviewActionsDevBridgeCardResourceURI, previousReviewAnalysisDevBridgeCardResourceURI, previousConversationDevBridgeCardResourceURI, previousPiPV3DevBridgeCardResourceURI, previousPiPDevBridgeCardResourceURI, previousDevBridgeCardResourceURI, legacyDevBridgeCardResourceURI} {
		resource, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: uri})
		if err != nil || len(resource.Contents) != 1 {
			t.Fatalf("read resource %s: %+v %v", uri, resource, err)
		}
		content := resource.Contents[0]
		meta := jsonValue(t, content.Meta)
		if meta["devbridge/componentVersion"] != "TASK_PANEL_V45" {
			t.Fatalf("stale component version for %s: %+v", uri, meta)
		}
		modes := meta["openai/ui"].(map[string]any)["availableDisplayModes"].([]any)
		if len(modes) != 3 || modes[0] != "inline" || modes[1] != "pip" || modes[2] != "fullscreen" || content.Text != devBridgeUIHTML || meta["devbridge/presentationPolicy"] != "SINGLE_PANEL_WITH_EXPLICIT_USER_DECISIONS" {
			t.Fatalf("display contract changed for %s: %+v", uri, meta)
		}
	}
}

type exactPresentationService struct {
	*presentationOnlyService
	reviews    map[string]review.Review
	executions map[string]executionproposal.Proposal
}

func TestSurfacePresentationHintIsExactAndShowClosesIt(t *testing.T) {
	ctx := SurfaceContext{AISessionID: "session", ProjectID: "project", FeatureAreaID: "area", TaskID: "task"}
	target := &p.ActionTarget{TaskID: "task", FeatureAreaID: "area", RunID: "run", AgentConversationID: "conversation", SubjectID: "request"}
	hint := surfaceHintFor(ctx, SurfaceWorkflow{PendingDecisions: []p.PendingDecision{{Kind: "RUNTIME_REQUEST", TaskID: "task", FeatureAreaID: "area", RuntimeRequest: &agentruntime.Request{Identity: agentruntime.Identity{RunID: "run", ConnectionID: "conversation"}, ItemID: "request", State: "PENDING"}, Options: []p.Capability{{Available: true, Target: target}}}}}, false)
	if hint.Required || hint.Reason != "PENDING_RUNTIME_REQUEST" || hint.Arguments == nil || hint.Arguments.ActionTarget == nil || hint.Arguments.ActionTarget.RunID != "run" {
		t.Fatalf("runtime hint lost exact bounded identity: %+v", hint)
	}
	foreign := surfaceHintFor(ctx, SurfaceWorkflow{PendingDecisions: []p.PendingDecision{{Kind: "REVIEW_APPROVAL", TaskID: "other", Decision: &p.Decision{ID: "REVIEW_APPROVAL:other"}}}}, false)
	if foreign.Required || foreign.Reason != "NO_PENDING_INTERACTION" {
		t.Fatalf("foreign pending produced hint: %+v", foreign)
	}
	resolved := surfaceHintFor(ctx, SurfaceWorkflow{PendingDecisions: []p.PendingDecision{{Kind: "RUNTIME_REQUEST", TaskID: "task", RuntimeRequest: &agentruntime.Request{State: "RESOLVED"}}}}, false)
	if resolved.Required {
		t.Fatalf("resolved runtime request produced hint: %+v", resolved)
	}
	closed := WorkspaceShowOutput{AISessionID: "session", ProjectID: "project", Display: "COLLAPSED", Workspace: p.WorkspaceProjection{}}
	raw, err := json.Marshal(closed)
	if err != nil || !strings.Contains(string(raw), `"required":false`) || !strings.Contains(string(raw), `"reason":"PRESENTATION_RETURNED"`) {
		t.Fatalf("show did not close hint: %s %v", raw, err)
	}
}

func (s *exactPresentationService) GetReview(_ context.Context, _ string, id string) (review.Review, error) {
	v, ok := s.reviews[id]
	if !ok {
		return v, errors.New("review unavailable")
	}
	return v, nil
}
func (s *exactPresentationService) GetExecutionProposal(_ context.Context, _ string, id string) (executionproposal.Proposal, error) {
	v, ok := s.executions[id]
	if !ok {
		return v, errors.New("execution unavailable")
	}
	return v, nil
}
func (*exactPresentationService) GetDecisionResolution(context.Context, string, string) (decisionresolution.Receipt, error) {
	return decisionresolution.Receipt{}, errors.New("historical decision receipt unavailable")
}
func exactPresentationFixture() *exactPresentationService {
	projection := p.Projection{SchemaVersion: "1", FeatureArea: p.Identity{ID: "area", Name: "Area"}, DevelopmentContext: p.DevelopmentContext{Project: p.Identity{ID: "project", Name: "Project"}, Repositories: []p.RepositoryContext{}}, Details: p.Details{FeatureAreaID: "area", TaskID: "task"}, TaskView: &p.TaskView{ID: "task", Phase: task.PhaseReviewApprovalPending}, Capabilities: []p.Capability{}, Blockers: []string{}}
	return &exactPresentationService{presentationOnlyService: &presentationOnlyService{projection: projection, taskProjection: projection, areas: []featurearea.FeatureArea{{ID: "area", ProjectID: "project", Status: featurearea.StatusActive}}, tasks: []task.Task{{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment}}}, reviews: map[string]review.Review{"old-review": {ID: "old-review", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", RunID: "old-run"}}, executions: map[string]executionproposal.Proposal{"execution": {ID: "execution", ProjectID: "project", TaskID: "task", ImplementationRunID: "impl-run"}}}
}
func TestWorkspaceShowDerivesExactDecisionOwnerAndPreservesResolvedTarget(t *testing.T) {
	svc := exactPresentationFixture()
	input := WorkspaceShowInput{AISessionID: "session", PresentationRequestID: "one", DecisionID: "REVIEW_APPROVAL:old-review", Target: &PresentationTarget{Kind: "REVIEW", OperationID: "old-review", OwnerTaskID: "task", RunID: "old-run"}}
	out, err := workspaceShowCore(context.Background(), svc, input, false)
	if err != nil || out.TaskID != "task" || out.FeatureAreaID != "area" || out.DecisionID != input.DecisionID || out.DecisionStatus != "HISTORICAL_STATUS_UNAVAILABLE" || out.HostVisibility != "UNCONFIRMED" {
		t.Fatalf("old entry drifted: %+v %v", out, err)
	}
	for _, change := range []func(*WorkspaceShowInput){func(v *WorkspaceShowInput) { v.ProjectID = "foreign" }, func(v *WorkspaceShowInput) { v.TaskID = "foreign" }, func(v *WorkspaceShowInput) { v.FeatureAreaID = "foreign" }, func(v *WorkspaceShowInput) {
		v.Target = &PresentationTarget{Kind: "REVIEW", OperationID: "old-review", RunID: "other-run"}
	}, func(v *WorkspaceShowInput) {
		v.Target = &PresentationTarget{Kind: "EXECUTION", OperationID: "execution"}
	}} {
		bad := input
		change(&bad)
		if _, err := workspaceShowCore(context.Background(), svc, bad, false); err == nil {
			t.Fatalf("conflicting exact target accepted: %+v", bad)
		}
	}
}
func TestWorkspaceShowActualSDKReplayKeepsPrivateDataAndRejectsChangedTarget(t *testing.T) {
	ctx := context.Background()
	svc := exactPresentationFixture()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "exact-show", Version: "1"}, nil)
	server.AddReceivingMiddleware(presentationResultMiddleware(svc))
	RegisterSurfacePresentationTools(server, svc)
	RegisterDevelopmentContextCard(server)
	client := connectPresentationTest(t, ctx, server)
	args := map[string]any{"aiSessionId": "session", "presentationRequestId": "one", "decisionId": "REVIEW_APPROVAL:old-review"}
	first, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_show", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("%+v %v", first, err)
	}
	raw, err := json.Marshal(first.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"result", "context", "workflow", "presentationHint"} {
		if envelope[key] == nil {
			t.Fatalf("missing unified show envelope %s: %s", key, raw)
		}
	}
	identity := envelope["result"].(map[string]any)
	if identity["taskId"] != "task" || identity["workspace"] != nil || identity["hostVisibility"] != "UNCONFIRMED" {
		t.Fatalf("model identity lost or private workspace repeated: %s", raw)
	}
	t.Logf("fixture workspace_show tools/call structuredContent: %s", raw)
	for i := 0; i < 2; i++ {
		replay, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_show", Arguments: args})
		if err != nil || replay.IsError {
			t.Fatalf("%+v %v", replay, err)
		}
		if replay.Meta["workspace"] == nil || replay.Meta["entryIdentity"] != first.Meta["entryIdentity"] || replay.Meta["taskId"] != "task" || replay.Meta["emission"] != "MAIN" {
			t.Fatalf("incomplete replay %+v", replay.Meta)
		}
	}
	args["display"] = "EXPANDED"
	bad, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_show", Arguments: args})
	if err == nil && !bad.IsError {
		t.Fatal("changed retry accepted")
	}
	args["presentationRequestId"] = "two"
	next, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_show", Arguments: args})
	if err != nil || next.IsError || next.Meta["entryIdentity"] == first.Meta["entryIdentity"] {
		t.Fatal("different reply suppressed", err)
	}
	resource, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: devBridgeCardResourceURI})
	if err != nil || len(resource.Contents) != 1 || resource.Contents[0].Text != devBridgeUIHTML {
		t.Fatal("production resource unavailable", err)
	}
}

func TestWorkspaceShowSharesPrimaryNextStepWithModelWithoutLeakingWorkspace(t *testing.T) {
	step := &p.TaskNextStep{Code: "ANALYZE_RUN_OUTPUT", TaskID: "task", ExecutionID: "execution", TaskDefinitionRevisionID: "revision", RunID: "impl", Purpose: "IMPLEMENTATION", Label: "待 GPT 分析实施结果", Summary: "先分析实施输出，再决定后续处理。", Recommended: true}
	out := WorkspaceShowOutput{AISessionID: "session", ProjectID: "project", TaskID: "task", Display: "EXPANDED", HostVisibility: "UNCONFIRMED", PresentationMode: "WORKSPACE", Workspace: p.WorkspaceProjection{Presentation: &p.Projection{TaskView: &p.TaskView{ID: "task", Phase: task.PhaseRecoveryRequired, NextStep: step}}}}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	next := wire["workflow"].(map[string]any)["nextStep"].(map[string]any)
	if next["label"] != step.Label || next["runId"] != "impl" || next["recommended"] != true {
		t.Fatalf("model lost primary next step: %s", raw)
	}
	if strings.Contains(string(raw), `"workspace":`) || strings.Contains(string(raw), `"taskView":`) {
		t.Fatal("private workspace leaked")
	}
	if err := resolveSchema(t, workspaceShowOutputSchema()).Validate(wire); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"RECOVER_RUN_RESULT", "PROCESS_RUN_RESULT", "CHECK_RUN_RESULT"} {
		step.Code, step.RunID = code, "verify"
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err = json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got["workflow"].(map[string]any)["nextStep"].(map[string]any)["code"] != code {
			t.Fatal("result recovery hidden from model")
		}
		if err = resolveSchema(t, workspaceShowOutputSchema()).Validate(got); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"historical", "other task", "read unavailable"} {
		copy := out
		switch kind {
		case "historical":
			copy.ReadOnly = true
		case "other task":
			copy.TaskID = "other"
		case "read unavailable":
			copy.Workspace.ReadError = "unavailable"
		}
		raw, _ := json.Marshal(copy)
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		if got["workflow"].(map[string]any)["nextStep"] != nil {
			t.Fatalf("%s borrowed current step", kind)
		}
	}
}

func TestPanelShowPresentationKindSurvivesMetadataLoss(t *testing.T) {
	out := WorkspaceShowOutput{SinglePanel: true, AISessionID: "session", ProjectID: "project", PresentationMode: "WORKSPACE", PresentationRequestID: "request", EntryIdentity: "entry", Display: "EXPANDED"}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	result := body["result"].(map[string]any)
	if result["singlePanel"] != true || body["presentationHint"].(map[string]any)["tool"] != "panel_show" {
		t.Fatalf("panel kind only survives in _meta: %s", raw)
	}
	if workspaceShowMeta(out)["singlePanel"] != true {
		t.Fatal("legacy host metadata missing")
	}
}
