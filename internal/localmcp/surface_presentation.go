package localmcp

import (
	"context"
	"encoding/json"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Local clients consume the same business DTO without a ChatGPT presentation
// dependency. No show tool or resource is registered in this adapter.
func localSurfaceResultMiddleware() sdkmcp.Middleware {
	return func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
			result, err := next(ctx, method, request)
			if method == "tools/list" && err == nil {
				if list, ok := result.(*sdkmcp.ListToolsResult); ok {
					for i, original := range list.Tools {
						copyTool := *original
						copyTool.OutputSchema = localSurfaceOutputSchema(original.OutputSchema)
						list.Tools[i] = &copyTool
					}
				}
				return result, err
			}
			if method != "tools/call" || err != nil {
				return result, err
			}
			out, ok := result.(*sdkmcp.CallToolResult)
			if !ok || out == nil {
				return result, err
			}
			raw, e := json.Marshal(out.StructuredContent)
			if e != nil {
				return result, err
			}
			var value map[string]any
			if json.Unmarshal(raw, &value) != nil {
				return result, err
			}
			hint, ok := value["presentationHint"].(map[string]any)
			if !ok {
				return result, err
			}
			delete(hint, "tool")
			delete(hint, "arguments")
			hint["display"] = "TEXT"
			hint["hostVisibility"] = "UNCONFIRMED"
			out.StructuredContent = value
			raw, e = json.Marshal(value)
			if e == nil {
				out.Content = []sdkmcp.Content{&sdkmcp.TextContent{Text: string(raw)}}
			}
			return out, nil
		}
	}
}

// The Local adapter has no UI tool handoff. Match its actual TEXT result rather
// than advertising the shared ChatGPT tool/display enum to text-only clients.
func localSurfaceOutputSchema(original any) any {
	raw, err := json.Marshal(original)
	if err != nil {
		return original
	}
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil {
		return original
	}
	properties, _ := schema["properties"].(map[string]any)
	hint, _ := properties["presentationHint"].(map[string]any)
	if hint == nil {
		return original
	}
	hintProperties, _ := hint["properties"].(map[string]any)
	delete(hintProperties, "tool")
	delete(hintProperties, "arguments")
	hintProperties["display"] = map[string]any{"type": "string", "const": "TEXT"}
	if required, ok := hint["required"].([]any); ok {
		kept := make([]any, 0, len(required))
		for _, name := range required {
			if name != "tool" && name != "arguments" {
				kept = append(kept, name)
			}
		}
		hint["required"] = kept
	}
	return schema
}
