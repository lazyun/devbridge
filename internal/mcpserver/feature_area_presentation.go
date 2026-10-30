package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type featureAreaPresentationReader interface {
	FeatureAreaPresentationForSession(context.Context, string, string) (presentation.Projection, error)
	DevelopmentContextPresentationForSession(context.Context, string) (presentation.DevelopmentContext, error)
}

type taskPresentationReader interface {
	TaskPresentationForSession(context.Context, string, string, string) (presentation.Projection, error)
}

func (s *StaticService) FeatureAreaPresentationForSession(ctx context.Context, sessionID, areaID string) (presentation.Projection, error) {
	if s.control == nil {
		return presentation.Projection{}, errors.New("FeatureArea presentation unavailable")
	}
	return s.control.FeatureAreaPresentationForSession(ctx, sessionID, areaID)
}

func (s *StaticService) DevelopmentContextPresentationForSession(ctx context.Context, sessionID string) (presentation.DevelopmentContext, error) {
	if s.control == nil {
		return presentation.DevelopmentContext{}, errors.New("Development Context presentation unavailable")
	}
	return s.control.DevelopmentContextPresentationForSession(ctx, sessionID)
}

type featureAreaPresentationInput struct {
	PresentationRequestID string `json:"presentationRequestId,omitempty"`
	AISessionID           string `json:"aiSessionId"`
	FeatureAreaID         string `json:"featureAreaId"`
}

type developmentPresentationInput struct {
	PresentationRequestID string `json:"presentationRequestId,omitempty"`
	AISessionID           string `json:"aiSessionId"`
}

type devBridgeCardInput struct {
	ReadMode      string                     `json:"readMode,omitempty"`
	ProjectID     string                     `json:"projectId,omitempty"`
	SelectionMode string                     `json:"selectionMode,omitempty"`
	DecisionID    string                     `json:"decisionId,omitempty"`
	Target        *PresentationTarget        `json:"target,omitempty"`
	ActionTarget  *presentation.ActionTarget `json:"actionTarget,omitempty"`
	AISessionID   string                     `json:"aiSessionId"`
	FeatureAreaID string                     `json:"featureAreaId,omitempty"`
	TaskID        string                     `json:"taskId,omitempty"`
}

const mainCardShowDescription = "Present one Dev Bridge entry per reply after necessary reads. Default COLLAPSED; explicit user open/show uses EXPANDED. Display is not approval or host visibility evidence. "
const FeatureAreaCardShowDescription = mainCardShowDescription + "Removed compatibility entry; use workspace_show with an exact FeatureArea."
const DevelopmentContextCardShowDescription = mainCardShowDescription + "Use workspace_show with a valid aiSessionId. Project choice and binding happen only through the user conversation and model commands."
const FeatureAreaPresentationGetDescription = "Read the current FeatureArea workflow projection and text fallback silently, without inserting a main Card. Used for auxiliary queries, polling, reconciliation, and in-place Card Refresh. Pure read: no creation, continuation, decision, cancellation, or recovery."

