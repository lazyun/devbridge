package runtime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
)

// Exercise actual production adapters on temporary loopback listeners. No Core,
// installed runtime or production data root is used.
func TestDiagnosticBuildFromActualBootstrapResponse(t *testing.T) {
	service, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "bootstrap-contract-test", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	gpt, err := mcpserver.New(mcpserver.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	local, err := localmcp.New(localmcp.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	var firstSchema any
	for _, adapter := range []struct {
		name    string
		handler http.Handler
	}{{"gpt", gpt.Handler()}, {"local", local.Handler()}} {
		t.Run(adapter.name, func(t *testing.T) {
			server := httptest.NewServer(adapter.handler)
			defer server.Close()
			post := func(name, payload string) []byte {
				req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil || response.StatusCode != 200 {
					t.Fatalf("%s: %s %v", name, body, err)
				}
				if dir := os.Getenv("DEVBRIDGE_BOOTSTRAP_EVIDENCE_DIR"); dir != "" {
					if err := os.WriteFile(filepath.Join(dir, adapter.name+"-"+name+".json"), body, 0600); err != nil {
						t.Fatal(err)
					}
				}
				return body
			}
			post("initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"bootstrap-contract-test","version":"1"}}}`)
			listed := post("tools-list", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
			var catalog struct {
				Result struct {
					Tools []struct {
						Name         string
						OutputSchema any
					}
				}
			}
			if err := json.Unmarshal(listed, &catalog); err != nil {
				t.Fatal(err)
			}
			var schema any
			for _, tool := range catalog.Result.Tools {
				if tool.Name == "bridge_diagnostics" {
					schema = tool.OutputSchema
				}
			}
			if schema == nil {
				t.Fatal("diagnostics schema missing")
			}
			if firstSchema == nil {
				firstSchema = schema
			} else if !reflect.DeepEqual(firstSchema, schema) {
				t.Fatal("adapter bootstrap schemas differ")
			}
			body := post("bridge-diagnostics", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"bridge_diagnostics","arguments":{}}}`)
			if build := diagnosticBuild(body); build == "" {
				t.Fatalf("MCP diagnostics did not identify Dev Bridge: %s", body)
			} else {
				t.Logf("diagnosticBuild(actual %s response) = %q", adapter.name, build)
			}
		})
	}
}
