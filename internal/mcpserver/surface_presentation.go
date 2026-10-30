package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/aicontext"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// PresentationTarget names existing Core entities; it creates no operation or
// authorization registry. Optional fields are assertions, never selection hints.
type PresentationTarget = presentation.OperationTarget

type WorkspaceShowInput struct {
	AISessionID           string                     `json:"aiSessionId"`
	PresentationRequestID string                     `json:"presentationRequestId"`
	Display               string                     `json:"display,omitempty"`
	ProjectID             string                     `json:"projectId,omitempty"`
	FeatureAreaID         string                     `json:"featureAreaId,omitempty"`
	TaskID                string                     `json:"taskId,omitempty"`
	DecisionID            string                     `json:"decisionId,omitempty"`
	Target                *PresentationTarget        `json:"target,omitempty"`
	ActionTarget          *presentation.ActionTarget `json:"actionTarget,omitempty"`
}
type WorkspaceShowOutput struct {
	SinglePanel           bool                             `json:"singlePanel,omitempty"`
	AISessionID           string                           `json:"aiSessionId"`
	ProjectID             string                           `json:"projectId"`
	WorkspaceIdentity     string                           `json:"workspaceIdentity"`
	EntryIdentity         string                           `json:"entryIdentity"`
	PresentationRequestID string                           `json:"presentationRequestId"`
	PresentationMode      string                           `json:"presentationMode"`
	Display               string                           `json:"display"`
	HostVisibility        string                           `json:"hostVisibility"`
	FeatureAreaID         string                           `json:"featureAreaId,omitempty"`
	TaskID                string                           `json:"taskId,omitempty"`
	DecisionID            string                           `json:"decisionId,omitempty"`
	DecisionStatus        string                           `json:"decisionStatus,omitempty"`
	Target                *PresentationTarget              `json:"target,omitempty"`
	ActionTarget          *presentation.ActionTarget       `json:"actionTarget,omitempty"`
	ActionTargetStatus    string                           `json:"actionTargetStatus,omitempty"`
	ActionTargetKind      string                           `json:"actionTargetKind,omitempty"`
	OperationDetails      *presentation.SelectedOperation  `json:"-"`
	ReadOnly              bool                             `json:"readOnly"`
	Workspace             presentation.WorkspaceProjection `json:"-"`
}

const presentationTargetSchema = OperationTargetSchema

const surfaceProjectSelectionOutputSchema = `{"type":"object","properties":{"result":{"type":"object","properties":{` + sessionInitializationResultProperties + `,"presentationMode":{"type":"string","const":"PROJECT_SELECTION"},"presentationRequestId":{"type":"string"},"entryIdentity":{"type":"string"},"display":{"type":"string","const":"EXPANDED"},"hostVisibility":{"type":"string","const":"UNCONFIRMED"}},"required":["aiSessionId","state","boundProjectId","availableProjects","availableProjectsStatus","presentationMode","presentationRequestId","entryIdentity","display","hostVisibility"],"additionalProperties":false},"context":` + sessionIdentityContextSchema + `,"workflow":` + sessionIdentityWorkflowSchema + `,"presentationHint":` + surfacePresentationHintSchema + `},"required":["result","context","workflow","presentationHint"],"additionalProperties":false}`
const workspaceShowSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"presentationRequestId":{"type":"string","minLength":1,"maxLength":128},"display":{"type":"string","enum":["COLLAPSED","EXPANDED"],"default":"COLLAPSED"},"projectId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128},"decisionId":{"type":"string","minLength":1,"maxLength":256},"target":` + presentationTargetSchema + `,"actionTarget":` + surfaceActionTargetSchema + `},"required":["aiSessionId","presentationRequestId"],"additionalProperties":false}`

