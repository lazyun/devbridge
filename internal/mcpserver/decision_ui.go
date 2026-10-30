package mcpserver

import (
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	decisionResourceMIME = "text/html;profile=mcp-app"
)

func decisionToolMeta() sdkmcp.Meta {
	return sdkmcp.Meta{"ui": map[string]any{
		"visibility": []string{"model", "app"},
	}}
}
