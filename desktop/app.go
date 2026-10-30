package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	codexprobe "github.com/lazyun/devbridge/desktop/internal/codex"
	"github.com/lazyun/devbridge/desktop/internal/execresolve"
	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
	runtimecontrol "github.com/lazyun/devbridge/desktop/internal/runtime"
	"github.com/lazyun/devbridge/desktop/internal/settings"
	tunnelpkg "github.com/lazyun/devbridge/desktop/internal/tunnel"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/codexruntime"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	desktopVersion        = "0.1.0-dev"
	defaultCoreConsoleURL = "http://127.0.0.1:8081/ui/"
)

// App is the deliberately small Wails boundary around local-only operational
// services. Codex probing shares the Core launch transport without domain changes.
type App struct {
	mu          sync.RWMutex
	operationMu sync.Mutex
	ctx         context.Context
	paths       settings.Paths
	store       interface {
		Load() (settings.Settings, error)
		Save(settings.Settings) error
	}
	secrets              settings.SecretStore
	settings             settings.Settings
	supervisorSettings   settings.Settings
	supervisor           *runtimecontrol.RuntimeSupervisor
	tunnel               *runtimecontrol.TunnelSupervisor
	runtimeLogs          *logbuffer.Buffer
	codex                codexprobe.Result
	managedDataRoot      string
	lastError            string
	setupError           error
	pickDirectory        func(context.Context, string) (string, error)
	pickProjectDirectory func(context.Context, string) (string, error)
	pickFile             func(context.Context, string) (string, error)
	pickCodexFile        func(context.Context, string) (string, error)
	pickPiFile           func(context.Context, string) (string, error)
	// stackFactory and tunnelFactory default to real child processes and are
	// injectable for tests.
	stackFactory       runtimecontrol.ProcessFactory
	tunnelFactory      runtimecontrol.ProcessFactory
	prober             runtimecontrol.EndpointProber
	projectAPI         projectCoreAPI
	runtimeAPI         runtimeInventoryCoreAPI
	coreReadyCheck     func() bool
	modelCatalogReader func(context.Context, string) ([]codexruntime.CodingAgentModelConfig, error)
	// Tunnel supervision timings, defaulted for production and injectable
	// for tests.
	tunnelStartupTimeout    time.Duration
	tunnelShutdownTimeout   time.Duration
	tunnelReadyPollInterval time.Duration
}

func NewApp() *App {
	paths, err := settings.DefaultPaths()
	return newApp(paths, err)
}

func newApp(paths settings.Paths, pathErr error) *App {
	return &App{
		paths:                paths,
		store:                settings.NewStore(paths),
		secrets:              settings.NewSecretStore(paths.StateDir),
		settings:             settings.DefaultsFor(paths.DefaultDataRoot),
		codex:                codexprobe.Result{Status: codexprobe.Missing},
		setupError:           pathErr,
		pickDirectory:        nativeDirectoryPicker,
		pickProjectDirectory: nativeProjectDirectoryPicker,
		pickFile:             nativeFilePicker,
		pickCodexFile:        nativeCodexFilePicker,
		pickPiFile:           nativePiFilePicker,
		projectAPI:           newHTTPProjectCoreAPI(nil),
		runtimeAPI:           newHTTPRuntimeInventoryCoreAPI(nil),
	}
}

// startup is called by Wails. The initial Start first probes both configured
// endpoints, attaching compatible services and refusing foreign listeners.
func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
	if err := a.configure(); err != nil {
		a.setError(err)
		return
	}
	go func() { _ = a.Start() }()
}

func (a *App) shutdown(_ context.Context) {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := a.stop(ctx); err != nil {
		a.setError(err)
	}
}

// DesktopInfo is retained as a stable, tiny bootstrapping contract.
func (a *App) DesktopInfo() DesktopInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return DesktopInfo{Version: desktopVersion, ConsoleURL: consoleURL(a.settings.CoreEndpoint)}
}

type DesktopInfo struct {
	Version    string `json:"version"`
	ConsoleURL string `json:"consoleURL"`
}

type DesktopStatus struct {
	DesktopVersion string                       `json:"desktopVersion"`
	Core           runtimecontrol.ServiceStatus `json:"core"`
	MCP            runtimecontrol.ServiceStatus `json:"mcp"`
	LocalMCP       runtimecontrol.ServiceStatus `json:"localMCP"`
	Tunnel         runtimecontrol.TunnelStatus  `json:"tunnel"`
	// ChatGPTIntegration is "Available" only when the tunnel reports its own
	// readiness signal; it is never inferred from process liveness.
	ChatGPTIntegration string            `json:"chatgptIntegration"`
	BuildMismatch      bool              `json:"buildMismatch"`
	PiRestartRequired  bool              `json:"piRestartRequired"`
	Codex              codexprobe.Result `json:"codex"`
	ConsoleURL         string            `json:"consoleURL"`
	LastError          string            `json:"lastError,omitempty"`
	Logs               []logbuffer.Entry `json:"logs"`
	DataRoot           string            `json:"dataRoot"`
}

