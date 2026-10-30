package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/adapterproxy"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
)

func TestRealSharedCoreHandlersAreReachedThroughBothProxiesAndRejectCrossOrigin(t *testing.T) {
	service, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
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
	coreMux := http.NewServeMux()
	coreMux.Handle("/_adapters/gpt/", http.StripPrefix("/_adapters/gpt", gpt.Handler()))
	coreMux.Handle("/_adapters/local/", http.StripPrefix("/_adapters/local", local.Handler()))
	coreMux.HandleFunc("GET "+adapterproxy.IdentityPath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"dataRoot":"/data"}`)
	})
	core := httptest.NewServer(coreMux)
	defer core.Close()

	targets := map[string]struct{ path, health string }{
		"gpt":   {"/_adapters/gpt", "{\"status\":\"ok\"}\n"},
		"local": {"/_adapters/local", "live"},
	}
	for name, target := range targets {
		proxy, err := adapterproxy.New(context.Background(), adapterproxy.Config{CoreURL: core.URL, DataRoot: "/data", AdapterPath: target.path})
		if err != nil {
			t.Fatalf("%s proxy: %v", name, err)
		}
		body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"browser","version":"1"}}}`)
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", body)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://hostile.example")
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s cross-origin proxy status=%d body=%q", name, response.Code, response.Body.String())
		}

		healthRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/healthz", nil)
		healthResponse := httptest.NewRecorder()
		proxy.ServeHTTP(healthResponse, healthRequest)
		if healthResponse.Code != http.StatusOK || healthResponse.Body.String() != target.health {
			t.Fatalf("%s real shared handler health=%d/%q", name, healthResponse.Code, healthResponse.Body.String())
		}
	}
}