func RegisterSurfacePresentationTools(server *sdkmcp.Server, service BridgeService) {
	for _, name := range []string{"workspace_show", "details_show"} {
		schema := workspaceShowSchema
		description := "For a Project already explicitly selected and actually bound in this conversation, show one persistent Task panel. Its progress and navigation remain read-only. For an exact selected Task, the panel automatically lists prepared formal confirmations from the same Session and Task; each has an isolated immutable intent/version/contentHash renderer and commits only after the user clicks its own formal confirmation. When that panel is already open, prepare the next operation and let its list refresh instead of emitting another confirmation_show Card. The model must never call confirmation_commit on the user's behalf. Without a panel use confirmation_show as the ordinary fallback. Read session_initialize with the already known aiSessionId when binding is uncertain. If no Session exists, the model first calls session_initialize(requestId), then uses the client-specific selection flow with that real Session: ChatGPT project_setup_prepare then confirmation_show, user confirmation and confirmation_get; Local/Desktop retain their canonical project-selection controls. Reuse PROJECT_BOUND aiSessionId/projectId; never create a replacement for an invalid Session. Default COLLAPSED; explicit user show/open uses EXPANDED. Opening only a Project selects no FeatureArea, Task, Decision or actionTarget. Supply exact additional targets only for an explicit selection or authorized same-entry navigation. Copy action targets from authoritative projections. Retrying one presentationRequestId retains the complete identical initialization; a new entry cannot borrow old widget state. Display is not approval or proof of host visibility. When workflow.nextStep is present, summarize its label and summary as the user-facing next step; it is read-only next-step metadata and does not grant execution authority. A recovery recommendation applies only to its exact Run with an unconfirmed native result; known native success or failure belongs to analysis or workflow processing, not result recovery."
		if name == "details_show" {
			description = "Open exact read-only FeatureArea, Task, Decision or Operation details in the production component. Requires at least one exact target; does not change business focus or grant authority."
			schema = strings.TrimSuffix(schema, "}") + `,"anyOf":[{"required":["featureAreaId"]},{"required":["taskId"]},{"required":["decisionId"]},{"required":["target"]}]}`
		}
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: name, Description: description, InputSchema: json.RawMessage(schema), OutputSchema: workspaceShowOutputSchema(), Meta: primaryCardMeta(devBridgeCardResourceURI), Annotations: toolAnnotations("Show Dev Bridge", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in WorkspaceShowInput) (*sdkmcp.CallToolResult, WorkspaceShowOutput, error) {
			if name == "details_show" && in.FeatureAreaID == "" && in.TaskID == "" && in.DecisionID == "" && in.Target == nil {
				return nil, WorkspaceShowOutput{}, errors.New("details_show requires an exact target")
			}
			out, err := workspaceShowCore(ctx, service, in, name == "details_show")
			if err != nil {
				return nil, out, publicToolError(err)
			}
			return &sdkmcp.CallToolResult{Meta: workspaceShowMeta(out)}, out, nil
		})
	}
	type selectionInput struct {
		AISessionID           string `json:"aiSessionId"`
		PresentationRequestID string `json:"presentationRequestId"`
	}
	type selectionIdentity struct {
		aicontext.InitializationResult
		PresentationMode      string `json:"presentationMode"`
		PresentationRequestID string `json:"presentationRequestId"`
		EntryIdentity         string `json:"entryIdentity"`
		Display               string `json:"display"`
		HostVisibility        string `json:"hostVisibility"`
	}
	type selectionOutput struct {
		Result           selectionIdentity       `json:"result"`
		Context          SessionBindingContext   `json:"context"`
		Workflow         sessionIdentityWorkflow `json:"workflow"`
		PresentationHint SurfacePresentationHint `json:"presentationHint"`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "project_selection_show", Description: "Compatibility display of Project candidates for an EXISTING real aiSessionId returned by session_initialize. Prefer panel_show once on entry. Display is read-only; the unified panel can open the formal SELECT_PROJECT form on an explicit user click. The user selects and confirms there; do not require a GPT message or a removed direct project_open tool. Call session_initialize first if no Session is known; this Card never initializes a Session or creates a selectionHandle. It only reads this Session's current binding and bounded candidates. Before the user chooses, project_open is never called, even with one Project. Binding uses project_setup_prepare and the exact formal user confirmation for the SAME Session; reading or showing candidates never binds. Reuse presentationRequestId only for this exact entry; remount reads the same Session and cannot borrow old widget identity. Bound Project, candidate Projects, inventory availability and engineering readiness are separate facts. Display is not authorization or proof of host visibility; browsing does not push an operation target or automatically wake GPT.", InputSchema: json.RawMessage(`{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128,"pattern":"^\\S+$"},"presentationRequestId":{"type":"string","minLength":1,"maxLength":128,"pattern":"^\\S+$"}},"required":["aiSessionId","presentationRequestId"],"additionalProperties":false}`), OutputSchema: json.RawMessage(surfaceProjectSelectionOutputSchema), Meta: primaryCardMeta(devBridgeCardResourceURI), Annotations: toolAnnotations("Show Project selection", true, true)}, func(ctx context.Context, request *sdkmcp.CallToolRequest, in selectionInput) (*sdkmcp.CallToolResult, selectionOutput, error) {
		if in.AISessionID == "" {
			return nil, selectionOutput{}, errors.New("SESSION_INITIALIZATION_REQUIRED: call session_initialize before showing Project selection")
		}
		if !validSessionInputID(in.AISessionID) || !validSessionInputID(in.PresentationRequestID) {
			return nil, selectionOutput{}, errors.New("valid aiSessionId and presentationRequestId are required")
		}
		ctx, err := withSessionRequest(ctx, request)
		if err != nil {
			return nil, selectionOutput{}, publicToolError(err)
		}
		initialized, err := SessionInitializeCore(ctx, service, SessionInitializeInput{AISessionID: in.AISessionID})
		if err != nil {
			return nil, selectionOutput{}, err
		}
		out := selectionOutput{Result: selectionIdentity{InitializationResult: initialized.Result, PresentationMode: "PROJECT_SELECTION", PresentationRequestID: in.PresentationRequestID, EntryIdentity: "entry:" + presentationHash("selection:"+in.AISessionID+":"+in.PresentationRequestID), Display: "EXPANDED", HostVisibility: "UNCONFIRMED"}, Context: SessionBindingContext{AISessionID: initialized.Result.AISessionID}, Workflow: initialized.Workflow, PresentationHint: SurfacePresentationHint{Required: false, Reason: "PRESENTATION_RETURNED", Tool: "project_selection_show", Display: "EXPANDED", HostVisibility: "UNCONFIRMED"}}
		return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"singlePanel": true, "presentationMode": out.Result.PresentationMode, "aiSessionId": out.Result.AISessionID, "presentationRequestId": out.Result.PresentationRequestID, "entryIdentity": out.Result.EntryIdentity, "display": out.Result.Display}}, out, nil
	})
}

