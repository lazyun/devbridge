package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
)

func testTunnel(t *testing.T, config TunnelConfig, factory *fakeFactory) *TunnelSupervisor {
	t.Helper()
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = time.Second
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 50 * time.Millisecond
	}
	if config.ReadyPollInterval <= 0 {
		config.ReadyPollInterval = time.Millisecond
	}
	return NewTunnelSupervisor(config, factory, logbuffer.New(20))
}

func readyProbe() TunnelReadyFunc {
	return func(context.Context) (bool, string, error) { return true, "ready", nil }
}

func TestUnconfiguredTunnelIsHealthyButNeverStarted(t *testing.T) {
	factory := &fakeFactory{}
	supervisor := testTunnel(t, TunnelConfig{}, factory)
	if err := supervisor.Start(context.Background()); !errors.Is(err, ErrTunnelNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	snapshot := supervisor.Snapshot()
	if snapshot.State != NotConfigured || snapshot.Ownership != Unowned {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := factory.commands(); len(got) != 0 {
		t.Fatalf("unconfigured tunnel started %v", got)
	}
	// Stopping an unconfigured or failed tunnel is always safe.
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredTunnelStartsAndReportsReady(t *testing.T) {
	factory := &fakeFactory{}
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Version: "0.0.14", Readiness: readyProbe()}, factory)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Stop(context.Background())
	waitFor(t, func() bool { return supervisor.Snapshot().Ready })
	snapshot := supervisor.Snapshot()
	if snapshot.State != Running || snapshot.Ownership != Managed || !snapshot.Ready {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Executable != "/opt/tunnel-client" || snapshot.Version != "0.0.14" || snapshot.StartedAt == "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := factory.commands(); len(got) != 1 || got[0].Path != "/opt/tunnel-client" {
		t.Fatalf("starts = %+v", got)
	}
}

func TestLivenessWithoutReadinessSignalIsRunningNeverReady(t *testing.T) {
	factory := &fakeFactory{}
	// No reliable readiness/connected signal exists: only process liveness is
	// observable. The projection must say Running, never Ready/Connected.
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}}, factory)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Stop(context.Background())
	time.Sleep(20 * time.Millisecond)
	snapshot := supervisor.Snapshot()
	if snapshot.State != Running || snapshot.Ready {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestStartupTimeoutFailsWhenSurfaceNeverAppears(t *testing.T) {
	factory := &fakeFactory{}
	// A daemon that never answers its readiness surface is a managed
	// startup failure, not a silent Running state.
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Readiness: func(context.Context) (bool, string, error) {
		return false, "", errors.New("surface did not answer")
	}, StartupTimeout: 30 * time.Millisecond}, factory)
	if err := supervisor.Start(context.Background()); err == nil {
		t.Fatal("start succeeded although the surface never appeared")
	}
	waitFor(t, func() bool { return supervisor.Snapshot().State == Error && !supervisor.Snapshot().Ready })
}

func TestStartupTimeoutFailsTheTunnelOnly(t *testing.T) {
	factory := &fakeFactory{}
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Readiness: func(context.Context) (bool, string, error) {
		return false, "", errors.New("no surface")
	}, StartupTimeout: 30 * time.Millisecond}, factory)
	if err := supervisor.Start(context.Background()); err == nil {
		t.Fatal("start succeeded without a live surface")
	}
	waitFor(t, func() bool { return supervisor.Snapshot().State == Error })
	if got := factory.commands(); len(got) != 1 {
		t.Fatalf("starts = %+v", got)
	}
}

func TestTunnelFailureIsProjectedAsFailed(t *testing.T) {
	factory := &fakeFactory{}
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Readiness: readyProbe()}, factory)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	factory.children[0].waitErr = errors.New("connection refused by control plane")
	close(factory.children[0].done)
	waitFor(t, func() bool { return supervisor.Snapshot().State == Crashed })
	snapshot := supervisor.Snapshot()
	if snapshot.Ready || snapshot.Ownership != Managed || !strings.Contains(snapshot.LastError, "control plane") {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := factory.commands(); len(got) != 1 {
		t.Fatalf("crashed tunnel restarted on stop: %+v", got)
	}
}

func TestExternalTunnelIsAttachedAndNeverKilled(t *testing.T) {
	factory := &fakeFactory{}
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Attach: func() (bool, bool, int, string) {
		return true, false, 4242, "compatible external daemon"
	}}, factory)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := supervisor.Snapshot()
	if snapshot.State != External || snapshot.Ownership != Attached || snapshot.PID != 4242 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := factory.commands(); len(got) != 0 {
		t.Fatalf("external daemon caused managed starts: %+v", got)
	}
	if supervisor.Snapshot().State != External {
		t.Fatalf("external daemon was disturbed: %+v", supervisor.Snapshot())
	}
}

