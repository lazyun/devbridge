package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"net/http"
	"net/http/httptest"
	"net/url"

	codexprobe "github.com/lazyun/devbridge/desktop/internal/codex"
	"github.com/lazyun/devbridge/desktop/internal/execresolve"
	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
	runtimecontrol "github.com/lazyun/devbridge/desktop/internal/runtime"
	"github.com/lazyun/devbridge/desktop/internal/settings"
	tunnelpkg "github.com/lazyun/devbridge/desktop/internal/tunnel"
	"github.com/lazyun/devbridge/internal/codexruntime"
)

func TestDesktopInfoUsesDefaultConsoleEndpoint(t *testing.T) {
	info := NewApp().DesktopInfo()
	if info.Version != "0.1.0-dev" {
		t.Fatalf("Version = %q, want desktop development version", info.Version)
	}
	if info.ConsoleURL != defaultCoreConsoleURL {
		t.Fatalf("ConsoleURL = %q, want %q", info.ConsoleURL, defaultCoreConsoleURL)
	}
}

func TestEndpointAddressRequiresExplicitPort(t *testing.T) {
	if got, err := endpointAddress("http://127.0.0.1:8081"); err != nil || got != "127.0.0.1:8081" {
		t.Fatalf("endpointAddress() = %q, %v", got, err)
	}
	if _, err := endpointAddress("http://127.0.0.1"); err == nil {
		t.Fatal("endpointAddress accepted missing port")
	}
}

func TestResolveExecutableHonorsCustomCommandName(t *testing.T) {
	if got := resolveExecutable("custom-devbridge", "devbridge"); got != "custom-devbridge" {
		t.Fatalf("resolveExecutable() = %q", got)
	}
}