type workspacePresentationResult WorkspaceShowOutput
type surfaceShowPending struct {
	Kind           string                     `json:"kind"`
	DecisionID     string                     `json:"decisionId,omitempty"`
	TaskID         string                     `json:"taskId,omitempty"`
	FeatureAreaID  string                     `json:"featureAreaId,omitempty"`
	SemanticState  string                     `json:"semanticState"`
	Summary        string                     `json:"summary,omitempty"`
	ActionTarget   *presentation.ActionTarget `json:"actionTarget,omitempty"`
	AllowedActions []string                   `json:"allowedActions,omitempty"`
	Options        []presentation.Capability  `json:"options"`
}
type surfaceShowWorkflow struct {
	NextStep         *presentation.TaskNextStep `json:"nextStep"`
	State            string                     `json:"state"`
	Phase            string                     `json:"phase,omitempty"`
	Activity         *presentation.TaskActivity `json:"activity,omitempty"`
	Generation       string                     `json:"generation,omitempty"`
	SourceSequence   int64                      `json:"sourceSequence"`
	NextAction       wp.Action                  `json:"nextAction"`
	Blockers         []string                   `json:"blockers"`
	PendingDecisions []surfaceShowPending       `json:"pendingDecisions"`
	RefreshRequired  bool                       `json:"refreshRequired"`
	Partitions       []SurfaceReadPartition     `json:"partitions"`
}
type workspacePresentationEnvelope struct {
	Result           workspacePresentationResult `json:"result"`
	Context          SurfaceContext              `json:"context"`
	Workflow         surfaceShowWorkflow         `json:"workflow"`
	PresentationHint SurfacePresentationHint     `json:"presentationHint"`
}

func (out WorkspaceShowOutput) MarshalJSON() ([]byte, error) {
	workflow := surfaceWorkflow()
	if out.Workspace.Presentation != nil {
		workflow = projectSurfaceWorkflow(*out.Workspace.Presentation)
	}
	workflow.RefreshRequired = out.Workspace.ReadError != "" || out.Workspace.SelectedWorkReadError != "" || out.Workspace.PendingDecisionsReadError != ""
	show := compactShowWorkflow(out, workflow)
	hint := SurfacePresentationHint{Required: false, Reason: "PRESENTATION_RETURNED", Tool: "workspace_show", Display: out.Display, HostVisibility: "UNCONFIRMED"}
	if out.ReadOnly {
		hint.Tool = "details_show"
	}
	if out.SinglePanel {
		hint.Tool = "panel_show"
	}
	return json.Marshal(workspacePresentationEnvelope{Result: workspacePresentationResult(out), Context: SurfaceContext{AISessionID: out.AISessionID, ProjectID: out.ProjectID, FeatureAreaID: out.FeatureAreaID, TaskID: out.TaskID, Target: out.Target}, Workflow: show, PresentationHint: hint})
}