func (a *App) Status() DesktopStatus {
	a.mu.RLock()
	supervisor := a.supervisor
	tunnel := a.tunnel
	runtimeLogs := a.runtimeLogs
	codex := a.codex
	managedDataRoot := a.managedDataRoot
	lastError := a.lastError
	value := a.settings
	launched := a.supervisorSettings
	a.mu.RUnlock()
	status := DesktopStatus{
		DesktopVersion: desktopVersion,
		Codex:          codex,
		ConsoleURL:     consoleURL(value.CoreEndpoint),
		LastError:      lastError,
		DataRoot:       managedDataRoot,
		Core: runtimecontrol.ServiceStatus{
			Service: runtimecontrol.Core, State: runtimecontrol.Stopped, Endpoint: value.CoreEndpoint,
		},
		MCP: runtimecontrol.ServiceStatus{
			Service: runtimecontrol.MCP, State: runtimecontrol.Stopped, Endpoint: value.MCPEndpoint,
		},
		LocalMCP: runtimecontrol.ServiceStatus{
			Service: runtimecontrol.LocalMCP, State: runtimecontrol.Stopped, Endpoint: value.LocalMCPEndpoint,
		},
		Tunnel: runtimecontrol.TunnelStatus{State: runtimecontrol.NotConfigured},
	}
	if tunnel != nil {
		status.Tunnel = tunnel.Snapshot()
	} else {
		status.Tunnel.Executable = value.OpenAITunnelExecutable
	}
	if status.Tunnel.Ready {
		status.ChatGPTIntegration = "Available"
	} else {
		status.ChatGPTIntegration = "Unavailable"
	}
	if supervisor != nil {
		snapshot := supervisor.Snapshot()
		status.Core, status.MCP, status.LocalMCP, status.BuildMismatch = snapshot.Core, snapshot.MCP, snapshot.LocalMCP, snapshot.BuildMismatch
		status.PiRestartRequired = (isActiveManaged(snapshot.Core) || isActiveManaged(snapshot.MCP)) && piRuntimeSettingsChanged(launched, value)
		status.Logs = supervisor.Logs()
	} else if runtimeLogs != nil {
		status.Logs = runtimeLogs.Snapshot()
	}
	if status.Core.State == runtimecontrol.Running || status.Core.State == runtimecontrol.External {
		status.Codex = codexprobe.VerifyCore(value.CoreEndpoint, a.codexSelectionFile(), status.Codex)
	}
	return status
}

// DesktopSettings is the frontend settings projection. It never contains
// secret material: the stored tunnel API key appears only as a boolean.
type DesktopSettings struct {
	settings.Settings
	TunnelAPIKeyConfigured bool     `json:"tunnelApiKeyConfigured"`
	CodexModelCandidates   []string `json:"codexModelCandidates"`
	PiModelCandidates      []string `json:"piModelCandidates"`
}

func (a *App) GetSettings() DesktopSettings {
	a.mu.RLock()
	defer a.mu.RUnlock()
	candidates := make([]string, 0, len(a.settings.CodingAgentModels))
	for _, model := range a.settings.CodingAgentModels {
		candidates = append(candidates, model.ModelID)
	}
	piCandidates := make([]string, 0, len(a.settings.PiAgentModels))
	for _, model := range a.settings.PiAgentModels {
		piCandidates = append(piCandidates, model.ModelID)
	}
	value := a.settings
	value.CodingAgentModels = cloneCodingAgentModels(value.CodingAgentModels)
	value.PiAgentModels = clonePiAgentModels(value.PiAgentModels)
	return DesktopSettings{Settings: value, TunnelAPIKeyConfigured: a.secrets.Exists(tunnelAPIKeySecretName), CodexModelCandidates: candidates, PiModelCandidates: piCandidates}
}

func clonePiAgentModels(value []settings.PiAgentModelConfig) []settings.PiAgentModelConfig {
	if value == nil {
		return nil
	}
	cloned := make([]settings.PiAgentModelConfig, len(value))
	for i, model := range value {
		cloned[i] = model
		cloned[i].SupportedReasoningEfforts = append([]string(nil), model.SupportedReasoningEfforts...)
	}
	return cloned
}

func (a *App) Start() error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	if err := a.ensureConfigured(); err != nil {
		a.setError(err)
		return err
	}
	a.mu.RLock()
	supervisor := a.supervisor
	value := a.settings
	appCtx := a.ctx
	a.mu.RUnlock()
	if appCtx == nil {
		appCtx = context.Background()
	}
	if a.piLaunchSettingsChanged(value) {
		status := supervisor.Snapshot()
		if !isActiveManaged(status.Core) && !isActiveManaged(status.MCP) && status.Core.Ownership != runtimecontrol.Attached {
			if err := a.installSupervisor(value); err != nil {
				a.setError(err)
				return err
			}
			a.mu.RLock()
			supervisor = a.supervisor
			a.mu.RUnlock()
		}
	}
	a.updateCodexProbe(value.CodexExecutable)
	a.mu.Lock()
	a.lastError = ""
	a.mu.Unlock()
	if err := supervisor.Start(appCtx); err != nil {
		a.setError(err)
		return err
	}
	identityErr := a.captureManagedRuntimeIdentity(supervisor)
	// The tunnel starts only after Core and MCP are ready, and its failure
	// never fails the managed stack: Core and the Web Console stay usable.
	_ = a.startTunnel(appCtx)
	if identityErr != nil {
		a.setError(identityErr)
		return identityErr
	}
	return nil
}

func (a *App) Stop() error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return a.stop(ctx)
}

func (a *App) stop(ctx context.Context) error {
	a.mu.RLock()
	supervisor, tunnel := a.supervisor, a.tunnel
	a.mu.RUnlock()
	// The managed tunnel stops with the stack it belongs to; an EXTERNAL
	// daemon is left running.
	if tunnel != nil {
		if err := tunnel.Stop(ctx); err != nil {
			a.setError(err)
			return err
		}
	}
	if supervisor == nil {
		return nil
	}
	if err := supervisor.Stop(ctx); err != nil {
		a.setError(err)
		return err
	}
	a.setError(nil)
	return nil
}

func (a *App) Restart() error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	if err := a.ensureConfigured(); err != nil {
		a.setError(err)
		return err
	}
	a.mu.RLock()
	supervisor := a.supervisor
	value := a.settings
	appCtx := a.ctx
	a.mu.RUnlock()
	if appCtx == nil {
		appCtx = context.Background()
	}
	a.updateCodexProbe(value.CodexExecutable)
	a.mu.Lock()
	a.lastError = ""
	a.mu.Unlock()
	if a.piLaunchSettingsChanged(value) {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		err := a.stop(ctx)
		cancel()
		if err != nil {
			return err
		}
		if err := a.installSupervisor(value); err != nil {
			a.setError(err)
			return err
		}
		a.mu.RLock()
		supervisor = a.supervisor
		a.mu.RUnlock()
	}
	if err := supervisor.Restart(appCtx); err != nil {
		a.setError(err)
		return err
	}
	identityErr := a.captureManagedRuntimeIdentity(supervisor)
	// A full restart also restarts the managed tunnel, after Core/MCP return;
	// tunnel failure still never affects the stack projection.
	_ = a.startTunnel(appCtx)
	if identityErr != nil {
		a.setError(identityErr)
		return identityErr
	}
	return nil
}