// RegisterFeatureAreaPresentation installs the default pure-read presentation.
func RegisterFeatureAreaPresentation(server *sdkmcp.Server, service BridgeService) {
	projectionOutputSchema := mustProjectionOutputSchema()
	registerTaskPresentation(server, service, projectionOutputSchema)
	registerReviewWakeupClaim(server, service)
	registerRuntimeRequestWakeupClaim(server, service)
	for _, name := range []string{"feature_area_presentation_get"} {
		description := FeatureAreaPresentationGetDescription
		meta := sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}
		schema := `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","featureAreaId"],"additionalProperties":false}`
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: name, Title: "Read FeatureArea presentation", Description: description, InputSchema: json.RawMessage(schema), OutputSchema: projectionOutputSchema, Annotations: toolAnnotations("Read FeatureArea presentation", true, true), Meta: meta}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input featureAreaPresentationInput) (*sdkmcp.CallToolResult, presentation.Projection, error) {
			reader, ok := service.(featureAreaPresentationReader)
			if !ok {
				return nil, presentation.Projection{}, publicToolError(errors.New("FeatureArea presentation unavailable"))
			}
			value, err := reader.FeatureAreaPresentationForSession(ctx, input.AISessionID, input.FeatureAreaID)
			if err != nil {
				return nil, presentation.Projection{}, publicToolError(err)
			}
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: presentation.RenderText(value)}}, Meta: sdkmcp.Meta{"aiSessionId": input.AISessionID, "featureAreaId": input.FeatureAreaID}}, value, nil
		})
	}
	for _, name := range []string{"development_context_presentation_get"} {
		description := "Pure authoritative Development Context Refresh; no Project open, branch selection, checkout, or workflow mutation."
		meta := sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}
		schema := `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId"],"additionalProperties":false}`
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: name, Title: "Read Development Context", Description: description, InputSchema: json.RawMessage(schema), Annotations: toolAnnotations("Read Development Context", true, true), Meta: meta}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input developmentPresentationInput) (*sdkmcp.CallToolResult, presentation.DevelopmentContext, error) {
			reader, ok := service.(featureAreaPresentationReader)
			if !ok {
				return nil, presentation.DevelopmentContext{}, publicToolError(errors.New("Development Context presentation unavailable"))
			}
			value, err := reader.DevelopmentContextPresentationForSession(ctx, input.AISessionID)
			return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"aiSessionId": input.AISessionID}}, value, publicToolError(err)
		})
	}
	registerDevelopmentWorkspace(server, service, []string{"dev_bridge_card_get", "development_workspace_get"})
}

const DevelopmentWorkspaceGetDescription = "Read all current Project FeatureAreas, layered review/commit/task/action facts and the current Task-first workspace in one pure read. No workflow mutation, recovery, or Codex invocation."