func compactShowWorkflow(out WorkspaceShowOutput, workflow SurfaceWorkflow) surfaceShowWorkflow {
	readAt := out.Workspace.RefreshedAt
	show := surfaceShowWorkflow{State: workflow.State, Phase: workflow.Phase, NextAction: workflow.NextAction, Blockers: append([]string(nil), workflow.Blockers...), PendingDecisions: []surfaceShowPending{}, RefreshRequired: workflow.RefreshRequired, Partitions: []SurfaceReadPartition{{Name: "WORKSPACE", ReadAt: readAt}, {Name: "SELECTED_WORK", ReadAt: readAt}, {Name: "PENDING_DECISIONS", ReadAt: readAt}}}
	if out.Workspace.Presentation != nil {
		show.Generation, show.SourceSequence = out.Workspace.Presentation.Generation, out.Workspace.Presentation.SourceSequence
	}
	if out.Workspace.Presentation != nil && out.Workspace.Presentation.TaskView != nil {
		show.Phase = string(out.Workspace.Presentation.TaskView.Phase)
		view := out.Workspace.Presentation.TaskView
		if !out.ReadOnly && !show.RefreshRequired && out.TaskID == view.ID && out.DecisionID == "" {
			show.NextStep = view.NextStep
		}
		activity := out.Workspace.Presentation.TaskView.Activity
		show.Activity = &presentation.TaskActivity{Status: activity.Status, Summary: activity.Summary}
	}
	if out.Workspace.ReadError != "" {
		show.Partitions[0].Stale, show.Partitions[0].ReadError = true, out.Workspace.ReadError
	}
	if out.Workspace.SelectedWorkReadError != "" {
		show.Partitions[1].Stale, show.Partitions[1].ReadError = true, out.Workspace.SelectedWorkReadError
	}
	if out.Workspace.PendingDecisionsReadError != "" {
		show.Partitions[2].Stale, show.Partitions[2].ReadError = true, out.Workspace.PendingDecisionsReadError
	}
	if !out.ReadOnly && !show.RefreshRequired && (out.DecisionID == "" || out.DecisionStatus == "PENDING") {
		// The selected projection carries authoritative gates independently of the
		// Project inbox and TaskView.Actions. Preserve every exact option in the
		// compact text/JSON handoff as well as in the full widget snapshot.
		pending := append([]presentation.PendingDecision(nil), out.Workspace.PendingDecisions...)
		for _, projected := range workflow.PendingDecisions {
			matched := false
			for i, item := range pending {
				if item.Decision != nil && projected.Decision != nil && item.Decision.ID == projected.Decision.ID && item.TaskID == projected.TaskID && item.FeatureAreaID == projected.FeatureAreaID {
					matched = true
					pending[i].Options = append(append([]presentation.Capability(nil), item.Options...), projected.Options...)
					break
				}
			}
			if !matched {
				pending = append(pending, projected)
			}
		}
		for _, item := range pending {
			if selected := out.Workspace.Presentation; selected != nil && selected.Details.TaskID == item.TaskID && (item.Kind == "REVIEW_APPROVAL" || item.Kind == "TASK_ACCEPTANCE") {
				if selected.PendingDecision != nil && (item.Decision == nil || selected.PendingDecision.ID != item.Decision.ID) {
					continue
				}
				if selected.TaskView != nil {
					phase := string(selected.TaskView.Phase)
					if item.Kind == "REVIEW_APPROVAL" && phase != "REVIEW_APPROVAL_PENDING" {
						continue
					}
				}
			}
			if out.Target != nil && !pendingMatchesOperation(item, *out.Target) {
				continue
			}
			if out.ActionTarget != nil && !pendingAuthorizesActionTarget(item, out.ActionTarget, out.FeatureAreaID, out.TaskID) {
				continue
			}
			if out.DecisionID != "" && (item.Decision == nil || item.Decision.ID != out.DecisionID) {
				continue
			}
			if item.TaskID != out.TaskID {
				continue
			}
			if item.FeatureAreaID != out.FeatureAreaID {
				continue
			}
			v := surfaceShowPending{Kind: item.Kind, TaskID: item.TaskID, FeatureAreaID: item.FeatureAreaID, SemanticState: item.SemanticState, Options: []presentation.Capability{}}
			if item.Decision != nil {
				v.DecisionID, v.Summary = item.Decision.ID, boundedUTF8(item.Decision.Summary, 512)
				v.AllowedActions = append([]string(nil), item.Decision.AllowedActions...)
			}
			seenOptions := map[string]bool{}
			for _, option := range item.Options {
				if option.Target == nil || item.Decision != nil && option.Target.DecisionID != item.Decision.ID {
					continue
				}
				encoded, _ := json.Marshal(option)
				if seenOptions[string(encoded)] {
					continue
				}
				seenOptions[string(encoded)] = true
				v.Options = append(v.Options, option)
				if v.ActionTarget == nil && option.Available {
					copy := *option.Target
					v.ActionTarget = &copy
				}
			}
			show.PendingDecisions = append(show.PendingDecisions, v)
		}
	}
	return show
}
func workspaceShowOutputSchema() any {
	return json.RawMessage(surfaceOutputSchema[workspacePresentationEnvelope]())
}

