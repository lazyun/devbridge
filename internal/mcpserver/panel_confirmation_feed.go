package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/controlapi"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *StaticService) PanelConfirmationFeed(ctx context.Context, session string) (controlapi.PanelConfirmationFeed, error) {
	if s.control == nil {
		return controlapi.PanelConfirmationFeed{}, errors.New("panel feed unavailable")
	}
	return s.control.PanelConfirmationFeed(ctx, session)
}

func (s *StaticService) PanelConfirmationFeedForTask(ctx context.Context, session, taskID string, requests ...string) (controlapi.PanelConfirmationFeed, error) {
	if s.control == nil {
		return controlapi.PanelConfirmationFeed{}, errors.New("panel feed unavailable")
	}
	return s.control.PanelConfirmationFeedForTask(ctx, session, taskID, requests...)
}

func registerPanelConfirmationFeed(server *sdkmcp.Server, service BridgeService) {
	navigation := newPanelNavigationStore()
	registerPanelFocus(server, service, navigation)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "panel_show", Title: "Open the Dev Bridge side panel", Description: "Open the ONE persistent Dev Bridge panel for this conversation's existing aiSessionId. Call once on entry after session_initialize. If the user explicitly asks to reopen a hidden or closed workbench, call panel_show again with the SAME existing aiSessionId and a fresh presentationRequestId; this restores presentation only, preserves the original Task and history, and grants no business authority. Do not reopen automatically after ordinary reads, replies or phase changes. All later prepared confirmations, Project setup, FeatureArea/Task creation, review, execution, results and history stay inside this panel. Never call confirmation_show, details_show or workspace_show to emit additional chat Cards. If already open, switch an explicitly requested Area/Task using silent panel_focus; use silent reads/prepares and let its Session operation list refresh. Supply Project/Area/Task only when explicitly selected; no implicit latest Task. The panel requests the host's PiP mode; display availability is not approval, execution or evidence of host visibility. Without a bound Project omit all work targets; its 选择项目 button opens the formal project picker directly, without a GPT message. Only the user confirmation binds that same Session, and this panel observes binding without a new mount.", InputSchema: json.RawMessage(workspaceShowSchema), OutputSchema: workspaceShowOutputSchema(), Annotations: toolAnnotations("Open single side panel", true, true), Meta: primaryCardMeta(devBridgeCardResourceURI)}, func(ctx context.Context, request *sdkmcp.CallToolRequest, in WorkspaceShowInput) (*sdkmcp.CallToolResult, WorkspaceShowOutput, error) {
		if !validSessionInputID(in.AISessionID) || !validSessionInputID(in.PresentationRequestID) {
			return nil, WorkspaceShowOutput{}, errors.New("existing aiSessionId and presentationRequestId required")
		}
		var out WorkspaceShowOutput
		if in.ProjectID != "" {
			var err error
			in.Display = "EXPANDED"
			out, err = workspaceShowCore(ctx, service, in, false)
			if err != nil {
				return nil, out, err
			}
		} else {
			if in.FeatureAreaID != "" || in.TaskID != "" || in.DecisionID != "" || in.Target != nil || in.ActionTarget != nil {
				return nil, out, errors.New("work targets require the explicitly selected Project")
			}
			owned, err := withSessionRequest(ctx, request)
			if err != nil {
				return nil, out, publicToolError(err)
			}
			if _, err = SessionInitializeCore(owned, service, SessionInitializeInput{AISessionID: in.AISessionID}); err != nil {
				return nil, out, err
			}
			out = WorkspaceShowOutput{AISessionID: in.AISessionID, PresentationRequestID: in.PresentationRequestID, EntryIdentity: "entry:" + presentationHash("panel:"+in.AISessionID+":"+in.PresentationRequestID), PresentationMode: "PROJECT_SELECTION", Display: "EXPANDED", HostVisibility: "UNCONFIRMED"}
		}
		out.SinglePanel = true
		return &sdkmcp.CallToolResult{Meta: workspaceShowMeta(out)}, out, nil
	})
	type input struct {
		AISessionID       string `json:"aiSessionId"`
		TaskID            string `json:"taskId,omitempty"`
		ProposalRequestID string `json:"proposalRequestId,omitempty"`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "dev_bridge_panel_get", Title: "Read existing panel operations", Description: "App-only pure read of the caller-owned Session confirmation references and latest explicit panel focus, including Project setup and proposals before Task creation. It never selects a Task, prepares or submits an intent, or emits another Card.", InputSchema: json.RawMessage(`{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128},"proposalRequestId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId"],"additionalProperties":false}`), Annotations: toolAnnotations("Read panel operations", true, true), Meta: confirmationToolMeta(true)}, func(ctx context.Context, request *sdkmcp.CallToolRequest, in input) (*sdkmcp.CallToolResult, panelFeedOutput, error) {
		ctx, err := withSessionRequest(ctx, request)
		if err != nil {
			return nil, panelFeedOutput{}, publicToolError(err)
		}
		reader, ok := service.(interface {
			PanelConfirmationFeed(context.Context, string) (controlapi.PanelConfirmationFeed, error)
		})
		if !ok {
			return nil, panelFeedOutput{}, errors.New("panel feed unavailable")
		}
		var out controlapi.PanelConfirmationFeed
		if in.TaskID != "" || in.ProposalRequestID != "" {
			if focused, ok := service.(interface {
				PanelConfirmationFeedForTask(context.Context, string, string, ...string) (controlapi.PanelConfirmationFeed, error)
			}); ok {
				out, err = focused.PanelConfirmationFeedForTask(ctx, in.AISessionID, in.TaskID, in.ProposalRequestID)
			} else {
				return nil, panelFeedOutput{}, errors.New("focused panel feed unavailable")
			}
		} else {
			out, err = reader.PanelConfirmationFeed(ctx, in.AISessionID)
		}
		result := panelFeedOutput{PanelConfirmationFeed: out}
		if err == nil {
			result.Navigation = navigation.get(in.AISessionID, out.ProjectID)
		}
		return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"emission": "SILENT"}}, result, publicToolError(err)
	})
}
