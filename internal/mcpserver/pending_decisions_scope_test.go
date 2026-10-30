package mcpserver

import (
	"context"
	"testing"

	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type pendingScopeFixture struct{ *presentationFlowFixture }

func (s *pendingScopeFixture) WorkspaceRuntimeDecisionsForSession(context.Context, string, string) ([]p.PendingDecision, error) {
	return nil, nil // Only the navigation reader returns the foreign Project item.
}

// The wire keeps the Project inventory for navigation. Current workflow and
// show summaries must not promote its foreign owner into the selected scope.
func TestPendingDecisionsWireScopeDefinitions(t *testing.T) {
	ctx := context.Background()
	svc := &pendingScopeFixture{&presentationFlowFixture{exactPresentationService: exactPresentationFixture()}}
	svc.taskProjection.PendingDecision = &p.Decision{ID: "REVIEW_APPROVAL:old-review", Kind: "REVIEW_APPROVAL", SubjectID: "task", Summary: "Current approval"}
	svc.runtime = []p.PendingDecision{{Kind: "REVIEW_APPROVAL", Owner: "TASK", FeatureAreaID: "foreign-area", TaskID: "foreign-task", Decision: &p.Decision{ID: "REVIEW_APPROVAL:foreign", Kind: "REVIEW_APPROVAL", SubjectID: "foreign-task", Summary: "Foreign approval"}, Options: []p.Capability{}, RequiredInput: []string{}}}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "pending-scope", Version: "1"}, nil)
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
	for _, name := range []string{"workspace_get", "workspace_show"} {
		args := map[string]any{"aiSessionId": "session", "featureAreaId": "area", "taskId": "task"}
		if name == "workspace_get" {
			args["include"] = []string{"NAVIGATION"}
		} else {
			args["presentationRequestId"] = "pending-scope"
		}
		result, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s: %v %+v", name, err, result)
		}
		wire := jsonValue(t, result.StructuredContent)
		if err := resolveSchema(t, schemas[name]).Validate(wire); err != nil {
			t.Fatal(err)
		}
		workflow := wire["workflow"].(map[string]any)
		pending := workflow["pendingDecisions"].([]any)
		if len(pending) != 1 || pending[0].(map[string]any)["taskId"] != "task" {
			t.Fatalf("foreign current interaction: %#v", pending)
		}
		var inventory map[string]any
		if name == "workspace_get" {
			inventory = wire["result"].(map[string]any)["navigation"].(map[string]any)
		} else {
			inventory = jsonValue(t, result.Meta["workspace"])
			if err := resolveSchema(t, mustWorkspaceProjectionOutputSchema()).Validate(inventory); err != nil {
				t.Fatal(err)
			}
		}
		all := inventory["pendingDecisions"].([]any)
		if len(all) != 2 || all[1].(map[string]any)["taskId"] != "foreign-task" {
			t.Fatalf("Project inventory changed scope: %#v", all)
		}
	}
}

