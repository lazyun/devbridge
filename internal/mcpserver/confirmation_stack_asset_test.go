package mcpserver

import (
	"os"
	"strings"
	"testing"
)

func TestUnifiedTaskPanelEmbedsUnchangedFormalRendererWithoutFrames(t *testing.T) {
	html := devBridgeUIHTML
	_, formal, ok := strings.Cut(confirmationHTML, `<script id="confirmation-main">`)
	if !ok {
		t.Fatal("missing source")
	}
	formal, _, _ = strings.Cut(formal, "</script>")
	if !strings.Contains(html, formal) || strings.Contains(html, "__CONFIRMATION_") {
		t.Fatal("panel substituted or truncated the formal renderer")
	}
	if !strings.Contains(html, "attachShadow") || strings.Contains(html, "<iframe") || strings.Contains(html, "new Function(") || strings.Contains(html, "eval(") {
		t.Fatal("panel requires unsafe evaluation or nesting")
	}
	if path := os.Getenv("DEVBRIDGE_PANEL_HTML_PATH"); path != "" {
		if err := os.WriteFile(path, []byte(html), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