// The persisted shipped settings carry bare default names. A packaged
// application must resolve them from the adjacent build layout next to its
// bundle, never return the bare names verbatim for spawn.
func TestManagedRuntimeConfigResolvesDefaultBareNamesFromAdjacentLayout(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	for _, name := range []string{execresolve.DefaultName(execresolve.Core), execresolve.DefaultName(execresolve.MCP), execresolve.DefaultName(execresolve.LocalMCP)} {
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate the packaged layout through the resolver-options seam: the
	// Desktop executable sits inside the bundle under root and PATH is
	// empty, so only adjacent discovery can succeed.
	resolveOptions = func() execresolve.Options {
		return execresolve.Options{
			ExecutablePath: filepath.Join(root, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop"),
			Env:            []string{},
		}
	}
	t.Cleanup(func() { resolveOptions = nil })
	want := settings.Defaults()
	want.DataRoot = filepath.Join(t.TempDir(), "data")
	logs := logbuffer.New(20)
	config, err := managedRuntimeConfig(want, logs)
	if err != nil {
		t.Fatal(err)
	}
	// The running test binary is not inside a bundle, so the dev-build root
	// bundle bin is discovered. Assert the three commands resolve from one
	// coherent adjacent location and are no longer bare names.
	dir := filepath.Dir(config.Core.Command.Path)
	canonical, err := filepath.EvalSymlinks(filepath.Join(root, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	if dir != canonical {
		t.Fatalf("core path = %q, want adjacent bundle bin %q", config.Core.Command.Path, canonical)
	}
	if config.MCP.Command.Path != filepath.Join(dir, execresolve.DefaultName(execresolve.MCP)) {
		t.Fatalf("mcp path = %q, want sibling of core", config.MCP.Command.Path)
	}
	if config.LocalMCP.Command.Path != filepath.Join(dir, execresolve.DefaultName(execresolve.LocalMCP)) {
		t.Fatalf("local mcp path = %q, want sibling of core", config.LocalMCP.Command.Path)
	}
	entries := logs.Snapshot()
	joined := ""
	for _, entry := range entries {
		joined += entry.Text + "\n"
	}
	if !strings.Contains(joined, "runtime executable: adjacent build (") {
		t.Fatalf("resolution logs missing adjacent-build source: %s", joined)
	}
}

// A broken adjacent build (non-executable binary) must fail configuration
// instead of silently falling back to an unrelated PATH binary.
func TestManagedRuntimeConfigFailsFastOnBrokenAdjacentBuild(t *testing.T) {
	_ = t.TempDir()
	want := settings.Defaults()
	want.DataRoot = filepath.Join(t.TempDir(), "data")
	want.CoreBinary = filepath.Join(t.TempDir(), "bin", "devbridge")
	if err := os.MkdirAll(filepath.Dir(want.CoreBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want.CoreBinary, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An explicit configured path containing a separator is returned
	// verbatim, so this exercises the configured-path branch; the fatal
	// NotExecutableError path is covered in the resolver tests.
	config, err := managedRuntimeConfig(want, logbuffer.New(20))
	if err != nil {
		t.Fatalf("configured path is authoritative even if not executable at config time: %v", err)
	}
	if config.Core.Command.Path != want.CoreBinary {
		t.Fatalf("core path = %q, want configured", config.Core.Command.Path)
	}
}

func TestAppSettingsPersistWithoutRepositoryState(t *testing.T) {
	root := t.TempDir()
	paths := settings.PathsFor(filepath.Join(root, "config"), filepath.Join(root, "state"))
	app := newApp(paths, nil)
	if err := app.configure(); err != nil {
		t.Fatal(err)
	}
	want := settings.Defaults()
	want.DataRoot = filepath.Join(root, "bridge-data")
	want.CoreBinary = filepath.Join(root, "bin", "devbridge")
	if err := app.SaveSettings(want); err != nil {
		t.Fatal(err)
	}
	reloaded := newApp(paths, nil)
	if err := reloaded.configure(); err != nil {
		t.Fatal(err)
	}
	wantProjection := DesktopSettings{Settings: want, CodexModelCandidates: codexruntime.ModelCandidates(), PiModelCandidates: []string{}}
	if got := reloaded.GetSettings(); !reflect.DeepEqual(got, wantProjection) {
		t.Fatalf("settings = %#v, want %#v", got, want)
	}
	projection := reloaded.GetSettings()
	projection.CodexModelCandidates[0] = "mutated"
	if reloaded.GetSettings().CodexModelCandidates[0] != "gpt-5.6-luna" {
		t.Fatal("GetSettings returned shared model candidate storage")
	}
}

func TestManagedLocalMCPCommandSharesDataRootAndStaysLoopback(t *testing.T) {
	want := settings.Defaults()
	want.DataRoot = filepath.Join(t.TempDir(), "data")
	want.LocalMCPBinary = filepath.Join(t.TempDir(), "bin", "devbridge-local-mcp")
	config, err := managedRuntimeConfig(want, logbuffer.New(20))
	if err != nil {
		t.Fatal(err)
	}
	args := config.LocalMCP.Command.Args
	if got := config.LocalMCP.Command.Path; got != want.LocalMCPBinary {
		t.Fatalf("local mcp path = %q", got)
	}
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-data-root" && args[i+1] == want.DataRoot {
			found = true
		}
		if args[i] == "-listen" && args[i+1] != "127.0.0.1:8090" {
			t.Fatalf("local mcp listen = %q", args[i+1])
		}
	}
	if !found {
		t.Fatalf("local mcp args = %v, missing shared DataRoot", args)
	}
	for _, arg := range args {
		if arg == "-allow-non-loopback" || arg == "0.0.0.0" || arg == "::" {
			t.Fatalf("local mcp args contain forbidden listener configuration: %v", args)
		}
	}
	if !contains(args, "-core-url") || !contains(args, want.CoreEndpoint) {
		t.Fatalf("local mcp args = %v, missing Core endpoint", args)
	}
}

func TestLocalMCPEndpointSettingsValidation(t *testing.T) {
	valid := settings.Defaults()
	if err := valid.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	nonLoopback := valid
	nonLoopback.LocalMCPEndpoint = "http://0.0.0.0:8090"
	if err := nonLoopback.Validate(); err == nil {
		t.Fatal("non-loopback Local MCP endpoint accepted")
	}
	missing := valid
	missing.LocalMCPEndpoint = ""
	if err := missing.Validate(); err != nil {
		t.Fatalf("empty Local MCP endpoint should stay valid: %v", err)
	}
}

func TestLocalMCPSettingsChangeRequiresManagedStackStop(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	current := settings.DefaultsFor(paths.DefaultDataRoot)
	app.settings = current
	app.supervisor, _ = appTestSupervisor(t, false)
	if err := app.supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.supervisor.Stop(context.Background()) }()
	changed := current
	changed.LocalMCPBinary = filepath.Join(t.TempDir(), "other-local-mcp")
	if err := app.SaveSettings(changed); err == nil || err.Error() != "stop the managed stack before changing settings" {
		t.Fatalf("active change error = %v", err)
	}
}

type appRuntimeInventoryAPI struct {
	mu          sync.Mutex
	snapshot    runtimeInventorySnapshot
	applyErrors []error
	applyFirst  []bool
	applies     []runtimeInventorySnapshot
}

func (f *appRuntimeInventoryAPI) Get(context.Context, string) (runtimeInventorySnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, nil
}

func (f *appRuntimeInventoryAPI) Apply(_ context.Context, _ string, value runtimeInventorySnapshot) (runtimeInventorySnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies = append(f.applies, value)
	index := len(f.applies) - 1
	if index < len(f.applyErrors) && f.applyErrors[index] != nil {
		if index < len(f.applyFirst) && f.applyFirst[index] {
			f.snapshot = runtimeInventorySnapshot{Revision: f.snapshot.Revision + 1, CodingAgentModels: cloneCodingAgentModels(value.CodingAgentModels)}
		}
		return runtimeInventorySnapshot{}, f.applyErrors[index]
	}
	f.snapshot = runtimeInventorySnapshot{Revision: f.snapshot.Revision + 1, CodingAgentModels: cloneCodingAgentModels(value.CodingAgentModels)}
	return f.snapshot, nil
}

type appSettingsStore struct {
	value settings.Settings
	err   error
	saves int
}

func (s *appSettingsStore) Load() (settings.Settings, error) { return s.value, nil }
func (s *appSettingsStore) Save(value settings.Settings) error {
	s.saves++
	if s.err != nil {
		return s.err
	}
	s.value = value
	return nil
}

func TestRunningManagedCoreHotAppliesInventoryWithoutRestart(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	current := settings.DefaultsFor(paths.DefaultDataRoot)
	app.settings = current
	var factory *appTestFactory
	app.supervisor, factory = appTestSupervisor(t, false)
	if err := app.supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.supervisor.Stop(context.Background())
	api := &appRuntimeInventoryAPI{snapshot: runtimeInventorySnapshot{Revision: 1, CodingAgentModels: cloneCodingAgentModels(current.CodingAgentModels)}}
	app.runtimeAPI = api
	factory.mu.Lock()
	childrenBefore := len(factory.children)
	factory.mu.Unlock()
	changed := current
	changed.CodingAgentModels = cloneCodingAgentModels(current.CodingAgentModels)
	changed.CodingAgentModels[0].DefaultReasoningEffort = "high"
	tier := "priority"
	changed.CodingAgentModels = append(changed.CodingAgentModels, codexruntime.CodingAgentModelConfig{ModelID: "gpt-new-model", Enabled: true, SupportedReasoningEfforts: []string{"high"}, DefaultReasoningEffort: "high", FastServiceTier: &tier})
	if err := app.SaveSettings(changed); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	childrenAfter := len(factory.children)
	factory.mu.Unlock()
	if childrenBefore != childrenAfter {
		t.Fatalf("inventory save relaunched stack: children before=%d after=%d", childrenBefore, childrenAfter)
	}
	if len(api.applies) != 1 || !reflect.DeepEqual(api.snapshot.CodingAgentModels, changed.CodingAgentModels) {
		t.Fatalf("Core applies=%+v snapshot=%+v", api.applies, api.snapshot)
	}
	loaded, err := settings.NewStore(paths).Load()
	if err != nil || !reflect.DeepEqual(loaded.CodingAgentModels, changed.CodingAgentModels) {
		t.Fatalf("persisted settings=%+v err=%v", loaded, err)
	}
}

func TestRunningInventoryApplyAndPersistFailuresDoNotReportSuccess(t *testing.T) {
	for _, test := range []struct {
		name         string
		applyErrors  []error
		storeErr     error
		wantApplies  int
		wantSaves    int
		applyFirst   []bool
		wantRollback bool
	}{
		{name: "apply fails", applyErrors: []error{errors.New("Core exited")}, wantApplies: 1, wantSaves: 0},
		{name: "apply transport fails after commit and rolls back", applyErrors: []error{&url.Error{Op: "PUT", URL: "http://127.0.0.1", Err: errors.New("connection reset")}}, applyFirst: []bool{true}, wantApplies: 2, wantSaves: 0, wantRollback: true},
		{name: "persist fails and rolls back", storeErr: errors.New("disk full"), wantApplies: 2, wantSaves: 1, wantRollback: true},
		{name: "persist and rollback fail", applyErrors: []error{nil, errors.New("Core exited")}, storeErr: errors.New("disk full"), wantApplies: 2, wantSaves: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := settings.PathsFor(t.TempDir(), t.TempDir())
			app := newApp(paths, nil)
			current := settings.DefaultsFor(paths.DefaultDataRoot)
			app.settings = current
			app.supervisor, _ = appTestSupervisor(t, false)
			if err := app.supervisor.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer app.supervisor.Stop(context.Background())
			api := &appRuntimeInventoryAPI{snapshot: runtimeInventorySnapshot{Revision: 4, CodingAgentModels: cloneCodingAgentModels(current.CodingAgentModels)}, applyErrors: test.applyErrors, applyFirst: test.applyFirst}
			store := &appSettingsStore{value: current, err: test.storeErr}
			app.runtimeAPI, app.store = api, store
			changed := current
			changed.CodingAgentModels = cloneCodingAgentModels(current.CodingAgentModels)
			changed.CodingAgentModels[0].DefaultReasoningEffort = "high"
			if err := app.SaveSettings(changed); err == nil {
				t.Fatal("SaveSettings succeeded")
			}
			if len(api.applies) != test.wantApplies {
				t.Fatalf("apply calls=%d want=%d", len(api.applies), test.wantApplies)
			}
			if store.saves != test.wantSaves {
				t.Fatalf("store saves=%d want=%d", store.saves, test.wantSaves)
			}
			if got := app.GetSettings().CodingAgentModels; !reflect.DeepEqual(got, current.CodingAgentModels) {
				t.Fatalf("Desktop settings advanced after failure: %v", got)
			}
			if test.wantRollback && !reflect.DeepEqual(api.snapshot.CodingAgentModels, current.CodingAgentModels) {
				t.Fatalf("Core snapshot was not rolled back: %+v", api.snapshot)
			}
		})
	}
}

func TestStatusProjectsLocalMCPEndpointAndState(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	if err := app.configure(); err != nil {
		t.Fatal(err)
	}
	status := app.Status()
	if status.LocalMCP.Service != runtimecontrol.LocalMCP {
		t.Fatalf("local mcp service = %q", status.LocalMCP.Service)
	}
	if got := status.LocalMCP.Endpoint; got != "http://127.0.0.1:8090" {
		t.Fatalf("local mcp endpoint = %q", got)
	}
	if status.LocalMCP.State != runtimecontrol.Stopped {
		t.Fatalf("local mcp state = %q", status.LocalMCP.State)
	}
}

func TestManagedCommandsUseSameDataRoot(t *testing.T) {
	want := settings.Defaults()
	want.DataRoot = filepath.Join(t.TempDir(), "data")
	want.CodingAgentModels = cloneCodingAgentModels(want.CodingAgentModels)
	want.CodingAgentModels[3].DefaultReasoningEffort = "xhigh"
	config, err := managedRuntimeConfig(want, logbuffer.New(20))
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{"Core": config.Core.Command.Args, "MCP": config.MCP.Command.Args, "Local MCP": config.LocalMCP.Command.Args} {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-data-root" && args[i+1] == want.DataRoot {
				found = true
			}
			if args[i] == "-state-root" || args[i] == "-worktree-root" || args[i] == "-log-dir" {
				t.Fatalf("%s retained obsolete storage argument: %v", name, args)
			}
		}
		if !found {
			t.Fatalf("%s args = %v, missing shared DataRoot", name, args)
		}
		if name == "Core" {
			foundConfig := false
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "-coding-agent-model-configs" && args[i+1] == mustRuntimeModelConfigs(want.CodingAgentModels) {
					foundConfig = true
				}
			}
			if !foundConfig {
				t.Fatalf("%s args = %v, missing model-local runtime configuration", name, args)
			}
		}
		if name != "Core" && (!contains(args, "-core-url") || !contains(args, want.CoreEndpoint)) {
			t.Fatalf("%s args = %v, missing Core endpoint", name, args)
		}
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestChooseDataRootUpdatesDraftAndCancelPreservesIt(t *testing.T) {
	app := newApp(settings.PathsFor(t.TempDir(), t.TempDir()), nil)
	selected := t.TempDir()
	app.pickDirectory = func(context.Context, string) (string, error) { return selected, nil }
	got, err := app.ChooseDataRoot("previous")
	resolved, _ := filepath.EvalSymlinks(selected)
	if err != nil || got != resolved {
		t.Fatalf("selected = %q, %v", got, err)
	}
	app.pickDirectory = func(context.Context, string) (string, error) { return "", nil }
	got, err = app.ChooseDataRoot(resolved)
	if err != nil || got != resolved {
		t.Fatalf("cancel = %q, %v", got, err)
	}
}

type appTestProcess struct {
	done chan struct{}
	once sync.Once
}

func (p *appTestProcess) Wait() error { <-p.done; return nil }
func (p *appTestProcess) Signal(os.Signal) error {
	p.once.Do(func() { close(p.done) })
	return nil
}
func (p *appTestProcess) Kill() error { return p.Signal(os.Kill) }

type appTestFactory struct {
	mu       sync.Mutex
	children []*appTestProcess
	commands []runtimecontrol.Command
}

func (f *appTestFactory) Start(_ context.Context, command runtimecontrol.Command, _, _ io.Writer) (runtimecontrol.Process, error) {
	child := &appTestProcess{done: make(chan struct{})}
	f.mu.Lock()
	f.children = append(f.children, child)
	f.commands = append(f.commands, command)
	f.mu.Unlock()
	return child, nil
}

type appTestProber struct {
	external            bool
	identityUnavailable bool
	dataRoot            string
	mu                  sync.Mutex
	calls               map[runtimecontrol.Service]int
}

func (p *appTestProber) Probe(_ context.Context, service runtimecontrol.Service, _, _ string) (runtimecontrol.ProbeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	dataRoot := p.dataRoot
	if dataRoot == "" && !p.identityUnavailable {
		dataRoot = "/authoritative/test-data"
	}
	if p.external {
		return runtimecontrol.ProbeResult{Reachable: true, Compatible: true, Ready: true, Build: "test", DataRoot: dataRoot}, nil
	}
	p.calls[service]++
	if p.calls[service]%2 == 1 {
		return runtimecontrol.ProbeResult{}, nil
	}
	result := runtimecontrol.ProbeResult{Reachable: true, Compatible: true, Ready: true, Build: "test"}
	if service == runtimecontrol.Core {
		result.DataRoot = dataRoot
	}
	return result, nil
}

type gatedAppProber struct {
	gate  <-chan struct{}
	mu    sync.Mutex
	calls map[runtimecontrol.Service]int
	root  string
}

func (p *gatedAppProber) Probe(ctx context.Context, service runtimecontrol.Service, _, _ string) (runtimecontrol.ProbeResult, error) {
	p.mu.Lock()
	p.calls[service]++
	call := p.calls[service]
	root := p.root
	p.mu.Unlock()
	if call%2 == 1 {
		return runtimecontrol.ProbeResult{}, nil
	}
	if service == runtimecontrol.Core {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return runtimecontrol.ProbeResult{}, ctx.Err()
		}
		return runtimecontrol.ProbeResult{Reachable: true, Compatible: true, Ready: true, Build: "test", DataRoot: root}, nil
	}
	return runtimecontrol.ProbeResult{Reachable: true, Compatible: true, Ready: true, Build: "test"}, nil
}