func workspaceShowMeta(out WorkspaceShowOutput) sdkmcp.Meta {
	raw, _ := json.Marshal(workspacePresentationResult(out))
	var meta sdkmcp.Meta
	_ = json.Unmarshal(raw, &meta)
	meta["presentationId"] = out.WorkspaceIdentity
	meta["workspace"] = out.Workspace
	meta["singlePanel"] = true
	if out.OperationDetails != nil {
		meta["operationDetails"] = out.OperationDetails
	}
	return meta
}
func workspaceShowCore(ctx context.Context, service BridgeService, in WorkspaceShowInput, readOnly bool) (WorkspaceShowOutput, error) {
	if strings.TrimSpace(in.AISessionID) == "" || strings.TrimSpace(in.PresentationRequestID) == "" {
		return WorkspaceShowOutput{}, errors.New("valid aiSessionId and presentationRequestId are required")
	}
	if in.Display == "" {
		in.Display = "COLLAPSED"
	}
	if in.Display != "COLLAPSED" && in.Display != "EXPANDED" {
		return WorkspaceShowOutput{}, errors.New("invalid display")
	}
	identity, err := validatePresentationIdentity(ctx, service, devBridgeCardInput{AISessionID: in.AISessionID, ProjectID: in.ProjectID, FeatureAreaID: in.FeatureAreaID, TaskID: in.TaskID, DecisionID: in.DecisionID, Target: in.Target, ActionTarget: in.ActionTarget})
	if err != nil {
		return WorkspaceShowOutput{}, err
	}
	projectOnly := in.FeatureAreaID == "" && in.TaskID == "" && in.DecisionID == "" && in.Target == nil && in.ActionTarget == nil
	if presentationReplayExact(ctx) || projectOnly {
		// Project-only entry is an explicit absence of a work target. Pending
		// inventory and recent work may be shown, but cannot choose its focus.
		identity.SelectionMode = "EXACT"
	}
	workspace, err := devBridgeCardProjection(ctx, service, identity)
	if err != nil {
		return WorkspaceShowOutput{}, err
	}
	if workspace.DevelopmentContext.Project.ID != identity.ProjectID {
		return WorkspaceShowOutput{}, errors.New("PROJECT_BINDING_MISMATCH: workspace and Session disagree")
	}
	if projectOnly && (workspace.SelectedFeatureAreaID != "" || workspace.SelectedTaskID != "" || workspace.SelectedWork != nil || workspace.Presentation != nil) {
		return WorkspaceShowOutput{}, errors.New("TARGET_MISMATCH: project-only workspace selected a work target")
	}
	if identity.TaskID != "" && workspace.SelectedTaskID != identity.TaskID && !workspaceSelectsInspection(workspace, identity.Target) {
		return WorkspaceShowOutput{}, errors.New("TARGET_MISMATCH: requested Task was not selected")
	}
	if identity.FeatureAreaID != "" && workspace.SelectedFeatureAreaID != identity.FeatureAreaID {
		return WorkspaceShowOutput{}, errors.New("TARGET_MISMATCH: requested FeatureArea was not selected")
	}
	// Preserve validated explicit scope and the selected projection identity.
	// Project-only entries remain unselected; later component reads retain
	// exact navigation without consulting a remembered recent work target.
	out := WorkspaceShowOutput{AISessionID: in.AISessionID, ProjectID: identity.ProjectID, WorkspaceIdentity: presentation.WorkspaceIdentity(in.AISessionID, identity.ProjectID), EntryIdentity: "entry:" + presentationHash(in.AISessionID+":"+in.PresentationRequestID), PresentationRequestID: in.PresentationRequestID, PresentationMode: "WORKSPACE", Display: in.Display, HostVisibility: "UNCONFIRMED", FeatureAreaID: workspace.SelectedFeatureAreaID, TaskID: workspace.SelectedTaskID, DecisionID: in.DecisionID, Target: in.Target, ActionTarget: in.ActionTarget, ReadOnly: readOnly, Workspace: workspace}
	if readOnly {
		out.PresentationMode = "DETAILS"
	}
	out.ActionTargetStatus = presentationActionTargetStatus(workspace, identity.ActionTarget)
	out.ActionTargetKind = presentationActionTargetKind(workspace, identity.ActionTarget)
	out.DecisionStatus = presentationDecisionStatus(ctx, service, in.AISessionID, workspace, in.DecisionID)
	if readOnly && in.Target != nil {
		exact, readErr := OperationGetCore(ctx, service, OperationGetInput{AISessionID: in.AISessionID, Target: *in.Target})
		if readErr != nil {
			return WorkspaceShowOutput{}, readErr
		}
		copy := exact.Result.Operation
		out.OperationDetails = &copy
	}
	return out, nil
}
func presentationDecisionStatus(ctx context.Context, service BridgeService, sessionID string, workspace presentation.WorkspaceProjection, decisionID string) string {
	if decisionID == "" {
		return ""
	}
	if workspace.PendingDecisionsReadError != "" || workspace.ReadError != "" {
		return "UNAVAILABLE"
	}
	for _, item := range workspace.PendingDecisions {
		if item.Decision != nil && item.Decision.ID == decisionID {
			return "PENDING"
		}
	}
	if workspace.Presentation != nil && workspace.Presentation.PendingDecision != nil && workspace.Presentation.PendingDecision.ID == decisionID {
		return "PENDING"
	}
	if kind, approvalID, ok := strings.Cut(decisionID, ":"); ok && kind == "RUNTIME_APPROVAL" && approvalID != "" {
		value, err := service.GetApprovalForSession(ctx, sessionID, approvalID)
		if err != nil {
			return "HISTORICAL_STATUS_UNAVAILABLE"
		}
		if value.ID != approvalID {
			return "HISTORICAL_STATUS_UNAVAILABLE"
		}
		return string(value.Status)
	}
	if receipt, err := service.GetDecisionResolution(ctx, sessionID, decisionID); err == nil && receipt.DecisionID == decisionID && receipt.ProjectID == workspace.DevelopmentContext.Project.ID && !receipt.ResolvedAt.IsZero() {
		return "RESOLVED"
	}
	// A different explicit source on the same authoritative Task proves this
	// gate was replaced. It does not tell us how the old gate was resolved.
	kind, sourceID, _ := strings.Cut(decisionID, ":")
	owner := ""
	if kind == "REVIEW_APPROVAL" {
		if source, err := service.GetReview(ctx, sessionID, sourceID); err == nil && source.ID == sourceID {
			owner = source.TaskID
		}
	} else if kind == "TASK_ACCEPTANCE" {
		if source, err := service.GetExecutionProposal(ctx, sessionID, sourceID); err == nil && source.ID == sourceID {
			owner = source.TaskID
		}
	}
	if owner != "" {
		if current, err := service.GetTask(ctx, sessionID, owner); err == nil && current.ID == owner && current.ProjectID == workspace.DevelopmentContext.Project.ID {
			if kind == "REVIEW_APPROVAL" && current.CurrentReviewID != "" && current.CurrentReviewID != sourceID || kind == "TASK_ACCEPTANCE" && current.CurrentExecutionID != "" && current.CurrentExecutionID != sourceID {
				return "SUPERSEDED"
			}
		}
	}
	// Absence from a current pending inventory cannot prove a historical gate
	// completed: a gate may be superseded or its history read unavailable.
	return "HISTORICAL_STATUS_UNAVAILABLE"
}

