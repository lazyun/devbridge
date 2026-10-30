package localmcp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/localmcp"
)

func TestLocalMCPHandlerRejectsCrossOriginBrowserMutationRequests(t *testing.T) {
	server, err := localmcp.New(localmcp.Config{}, readyService(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		origin string
		status int
	}{"non-browser": {"", http.StatusOK}, "same-origin browser": {"http://127.0.0.1", http.StatusOK}, "hostile browser": {"https://hostile.example", http.StatusForbidden}} {
		t.Run(name, func(t *testing.T) {
			body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"browser","version":"1"}}}`)
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+localmcp.MCPPath, body)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("origin %q status=%d, want %d; body=%q", test.origin, response.Code, test.status, response.Body.String())
			}
		})
	}
}