type neverReadyAppProber struct{}

func (neverReadyAppProber) Probe(context.Context, runtimecontrol.Service, string, string) (runtimecontrol.ProbeResult, error) {
	return runtimecontrol.ProbeResult{}, nil
}

func TestManagedDataRootWaitsForReadyThenRefreshesAfterStartAndRestart(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	// This tests Core readiness, not the latency of the host's Codex install.
	value := settings.DefaultsFor(paths.DefaultDataRoot)
	value.CodexExecutable = filepath.Join(t.TempDir(), "missing-codex")
	if err := app.store.Save(value); err != nil {
		t.Fatal(err)
	}
	factory := &appTestFactory{}
	gate := make(chan struct{})
	prober := &gatedAppProber{gate: gate, calls: map[runtimecontrol.Service]int{}, root: "/runtime/first"}
	app.stackFactory, app.prober = factory, prober
	if err := app.configure(); err != nil {
		t.Fatal(err)
	}
	if got := app.Status().DataRoot; got != "" {
		t.Fatalf("DataRoot before startup = %q, want no optimistic identity", got)
	}

	started := make(chan error, 1)
	go func() { started <- app.Start() }()
	deadline := time.Now().Add(time.Second)
	for {
		factory.mu.Lock()
		children := len(factory.children)
		factory.mu.Unlock()
		if children > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("managed Core process was not created")
		}
		time.Sleep(time.Millisecond)
	}
	if got := app.Status().DataRoot; got != "" {
		t.Fatalf("DataRoot while readiness is pending = %q, want no optimistic identity", got)
	}
	close(gate)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if got := app.Status().DataRoot; got != "/runtime/first" {
		t.Fatalf("DataRoot after ready = %q", got)
	}

	prober.mu.Lock()
	prober.root = "/runtime/second"
	prober.mu.Unlock()
	if err := app.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	if got := app.Status().DataRoot; got != "/runtime/second" {
		t.Fatalf("DataRoot after Stop and Start = %q", got)
	}

	prober.mu.Lock()
	prober.root = "/runtime/restarted"
	prober.mu.Unlock()
	if err := app.Restart(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop() }()
	if status := app.Status(); status.DataRoot != "/runtime/restarted" || status.Core.State != runtimecontrol.Running {
		t.Fatalf("status after Restart = %+v", status)
	}
}