// captureManagedRuntimeIdentity publishes only the Core-owned identity read
// after managed readiness. It intentionally never falls back to settings or
// launch arguments. Failed readback leaves the last successful projection
// intact while the independently truthful runtime status remains visible.
func (a *App) captureManagedRuntimeIdentity(supervisor *runtimecontrol.RuntimeSupervisor) error {
	if supervisor == nil {
		return errors.New("read managed runtime identity: supervisor is unavailable")
	}
	core := supervisor.Snapshot().Core
	if core.Ownership != runtimecontrol.Managed || core.State != runtimecontrol.Running {
		return nil
	}
	dataRoot := strings.TrimSpace(core.DataRoot)
	if dataRoot == "" {
		return errors.New("read managed runtime identity: Core DataRoot is unavailable")
	}
	a.mu.Lock()
	a.managedDataRoot = dataRoot
	a.mu.Unlock()
	return nil
}

// StartTunnel starts or retries only the OpenAI tunnel-client. It is the
// "retry only Tunnel" path: Core and MCP are untouched.
func (a *App) StartTunnel() error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.startTunnel(ctx)
}

func (a *App) startTunnel(ctx context.Context) error {
	a.mu.RLock()
	tunnel := a.tunnel
	a.mu.RUnlock()
	if tunnel == nil {
		return nil
	}
	if err := tunnel.Start(ctx); err != nil {
		if errors.Is(err, runtimecontrol.ErrTunnelNotConfigured) {
			return nil
		}
		// Tunnel failures are scoped to the tunnel projection and the
		// bounded runtime logs; they must not make the stack look failed.
		return err
	}
	return nil
}

// StopTunnel stops only the MANAGED tunnel-client. An EXTERNAL daemon is
// never terminated.
func (a *App) StopTunnel() error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	a.mu.RLock()
	tunnel := a.tunnel
	a.mu.RUnlock()
	if tunnel == nil {
		return nil
	}
	return tunnel.Stop(ctx)
}

// RestartTunnel affects only the tunnel process.
func (a *App) RestartTunnel() error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a.mu.RLock()
	tunnel := a.tunnel
	a.mu.RUnlock()
	if tunnel == nil {
		return nil
	}
	return tunnel.Restart(ctx)
}

// SaveSettings is intentionally refused while Desktop owns a running Core/MCP
// child for infrastructure values captured by those child commands. Pi changes
// are persisted for the next explicit restart; live Pi executions retain their
// launch configuration. The Codex inventory is
// hot-applied through Core's control API and can change while Core and MCP run.
func (a *App) SaveSettings(value settings.Settings) error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	if err := value.Validate(); err != nil {
		return err
	}
	a.mu.RLock()
	supervisor := a.supervisor
	tunnel := a.tunnel
	old := a.settings
	a.mu.RUnlock()
	if tunnelSettingsChanged(old, value) && tunnelIsExternal(tunnel) {
		return errors.New("an external tunnel-client daemon from a previous session is still attached; quit it before changing tunnel settings")
	}
	if supervisor != nil {
		status := supervisor.Snapshot()
		if piRuntimeSettingsChanged(old, value) && status.Core.Ownership == runtimecontrol.Attached {
			return errors.New("the attached external Core owns its Pi runtime; stop it before changing Desktop Pi settings")
		}
		if codexChanged(old, value) && status.Core.Ownership == runtimecontrol.Attached {
			return errors.New("the attached external Core owns its Codex runtime inventory; stop it before changing Desktop inventory settings")
		}
		if isActiveManaged(status.Core) || isActiveManaged(status.MCP) {
			if value.DataRoot != old.DataRoot {
				return errors.New("Stop Dev Bridge before changing the active data directory.")
			}
			if stackSettingsChanged(old, value) {
				return errors.New("stop the managed stack before changing settings")
			}
			if codexChanged(old, value) {
				if !isActiveManaged(status.Core) || status.Core.State != runtimecontrol.Running {
					return errors.New("Core must be running to apply Codex runtime inventory without restarting")
				}
				if err := a.applyAndPersistRuntimeInventory(value, status.Core.Endpoint); err != nil {
					return err
				}
			} else if err := a.store.Save(value); err != nil {
				return err
			}
			// Core reads the atomic Codex selection for each new runtime.
			a.mu.Lock()
			a.settings = value
			a.mu.Unlock()
			if old.CodexExecutable != value.CodexExecutable {
				a.updateCodexProbe(value.CodexExecutable)
			}
			if tunnelSettingsChanged(old, value) {
				return a.applyTunnelSettings(old, value)
			}
			return nil
		}
	}
	if err := a.store.Save(value); err != nil {
		return err
	}
	if err := a.installSupervisor(value); err != nil {
		return err
	}
	if old.CodexExecutable != value.CodexExecutable {
		a.updateCodexProbe(value.CodexExecutable)
	}
	a.setError(nil)
	return nil
}

func codexChanged(old, value settings.Settings) bool {
	return !slices.EqualFunc(old.CodingAgentModels, value.CodingAgentModels, func(left, right codexruntime.CodingAgentModelConfig) bool {
		return left.ModelID == right.ModelID && left.Enabled == right.Enabled && left.DefaultReasoningEffort == right.DefaultReasoningEffort && left.FastTier() == right.FastTier() && slices.Equal(left.SupportedReasoningEfforts, right.SupportedReasoningEfforts)
	})
}

