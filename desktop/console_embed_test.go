package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
	runtimecontrol "github.com/lazyun/devbridge/desktop/internal/runtime"
)

func TestDesktopConsoleDocumentAndActionsUseAttachedCore(t *testing.T) {
	var mu sync.Mutex
	var writes []string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(desktopEmbedHeader) != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Origin") != "" {
			t.Error("browser authority forwarded to Core")
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html><head></head><body>Core workbench</body></html>")
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		writes = append(writes, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"accepted":true}`)
	}))
	defer core.Close()
	supervisor, err := runtimecontrol.NewRuntimeSupervisor(runtimecontrol.Config{
		Core: runtimecontrol.ServiceConfig{Command: runtimecontrol.Command{Path: "unused-core"}, Endpoint: core.URL},
		MCP:  runtimecontrol.ServiceConfig{Command: runtimecontrol.Command{Path: "unused-mcp"}, Endpoint: core.URL},
	}, &appTestFactory{}, &appTestProber{external: true, calls: map[runtimecontrol.Service]int{}}, logbuffer.New(20))
	if err != nil {
		t.Fatal(err)
	}
	if err = supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Stop(context.Background())
	a := &App{supervisor: supervisor, ctx: context.Background()}
	handler := a.consoleEmbedMiddleware(http.NotFoundHandler())
	request := func(method, path, token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://wails"+path, strings.NewReader(`{"expected_revision":0}`))
		r.Header.Set(desktopEmbedHeader, token)
		r.Header.Set("Origin", origin)
		r.Header.Set("Cookie", "not-for-core")
		r.Header.Set("Authorization", "not-for-core")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	doc := request("GET", "/ui/?desktop=console", "", "wails://wails")
	if doc.Code != 200 || !strings.Contains(doc.Body.String(), `name="devbridge-desktop-embed"`) {
		t.Fatalf("document=%d %s", doc.Code, doc.Body)
	}
	match := regexp.MustCompile(`name="devbridge-desktop-embed" content="([a-f0-9]+)"`).FindStringSubmatch(doc.Body.String())
	if len(match) != 2 {
		t.Fatal("missing document token")
	}
	token := match[1]
	regular := request("GET", "/ui/?workbench=sessions&layout=embedded", "", "wails://wails")
	if strings.Contains(regular.Body.String(), "desktop-console.css") {
		t.Fatal("Console styling leaked into sessions")
	}
	for _, bad := range []struct {
		method, path, token, origin string
		code                        int
	}{
		{"PUT", "/v1/projects/p/execution-authorization", "", "wails://wails", 403},
		{"POST", "/ui/api/tasks/t/action", "wrong", "wails://wails", 403},
		{"PUT", "/v1/projects/p/execution-authorization", token, "https://other.invalid", 403},
		{"PUT", "/v1/projects/p/unrelated", token, "wails://wails", 405},
		{"PUT", "/v1/projects/p/execution-authorization", token, "wails://wails", 405},
		{"DELETE", "/ui/api/tasks/t", token, "wails://wails", 405},
		{"PUT", "/v1/projects/p%2fother/execution-authorization", token, "wails://wails", 403},
	} {
		if got := request(bad.method, bad.path, bad.token, bad.origin); got.Code != bad.code {
			t.Errorf("%s %s=%d want %d", bad.method, bad.path, got.Code, bad.code)
		}
	}
	for _, valid := range []struct{ method, path string }{
		{"PUT", "/v1/runtime/codex-inventory"},
		{"POST", "/ui/api/tasks/t/action?project_id=p"},
	} {
		if got := request(valid.method, valid.path, token, "wails://wails"); got.Code != 200 || got.Body.String() != `{"accepted":true}` {
			t.Errorf("write=%d %s", got.Code, got.Body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(writes) != 2 || !strings.Contains(writes[1], "?project_id=p") || !strings.Contains(writes[0], `"expected_revision":0`) {
		t.Fatalf("unexpected forwarded writes: %v", writes)
	}
}