// Status describes only this exact action target. A failed fact read cannot
// establish that it completed or expired; absence after a successful read only
// establishes that the target is not presently actionable.
func presentationActionTargetStatus(workspace presentation.WorkspaceProjection, target *presentation.ActionTarget) string {
	if target == nil {
		return ""
	}
	if presentationActionFactsUnavailable(workspace, target) {
		return "UNAVAILABLE"
	}
	if workspaceAuthorizesActionTarget(workspace, target) {
		return "PENDING"
	}
	return "NOT_PENDING"
}

// Kind comes from the exact authoritative pending item when available. An
// operation-only or historical target has a neutral operation label; neither
// an ID substring nor the mere presence of a target proves a Runtime Request.
func presentationActionTargetKind(workspace presentation.WorkspaceProjection, target *presentation.ActionTarget) string {
	if target == nil {
		return ""
	}
	kind := ""
	accept := func(item presentation.PendingDecision) {
		if !pendingMatchesActionTarget(item, target, workspace.SelectedFeatureAreaID, workspace.SelectedTaskID, false) {
			return
		}
		switch item.Kind {
		case "RUNTIME_REQUEST", "RUNTIME_APPROVAL", "REVIEW_APPROVAL", "TASK_ACCEPTANCE":
			if kind == "" {
				kind = item.Kind
			} else if kind != item.Kind {
				kind = "ACTION"
			}
		}
	}
	if workspace.ReadError == "" && workspace.SelectedWorkReadError == "" && workspace.PendingDecisionsReadError == "" {
		for _, item := range workspace.PendingDecisions {
			accept(item)
		}
		if value := workspace.Presentation; value != nil && value.PendingDecision != nil {
			accept(presentation.PendingDecision{Kind: value.PendingDecision.Kind, FeatureAreaID: value.Details.FeatureAreaID, TaskID: value.Details.TaskID, Decision: value.PendingDecision, Options: value.Capabilities})
		}
	}
	if kind != "" {
		return kind
	}
	switch target.OperationKind {
	case "REVIEW":
		return "REVIEW_OPERATION"
	case "EXECUTION":
		return "EXECUTION_OPERATION"
	case "TASK":
		return "TASK_OPERATION"
	default:
		return "ACTION"
	}
}

// Task action read failures cannot be replaced by a pending inventory option.
// Runtime requests remain an independent authority with an exact typed identity.
func presentationActionFactsUnavailable(workspace presentation.WorkspaceProjection, target *presentation.ActionTarget) bool {
	if workspace.ReadError != "" || workspace.SelectedWorkReadError != "" || workspace.PendingDecisionsReadError != "" {
		return true
	}
	value := workspace.Presentation
	if value == nil || value.Details.FeatureAreaID != workspace.SelectedFeatureAreaID || value.Details.TaskID != workspace.SelectedTaskID {
		return false
	}
	if value.Guidance != nil && value.Guidance.RefreshRequired {
		return true
	}
	kind := presentationActionTargetKind(workspace, target)
	if kind == "RUNTIME_REQUEST" || kind == "RUNTIME_APPROVAL" {
		return false
	}
	view := value.TaskView
	return view != nil && view.ID == workspace.SelectedTaskID && (view.ActionsStatus != "AVAILABLE" || view.ActionsReadError != "")
}