// stackSettingsChanged reports changes to anything a managed Core/MCP child
// was launched with.
// piAgentEnabled reports whether the PI Agent runtime should be offered to
// Core. It requires a configured executable and at least one enabled model
// from the settings catalog, mirroring how the Codex inventory gates on its
// own model list.
func piAgentEnabled(value settings.Settings) bool {
	if strings.TrimSpace(value.PiExecutable) == "" {
		return false
	}
	for _, model := range value.PiAgentModels {
		if model.Enabled {
			return true
		}
	}
	return false
}

func stackSettingsChanged(old, value settings.Settings) bool {
	return old.DataRoot != value.DataRoot || old.CoreBinary != value.CoreBinary ||
		old.MCPBinary != value.MCPBinary ||
		old.LocalMCPBinary != value.LocalMCPBinary ||
		old.CoreEndpoint != value.CoreEndpoint || old.MCPEndpoint != value.MCPEndpoint ||
		old.LocalMCPEndpoint != value.LocalMCPEndpoint
}

// Pi settings are saved independently of the immutable running child command.
// Compare against the supervisor's actual launch settings, not the previous save:
// repeated edits and reverting a pending change must reflect the running process.
func piRuntimeSettingsChanged(old, value settings.Settings) bool {
	return piAgentEnabled(old) != piAgentEnabled(value) ||
		(piAgentEnabled(value) && (old.PiExecutable != value.PiExecutable || old.PiModel != value.PiModel || old.PiThinkingLevel != value.PiThinkingLevel))
}

func (a *App) piLaunchSettingsChanged(value settings.Settings) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return piRuntimeSettingsChanged(a.supervisorSettings, value)
}

func (a *App) applyAndPersistRuntimeInventory(value settings.Settings, endpoint string) error {
	a.mu.RLock()
	api := a.runtimeAPI
	ctx := a.ctx
	a.mu.RUnlock()
	if api == nil {
		api = newHTTPRuntimeInventoryCoreAPI(nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	before, err := api.Get(requestCtx, endpoint)
	if err != nil {
		return fmt.Errorf("read current Core Codex runtime inventory: %w", err)
	}
	candidate := runtimeInventorySnapshot{
		Revision:          before.Revision,
		CodingAgentModels: cloneCodingAgentModels(value.CodingAgentModels),
	}
	next, err := api.Apply(requestCtx, endpoint, candidate)
	if err != nil {
		if isCoreTransportError(err) {
			return reconcileFailedRuntimeInventoryApply(api, endpoint, before, candidate, err)
		}
		return fmt.Errorf("apply Core Codex runtime inventory (settings were not persisted; Core state may be unchanged): %w", err)
	}
	if err := a.store.Save(value); err == nil {
		return nil
	} else {
		persistErr := err
		// Persistence can consume the original request deadline. Compensation
		// gets its own bounded context so a local disk failure does not make the
		// rollback fail solely because the apply deadline already expired.
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer rollbackCancel()
		_, rollbackErr := api.Apply(rollbackCtx, endpoint, runtimeInventorySnapshot{
			Revision:          next.Revision,
			CodingAgentModels: cloneCodingAgentModels(before.CodingAgentModels),
		})
		if rollbackErr != nil {
			return errors.Join(fmt.Errorf("persist settings after Core inventory apply: %w", persistErr), fmt.Errorf("rollback Core Codex runtime inventory: %w", rollbackErr))
		}
		return fmt.Errorf("persist settings after Core inventory apply; Core inventory was rolled back: %w", persistErr)
	}
}

// reconcileFailedRuntimeInventoryApply resolves the ambiguous case where the
// request transport failed after Core may have committed the CAS. It never
// overwrites a later generation owned by another control client.
func reconcileFailedRuntimeInventoryApply(api runtimeInventoryCoreAPI, endpoint string, before, candidate runtimeInventorySnapshot, applyErr error) error {
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer reconcileCancel()
	current, err := api.Get(reconcileCtx, endpoint)
	if err != nil {
		return errors.Join(
			fmt.Errorf("apply Core Codex runtime inventory failed with an unknown outcome; settings were not persisted: %w", applyErr),
			fmt.Errorf("reconcile Core inventory (Core may have exited or become unreachable): %w", err),
		)
	}
	if runtimeInventoryValuesEqual(current, before) {
		return fmt.Errorf("apply Core Codex runtime inventory failed; settings were not persisted and Core inventory is unchanged: %w", applyErr)
	}
	if current.Revision == before.Revision+1 && runtimeInventoryValuesEqual(current, candidate) {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer rollbackCancel()
		_, rollbackErr := api.Apply(rollbackCtx, endpoint, runtimeInventorySnapshot{
			Revision:          current.Revision,
			CodingAgentModels: cloneCodingAgentModels(before.CodingAgentModels),
		})
		if rollbackErr == nil {
			return fmt.Errorf("apply Core Codex runtime inventory response failed; settings were not persisted and Core inventory was rolled back: %w", applyErr)
		}
		return errors.Join(
			fmt.Errorf("apply Core Codex runtime inventory failed after Core committed it; settings were not persisted: %w", applyErr),
			fmt.Errorf("rollback Core Codex runtime inventory: %w", rollbackErr),
		)
	}
	return fmt.Errorf("apply Core Codex runtime inventory failed and Core now has a different generation; settings were not persisted and the concurrent Core inventory was not overwritten: %w", applyErr)
}

func runtimeInventoryValuesEqual(left, right runtimeInventorySnapshot) bool {
	return slices.EqualFunc(left.CodingAgentModels, right.CodingAgentModels, func(a, b codexruntime.CodingAgentModelConfig) bool {
		return a.ModelID == b.ModelID && a.Enabled == b.Enabled && a.DefaultReasoningEffort == b.DefaultReasoningEffort && a.FastTier() == b.FastTier() && slices.Equal(a.SupportedReasoningEfforts, b.SupportedReasoningEfforts)
	})
}

func cloneCodingAgentModels(value []codexruntime.CodingAgentModelConfig) []codexruntime.CodingAgentModelConfig {
	return (codexruntime.Allowlist{ModelConfigs: value}).Clone().ModelConfigs
}

func tunnelSettingsChanged(old, value settings.Settings) bool {
	return old.OpenAITunnelExecutable != value.OpenAITunnelExecutable ||
		old.OpenAITunnelID != value.OpenAITunnelID
}

// applyTunnelSettings stops the managed tunnel if Desktop owns one, rebuilds
// the tunnel supervisor from the new settings, and starts it again when the
// stack is up. Core and MCP are never touched.
func (a *App) applyTunnelSettings(old, value settings.Settings) error {
	a.mu.RLock()
	tunnel := a.tunnel
	a.mu.RUnlock()
	if tunnel != nil {
		if err := tunnel.Stop(context.Background()); err != nil {
			return err
		}
	}
	if tunnelSettingsChanged(old, value) {
		a.logRuntime("tunnel configuration changed; executable=" + tunnelLogPath(value.OpenAITunnelExecutable) + " tunnelID=" + tunnelLogPath(value.OpenAITunnelID))
	}
	a.mu.Lock()
	tunnel = a.buildTunnelSupervisor(value, a.runtimeLogs)
	a.tunnel = tunnel
	a.mu.Unlock()
	// Restart the tunnel only when the managed stack is up; when Core/MCP are
	// stopped, tunnel launch happens through the normal managed startup order.
	if a.stackUp() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := tunnel.Start(ctx); err != nil && !errors.Is(err, runtimecontrol.ErrTunnelNotConfigured) {
			// Scoped to the tunnel projection; the stack stays READY.
			return nil
		}
	}
	return nil
}

func tunnelIsExternal(tunnel *runtimecontrol.TunnelSupervisor) bool {
	if tunnel == nil {
		return false
	}
	return tunnel.Snapshot().State == runtimecontrol.External
}

func (a *App) stackUp() bool {
	a.mu.RLock()
	supervisor := a.supervisor
	a.mu.RUnlock()
	if supervisor == nil {
		return false
	}
	status := supervisor.Snapshot()
	return isActiveManaged(status.Core) && isActiveManaged(status.MCP)
}

func (a *App) logRuntime(message string) {
	a.mu.RLock()
	logs := a.runtimeLogs
	a.mu.RUnlock()
	if logs != nil {
		logs.AppendLine("tunnel", logbuffer.Stdout, message)
	}
}

func logTunnel(logs *logbuffer.Buffer, message string) {
	if logs != nil {
		logs.AppendLine("tunnel", logbuffer.Stdout, message)
	}
}

// tunnelLogPath renders a configured path or id for logs without ever
// including secret material.
func tunnelLogPath(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}

// ChooseDataRoot opens the platform folder picker. Cancel returns the current
// draft unchanged; Core remains the authoritative format validator at launch.
func (a *App) ChooseDataRoot(current string) (string, error) {
	a.mu.RLock()
	ctx := a.ctx
	picker := a.pickDirectory
	a.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if picker == nil {
		picker = nativeDirectoryPicker
	}
	selected, err := picker(ctx, current)
	if err != nil {
		return current, err
	}
	if selected == "" {
		return current, nil
	}
	absolute, err := filepath.Abs(selected)
	if err != nil {
		return current, fmt.Errorf("resolve selected data directory: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return current, fmt.Errorf("inspect selected data directory: %w", err)
	}
	if !info.IsDir() {
		return current, errors.New("selected data directory is not a directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return current, fmt.Errorf("resolve selected data directory: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func nativeDirectoryPicker(ctx context.Context, current string) (string, error) {
	return wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:            "Choose Dev Bridge data directory",
		DefaultDirectory: current,
	})
}

func nativeFilePicker(ctx context.Context, current string) (string, error) {
	return wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:           "Choose the OpenAI tunnel-client executable",
		DefaultFilename: current,
	})
}

func nativeCodexFilePicker(ctx context.Context, current string) (string, error) {
	return wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:           "Choose the Codex executable",
		DefaultFilename: current,
	})
}

