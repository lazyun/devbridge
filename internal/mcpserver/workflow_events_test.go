package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/workflowevents"
)

type eventTestService struct {
	BridgeService
	events *workflowevents.Service
}

func (s eventTestService) WorkflowEvents() *workflowevents.Service { return s.events }
func TestWorkflowEventsDiscoveryAndProtocolGate(t *testing.T) {
	base, _ := NewStaticService(BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
	calls := 0
	events := workflowevents.New(nil, func(context.Context, workflowevents.Arguments) (string, error) { calls++; return "project", nil })
	server, e := New(Config{}, eventTestService{base, events})
	if e != nil {
		t.Fatal(e)
	}
	invoke := func(method, version, params string) map[string]any {
		t.Helper()
		if version == "2026-07-28" {
			var v map[string]any
			json.Unmarshal([]byte(params), &v)
			v["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": version, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
			raw, _ := json.Marshal(v)
			params = string(raw)
		}
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Mcp-Method", method)
		r.Header.Set("Accept", "application/json, text/event-stream")
		if version != "" {
			r.Header.Set("MCP-Protocol-Version", version)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		var response map[string]any
		if json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body)
		}
		return response
	}
	discovery := invoke("server/discover", "2026-07-28", `{}`)
	result, ok := discovery["result"].(map[string]any)
	if !ok {
		t.Fatalf("discover: %+v", discovery)
	}
	caps := result["capabilities"].(map[string]any)
	if caps["events"] == nil || caps["tools"] == nil {
		t.Fatal(caps)
	}
	catalog := invoke("events/list", "2026-07-28", `{}`)
	result = catalog["result"].(map[string]any)
	if len(result["events"].([]any)) != 1 {
		t.Fatal(catalog)
	}
	if invoke("events/list", "2025-11-25", `{}`)["error"] == nil {
		t.Fatal("legacy Events accepted")
	}
	// Ordinary conversations continue to use the existing tools without a Work
	// subscription or callback. Merely advertising Events cannot start one.
	if response := invoke("tools/list", "2026-07-28", `{}`); response["error"] != nil {
		t.Fatalf("ordinary tool catalog broken: %+v", response)
	}
	if response := invoke("initialize", "2025-11-25", `{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"ordinary-chat","version":"1"}}`); response["error"] != nil {
		t.Fatalf("ordinary legacy initialization broken: %+v", response)
	}
	if response := invoke("tools/list", "2025-11-25", `{}`); response["error"] != nil {
		t.Fatalf("ordinary legacy tool catalog broken: %+v", response)
	}
	if invoke("events/subscribe", "2026-07-28", `{"name":"task.updated","arguments":{"aiSessionId":"s","taskId":"t"},"delivery":{"mode":"webhook","url":"http://127.0.0.1","secret":"bad"}}`)["error"] == nil || calls != 0 {
		t.Fatal("invalid subscription reached authority")
	}
}
