package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
)

func localMCPTestSupervisor(t *testing.T, factory ProcessFactory, prober EndpointProber) *RuntimeSupervisor {
	t.Helper()
	value, err := NewRuntimeSupervisor(Config{
		Core:           ServiceConfig{Command: Command{Path: "core"}, Endpoint: "http://127.0.0.1:18081"},
		MCP:            ServiceConfig{Command: Command{Path: "mcp"}, Endpoint: "http://127.0.0.1:18080"},
		LocalMCP:       ServiceConfig{Command: Command{Path: "local-mcp"}, Endpoint: "http://127.0.0.1:18090"},
		StartupTimeout: time.Second, ShutdownTimeout: 30 * time.Millisecond, ProbeInterval: time.Millisecond,
	}, factory, prober, logbuffer.New(20))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// The Local MCP adapter is uniquely identified by the decision capability it
// exposes and that no other Dev Bridge MCP surface does.
const localMCPToolsBody = `{"result":{"tools":[{"name":"bridge_diagnostics"},{"name":"project_list"},{"name":"decision_respond"}]}}`
const localMCPPing = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

func newLocalMCPSurface(t *testing.T, toolsBody, diagnosticsBody string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/readyz":
			_, _ = io.WriteString(w, `{"status":"ready"}`)
		case r.URL.Path == "/mcp" && strings.Contains(r.Header.Get("Accept"), "json"):
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), `"tools/list"`) {
				_, _ = io.WriteString(w, toolsBody)
				return
			}
			_, _ = io.WriteString(w, diagnosticsBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestHTTPProberIdentifiesCompatibleLocalMCPAdapter(t *testing.T) {
	server := newLocalMCPSurface(t, localMCPToolsBody,
		`{"result":{"structuredContent":{"applicationName":"Dev Bridge","applicationVersion":"0.1.0-test","ready":true,"protocol":"MCP","serverName":"devbridge","serverVersion":"0.1.0-test","transport":"streamable-http"}}}`)
	probe, err := HTTPProber{}.Probe(context.Background(), LocalMCP, server.URL, "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Reachable || !probe.Compatible || !probe.Ready || probe.Build != "0.1.0-test" {
		t.Fatalf("probe = %+v", probe)
	}
}

func TestHTTPProberRejectsGPTMCPSurfaceOnLocalMCPEndpoint(t *testing.T) {
	// A GPT MCP adapter answers bridge_diagnostics but has no
	// decision_respond tool; it must classify as incompatible CONFLICT.
	server := newLocalMCPSurface(t, `{"result":{"tools":[{"name":"bridge_diagnostics"},{"name":"project_list"}]}}`,
		`{"result":{"structuredContent":{"applicationName":"Dev Bridge","applicationVersion":"0.1.0-test","ready":true,"protocol":"MCP","serverName":"devbridge","serverVersion":"0.1.0-test","transport":"streamable-http"}}}`)
	probe, err := HTTPProber{}.Probe(context.Background(), LocalMCP, server.URL, "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	if probe.Compatible || probe.Ready {
		t.Fatalf("GPT MCP surface adopted as Local MCP: %+v", probe)
	}
}

func TestHTTPProberRejectsForeignHTTPServiceOnLocalMCPEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			_, _ = io.WriteString(w, `{"status":"ready"}`)
			return
		}
		_, _ = io.WriteString(w, `hello`)
	}))
	defer server.Close()
	probe, err := HTTPProber{}.Probe(context.Background(), LocalMCP, server.URL, "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	if probe.Compatible || probe.Ready {
		t.Fatalf("foreign service adopted as Local MCP: %+v", probe)
	}
}

func TestLocalMCPFailureIsScopedToItsOwnProjection(t *testing.T) {
	// Local MCP is unreachable and its launch fails; Core and MCP must come
	// up anyway and Start must not fail the stack.
	probe := &fakeProber{values: map[Service][]ProbeResult{
		Core:     {{}, ready("core")},
		MCP:      {{}, ready("mcp")},
		LocalMCP: {{}},
	}}
	factory := &failingFactory{inner: &fakeFactory{}}
	supervisor := localMCPTestSupervisor(t, factory, probe)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatalf("Local MCP failure failed the stack: %v", err)
	}
	defer supervisor.Stop(context.Background())
	snapshot := supervisor.Snapshot()
	if snapshot.Core.State != Running || snapshot.MCP.State != Running {
		t.Fatalf("stack = %+v", snapshot)
	}
	if snapshot.LocalMCP.Ownership == Managed && snapshot.LocalMCP.State == Running {
		t.Fatalf("local mcp falsely running: %+v", snapshot.LocalMCP)
	}
}

func TestLocalMCPExternalAttachNeverSpawnsOrStops(t *testing.T) {
	probe := &fakeProber{values: map[Service][]ProbeResult{
		Core:     {{}, ready("same")},
		MCP:      {{}, ready("same")},
		LocalMCP: {ready("external-build")},
	}}
	factory := &fakeFactory{}
	supervisor := localMCPTestSupervisor(t, factory, probe)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Stop(context.Background())
	starts := factory.commands()
	if len(starts) != 2 {
		t.Fatalf("external Local MCP caused managed starts = %+v", starts)
	}
	snapshot := supervisor.Snapshot()
	if snapshot.LocalMCP.Ownership != Attached || snapshot.LocalMCP.State != External {
		t.Fatalf("local mcp = %+v", snapshot.LocalMCP)
	}
}

func TestLocalMCPConflictNeverStartsOrStopsTheListener(t *testing.T) {
	probe := &fakeProber{values: map[Service][]ProbeResult{
		Core:     {ready("core")},
		MCP:      {ready("mcp")},
		LocalMCP: {{Reachable: true, Detail: "unknown service"}},
	}}
	factory := &fakeFactory{}
	supervisor := localMCPTestSupervisor(t, factory, probe)
	if err := supervisor.Start(context.Background()); err != nil {
		// Core and MCP failures propagate; a Local MCP conflict must not.
		t.Fatalf("Local MCP conflict failed the stack: %v", err)
	}
	defer supervisor.Stop(context.Background())
	snapshot := supervisor.Snapshot()
	if snapshot.LocalMCP.Ownership != Conflict {
		t.Fatalf("local mcp = %+v", snapshot.LocalMCP)
	}
	if snapshot.LocalMCP.State != Error {
		t.Fatalf("local mcp state = %q", snapshot.LocalMCP.State)
	}
}

func TestLocalMCPStoppedTogetherInReverseOrder(t *testing.T) {
	probe := &fakeProber{values: map[Service][]ProbeResult{
		Core:     {{}, ready("same")},
		MCP:      {{}, ready("same")},
		LocalMCP: {{}, ready("same")},
	}}
	factory := &fakeFactory{}
	supervisor := localMCPTestSupervisor(t, factory, probe)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := supervisor.Snapshot()
	if snapshot.LocalMCP.State != Stopped || snapshot.LocalMCP.Ownership != Unowned {
		t.Fatalf("local mcp after stop = %+v", snapshot.LocalMCP)
	}
}

type failingFactory struct{ inner ProcessFactory }

func (f *failingFactory) Start(ctx context.Context, command Command, stdout, stderr io.Writer) (Process, error) {
	if command.Path == "local-mcp" {
		return nil, errors.New("spawn failed")
	}
	return f.inner.Start(ctx, command, stdout, stderr)
}