func nativePiFilePicker(ctx context.Context, current string) (string, error) {
	return wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:           "Choose the PI Agent executable",
		DefaultFilename: current,
	})
}

// ChooseCodexExecutable selects and verifies a concrete Codex launcher. Probe
// supplies the launcher's directory on PATH, which is required by NVM's
// /usr/bin/env node based wrapper when Desktop was launched from Finder.
func (a *App) ChooseCodexExecutable(current string) (string, error) {
	a.mu.RLock()
	ctx := a.ctx
	picker := a.pickCodexFile
	a.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if picker == nil {
		picker = nativeCodexFilePicker
	}
	selected, err := picker(ctx, current)
	if err != nil {
		return current, err
	}
	if selected == "" {
		return current, nil
	}
	absolute, err := filepath.Abs(selected)
	if err != nil {
		return current, fmt.Errorf("resolve selected Codex executable: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return current, fmt.Errorf("inspect selected Codex executable: %w", err)
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return current, errors.New("selected Codex path is not an executable file")
	}
	result := codexprobe.Probe(ctx, absolute)
	if result.Status != codexprobe.Available {
		return current, errors.New(result.Error)
	}
	return filepath.Clean(absolute), nil
}

func (a *App) codexSelectionFile() string {
	return filepath.Join(a.paths.StateDir, "codex-selection.json")
}

// ChoosePiExecutable selects and verifies a concrete PI Agent executable.
func (a *App) ChoosePiExecutable(current string) (string, error) {
	a.mu.RLock()
	ctx := a.ctx
	picker := a.pickPiFile
	a.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if picker == nil {
		picker = nativePiFilePicker
	}
	selected, err := picker(ctx, current)
	if err != nil {
		return current, err
	}
	if selected == "" {
		return current, nil
	}
	absolute, err := filepath.Abs(selected)
	if err != nil {
		return current, fmt.Errorf("resolve selected PI executable: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return current, fmt.Errorf("inspect selected PI executable: %w", err)
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return current, errors.New("selected PI path is not an executable file")
	}
	return filepath.Clean(absolute), nil
}

// SetPiConfiguration updates PI Agent configuration settings.
func (a *App) SetPiConfiguration(executable, model, thinkingLevel string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	settings := a.settings
	settings.PiExecutable = executable
	settings.PiModel = model
	settings.PiThinkingLevel = thinkingLevel
	if err := settings.Validate(); err != nil {
		return err
	}
	if err := a.store.Save(settings); err != nil {
		return err
	}
	a.settings = settings
	return nil
}

// GetPiConfiguration returns current PI Agent configuration.
func (a *App) GetPiConfiguration() (executable, model, thinkingLevel string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.settings.PiExecutable, a.settings.PiModel, a.settings.PiThinkingLevel
}

func (a *App) updateCodexProbe(executable string) {
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	selected := executable
	// Pin a previously resolved path for the same Settings value. Never rediscover
	// a different installation because the selected executable was removed.
	if previous, err := codexlaunch.Read(a.codexSelectionFile()); err == nil && previous.Configured == executable && filepath.IsAbs(previous.Executable) {
		selected = previous.Executable
	}
	pending := codexlaunch.Selection{Configured: executable, Executable: selected, Error: "Codex availability verification in progress"}
	err := codexlaunch.Write(a.codexSelectionFile(), pending)
	result := codexprobe.Result{Status: codexprobe.Error}
	if err == nil {
		result = codexprobe.Probe(ctx, selected)
		err = codexlaunch.Write(a.codexSelectionFile(), codexlaunch.Selection{Configured: executable, Executable: result.Executable, Error: result.Error})
	}
	if err != nil {
		result.Status = codexprobe.Error
		result.Error = "CODEX_SELECTION_PERSIST_FAILED: " + err.Error()
	}
	a.mu.Lock()
	a.codex = result
	a.mu.Unlock()
}

func (a *App) OpenConsole() {
	a.mu.RLock()
	ctx := a.ctx
	endpoint := a.settings.CoreEndpoint
	a.mu.RUnlock()
	if ctx != nil {
		wailsruntime.BrowserOpenURL(ctx, consoleURL(endpoint))
	}
}

func (a *App) configure() error {
	if a.setupError != nil {
		return a.setupError
	}
	value, err := a.store.Load()
	if err != nil {
		return err
	}
	return a.installSupervisor(value)
}

func (a *App) ensureConfigured() error {
	a.mu.RLock()
	ready := a.supervisor != nil
	a.mu.RUnlock()
	if ready {
		return nil
	}
	return a.configure()
}

func (a *App) installSupervisor(value settings.Settings) error {
	if err := value.Validate(); err != nil {
		return err
	}
	logs := logbuffer.New(1000)
	config, err := managedRuntimeConfig(value, logs)
	if err != nil {
		return err
	}
	config.Core.Command.Env = append(config.Core.Command.Env, codexlaunch.SelectionEnv+"="+a.codexSelectionFile())
	// A previous managed tunnel from a replaced configuration is stopped
	// here; only Desktop-owned children are ever terminated.
	a.mu.RLock()
	oldTunnel := a.tunnel
	a.mu.RUnlock()
	if oldTunnel != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		_ = oldTunnel.Stop(ctx)
	}
	supervisor, err := runtimecontrol.NewRuntimeSupervisor(config, a.stackFactory, a.prober, logs)
	if err != nil {
		return err
	}
	tunnel := a.buildTunnelSupervisor(value, logs)
	a.mu.Lock()
	a.settings = value
	a.supervisorSettings = value
	a.supervisorSettings.PiAgentModels = clonePiAgentModels(value.PiAgentModels)
	a.supervisor = supervisor
	a.tunnel = tunnel
	a.runtimeLogs = logs
	a.mu.Unlock()
	return nil
}

// buildTunnelSupervisor constructs the tunnel supervisor from Desktop
// settings. An empty executable projects NOT_CONFIGURED; every CLI flag comes
// from the tunnel package, which mirrors the real tunnel-client CLI only.
func (a *App) buildTunnelSupervisor(value settings.Settings, logs *logbuffer.Buffer) *runtimecontrol.TunnelSupervisor {
	// The daemon writes its operator-surface files here; the directory is
	// machine-local Desktop state and deliberately never inside DataRoot.
	if err := os.MkdirAll(a.paths.TunnelDir, 0700); err != nil {
		logTunnel(logs, "create tunnel runtime directory: "+err.Error())
	}
	files := tunnelpkg.RuntimeFilesIn(a.paths.TunnelDir)
	config := runtimecontrol.TunnelConfig{
		Readiness: tunnelpkg.Readiness(files),
		Attach: func() (bool, bool, int, string) {
			result := tunnelpkg.AttachCheck(files)
			return result.External, result.Conflict, result.PID, result.Detail
		},
		StartupTimeout:    a.tunnelStartupTimeout,
		ShutdownTimeout:   a.tunnelShutdownTimeout,
		ReadyPollInterval: a.tunnelReadyPollInterval,
	}
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = 20 * time.Second
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 5 * time.Second
	}
	if config.ReadyPollInterval <= 0 {
		config.ReadyPollInterval = 2 * time.Second
	}
	var launchEnv []string
	if executable := strings.TrimSpace(value.OpenAITunnelExecutable); executable != "" {
		if err := tunnelpkg.ValidateExecutablePath(executable); err != nil {
			logTunnel(logs, "configured tunnel-client path is invalid: "+err.Error())
		} else if version, err := tunnelpkg.Version(context.Background(), executable); err == nil {
			config.Version = version
			logTunnel(logs, "tunnel-client version probe: "+version)
		} else {
			logTunnel(logs, "tunnel-client version probe failed: "+err.Error())
		}
		launchConfig := tunnelpkg.LaunchConfig{
			Executable:   value.OpenAITunnelExecutable,
			MCPEndpoint:  value.MCPEndpoint,
			TunnelID:     value.OpenAITunnelID,
			RuntimeFiles: files,
		}
		// The stored secret never travels as a command-line argument: the
		// launch passes only an env-var reference and Desktop injects the
		// value into the child environment.
		if a.secrets.Exists(tunnelAPIKeySecretName) {
			launchConfig.APIKeyReference = tunnelAPIKeyEnvReference
			if secret, err := a.secrets.Load(tunnelAPIKeySecretName); err == nil {
				launchEnv = []string{tunnelAPIKeyEnvName + "=" + secret}
			} else if !errors.Is(err, settings.ErrSecretNotFound) {
				logTunnel(logs, "tunnel api key could not be loaded: "+err.Error())
			}
		}
		if command, err := tunnelpkg.BuildCommand(launchConfig); err == nil {
			config.Command = runtimecontrol.Command{Path: command.Path, Args: command.Args, Env: launchEnv}
		} else {
			logTunnel(logs, "tunnel launch configuration is invalid: "+err.Error())
		}
	}
	return runtimecontrol.NewTunnelSupervisor(config, a.tunnelFactory, logs)
}

