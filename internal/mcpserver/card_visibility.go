package mcpserver

import (
	"context"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Card tools are selected from inspected read handlers, not annotations or names.
// This policy belongs only to the ChatGPT adapter; Core capabilities are unchanged.
func readOnlyCardTool(name string) bool {
	switch name {
	case "dev_bridge_card_get", "workspace_get", "operation_get", "session_binding_get", "dev_bridge_panel_get":
		return true
	}
	return false
}

// Keep this list aligned with the panel's explicit click dispatcher. Discovery
// must permit the UI to reach these existing scoped handlers; Core still checks
// the Session, exact target, authorization version and recorded user decision.
func panelComponentTool(name string) bool {
	switch name {
	case "runtime_request_respond", "panel_focus", "project_setup_prepare", "run_analysis_get", "ui_approval_get", "ui_approval_decide",
		"agent_operation_recover", "runtime_request_user_decision_prepare_hash",
		"runtime_request_user_decision_submit", "runtime_request_recovery":
		return true
	}
	return false
}

// The SDK carries ui.visibility as metadata. It does not authenticate an app/model
// origin on tools/call, so this controls host exposure, not caller authorization.
// Never reject a model command based on caller-supplied origin metadata.
func cardToolVisibilityMiddleware() sdkmcp.Middleware {
	return func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
			result, err := next(ctx, method, request)
			if err != nil || method != "tools/list" {
				return result, err
			}
			original, ok := result.(*sdkmcp.ListToolsResult)
			if !ok || original == nil {
				return result, nil
			}
			out := *original
			out.Tools = make([]*sdkmcp.Tool, 0, len(original.Tools))
			for _, tool := range original.Tools {
				if tool == nil {
					continue
				}
				copy := *tool
				// ChatGPT's plugin details show tool descriptions from its imported
				// catalog. Expose the release there so users can compare that saved
				// definition with bridge_diagnostics.serverVersion without confusing
				// it with ChatGPT's own asdk_app_v registration identity. Modify only
				// this response copy, never accumulate labels on registered tools.
				copy.Description = "[DevBridge 工具版本 ID: " + ServerVersion + "] " + tool.Description
				copy.Meta = sdkmcp.Meta{}
				for key, value := range tool.Meta {
					copy.Meta[key] = value
				}
				ui := map[string]any{}
				if source, ok := tool.Meta["ui"].(map[string]any); ok {
					for key, value := range source {
						ui[key] = value
					}
				}
				model, app := true, false
				switch visibility := ui["visibility"].(type) {
				case []string:
					model, app = false, false
					for _, audience := range visibility {
						model = model || audience == "model"
						app = app || audience == "app"
					}
				case []any:
					model, app = false, false
					for _, audience := range visibility {
						model = model || audience == "model"
						app = app || audience == "app"
					}
				}
				// One model-visible panel entry; separate confirmation/detail
				// resources remain available for exact legacy app reads only.
				if tool.Name == "confirmation_show" || tool.Name == "details_show" || tool.Name == "workspace_show" {
					model = false
					app = true
					// App-only helpers return data to the existing panel. Advertising
					// standalone templates conflicts with ChatGPT's private-tool
					// widget policy during plugin catalog validation.
					delete(ui, "resourceUri")
					delete(copy.Meta, "openai/outputTemplate")
				}
				// Retired app-only helpers are not promoted into new model capabilities.
				if !readOnlyCardTool(tool.Name) && !panelComponentTool(tool.Name) && !confirmationComponentTool(tool.Name) && tool.Name != "confirmation_show" && tool.Name != "details_show" && tool.Name != "workspace_show" && app && !model {
					continue
				}
				visibility := []string{}
				if model {
					visibility = append(visibility, "model")
				}
				if app || readOnlyCardTool(tool.Name) || panelComponentTool(tool.Name) || confirmationComponentTool(tool.Name) {
					visibility = append(visibility, "app")
				}
				if len(visibility) == 0 {
					continue
				}
				ui["visibility"] = visibility
				copy.Meta["ui"] = ui
				out.Tools = append(out.Tools, &copy)
			}
			return &out, nil
		}
	}
}