func TestManagedDataRootReadbackAndStartupFailuresPreserveLastProjection(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	prober := &appTestProber{calls: map[runtimecontrol.Service]int{}, dataRoot: "/runtime/previous"}
	app.stackFactory, app.prober = &appTestFactory{}, prober
	if err := app.configure(); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	if got := app.Status().DataRoot; got != "/runtime/previous" {
		t.Fatalf("initial DataRoot = %q", got)
	}
	prober.mu.Lock()
	prober.dataRoot, prober.identityUnavailable = "", true
	prober.mu.Unlock()
	if err := app.Restart(); err == nil || !strings.Contains(err.Error(), "Core DataRoot is unavailable") {
		t.Fatalf("Restart readback error = %v", err)
	}
	if status := app.Status(); status.DataRoot != "/runtime/previous" || status.Core.State != runtimecontrol.Running || !strings.Contains(status.LastError, "Core DataRoot is unavailable") {
		t.Fatalf("status after readback failure = %+v", status)
	}
	_ = app.Stop()

	failing := newApp(settings.PathsFor(t.TempDir(), t.TempDir()), nil)
	failing.settings = settings.DefaultsFor(t.TempDir())
	failing.ctx = context.Background()
	failedSupervisor, err := runtimecontrol.NewRuntimeSupervisor(runtimecontrol.Config{
		Core:           runtimecontrol.ServiceConfig{Command: runtimecontrol.Command{Path: "core"}, Endpoint: "http://127.0.0.1:18081"},
		MCP:            runtimecontrol.ServiceConfig{Command: runtimecontrol.Command{Path: "mcp"}, Endpoint: "http://127.0.0.1:18080"},
		StartupTimeout: 10 * time.Millisecond, ShutdownTimeout: 10 * time.Millisecond, ProbeInterval: time.Millisecond,
	}, &appTestFactory{}, neverReadyAppProber{}, logbuffer.New(20))
	if err != nil {
		t.Fatal(err)
	}
	failing.supervisor = failedSupervisor
	if err := failing.Start(); err == nil {
		t.Fatal("startup unexpectedly succeeded")
	}
	if status := failing.Status(); status.DataRoot != "" || status.LastError == "" {
		t.Fatalf("failed startup fabricated DataRoot: %+v", status)
	}
}