// ChooseTunnelExecutable opens the platform file picker and validates the
// selection before it is accepted. An invalid selection is rejected and the
// previously configured path is preserved.
func (a *App) ChooseTunnelExecutable(current string) (string, error) {
	a.mu.RLock()
	ctx := a.ctx
	picker := a.pickFile
	a.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if picker == nil {
		picker = nativeFilePicker
	}
	selected, err := picker(ctx, current)
	if err != nil {
		return current, err
	}
	if selected == "" {
		return current, nil
	}
	absolute, err := filepath.Abs(selected)
	if err != nil {
		return current, fmt.Errorf("resolve selected tunnel-client executable: %w", err)
	}
	if err := tunnelpkg.ValidateExecutablePath(absolute); err != nil {
		return current, err
	}
	if _, err := tunnelpkg.Version(ctx, absolute); err != nil {
		return current, err
	}
	return filepath.Clean(absolute), nil
}

// managedRuntimeConfig builds the supervisor configuration. Runtime
// executable resolution is performed here so Core, GPT MCP, and Local MCP all
// resolve from one coherent location, and the selected paths are appended to
// the bounded runtime log for diagnostics.
func managedRuntimeConfig(value settings.Settings, logs *logbuffer.Buffer) (runtimecontrol.Config, error) {
	coreAddress, err := endpointAddress(value.CoreEndpoint)
	if err != nil {
		return runtimecontrol.Config{}, fmt.Errorf("Core endpoint: %w", err)
	}
	mcpAddress, err := endpointAddress(value.MCPEndpoint)
	if err != nil {
		return runtimecontrol.Config{}, fmt.Errorf("MCP endpoint: %w", err)
	}
	// The Local MCP adapter is loopback-only by construction: Desktop always
	// derives the -listen argument from the configured endpoint, and
	// settings validation rejects non-loopback hosts. The CLI's development
	// escape hatch (-allow-non-loopback) is never part of a Desktop launch.
	localMCPAddress, err := endpointAddress(value.LocalMCPEndpoint)
	if err != nil {
		return runtimecontrol.Config{}, fmt.Errorf("Local MCP endpoint: %w", err)
	}
	corePath, coreSource, err := resolveRuntime(logs, execresolve.Core, value.CoreBinary)
	if err != nil {
		return runtimecontrol.Config{}, err
	}
	mcpPath, mcpSource, err := resolveRuntime(logs, execresolve.MCP, value.MCPBinary)
	if err != nil {
		return runtimecontrol.Config{}, err
	}
	localMCPPath, localMCPSource, err := resolveRuntime(logs, execresolve.LocalMCP, value.LocalMCPBinary)
	if err != nil {
		return runtimecontrol.Config{}, err
	}
	logRuntimeResolution(logs, "core", corePath, coreSource)
	logRuntimeResolution(logs, "mcp", mcpPath, mcpSource)
	logRuntimeResolution(logs, "mcp/local", localMCPPath, localMCPSource)
	coreArgs := []string{
		"-mode", "server", "-data-root", value.DataRoot, "-listen", coreAddress,
		"-coding-agent-model-configs", mustRuntimeModelConfigs(value.CodingAgentModels),
	}
	if piAgentEnabled(value) {
		coreArgs = append(coreArgs, "-pi-agent", "-pi-agent-path", value.PiExecutable)
		if value.PiModel != "" {
			coreArgs = append(coreArgs, "-pi-agent-model", value.PiModel)
		}
		if value.PiThinkingLevel != "" {
			coreArgs = append(coreArgs, "-pi-agent-thinking", value.PiThinkingLevel)
		}
	}
	config := runtimecontrol.Config{
		Core: runtimecontrol.ServiceConfig{
			Command:  runtimecontrol.Command{Path: corePath, Args: coreArgs},
			Endpoint: value.CoreEndpoint,
		},
		MCP: runtimecontrol.ServiceConfig{
			Command: runtimecontrol.Command{Path: mcpPath, Args: []string{
				"-data-root", value.DataRoot, "-listen", mcpAddress,
				"-core-url", value.CoreEndpoint,
			}},
			Endpoint: value.MCPEndpoint,
		},
		LocalMCP: runtimecontrol.ServiceConfig{
			Command: runtimecontrol.Command{Path: localMCPPath, Args: []string{
				"-data-root", value.DataRoot, "-listen", localMCPAddress,
				"-core-url", value.CoreEndpoint,
			}},
			Endpoint: value.LocalMCPEndpoint,
		},
		StartupTimeout:  15 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		ProbeInterval:   100 * time.Millisecond,
	}
	return config, nil
}