func RegisterDevelopmentWorkspace(server *sdkmcp.Server, service BridgeService) {
	registerDevelopmentWorkspace(server, service, []string{"development_workspace_get"})
}
func registerDevelopmentWorkspace(server *sdkmcp.Server, service BridgeService, names []string) {
	workspaceProjectionOutputSchema := mustWorkspaceProjectionOutputSchema()

	workspaceSchema := `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"projectId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","maxLength":128},"taskId":{"type":"string","maxLength":128},"selectionMode":{"type":"string","enum":["EXACT"]},"decisionId":{"type":"string","minLength":1,"maxLength":256},"target":` + presentationTargetSchema + `,"actionTarget":` + surfaceActionTargetSchema + `},"required":["aiSessionId"],"additionalProperties":false}`
	for _, name := range names {
		inputSchema := json.RawMessage(workspaceSchema)
		var outputSchema any = workspaceProjectionOutputSchema
		if name == "dev_bridge_card_get" {
			sessionSchema := json.RawMessage(`{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128,"pattern":"^\\S+$"},"readMode":{"const":"SESSION"}},"required":["aiSessionId","readMode"],"additionalProperties":false}`)
			inputSchema, _ = json.Marshal(map[string]any{"type": "object", "oneOf": []any{inputSchema, sessionSchema}})
			outputSchema = map[string]any{"type": "object", "$defs": workspaceProjectionOutputSchema.Defs, "oneOf": []any{workspaceProjectionOutputSchema, json.RawMessage(SessionInitializeOutputSchema)}}
		}
		meta := sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}
		if name == "dev_bridge_card_get" {
			meta = sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}
		}
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: name, Title: "Read Dev Bridge Card", Description: DevelopmentWorkspaceGetDescription + " Progress and list reads are READ_ONLY. The unified panel has isolated formal confirmation entries whose own user-clicked confirmation_commit path remains separate; this read never submits a choice. dev_bridge_card_get additionally accepts only {aiSessionId,readMode:SESSION} for pure reading of that existing Session and candidate inventory; this branch cannot create, bind or prepare anything. Browsing is not model operation context.", InputSchema: inputSchema, OutputSchema: outputSchema, Annotations: toolAnnotations("Read Dev Bridge Card", true, true), Meta: meta}, func(ctx context.Context, request *sdkmcp.CallToolRequest, input devBridgeCardInput) (*sdkmcp.CallToolResult, any, error) {
			if name == "dev_bridge_card_get" && input.ReadMode == "SESSION" {
				if !validSessionInputID(input.AISessionID) || input.ProjectID != "" || input.FeatureAreaID != "" || input.TaskID != "" || input.SelectionMode != "" || input.DecisionID != "" || input.Target != nil || input.ActionTarget != nil {
					return nil, nil, errors.New("SESSION read accepts only exact aiSessionId and readMode")
				}
				ctx, err := withSessionRequest(ctx, request)
				if err != nil {
					return nil, nil, publicToolError(err)
				}
				value, err := SessionInitializeCore(ctx, service, SessionInitializeInput{AISessionID: input.AISessionID})
				return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"devbridge/presentationPolicy": "READ_ONLY"}}, value, err
			}
			exact, err := validatePresentationIdentity(ctx, service, input)
			if err != nil {
				return nil, presentation.WorkspaceProjection{}, publicToolError(err)
			}
			value, err := devBridgeCardProjection(ctx, service, exact)
			if err != nil {
				return nil, presentation.WorkspaceProjection{}, publicToolError(err)
			}
			if value.DevelopmentContext.Project.ID != exact.ProjectID || exact.FeatureAreaID != "" && value.SelectedFeatureAreaID != exact.FeatureAreaID || exact.TaskID != "" && value.SelectedTaskID != exact.TaskID && !workspaceSelectsInspection(value, exact.Target) {
				return nil, presentation.WorkspaceProjection{}, errors.New("WORKSPACE_TARGET_MISMATCH")
			}
			status := presentationDecisionStatus(ctx, service, input.AISessionID, value, exact.DecisionID)
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: presentation.RenderWorkspaceText(value)}}, Meta: sdkmcp.Meta{"aiSessionId": input.AISessionID, "presentationId": value.PresentationID, "featureAreaId": value.SelectedFeatureAreaID, "taskId": value.SelectedTaskID, "projectId": exact.ProjectID, "decisionId": exact.DecisionID, "decisionStatus": status, "target": exact.Target, "actionTarget": exact.ActionTarget, "actionTargetStatus": presentationActionTargetStatus(value, exact.ActionTarget), "actionTargetKind": presentationActionTargetKind(value, exact.ActionTarget)}}, value, nil
		})
	}
}

type developmentWorkspaceReader interface {
	DevelopmentWorkspaceForSession(context.Context, string, string, string) (presentation.WorkspaceProjection, error)
}