func appTestSupervisor(t *testing.T, external bool) (*runtimecontrol.RuntimeSupervisor, *appTestFactory) {
	t.Helper()
	factory := &appTestFactory{}
	prober := &appTestProber{external: external, calls: map[runtimecontrol.Service]int{}}
	supervisor, err := runtimecontrol.NewRuntimeSupervisor(runtimecontrol.Config{
		Core:           runtimecontrol.ServiceConfig{Command: runtimecontrol.Command{Path: "core"}, Endpoint: "http://127.0.0.1:18081"},
		MCP:            runtimecontrol.ServiceConfig{Command: runtimecontrol.Command{Path: "mcp"}, Endpoint: "http://127.0.0.1:18080"},
		StartupTimeout: time.Second, ShutdownTimeout: time.Second, ProbeInterval: time.Millisecond,
	}, factory, prober, logbuffer.New(20))
	if err != nil {
		t.Fatal(err)
	}
	return supervisor, factory
}

func TestDataRootChangeRequiresManagedStackStop(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	current := settings.DefaultsFor(paths.DefaultDataRoot)
	app.settings = current
	app.supervisor, _ = appTestSupervisor(t, false)
	if err := app.supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	changed := current
	changed.DataRoot = filepath.Join(t.TempDir(), "next-data")
	if err := app.SaveSettings(changed); err == nil || err.Error() != "Stop Dev Bridge before changing the active data directory." {
		t.Fatalf("active change error = %v", err)
	}
	if err := app.supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveSettings(changed); err != nil {
		t.Fatalf("save after stop: %v", err)
	}
}

func TestDataRootChangeDoesNotRestartOrStopExternalServices(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	current := settings.DefaultsFor(paths.DefaultDataRoot)
	app.settings = current
	var factory *appTestFactory
	app.supervisor, factory = appTestSupervisor(t, true)
	if err := app.supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	changed := current
	changed.DataRoot = filepath.Join(t.TempDir(), "other-data")
	if err := app.SaveSettings(changed); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	started := len(factory.children)
	factory.mu.Unlock()
	if started != 0 {
		t.Fatalf("external services caused %d managed starts", started)
	}
}

type recordedFactory struct {
	name  string
	inner runtimecontrol.ProcessFactory
	seq   *[]string
	mu    sync.Mutex
}

func (f *recordedFactory) Start(ctx context.Context, c runtimecontrol.Command, out, err io.Writer) (runtimecontrol.Process, error) {
	f.mu.Lock()
	*f.seq = append(*f.seq, f.name)
	f.mu.Unlock()
	return f.inner.Start(ctx, c, out, err)
}

func tunnelTestApp(t *testing.T) (*App, *appTestFactory, *appTestFactory, *[]string) {
	t.Helper()
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	app.tunnelStartupTimeout = 2 * time.Second
	app.tunnelShutdownTimeout = time.Second
	app.tunnelReadyPollInterval = 5 * time.Millisecond
	stackFactory := &appTestFactory{}
	tunnelFactory := &appTestFactory{}
	sequence := &[]string{}
	app.stackFactory = &recordedFactory{name: "stack", inner: stackFactory, seq: sequence}
	app.tunnelFactory = &recordedFactory{name: "tunnel", inner: tunnelFactory, seq: sequence}
	app.prober = &appTestProber{calls: map[runtimecontrol.Service]int{}}
	if err := app.configure(); err != nil {
		t.Fatal(err)
	}
	return app, stackFactory, tunnelFactory, sequence
}

func recordedNames(sequence *[]string) []string {
	return append([]string(nil), *sequence...)
}

// tunnelSurface simulates a live tunnel-client operator surface: /healthz
// reports liveness, /readyz reports the configured readiness verdict. The
// health URL file is written only after the supervisor's attach check has
// run, so the daemon is started as MANAGED.
func tunnelSurface(t *testing.T, app *App, readyBody string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The simulated surface only answers while the supervised tunnel is
		// observable; once the supervisor projects a stopped/failed tunnel,
		// the simulated daemon is gone too.
		switch app.Status().Tunnel.State {
		case runtimecontrol.Running, runtimecontrol.Starting, runtimecontrol.External:
		default:
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("live"))
			return
		}
		_, _ = w.Write([]byte(readyBody))
	}))
	t.Cleanup(server.Close)
	go func() {
		files := tunnelpkg.RuntimeFilesIn(app.paths.TunnelDir)
		time.Sleep(50 * time.Millisecond)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			// Rewrite the health URL file only while the supervisor has a
			// live managed daemon, never during the attach check.
			switch app.Status().Tunnel.State {
			case runtimecontrol.Starting, runtimecontrol.Running:
				_ = os.WriteFile(files.HealthURLFile, []byte(server.URL), 0600)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

func TestUnconfiguredTunnelDoesNotBlockCoreOrMCP(t *testing.T) {
	app, _, tunnelFactory, sequence := tunnelTestApp(t)
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop() }()
	if names := recordedNames(sequence); len(names) != 3 || names[0] != "stack" || names[1] != "stack" || names[2] != "stack" {
		t.Fatalf("start sequence = %v", names)
	}
	if got := tunnelFactory.children; len(got) != 0 {
		t.Fatalf("unconfigured tunnel started %d children", len(got))
	}
	status := app.Status()
	if status.Tunnel.State != runtimecontrol.NotConfigured {
		t.Fatalf("tunnel state = %q", status.Tunnel.State)
	}
	if status.ChatGPTIntegration != "Unavailable" {
		t.Fatalf("chatgpt integration = %q", status.ChatGPTIntegration)
	}
	if status.Core.State != runtimecontrol.Running || status.MCP.State != runtimecontrol.Running || status.LocalMCP.State != runtimecontrol.Running {
		t.Fatalf("core/mcp/local = %+v / %+v / %+v", status.Core, status.MCP, status.LocalMCP)
	}
}

func TestConfiguredTunnelStartsAfterMCPReadiness(t *testing.T) {
	app, _, _, _ := tunnelTestApp(t)
	value := app.GetSettings().Settings
	script := filepath.Join(t.TempDir(), "tunnel-client")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 0.0.14+test\n"), 0755); err != nil {
		t.Fatal(err)
	}
	value.OpenAITunnelExecutable = script
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	if err := app.SaveSettings(value); err != nil {
		t.Fatal(err)
	}
	// A live /readyz surface behind the health URL file lets the managed
	// daemon reach Ready once the supervisor discovers it.
	tunnelSurface(t, app, "ready")
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop() }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := app.Status()
		if status.Tunnel.Ready && status.ChatGPTIntegration == "Available" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("tunnel never became ready: %+v", app.Status().Tunnel)
}

