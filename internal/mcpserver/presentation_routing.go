package mcpserver

import (
	"context"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type presentationEmission string

const (
	emissionMain    presentationEmission = "MAIN"
	emissionSilent  presentationEmission = "SILENT"
	emissionDetails presentationEmission = "DETAILS"
)

func presentationPolicy(name string) presentationEmission {
	switch name {
	case "panel_show", "workspace_show", "project_selection_show":
		return emissionMain
	case "details_show":
		return emissionDetails
	}
	return emissionSilent
}
func primaryCardResource(name string) string {
	if presentationPolicy(name) == emissionMain {
		return devBridgeCardResourceURI
	}
	return ""
}
func primaryCardMeta(uri string) sdkmcp.Meta {
	return sdkmcp.Meta{"ui": map[string]any{"resourceUri": uri, "visibility": []string{"model"}}, "openai/outputTemplate": uri}
}
func detailsCardMeta() sdkmcp.Meta { return primaryCardMeta(devBridgeCardResourceURI) }

// Rendering is explicit. The metadata records a render request, never evidence
// of host mounting or visibility. Readback belongs to the business adapter and
// cannot be replaced by a full implicit workspace read on every tool call.
func presentationResultMiddleware(_ BridgeService) sdkmcp.Middleware {
	leases := newPresentationLeases()
	return func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
			if method == "tools/call" {
				if params, ok := request.GetParams().(*sdkmcp.CallToolParamsRaw); ok && params != nil && params.Name == "confirmation_show" {
					// The immutable intent owns its presentation identity and result;
					// never attach a workspace lease or refresh to this resource.
					return next(ctx, method, request)
				}
				if params, ok := request.GetParams().(*sdkmcp.CallToolParamsRaw); ok && params != nil && presentationPolicy(params.Name) != emissionSilent {
					return leases.emit(ctx, next, method, request, params)
				}
			}
			result, err := next(ctx, method, request)
			if method == "tools/call" && err == nil {
				if out, ok := result.(*sdkmcp.CallToolResult); ok && out != nil {
					if out.Meta == nil {
						out.Meta = sdkmcp.Meta{}
					}
					out.Meta["emission"] = "SILENT"
					delete(out.Meta, "openai/outputTemplate")
					if ui, ok := out.Meta["ui"].(map[string]any); ok {
						delete(ui, "resourceUri")
					}
				}
			}
			return result, err
		}
	}
}