func workspaceAuthorizesActionTarget(workspace presentation.WorkspaceProjection, wanted *presentation.ActionTarget) bool {
	if wanted == nil {
		return true
	}
	if presentationActionFactsUnavailable(workspace, wanted) {
		return false
	}
	if wanted.TaskID != "" && wanted.TaskID != workspace.SelectedTaskID || wanted.FeatureAreaID != "" && wanted.FeatureAreaID != workspace.SelectedFeatureAreaID {
		return false
	}
	for _, pending := range workspace.PendingDecisions {
		if pendingAuthorizesActionTarget(pending, wanted, workspace.SelectedFeatureAreaID, workspace.SelectedTaskID) {
			return true
		}
	}
	if v := workspace.Presentation; v != nil && v.Details.FeatureAreaID == workspace.SelectedFeatureAreaID && v.Details.TaskID == workspace.SelectedTaskID {
		pending := presentation.PendingDecision{FeatureAreaID: v.Details.FeatureAreaID, TaskID: v.Details.TaskID, Decision: v.PendingDecision, Options: v.Capabilities}
		if v.PendingDecision != nil {
			pending.Kind = v.PendingDecision.Kind
		}
		return pendingAuthorizesActionTarget(pending, wanted, workspace.SelectedFeatureAreaID, workspace.SelectedTaskID)
	}
	return false
}

// Review targets from FeatureArea and Task projections differ only in optional
// owner fields. Resolve those from the exact authoritative owner, never from a
// project inbox neighbour. Runtime/operation fields retain exact equality.
func pendingAuthorizesActionTarget(item presentation.PendingDecision, wanted *presentation.ActionTarget, areaID, taskID string) bool {
	return pendingMatchesActionTarget(item, wanted, areaID, taskID, true)
}

// Descriptive identity matching does not require availability. Only the
// authorization wrapper requires an available exact option.
func pendingMatchesActionTarget(item presentation.PendingDecision, wanted *presentation.ActionTarget, areaID, taskID string, requireAvailable bool) bool {
	if wanted == nil || item.FeatureAreaID != areaID || item.TaskID != taskID {
		return false
	}
	businessGate := wanted.DecisionID != "" && (item.Kind == "REVIEW_APPROVAL" || item.Kind == "TASK_ACCEPTANCE")
	if businessGate && (taskID == "" || item.Decision == nil || wanted.DecisionID != item.Decision.ID || wanted.SubjectID != item.Decision.SubjectID) {
		return false
	}
	if businessGate && item.Kind == "REVIEW_APPROVAL" && item.Decision.SubjectID != taskID {
		return false
	}
	normalize := func(target presentation.ActionTarget) presentation.ActionTarget {
		if businessGate {
			if target.TaskID == "" {
				target.TaskID = taskID
			}
			if target.FeatureAreaID == "" {
				target.FeatureAreaID = areaID
			}
		}
		return target
	}
	exact := normalize(*wanted)
	if exact.TaskID != "" && exact.TaskID != taskID || exact.FeatureAreaID != "" && exact.FeatureAreaID != areaID {
		return false
	}
	for _, option := range item.Options {
		if (!requireAvailable || option.Available) && option.Target != nil && reflect.DeepEqual(normalize(*option.Target), exact) {
			return true
		}
	}
	return false
}