func (s *StaticService) DevelopmentWorkspaceForSession(ctx context.Context, session, area, task string) (presentation.WorkspaceProjection, error) {
	if s.control == nil {
		return presentation.WorkspaceProjection{}, errors.New("Workspace presentation unavailable")
	}
	return s.control.DevelopmentWorkspaceForSession(ctx, session, area, task)
}
func devBridgeCardProjection(ctx context.Context, service BridgeService, input devBridgeCardInput) (presentation.WorkspaceProjection, error) {
	if input.SelectionMode == "EXACT" {
		if reader, ok := service.(interface {
			ExactDevelopmentWorkspaceForSession(context.Context, string, string, string) (presentation.WorkspaceProjection, error)
		}); ok {
			return reader.ExactDevelopmentWorkspaceForSession(ctx, input.AISessionID, input.FeatureAreaID, input.TaskID)
		}
	}
	if reader, ok := service.(developmentWorkspaceReader); ok {
		if input.SelectionMode == "EXACT" && input.FeatureAreaID == "" && input.TaskID == "" {
			return presentation.WorkspaceProjection{}, errors.New("Exact Project inventory read unavailable")
		}
		return reader.DevelopmentWorkspaceForSession(ctx, input.AISessionID, input.FeatureAreaID, input.TaskID)
	}
	reader, ok := service.(featureAreaPresentationReader)
	if !ok {
		return presentation.WorkspaceProjection{}, errors.New("Dev Bridge Card presentation unavailable")
	}

	development, err := reader.DevelopmentContextPresentationForSession(ctx, input.AISessionID)
	if err != nil {
		return presentation.WorkspaceProjection{}, err
	}
	out := presentation.WorkspaceProjection{SchemaVersion: "1", DevelopmentContext: development, FeatureAreas: []presentation.FeatureAreaNavigationItem{}, Tasks: []presentation.TaskNavigationItem{}}
	areas, err := service.ListFeatureAreas(ctx, input.AISessionID)
	if err != nil {
		return presentation.WorkspaceProjection{}, err
	}
	areaFound := input.FeatureAreaID == ""
	for _, area := range areas {
		name := boundedUTF8(area.Title, maxNameBytes)
		out.FeatureAreas = append(out.FeatureAreas, presentation.FeatureAreaNavigationItem{Retirement: area.Retirement, Availability: presentation.Availability(string(area.Status)), ID: area.ID, Name: name, Status: string(area.Status), Product: presentation.NewFeatureAreaProductProjection(area.ID, name, string(area.Status), area.LegacyReadOnly)})
		areaFound = areaFound || area.ID == input.FeatureAreaID
	}
	if !areaFound {
		area, readErr := service.GetFeatureArea(ctx, input.AISessionID, input.FeatureAreaID)
		if readErr != nil {
			return presentation.WorkspaceProjection{}, readErr
		}
		name := boundedUTF8(area.Title, maxNameBytes)
		out.FeatureAreas = append(out.FeatureAreas, presentation.FeatureAreaNavigationItem{Retirement: area.Retirement, Availability: presentation.Availability(string(area.Status)), ID: area.ID, Name: name, Status: string(area.Status), Product: presentation.NewFeatureAreaProductProjection(area.ID, name, string(area.Status), area.LegacyReadOnly)})
	}

	var selectedTaskFound = input.TaskID == ""
	var selectedProjectInspection *task.Task
	for offset := 0; ; offset += aicontext.MaxListLimit {
		values, readErr := service.ListTasks(ctx, input.AISessionID, aicontext.ListOptions{Limit: aicontext.MaxListLimit, Offset: offset})
		if readErr != nil {
			return presentation.WorkspaceProjection{}, readErr
		}
		for _, value := range values {
			if value.FeatureAreaID != input.FeatureAreaID {
				continue
			}
			work := workspaceNavigationWork(value)
			if value.ID == input.TaskID && value.FeatureAreaID == "" && work.Type == task.TypeInspection && (value.Scope == task.ScopeProject || work.Scope == task.ScopeProject) {
				copy := value
				selectedProjectInspection = &copy
			}
			name := value.ProblemStatement
			if name == "" {
				name = value.WorkSpec.Goal
			}
			if name == "" {
				name = value.ID
			}
			out.Tasks = append(out.Tasks, presentation.TaskNavigationItem{Work: work, ID: value.ID, FeatureAreaID: value.FeatureAreaID, Name: boundedUTF8(name, maxNameBytes), Status: string(value.Status), UpdatedAt: value.UpdatedAt})
			selectedTaskFound = selectedTaskFound || value.ID == input.TaskID
		}
		if len(values) < aicontext.MaxListLimit {
			break
		}
	}
	if !selectedTaskFound {
		return presentation.WorkspaceProjection{}, errors.New("Task is not owned by the selected FeatureArea")
	}
	sort.SliceStable(out.Tasks, func(i, j int) bool {
		if out.Tasks[i].UpdatedAt.Equal(out.Tasks[j].UpdatedAt) {
			return out.Tasks[i].ID < out.Tasks[j].ID
		}
		return out.Tasks[i].UpdatedAt.After(out.Tasks[j].UpdatedAt)
	})
	if input.FeatureAreaID == "" && input.TaskID == "" {
		return out, nil
	}
	presentation.SelectWorkspace(&out, input.FeatureAreaID, input.TaskID)
	if selectedProjectInspection != nil {
		out.SelectedTaskID = ""
		out.SelectedWork = projectInspectionSelectedOperation(ctx, service, input.AISessionID, *selectedProjectInspection)
		return out, nil
	}
	var projected presentation.Projection
	if out.SelectedTaskID != "" {
		taskReader, supported := service.(taskPresentationReader)
		if !supported {
			return presentation.WorkspaceProjection{}, errors.New("Task presentation unavailable")
		}
		projected, err = taskReader.TaskPresentationForSession(ctx, input.AISessionID, out.SelectedFeatureAreaID, out.SelectedTaskID)
	} else if out.SelectedFeatureAreaID != "" {
		projected, err = reader.FeatureAreaPresentationForSession(ctx, input.AISessionID, out.SelectedFeatureAreaID)
	} else {
		return out, nil
	}
	if err != nil {
		return presentation.WorkspaceProjection{}, err
	}
	out.Presentation = &projected
	if projected.TaskView != nil {
		out.SelectedWork = selectedOperationWithRecovery(presentation.SelectedOperationFromTaskView(*projected.TaskView), projected)
	}
	return out, nil
}