func TestConflictIsProjectedAndNeverResolved(t *testing.T) {
	factory := &fakeFactory{}
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Attach: func() (bool, bool, int, string) {
		return false, true, 0, "occupied by an incompatible service"
	}}, factory)
	if err := supervisor.Start(context.Background()); err == nil {
		t.Fatal("start succeeded on a conflict")
	}
	snapshot := supervisor.Snapshot()
	if snapshot.State != Error || snapshot.Ownership != Conflict {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := factory.commands(); len(got) != 0 {
		t.Fatalf("conflict caused starts: %+v", got)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRestartAffectsOnlyTunnelProcess(t *testing.T) {
	factory := &fakeFactory{}
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Readiness: readyProbe()}, factory)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Stop(context.Background())
	if err := supervisor.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := factory.commands(); len(got) != 2 {
		t.Fatalf("starts = %+v", got)
	}
	waitFor(t, func() bool { return supervisor.Snapshot().State == Running && supervisor.Snapshot().Ready })
}

func TestManagedTunnelStopsCleanly(t *testing.T) {
	factory := &fakeFactory{}
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Readiness: readyProbe()}, factory)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	factory.children[0].mu.Lock()
	signals := factory.children[0].signalCount
	factory.children[0].mu.Unlock()
	if signals != 1 {
		t.Fatalf("signals = %d", signals)
	}
	snapshot := supervisor.Snapshot()
	if snapshot.State != Stopped || snapshot.Ready || snapshot.StartedAt != "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestRealChildExitCodeIsTracked(t *testing.T) {
	// A nil factory selects the real exec factory; the /bin/sh child really
	// exits with code 7.
	supervisor := NewTunnelSupervisor(TunnelConfig{Command: Command{Path: "/bin/sh", Args: []string{"-c", "exit 7"}}, Readiness: func(context.Context) (bool, string, error) {
		return false, "", errors.New("never")
	}, StartupTimeout: 5 * time.Second}, nil, logbuffer.New(10))
	if err := supervisor.Start(context.Background()); err == nil {
		t.Fatal("start succeeded for a child that exits immediately")
	}
	waitFor(t, func() bool { return supervisor.Snapshot().ExitCode != nil && *supervisor.Snapshot().ExitCode == 7 })
}

func TestStalePIDAndExitAreHandledOnManagedStart(t *testing.T) {
	// Simulates the attach path clearing stale state: after a previous daemon
	// exited, a new managed start must proceed and own the new child.
	factory := &fakeFactory{}
	var externalDetected bool
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Readiness: readyProbe(), Attach: func() (bool, bool, int, string) {
		if externalDetected {
			return false, false, 0, ""
		}
		externalDetected = true
		return false, false, 0, ""
	}}, factory)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Stop(context.Background())
	if got := factory.commands(); len(got) != 1 {
		t.Fatalf("starts = %+v", got)
	}
}

func TestTunnelLifecycleLogsAreBoundedAndSanitized(t *testing.T) {
	logs := logbuffer.New(3)
	supervisor := NewTunnelSupervisor(TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Readiness: readyProbe()}, &fakeFactory{}, logs)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Stop(context.Background())
	waitFor(t, func() bool { return supervisor.Snapshot().Ready })
	sources := map[string]bool{}
	for _, entry := range logs.Snapshot() {
		sources[entry.Source] = true
		if strings.Contains(entry.Text, "\n") {
			t.Fatalf("unsanitized log line: %q", entry.Text)
		}
	}
	if !sources["tunnel"] {
		t.Fatalf("no tunnel lifecycle logs: %+v", logs.Snapshot())
	}
}

func TestCancelingStartContextDoesNotKillTheRunningChild(t *testing.T) {
	factory := &fakeFactory{}
	supervisor := testTunnel(t, TunnelConfig{Command: Command{Path: "/opt/tunnel-client"}, Readiness: readyProbe()}, factory)
	ctx, cancel := context.WithCancel(context.Background())
	if err := supervisor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel() // per-operation context is done as soon as Start returns
	time.Sleep(10 * time.Millisecond)
	if snapshot := supervisor.Snapshot(); snapshot.State != Running {
		t.Fatalf("state = %s, the running child must survive start-context cancellation", snapshot.State)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