func TestExactCurrentDecisionPresentationAuthority(t *testing.T) {
	ctx := context.Background()
	svc := &pendingScopeFixture{&presentationFlowFixture{exactPresentationService: exactPresentationFixture()}}
	decision := &p.Decision{ID: "REVIEW_APPROVAL:old-review", Kind: "REVIEW_APPROVAL", SubjectID: "task", AllowedActions: []string{"APPROVE", "REVISE", "REJECT"}}
	svc.taskProjection.PendingDecision = decision
	svc.taskProjection.CurrentState = "REVIEW_APPROVAL_PENDING"
	canonical := p.ActionTarget{FeatureAreaID: "area", TaskID: "task", DecisionID: decision.ID, SubjectID: "task"}
	for _, action := range decision.AllowedActions {
		target := canonical
		svc.taskProjection.Capabilities = append(svc.taskProjection.Capabilities, p.Capability{Action: action, Available: true, Target: &target})
	}
	svc.runtime = []p.PendingDecision{{Kind: "REVIEW_APPROVAL", TaskID: "foreign-task", FeatureAreaID: "foreign-area", Decision: &p.Decision{ID: "REVIEW_APPROVAL:foreign", SubjectID: "foreign-task"}, Options: []p.Capability{}}}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "exact-decision-authority", Version: "1"}, nil)
	RegisterSurfaceBusinessTools(server, svc)
	RegisterSurfacePresentationTools(server, svc)
	RegisterFeatureAreaPresentation(server, svc)
	client := connectPresentationTest(t, ctx, server)
	read, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_get", Arguments: map[string]any{"aiSessionId": "session", "featureAreaId": "area", "taskId": "task", "include": []string{"NAVIGATION"}}})
	if err != nil || read.IsError {
		t.Fatalf("workspace_get: %v %+v", err, read)
	}
	readPending := jsonValue(t, read.StructuredContent)["workflow"].(map[string]any)["pendingDecisions"].([]any)
	if len(readPending) != 1 || len(readPending[0].(map[string]any)["options"].([]any)) != 3 {
		t.Fatalf("authoritative read: %#v", readPending)
	}
	// Both published Review target shapes exist: the FeatureArea projection omits
	// taskId, while the Task projection includes it. The owner is still exact.
	for _, withTask := range []bool{false, true} {
		wanted := canonical
		if !withTask {
			wanted.TaskID = ""
		}
		for _, name := range []string{"workspace_show", "dev_bridge_card_get"} {
			args := map[string]any{"aiSessionId": "session", "featureAreaId": "area", "taskId": "task", "decisionId": decision.ID, "actionTarget": wanted, "target": map[string]any{"kind": "REVIEW", "operationId": "old-review", "ownerTaskId": "task", "runId": "old-run"}}
			if name == "workspace_show" {
				args["presentationRequestId"] = "exact-current"
			}
			result, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
			if err != nil || result.IsError {
				t.Fatalf("%s: %v %+v", name, err, result)
			}
			var inventory map[string]any
			if name == "workspace_show" {
				wire := jsonValue(t, result.StructuredContent)
				if err := resolveSchema(t, workspaceShowOutputSchema()).Validate(wire); err != nil {
					t.Fatal(err)
				}
				show := wire["result"].(map[string]any)
				if show["decisionStatus"] != "PENDING" || show["actionTargetStatus"] != "UNAVAILABLE" {
					t.Fatalf("exact pending authority lost: %#v", show)
				}
				pending := wire["workflow"].(map[string]any)["pendingDecisions"].([]any)
				if len(pending) != 1 || pending[0].(map[string]any)["decisionId"] != decision.ID {
					t.Fatalf("current summary: %#v", pending)
				}
				inventory = jsonValue(t, result.Meta["workspace"])
			} else {
				if result.Meta["decisionStatus"] != "PENDING" || result.Meta["actionTargetStatus"] != "UNAVAILABLE" {
					t.Fatalf("card authority lost: %#v", result.Meta)
				}
				inventory = jsonValue(t, result.StructuredContent)
			}
			if err := resolveSchema(t, mustWorkspaceProjectionOutputSchema()).Validate(inventory); err != nil {
				t.Fatal(err)
			}
			if inventory["selectedTaskId"] != "task" || inventory["selectedFeatureAreaId"] != "area" {
				t.Fatal(inventory)
			}
			all := inventory["pendingDecisions"].([]any)
			if len(all) != 2 || len(all[0].(map[string]any)["options"].([]any)) != 3 {
				t.Fatalf("Card inventory lost options: %#v", all)
			}
		}
	}
	workspace, _ := svc.DevelopmentWorkspaceForSession(ctx, "session", "area", "task")
	for _, field := range []string{"task", "area", "decision", "subject", "run"} {
		wrong := canonical
		switch field {
		case "task":
			wrong.TaskID = "foreign"
		case "area":
			wrong.FeatureAreaID = "foreign"
		case "decision":
			wrong.DecisionID = "REVIEW_APPROVAL:replacement"
		case "subject":
			wrong.SubjectID = "foreign"
		case "run":
			wrong.RunID = "foreign"
		}
		if workspaceAuthorizesActionTarget(workspace, &wrong) {
			t.Fatalf("authorized foreign %s", field)
		}
	}
	workspace.SelectedTaskID = ""
	if workspaceAuthorizesActionTarget(workspace, &canonical) {
		t.Fatal("empty Task authorized old decision")
	}
	workspace.SelectedTaskID = "task"
	workspace.PendingDecisions = svc.runtime
	workspace.Presentation = &p.Projection{}
	if workspaceAuthorizesActionTarget(workspace, &canonical) {
		t.Fatal("resolved decision authorized")
	}
}