func validatePresentationIdentity(ctx context.Context, service BridgeService, in devBridgeCardInput) (devBridgeCardInput, error) {
	if strings.TrimSpace(in.AISessionID) == "" {
		return in, errors.New("SESSION_REQUIRED: workspace requires a valid aiSessionId")
	}
	session, err := service.GetSession(ctx, in.AISessionID)
	if err != nil {
		return in, err
	}
	if session.Session.ID != in.AISessionID || session.Project.ID == "" || session.Session.ProjectID != session.Project.ID {
		return in, errors.New("SESSION_BINDING_UNAVAILABLE")
	}
	if in.ProjectID != "" && in.ProjectID != session.Project.ID {
		return in, errors.New("PROJECT_BINDING_MISMATCH")
	}
	in.ProjectID = session.Project.ID
	assertTask := func(taskID string) error {
		if in.TaskID != "" && in.TaskID != taskID {
			return errors.New("TARGET_TASK_MISMATCH")
		}
		in.TaskID = taskID
		return nil
	}
	if exact := in.ActionTarget; exact != nil {
		if exact.TaskID != "" {
			if err := assertTask(exact.TaskID); err != nil {
				return in, err
			}
		}
		if exact.FeatureAreaID != "" {
			if in.FeatureAreaID != "" && in.FeatureAreaID != exact.FeatureAreaID {
				return in, errors.New("ACTION_FEATURE_AREA_MISMATCH")
			}
			in.FeatureAreaID = exact.FeatureAreaID
		}
		if exact.DecisionID != "" && in.DecisionID != "" && exact.DecisionID != in.DecisionID {
			return in, errors.New("ACTION_DECISION_MISMATCH")
		}
	}
	if in.DecisionID != "" {
		kind, id, ok := strings.Cut(in.DecisionID, ":")
		if !ok || id == "" {
			return in, errors.New("invalid decision target")
		}
		switch kind {
		case "REVIEW_APPROVAL":
			value, e := service.GetReview(ctx, in.AISessionID, id)
			if e != nil {
				return in, e
			}
			if e = assertTask(value.TaskID); e != nil {
				return in, e
			}
			if in.Target != nil && (in.Target.Kind != "REVIEW" || in.Target.OperationID != id) {
				return in, errors.New("DECISION_OPERATION_MISMATCH")
			}
		case "TASK_ACCEPTANCE":
			value, e := service.GetExecutionProposal(ctx, in.AISessionID, id)
			if e != nil {
				return in, e
			}
			if e = assertTask(value.TaskID); e != nil {
				return in, e
			}
			if in.Target != nil && (in.Target.Kind != "EXECUTION" || in.Target.OperationID != id) {
				return in, errors.New("DECISION_OPERATION_MISMATCH")
			}
		case "RUNTIME_APPROVAL":
			value, e := service.GetApprovalForSession(ctx, in.AISessionID, id)
			if e != nil {
				return in, e
			}
			valueRun, e := service.GetRunForSession(ctx, in.AISessionID, value.RunID)
			if e != nil {
				return in, e
			}
			if e = assertTask(valueRun.TaskID); e != nil {
				return in, e
			}
		default:
			return in, fmt.Errorf("unsupported decision target kind %s", kind)
		}
	}
	if target := in.Target; target != nil {
		if strings.TrimSpace(target.OperationID) == "" {
			return in, errors.New("operationId is required")
		}
		owner := ""
		runID := ""
		switch target.Kind {
		case "REVIEW":
			value, e := service.GetReview(ctx, in.AISessionID, target.OperationID)
			if e != nil {
				return in, e
			}
			owner, runID = value.TaskID, value.RunID
		case "EXECUTION":
			value, e := service.GetExecutionProposal(ctx, in.AISessionID, target.OperationID)
			if e != nil {
				return in, e
			}
			owner = value.TaskID
			if target.RunID != "" && target.RunID != value.ImplementationRunID && target.RunID != value.VerificationRunID {
				return in, errors.New("OPERATION_RUN_MISMATCH")
			}
			runID = target.RunID
		case "INSPECTION":
			reader, ok := service.(agentExecutionInventoryReader)
			if !ok {
				return in, errors.New("inspection read unavailable")
			}
			values, e := reader.ListAgentExecutionsForSession(ctx, in.AISessionID)
			if e != nil {
				return in, e
			}
			found := false
			for _, v := range values {
				if v.Kind == agentexecution.KindInspection && v.SemanticOwnerID == target.OperationID {
					found = true
					owner, runID = v.TaskID, v.RunID
					break
				}
			}
			if !found {
				return in, errors.New("inspection target not found")
			}
		default:
			return in, errors.New("invalid operation target kind")
		}
		if target.OwnerTaskID != "" && target.OwnerTaskID != owner {
			return in, errors.New("OPERATION_OWNER_MISMATCH")
		}
		if target.RunID != "" && target.RunID != runID {
			return in, errors.New("OPERATION_RUN_MISMATCH")
		}
		if err = assertTask(owner); err != nil {
			return in, err
		}
	}
	if in.TaskID != "" {
		value, e := service.GetTask(ctx, in.AISessionID, in.TaskID)
		if e != nil {
			return in, e
		}
		if value.ProjectID != in.ProjectID {
			return in, errors.New("TASK_PROJECT_MISMATCH")
		}
		if in.FeatureAreaID != "" && in.FeatureAreaID != value.FeatureAreaID {
			return in, errors.New("TASK_FEATURE_AREA_MISMATCH")
		}
		in.FeatureAreaID = value.FeatureAreaID
	}
	if in.FeatureAreaID != "" {
		value, e := service.GetFeatureArea(ctx, in.AISessionID, in.FeatureAreaID)
		if e != nil {
			return in, e
		}
		if value.ProjectID != in.ProjectID {
			return in, errors.New("FEATURE_AREA_PROJECT_MISMATCH")
		}
	}
	return in, nil
}

func workspaceSelectsInspection(workspace presentation.WorkspaceProjection, target *PresentationTarget) bool {
	return target != nil && target.Kind == "INSPECTION" && workspace.SelectedTaskID == "" && workspace.SelectedWork != nil && workspace.SelectedWork.Kind == "INSPECTION" && workspace.SelectedWork.OperationID == target.OperationID
}
