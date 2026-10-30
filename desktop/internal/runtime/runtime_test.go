package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
)

type fakeProcess struct {
	done                   chan struct{}
	mu                     sync.Mutex
	signalCount, killCount int
	waitErr                error
	ignoreSignal           bool
}

func newFakeProcess() *fakeProcess                                 { return &fakeProcess{done: make(chan struct{})} }
func (p *fakeProcess) Wait() error                                 { <-p.done; return p.waitErr }
func (p *fakeProcess) Signal(_ interface{ String() string }) error { return p.stop(false) }
func (p *fakeProcess) Kill() error                                 { return p.stop(true) }
func (p *fakeProcess) stop(kill bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if kill {
		p.killCount++
	} else {
		p.signalCount++
	}
	if !kill && p.ignoreSignal {
		return nil
	}
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}

// os.Signal has the same dynamic implementation, but Go does not permit the
// narrower interface above to implement Process; keep the adapter explicit.
type processAdapter struct{ *fakeProcess }

func (p processAdapter) Signal(_ os.Signal) error { return p.fakeProcess.stop(false) }

type fakeFactory struct {
	mu       sync.Mutex
	started  []Command
	children []*fakeProcess
}

func (f *fakeFactory) Start(_ context.Context, c Command, _, _ io.Writer) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	child := newFakeProcess()
	f.started = append(f.started, c)
	f.children = append(f.children, child)
	return processAdapter{child}, nil
}
func (f *fakeFactory) commands() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Command(nil), f.started...)
}

type fakeProber struct {
	mu     sync.Mutex
	values map[Service][]ProbeResult
	calls  []Service
}

func (p *fakeProber) Probe(_ context.Context, service Service, _, _ string) (ProbeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, service)
	queue := p.values[service]
	if len(queue) == 0 {
		return ProbeResult{}, errors.New("unreachable")
	}
	value := queue[0]
	if len(queue) > 1 {
		p.values[service] = queue[1:]
	}
	return value, nil
}
func ready(build string) ProbeResult {
	return ProbeResult{Reachable: true, Compatible: true, Ready: true, Build: build}
}

func testSupervisor(t *testing.T, factory *fakeFactory, probe *fakeProber) *RuntimeSupervisor {
	t.Helper()
	value, err := NewRuntimeSupervisor(Config{Core: ServiceConfig{Command: Command{Path: "core"}, Endpoint: "http://127.0.0.1:18081"}, MCP: ServiceConfig{Command: Command{Path: "mcp"}, Endpoint: "http://127.0.0.1:18080"}, StartupTimeout: 100 * time.Millisecond, ShutdownTimeout: 30 * time.Millisecond, ProbeInterval: time.Millisecond}, factory, probe, logbuffer.New(3))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestStartStartsCoreBeforeMCP(t *testing.T) {
	factory := &fakeFactory{}
	probe := &fakeProber{values: map[Service][]ProbeResult{Core: {{}, ready("core")}, MCP: {{}, ready("mcp")}}}
	supervisor := testSupervisor(t, factory, probe)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Stop(context.Background())
	commands := factory.commands()
	if len(commands) != 2 || commands[0].Path != "core" || commands[1].Path != "mcp" {
		t.Fatalf("starts=%+v", commands)
	}
	snapshot := supervisor.Snapshot()
	if !snapshot.BuildMismatch {
		t.Fatalf("mismatch not reported: %+v", snapshot)
	}
}

func TestCoreReadinessFailureNeverStartsMCP(t *testing.T) {
	factory := &fakeFactory{}
	probe := &fakeProber{values: map[Service][]ProbeResult{Core: {{}}}}
	supervisor := testSupervisor(t, factory, probe)
	if err := supervisor.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded")
	}
	commands := factory.commands()
	if len(commands) != 1 || commands[0].Path != "core" {
		t.Fatalf("starts=%+v", commands)
	}
}