func TestTunnelFailureDoesNotStopCoreOrMCP(t *testing.T) {
	app, _, _, _ := tunnelTestApp(t)
	value := app.GetSettings().Settings
	script := filepath.Join(t.TempDir(), "tunnel-client")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 0.0.14+test\n"), 0755); err != nil {
		t.Fatal(err)
	}
	value.OpenAITunnelExecutable = script
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	// Point the launch at an invalid tunnel id is impossible through
	// validation; instead fail the start by removing the executable.
	if err := app.SaveSettings(value); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	app.tunnelStartupTimeout = 50 * time.Millisecond
	if err := app.Start(); err != nil {
		t.Fatalf("stack start failed because of the tunnel: %v", err)
	}
	defer func() { _ = app.Stop() }()
	status := app.Status()
	if status.Core.State != runtimecontrol.Running || status.MCP.State != runtimecontrol.Running {
		t.Fatalf("core/mcp = %+v / %+v", status.Core, status.MCP)
	}
	if status.Tunnel.State == runtimecontrol.Running || status.Tunnel.Ready {
		t.Fatalf("tunnel should not be ready: %+v", status.Tunnel)
	}
	if status.ChatGPTIntegration != "Unavailable" {
		t.Fatalf("chatgpt integration = %q", status.ChatGPTIntegration)
	}
}

func TestStartTunnelRetriesOnlyTheTunnel(t *testing.T) {
	app, stackFactory, tunnelFactory, _ := tunnelTestApp(t)
	value := app.GetSettings().Settings
	script := filepath.Join(t.TempDir(), "tunnel-client")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 0.0.14+test\n"), 0755); err != nil {
		t.Fatal(err)
	}
	value.OpenAITunnelExecutable = script
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	if err := app.SaveSettings(value); err != nil {
		t.Fatal(err)
	}
	tunnelSurface(t, app, "ready")
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop() }()
	stackFactory.mu.Lock()
	stackChildren := len(stackFactory.children)
	stackFactory.mu.Unlock()
	if err := app.StartTunnel(); err != nil {
		t.Fatal(err)
	}
	stackFactory.mu.Lock()
	defer stackFactory.mu.Unlock()
	if len(stackFactory.children) != stackChildren {
		t.Fatalf("stack children grew from %d to %d", stackChildren, len(stackFactory.children))
	}
	tunnelFactory.mu.Lock()
	defer tunnelFactory.mu.Unlock()
	if len(tunnelFactory.children) < 1 {
		t.Fatalf("tunnel was never started")
	}
}

func TestRestartTunnelAffectsOnlyTunnel(t *testing.T) {
	app, stackFactory, tunnelFactory, _ := tunnelTestApp(t)
	value := app.GetSettings().Settings
	script := filepath.Join(t.TempDir(), "tunnel-client")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 0.0.14+test\n"), 0755); err != nil {
		t.Fatal(err)
	}
	value.OpenAITunnelExecutable = script
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	if err := app.SaveSettings(value); err != nil {
		t.Fatal(err)
	}
	tunnelSurface(t, app, "ready")
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop() }()
	stackFactory.mu.Lock()
	stackChildren := len(stackFactory.children)
	stackFactory.mu.Unlock()
	if err := app.RestartTunnel(); err != nil {
		t.Fatal(err)
	}
	stackFactory.mu.Lock()
	defer stackFactory.mu.Unlock()
	if len(stackFactory.children) != stackChildren {
		t.Fatalf("restart touched the stack: %d -> %d", stackChildren, len(stackFactory.children))
	}
	tunnelFactory.mu.Lock()
	defer tunnelFactory.mu.Unlock()
	if len(tunnelFactory.children) != 2 {
		t.Fatalf("tunnel children = %d, want 2 (start + restart)", len(tunnelFactory.children))
	}
}

func TestStopTunnelStopsOnlyManagedTunnel(t *testing.T) {
	app, stackFactory, tunnelFactory, _ := tunnelTestApp(t)
	value := app.GetSettings().Settings
	script := filepath.Join(t.TempDir(), "tunnel-client")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 0.0.14+test\n"), 0755); err != nil {
		t.Fatal(err)
	}
	value.OpenAITunnelExecutable = script
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	if err := app.SaveSettings(value); err != nil {
		t.Fatal(err)
	}
	tunnelSurface(t, app, "ready")
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop() }()
	if err := app.StopTunnel(); err != nil {
		t.Fatal(err)
	}
	status := app.Status()
	if status.Tunnel.State != runtimecontrol.Stopped {
		t.Fatalf("tunnel state = %q", status.Tunnel.State)
	}
	if status.Core.State != runtimecontrol.Running || status.MCP.State != runtimecontrol.Running {
		t.Fatalf("stopping the tunnel disturbed the stack: %+v / %+v", status.Core, status.MCP)
	}
	stackFactory.mu.Lock()
	stackChildren := len(stackFactory.children)
	stackFactory.mu.Unlock()
	tunnelFactory.mu.Lock()
	tunnelChildren := len(tunnelFactory.children)
	tunnelFactory.mu.Unlock()
	if tunnelChildren != 1 || stackChildren != 3 {
		t.Fatalf("children stack=%d tunnel=%d", stackChildren, tunnelChildren)
	}
}