type agentExecutionInventoryReader interface {
	ListAgentExecutionsForSession(context.Context, string) ([]agentexecution.Projection, error)
}

func projectInspectionSelectedOperation(ctx context.Context, service BridgeService, sessionID string, owner task.Task) *presentation.SelectedOperation {
	reader, ok := service.(agentExecutionInventoryReader)
	if !ok {
		return nil
	}
	values, err := reader.ListAgentExecutionsForSession(ctx, sessionID)
	if err != nil {
		return nil
	}
	for _, value := range values {
		if value.Kind == agentexecution.KindInspection && value.TaskID == owner.ID && value.ProjectID == owner.ProjectID && value.FeatureAreaID == "" && (owner.WorkOwnerID == "" || value.SemanticOwnerID == owner.WorkOwnerID) {
			return presentation.SelectedOperationFromAgentExecution(value)
		}
	}
	return nil
}

func workspaceNavigationWork(value task.Task) task.Work {
	work := value.Work
	if work.Type == "" {
		work.Type = value.Type
	}
	if work.Type == "" {
		work.Type = task.TypeDevelopment
	}
	return work
}

func selectedOperationWithRecovery(op *presentation.SelectedOperation, value presentation.Projection) *presentation.SelectedOperation {
	if op == nil {
		return nil
	}
	if value.Recovery != nil {
		op.RecoveryRequired = value.Recovery.Required
	} else if value.TaskStages != nil && value.TaskStages.Recovery != nil {
		op.RecoveryRequired = value.TaskStages.Recovery.Required
	}
	return op
}

const TaskPresentationGetDescription = "Pure read of one opened Task: semantic progress, activity, goals, aggregate usage, result and exact actions. No project inventory or workflow mutation."

func RegisterTaskPresentation(server *sdkmcp.Server, service BridgeService) {
	registerTaskPresentation(server, service, mustProjectionOutputSchema())
}

func registerTaskPresentation(server *sdkmcp.Server, service BridgeService, outputSchema any) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "task_presentation_get", Title: "Read Task presentation", Meta: sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}, Description: TaskPresentationGetDescription, InputSchema: json.RawMessage(`{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","taskId"],"additionalProperties":false}`), OutputSchema: outputSchema, Annotations: toolAnnotations("Read Task presentation", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input devBridgeCardInput) (*sdkmcp.CallToolResult, presentation.Projection, error) {
		reader, ok := service.(taskPresentationReader)
		if !ok {
			return nil, presentation.Projection{}, errors.New("Task presentation unavailable")
		}
		v, err := reader.TaskPresentationForSession(ctx, input.AISessionID, input.FeatureAreaID, input.TaskID)
		if err != nil {
			return nil, v, publicToolError(err)
		}
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: presentation.RenderText(v)}}}, v, nil
	})

}

func (s *StaticService) ExactDevelopmentWorkspaceForSession(ctx context.Context, session, area, task string) (presentation.WorkspaceProjection, error) {
	if s.control == nil {
		return presentation.WorkspaceProjection{}, errors.New("Workspace presentation unavailable")
	}
	return s.control.ExactDevelopmentWorkspaceForSession(ctx, session, area, task)
}