func TestExternalIsAttachedAndNeverStopped(t *testing.T) {
	factory := &fakeFactory{}
	probe := &fakeProber{values: map[Service][]ProbeResult{Core: {ready("same")}, MCP: {ready("same")}}}
	supervisor := testSupervisor(t, factory, probe)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := factory.commands(); len(got) != 0 {
		t.Fatalf("unexpected starts=%+v", got)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := supervisor.Snapshot()
	if snapshot.Core.State != External || snapshot.MCP.State != External {
		t.Fatalf("external state changed: %+v", snapshot)
	}
}

func TestConflictDoesNotStartOrStopForeign(t *testing.T) {
	factory := &fakeFactory{}
	probe := &fakeProber{values: map[Service][]ProbeResult{Core: {{Reachable: true, Detail: "nginx"}}}}
	supervisor := testSupervisor(t, factory, probe)
	err := supervisor.Start(context.Background())
	if !errors.Is(err, ErrEndpointConflict) {
		t.Fatalf("err=%v", err)
	}
	if got := factory.commands(); len(got) != 0 {
		t.Fatalf("started=%+v", got)
	}
	snapshot := supervisor.Snapshot()
	if snapshot.Core.Ownership != Conflict {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestOwnedStopAndCrashTransition(t *testing.T) {
	factory := &fakeFactory{}
	probe := &fakeProber{values: map[Service][]ProbeResult{Core: {{}, ready("same")}, MCP: {{}, ready("same")}}}
	supervisor := testSupervisor(t, factory, probe)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	factory.children[0].waitErr = errors.New("boom")
	close(factory.children[0].done)
	waitFor(t, func() bool { return supervisor.Snapshot().Core.State == Crashed })
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	factory.children[1].mu.Lock()
	signals := factory.children[1].signalCount
	factory.children[1].mu.Unlock()
	if signals != 1 {
		t.Fatalf("mcp signals=%d", signals)
	}
}

func TestRestartStartsNewOwnedChildren(t *testing.T) {
	factory := &fakeFactory{}
	probe := &fakeProber{values: map[Service][]ProbeResult{Core: {{}, ready("same"), {}, ready("same")}, MCP: {{}, ready("same"), {}, ready("same")}}}
	supervisor := testSupervisor(t, factory, probe)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Stop(context.Background())
	if got := factory.commands(); len(got) != 4 {
		t.Fatalf("starts=%+v", got)
	}
}

func TestGracefulShutdownTimeoutTerminatesOnlyOwnedChildren(t *testing.T) {
	factory := &fakeFactory{}
	probe := &fakeProber{values: map[Service][]ProbeResult{Core: {{}, ready("same")}, MCP: {{}, ready("same")}}}
	supervisor := testSupervisor(t, factory, probe)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	factory.children[0].ignoreSignal, factory.children[1].ignoreSignal = true, true
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, child := range factory.children {
		child.mu.Lock()
		killed := child.killCount
		child.mu.Unlock()
		if killed != 1 {
			t.Fatalf("owned child kill count=%d", killed)
		}
	}
}

func TestLogBufferIsBounded(t *testing.T) {
	logs := logbuffer.New(2)
	logs.AppendLine(string(Core), logbuffer.Stdout, "one")
	logs.AppendLine(string(Core), logbuffer.Stdout, "two")
	logs.AppendLine(string(MCP), logbuffer.Stderr, "three")
	entries := logs.Snapshot()
	if len(entries) != 2 || entries[0].Text != "two" || entries[1].Text != "three" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestManagedCoreBuildCapturedFromReadyOutput(t *testing.T) {
	supervisor := testSupervisor(t, &fakeFactory{}, &fakeProber{values: map[Service][]ProbeResult{}})
	supervisor.setStatus(Core, Starting, Managed, "", "", "starting", "")
	supervisor.observeLine(Core, "DEVBRIDGE_READY http://127.0.0.1:8081 build=0.1.0-dev+g8019789")
	if got := supervisor.Snapshot().Core.Build; got != "0.1.0-dev+g8019789" {
		t.Fatalf("captured build=%q", got)
	}
}

func TestStartupFailureReportsBoundedChildError(t *testing.T) {
	supervisor := testSupervisor(t, &fakeFactory{}, &fakeProber{values: map[Service][]ProbeResult{}})
	supervisor.setStatus(Core, Starting, Managed, "", "", "starting", "")
	supervisor.observeErrorLine(Core, "devbridge failed: the selected directory is not empty and is not a Dev Bridge data directory")
	supervisor.mu.Lock()
	supervisor.services[Core].status.State = Crashed
	supervisor.mu.Unlock()
	_, err := supervisor.waitReady(context.Background(), Core, supervisor.config.Core)
	if err == nil || !strings.Contains(err.Error(), "not a Dev Bridge data directory") {
		t.Fatalf("startup error = %v", err)
	}
}

// A second Core that loses the DataRoot writer lock exits at startup with the
// writer-lock conflict on stderr. Desktop must surface a distinct ownership
// conflict, never generic Core unavailability, and never force unlock.
func TestWriterLockConflictIsDistinctStartupError(t *testing.T) {
	supervisor := testSupervisor(t, &fakeFactory{}, &fakeProber{values: map[Service][]ProbeResult{}})
	supervisor.setStatus(Core, Starting, Managed, "", "", "starting", "")
	supervisor.observeErrorLine(Core, "devbridge failed: Dev Bridge DataRoot is already open for writing: /data/state/core-writer.lock")
	supervisor.mu.Lock()
	supervisor.services[Core].status.State = Crashed
	supervisor.mu.Unlock()
	_, err := supervisor.waitReady(context.Background(), Core, supervisor.config.Core)
	var conflict *WriterLockConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("startup error = %v, want WriterLockConflictError", err)
	}
	if !errors.Is(err, ErrWriterLocked) {
		t.Fatalf("error does not unwrap to ErrWriterLocked: %v", err)
	}
	if conflict.Service != Core {
		t.Fatalf("conflict service = %q", conflict.Service)
	}
	snapshot := supervisor.Snapshot()
	if snapshot.Core.Detail != ErrWriterLocked.Error() {
		t.Fatalf("core detail = %q, want actionable writer-lock message", snapshot.Core.Detail)
	}
	if !strings.Contains(err.Error(), "already in use by another running Core") || strings.Contains(err.Error(), "force unlock") {
		t.Fatalf("error wording = %q", err.Error())
	}
}

// Ordinary startup failures keep their raw bounded child output; only the
// writer-lock line is reclassified.
func TestNonWriterLockStartupFailureIsNotReclassified(t *testing.T) {
	supervisor := testSupervisor(t, &fakeFactory{}, &fakeProber{values: map[Service][]ProbeResult{}})
	supervisor.setStatus(Core, Starting, Managed, "", "", "starting", "")
	supervisor.observeErrorLine(Core, "devbridge failed: open control plane: some unrelated failure")
	supervisor.mu.Lock()
	supervisor.services[Core].status.State = Crashed
	supervisor.mu.Unlock()
	_, err := supervisor.waitReady(context.Background(), Core, supervisor.config.Core)
	var conflict *WriterLockConflictError
	if errors.As(err, &conflict) {
		t.Fatalf("ordinary failure reclassified as writer lock: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "some unrelated failure") {
		t.Fatalf("startup error = %v", err)
	}
}

func TestServiceLogWriterBuffersPartialLines(t *testing.T) {
	logs := logbuffer.New(2)
	writer := &serviceLogWriter{buffer: logs, service: Core, stream: logbuffer.Stdout}
	_, _ = writer.Write([]byte("DEVBRIDGE_"))
	if got := logs.Snapshot(); len(got) != 0 {
		t.Fatalf("partial output was logged: %+v", got)
	}
	_, _ = writer.Write([]byte("READY\n"))
	got := logs.Snapshot()
	if len(got) != 1 || got[0].Text != "DEVBRIDGE_READY" {
		t.Fatalf("completed output = %+v", got)
	}
}

func TestDiagnosticBuildRequiresExpectedMCPIdentity(t *testing.T) {
	valid := []byte(`{"result":{"structuredContent":{"applicationName":"Dev Bridge","applicationVersion":"0.1.0-dev+g8019789","ready":true,"protocol":"MCP","serverName":"devbridge","serverVersion":"0.1.0-dev+g8019789","transport":"streamable-http"}}}`)
	if got := diagnosticBuild(valid); got != "0.1.0-dev+g8019789" {
		t.Fatalf("diagnosticBuild(valid) = %q", got)
	}
	foreign := []byte(`{"result":{"structuredContent":{"applicationName":"Other","applicationVersion":"fake","ready":true,"protocol":"MCP","serverName":"devbridge","serverVersion":"fake","transport":"streamable-http"}}}`)
	if got := diagnosticBuild(foreign); got != "" {
		t.Fatalf("diagnosticBuild(foreign) = %q", got)
	}
}

func TestHTTPProberReadsAuthoritativeCoreDataRootOnlyAfterReady(t *testing.T) {
	const dataRoot = "/authoritative/runtime/data"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			_, _ = io.WriteString(w, `{"ready":true}`)
		case "/ui/":
			_, _ = io.WriteString(w, `<title>Dev Bridge</title>`)
		case "/_adapters/identity":
			_, _ = io.WriteString(w, `{"dataRoot":"`+dataRoot+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	probe, err := (HTTPProber{}).Probe(context.Background(), Core, server.URL, "/readyz")
	if err != nil || !probe.Ready || !probe.Compatible || probe.DataRoot != dataRoot {
		t.Fatalf("Core probe = %+v, err=%v", probe, err)
	}
}

func TestHTTPProberKeepsCoreReadyButOmitsDataRootWhenIdentityReadbackFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			_, _ = io.WriteString(w, `{"ready":true}`)
		case "/ui/":
			_, _ = io.WriteString(w, `<title>Dev Bridge</title>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	probe, err := (HTTPProber{}).Probe(context.Background(), Core, server.URL, "/readyz")
	if err != nil || !probe.Ready || !probe.Compatible || probe.DataRoot != "" {
		t.Fatalf("Core probe = %+v, err=%v", probe, err)
	}
	if !strings.Contains(probe.Detail, "runtime identity readback failed") {
		t.Fatalf("Core probe detail = %q", probe.Detail)
	}
}

func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestHTTPProberCoreReadinessIndependentOfPresentation(t *testing.T) {
	for _, tc := range []struct {
		name, body, ui string
		status         int
		ready          bool
		detail         string
	}{
		{"production title", `{"ready":true}`, `<title>Dev Bridge</title>`, 200, true, "ready"},
		{"arbitrary title", `{"ready":true}`, `<title>Completely Different Product Text</title>`, 200, true, "ready"},
		{"empty UI", `{"ready":true}`, "", 200, true, "ready"},
		{"missing UI", `{"ready":true}`, "", 404, false, "Core Console HTTP 404"},
		{"not ready", `{"ready":false}`, `<title>Dev Bridge</title>`, 200, false, "unexpected readiness response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/readyz":
					io.WriteString(w, tc.body)
				case "/ui/":
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.ui)
				case "/_adapters/identity":
					io.WriteString(w, `{"dataRoot":"/isolated/data"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			result, err := (HTTPProber{}).Probe(context.Background(), Core, server.URL, "/readyz")
			if err != nil || !result.Reachable || result.Ready != tc.ready || (tc.ready && !result.Compatible) || !strings.Contains(result.Detail, tc.detail) {
				t.Fatalf("probe = %+v, err = %v", result, err)
			}
		})
	}
}

type readinessProbeFunc func(context.Context, Service, string, string) (ProbeResult, error)

func (f readinessProbeFunc) Probe(ctx context.Context, service Service, endpoint, path string) (ProbeResult, error) {
	return f(ctx, service, endpoint, path)
}

func TestWaitReadyTimeoutPreservesProbeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, detail string
		probeErr     error
	}{
		{"detail", "some readiness reason", nil},
		{"error", "", errors.New("probe transport failed")},
		{"empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			supervisor := testSupervisor(t, &fakeFactory{}, &fakeProber{})
			supervisor.prober = readinessProbeFunc(func(context.Context, Service, string, string) (ProbeResult, error) {
				return ProbeResult{Reachable: true, Detail: tc.detail}, tc.probeErr
			})
			start := time.Now()
			_, err := supervisor.waitReady(context.Background(), Core, supervisor.config.Core)
			if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "readiness timeout") || !strings.Contains(err.Error(), "context deadline exceeded") {
				t.Fatalf("timeout = %v", err)
			}
			if time.Since(start) < supervisor.config.StartupTimeout {
				t.Fatal("probe failure returned before startup timeout")
			}
			expected := tc.detail
			if tc.probeErr != nil {
				expected = tc.probeErr.Error()
			}
			if !strings.Contains(err.Error(), expected) {
				t.Fatalf("missing %q: %v", expected, err)
			}
		})
	}
}