func TestTunnelLivenessIsNotLabeledReady(t *testing.T) {
	app, _, _, _ := tunnelTestApp(t)
	value := app.GetSettings().Settings
	script := filepath.Join(t.TempDir(), "tunnel-client")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 0.0.14+test\n"), 0755); err != nil {
		t.Fatal(err)
	}
	value.OpenAITunnelExecutable = script
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	if err := app.SaveSettings(value); err != nil {
		t.Fatal(err)
	}
	// The daemon answers its operator surface but /readyz never reports the
	// connected verdict: status must say Running, ChatGPT Integration stays
	// Unavailable.
	tunnelSurface(t, app, "oauth discovery failed: connection refused")
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop() }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := app.Status()
		if status.Tunnel.State == runtimecontrol.Running {
			if status.Tunnel.Ready || status.ChatGPTIntegration == "Available" {
				t.Fatalf("liveness falsely labeled ready: %+v", status.Tunnel)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("tunnel never reached Running: %+v", app.Status().Tunnel)
}

func TestChooseTunnelExecutableValidatesAndPreservesPrevious(t *testing.T) {
	app := newApp(settings.PathsFor(t.TempDir(), t.TempDir()), nil)
	dir := t.TempDir()
	good := filepath.Join(dir, "tunnel-client")
	if err := os.WriteFile(good, []byte("#!/bin/sh\necho \"0.0.14+ok\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho no\n"), 0644); err != nil {
		t.Fatal(err)
	}
	app.pickFile = func(context.Context, string) (string, error) { return good, nil }
	got, err := app.ChooseTunnelExecutable("/previous/tunnel-client")
	if err != nil || got != good {
		t.Fatalf("good selection = %q, %v", got, err)
	}
	app.pickFile = func(context.Context, string) (string, error) { return bad, nil }
	got, err = app.ChooseTunnelExecutable(good)
	if err == nil || got != good {
		t.Fatalf("invalid selection = %q, %v; previous must be preserved", got, err)
	}
	app.pickFile = func(context.Context, string) (string, error) { return dir, nil }
	got, err = app.ChooseTunnelExecutable(good)
	if err == nil || got != good {
		t.Fatalf("directory selection = %q, %v", got, err)
	}
	app.pickFile = func(context.Context, string) (string, error) { return "", nil }
	got, err = app.ChooseTunnelExecutable(good)
	if err != nil || got != good {
		t.Fatalf("cancel = %q, %v", got, err)
	}
}

func TestChooseCodexExecutableValidatesAndPreservesPrevious(t *testing.T) {
	app := newApp(settings.PathsFor(t.TempDir(), t.TempDir()), nil)
	dir := t.TempDir()
	good := filepath.Join(dir, "codex")
	if err := os.WriteFile(good, []byte(codexTestLauncher("codex-cli 0.149.0")), 0755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho no\n"), 0644); err != nil {
		t.Fatal(err)
	}
	app.pickCodexFile = func(context.Context, string) (string, error) { return good, nil }
	got, err := app.ChooseCodexExecutable("/previous/codex")
	if err != nil || got != good {
		t.Fatalf("good selection = %q, %v", got, err)
	}
	app.pickCodexFile = func(context.Context, string) (string, error) { return bad, nil }
	got, err = app.ChooseCodexExecutable(good)
	if err == nil || got != good {
		t.Fatalf("invalid selection = %q, %v; previous must be preserved", got, err)
	}
	app.pickCodexFile = func(context.Context, string) (string, error) { return "", nil }
	got, err = app.ChooseCodexExecutable(good)
	if err != nil || got != good {
		t.Fatalf("cancel = %q, %v", got, err)
	}
}

func TestCodexExecutableChangeSavesWhileManagedStackRuns(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	current := settings.DefaultsFor(paths.DefaultDataRoot)
	app.settings = current
	app.supervisor, _ = appTestSupervisor(t, false)
	if err := app.supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.supervisor.Stop(context.Background()) }()

	launcher := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(launcher, []byte(codexTestLauncher("codex-cli live-save")), 0755); err != nil {
		t.Fatal(err)
	}
	changed := current
	changed.CodexExecutable = launcher
	if err := app.SaveSettings(changed); err != nil {
		t.Fatalf("SaveSettings() = %v", err)
	}
	if got := app.GetSettings().CodexExecutable; got != launcher {
		t.Fatalf("CodexExecutable = %q", got)
	}
	if got := app.codex; got.Status != codexprobe.Available || got.Version != "codex-cli live-save" {
		t.Fatalf("Codex status = %#v", got)
	}
}

func TestDataRootChangeDoesNotClearTunnelConfiguration(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	if err := app.configure(); err != nil {
		t.Fatal(err)
	}
	value := app.GetSettings().Settings
	value.OpenAITunnelExecutable = "/opt/bin/tunnel-client"
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	if err := app.SaveSettings(value); err != nil {
		t.Fatal(err)
	}
	changed := value
	changed.DataRoot = filepath.Join(t.TempDir(), "other-bridge-data")
	if err := app.SaveSettings(changed); err != nil {
		t.Fatal(err)
	}
	reloaded := newApp(paths, nil)
	if err := reloaded.configure(); err != nil {
		t.Fatal(err)
	}
	got := reloaded.GetSettings()
	if got.OpenAITunnelExecutable != "/opt/bin/tunnel-client" || got.OpenAITunnelID != "tunnel_0123456789abcdef0123456789abcdef" {
		t.Fatalf("tunnel configuration lost across DataRoot change: %#v", got)
	}
	if got.DataRoot != changed.DataRoot {
		t.Fatalf("DataRoot change was not persisted: %q", got.DataRoot)
	}
}