func mustRuntimeModelConfigs(configs []codexruntime.CodingAgentModelConfig) string {
	encoded, err := json.Marshal(configs)
	if err != nil {
		panic("validated coding agent model configuration cannot be encoded: " + err.Error())
	}
	return string(encoded)
}

// resolveRuntime resolves one supervised executable. A total miss
// (*NotFoundError) is non-fatal: the bare name is kept so an EXTERNAL attach
// and the spawn-time exec error still work, and the miss is logged.
func resolveRuntime(logs *logbuffer.Buffer, selection execresolve.Selection, configured string) (string, string, error) {
	options := execresolve.Options{}
	if resolveOptions != nil {
		options = resolveOptions()
	}
	path, source, err := execresolve.Resolve(selection, configured, options)
	var notFound *execresolve.NotFoundError
	if errors.As(err, &notFound) {
		if logs != nil {
			logs.AppendLine(string(selection), logbuffer.Stderr, notFound.Error())
		}
		return path, source, nil
	}
	return path, source, err
}

// resolveOptions is a seam for tests to simulate packaged layouts; it is nil
// in production.
var resolveOptions func() execresolve.Options

func (a *App) setError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.lastError = ""
	} else if errors.Is(err, runtimecontrol.ErrWriterLocked) {
		a.lastError = runtimecontrol.ErrWriterLocked.Error()
	} else {
		a.lastError = err.Error()
	}
}