func TestTunnelSettingsChangeWhileStackRunsRestartOnlyTunnel(t *testing.T) {
	app, stackFactory, tunnelFactory, _ := tunnelTestApp(t)
	value := app.GetSettings().Settings
	script := filepath.Join(t.TempDir(), "tunnel-client")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 0.0.14+test\n"), 0755); err != nil {
		t.Fatal(err)
	}
	value.OpenAITunnelExecutable = script
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	if err := app.SaveSettings(value); err != nil {
		t.Fatal(err)
	}
	tunnelSurface(t, app, "ready")
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop() }()
	changed := value
	changed.OpenAITunnelID = "tunnel_ffffffffffffffffffffffffffffffff"
	if err := app.SaveSettings(changed); err != nil {
		t.Fatalf("tunnel-only change while stack runs: %v", err)
	}
	if got := app.GetSettings().OpenAITunnelID; got != changed.OpenAITunnelID {
		t.Fatalf("tunnel id = %q", got)
	}
	stackFactory.mu.Lock()
	stackChildren := len(stackFactory.children)
	stackFactory.mu.Unlock()
	if stackChildren != 3 {
		t.Fatalf("stack children = %d, want 3", stackChildren)
	}
	tunnelFactory.mu.Lock()
	defer tunnelFactory.mu.Unlock()
	if len(tunnelFactory.children) < 2 {
		t.Fatalf("tunnel was not restarted with new settings: %d children", len(tunnelFactory.children))
	}
}

func TestSetTunnelAPIKeyStoresSecretOutsideSettingsAndProjectsBoolean(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	if err := app.configure(); err != nil {
		t.Fatal(err)
	}
	result, err := app.SetTunnelAPIKey("sk-tunnel-plaintext")
	if err != nil || !result.Saved {
		t.Fatalf("SetTunnelAPIKey() = %+v, %v", result, err)
	}
	// The secret lives only in the machine-local secret store.
	secret, err := app.secrets.Load("openai-tunnel-api-key")
	if err != nil || secret != "sk-tunnel-plaintext" {
		t.Fatalf("stored secret = %q, %v", secret, err)
	}
	// The settings projection never returns plaintext.
	projection := app.GetSettings()
	if !projection.TunnelAPIKeyConfigured {
		t.Fatal("projection does not report the configured key")
	}
	encoded, _ := json.Marshal(projection)
	if strings.Contains(string(encoded), "sk-tunnel-plaintext") {
		t.Fatalf("settings projection leaks the secret: %s", string(encoded))
	}
	settingsOnDisk, readErr := os.ReadFile(paths.ConfigFile)
	if readErr == nil && strings.Contains(string(settingsOnDisk), "sk-tunnel-plaintext") {
		t.Fatal("settings.json contains the plaintext secret")
	}
	// Runtime logs must not contain the secret.
	for _, entry := range app.Status().Logs {
		if strings.Contains(entry.Text, "sk-tunnel-plaintext") {
			t.Fatalf("runtime log leaks the secret: %q", entry.Text)
		}
	}
	if _, err := app.SetTunnelAPIKey("   "); err == nil {
		t.Fatal("empty key accepted")
	}
	if result, err := app.ClearTunnelAPIKey(); err != nil || !result.Saved {
		t.Fatalf("ClearTunnelAPIKey() = %+v, %v", result, err)
	}
	if app.GetSettings().TunnelAPIKeyConfigured {
		t.Fatal("key still configured after clear")
	}
}

func TestTunnelLaunchReceivesEnvReferenceNotPlaintextArgument(t *testing.T) {
	paths := settings.PathsFor(t.TempDir(), t.TempDir())
	app := newApp(paths, nil)
	app.tunnelStartupTimeout = time.Second
	stackFactory := &appTestFactory{}
	tunnelFactory := &appTestFactory{}
	app.stackFactory = stackFactory
	app.tunnelFactory = tunnelFactory
	app.prober = &appTestProber{calls: map[runtimecontrol.Service]int{}}
	if err := app.configure(); err != nil {
		t.Fatal(err)
	}
	value := app.GetSettings().Settings
	value.OpenAITunnelExecutable = "/opt/bin/tunnel-client"
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	if err := app.SaveSettings(value); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetTunnelAPIKey("sk-plaintext-secret"); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop() }()
	tunnelFactory.mu.Lock()
	defer tunnelFactory.mu.Unlock()
	if len(tunnelFactory.children) == 0 {
		t.Fatal("tunnel was not started")
	}
	var launch *runtimecontrol.Command
	for i := range tunnelFactory.commands {
		if strings.Contains(tunnelFactory.commands[i].Path, "tunnel-client") {
			launch = &tunnelFactory.commands[i]
		}
	}
	if launch == nil {
		t.Fatalf("no tunnel launch captured: %+v", tunnelFactory.commands)
	}
	joinedArgs := strings.Join(launch.Args, " ")
	if !strings.Contains(joinedArgs, "--control-plane.api-key env:DEVBRIDGE_OPENAI_TUNNEL_API_KEY") {
		t.Fatalf("launch args = %v", launch.Args)
	}
	if strings.Contains(joinedArgs, "sk-plaintext-secret") {
		t.Fatalf("launch args contain the plaintext secret: %v", launch.Args)
	}
	foundEnv := false
	for _, entry := range launch.Env {
		if entry == "DEVBRIDGE_OPENAI_TUNNEL_API_KEY=sk-plaintext-secret" {
			foundEnv = true
		}
		if strings.Contains(entry, "sk-plaintext-secret") && !strings.HasPrefix(entry, "DEVBRIDGE_OPENAI_TUNNEL_API_KEY=") {
			t.Fatalf("unexpected env entry carries the secret: %q", entry)
		}
	}
	if !foundEnv {
		t.Fatalf("launch env = %v, want injected tunnel key variable", launch.Env)
	}
	// The runtime logs must not carry the secret either.
	for _, entry := range app.Status().Logs {
		if strings.Contains(entry.Text, "sk-plaintext-secret") {
			t.Fatalf("runtime log leaks the secret: %q", entry.Text)
		}
	}
}

func codexTestLauncher(version string) string {
	return "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '" + version + "'; exit; fi\nwhile IFS= read -r line; do\ncase \"$line\" in *\\\"initialize\\\"*) echo '{\"id\":1,\"result\":{\"userAgent\":\"test\"}}';; esac\ndone\n"
}