func isActiveManaged(status runtimecontrol.ServiceStatus) bool {
	return status.Ownership == runtimecontrol.Managed && status.State != runtimecontrol.Stopped && status.State != runtimecontrol.Crashed
}

func endpointAddress(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", errors.New("invalid endpoint")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || host == "" || port == "" {
		return "", errors.New("endpoint must include host and port")
	}
	return parsed.Host, nil
}

func consoleURL(endpoint string) string {
	if strings.TrimSpace(endpoint) == "" {
		return defaultCoreConsoleURL
	}
	return strings.TrimRight(endpoint, "/") + "/ui/"
}

// resolveExecutable honors an explicit configured path. For command names it
// delegates to the centralized runtime resolver, which prefers the adjacent
// build/install bin directory before PATH.
func resolveExecutable(configured, sidecarName string) string {
	path, _, _ := execresolve.Resolve(execresolve.Selection(sidecarName), configured, execresolve.Options{})
	return path
}

// logRuntimeResolution records which runtime executable path was selected
// into the supervisor's bounded log stream so diagnostics can identify the
// resolved binary without exposing it as product state.
func logRuntimeResolution(logs *logbuffer.Buffer, service, path, source string) {
	if logs == nil {
		return
	}
	logs.AppendLine(service, logbuffer.Stdout, "runtime executable: "+source+" ("+path+")")
}

// The tunnel API key is stored only in the machine-local secret store. The
// daemon receives it through an injected environment variable and the launch
// command carries only the tunnel-client's env-var reference form.
const (
	tunnelAPIKeySecretName   = "openai-tunnel-api-key"
	tunnelAPIKeyEnvName      = "DEVBRIDGE_OPENAI_TUNNEL_API_KEY"
	tunnelAPIKeyEnvReference = "env:" + tunnelAPIKeyEnvName
)

// SaveResult reports the outcome of a settings or secret mutation that may
// need a running process to pick the change up.
type SaveResult struct {
	Saved           bool   `json:"saved"`
	RestartRequired bool   `json:"restartRequired"`
	Message         string `json:"message,omitempty"`
}

// SetTunnelAPIKey stores the OpenAI tunnel API key in the machine-local
// secret store. The plaintext secret is never persisted in settings.json,
// never returned to the frontend afterwards, and never logged. A managed
// tunnel is restarted so the new key takes effect immediately.
func (a *App) SetTunnelAPIKey(secret string) (SaveResult, error) {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	if strings.TrimSpace(secret) == "" {
		return SaveResult{}, errors.New("API key must not be empty; use clear to remove it")
	}
	if err := a.secrets.Save(tunnelAPIKeySecretName, secret); err != nil {
		return SaveResult{}, err
	}
	a.logRuntime("tunnel api key configured")
	return a.applyTunnelKeyChange()
}

// ClearTunnelAPIKey removes the stored OpenAI tunnel API key.
func (a *App) ClearTunnelAPIKey() (SaveResult, error) {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	if err := a.secrets.Delete(tunnelAPIKeySecretName); err != nil {
		return SaveResult{}, err
	}
	a.logRuntime("tunnel api key cleared")
	return a.applyTunnelKeyChange()
}

func (a *App) applyTunnelKeyChange() (SaveResult, error) {
	a.mu.RLock()
	tunnel := a.tunnel
	value := a.settings
	logs := a.runtimeLogs
	a.mu.RUnlock()
	if tunnel != nil && tunnelIsExternal(tunnel) {
		// The secret is stored; the external daemon must be restarted by its
		// owner to pick it up.
		return SaveResult{Saved: true, RestartRequired: true}, nil
	}
	if tunnel != nil {
		// Stop the Desktop-owned daemon so the rebuilt supervisor does not
		// orphan it; it restarts below with the new key.
		if err := tunnel.Stop(context.Background()); err != nil {
			return SaveResult{}, err
		}
	}
	a.mu.Lock()
	tunnel = a.buildTunnelSupervisor(value, logs)
	a.tunnel = tunnel
	a.mu.Unlock()
	if !a.stackUp() {
		return SaveResult{Saved: true}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := tunnel.Start(ctx); err != nil && !errors.Is(err, runtimecontrol.ErrTunnelNotConfigured) {
		// Scoped to the tunnel projection; the stack stays READY.
		return SaveResult{Saved: true, Message: sanitizeTunnelStartError(err)}, nil
	}
	return SaveResult{Saved: true}, nil
}

func sanitizeTunnelStartError(err error) string {
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 300 {
		message = message[:300]
	}
	return message
}
