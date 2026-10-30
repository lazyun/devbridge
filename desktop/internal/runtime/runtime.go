// Package runtime supervises only the local processes owned by Dev Bridge
// Desktop. It deliberately has no dependency on Dev Bridge Core packages.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
)

type Service string

const (
	Core     Service = "core"
	MCP      Service = "mcp"
	LocalMCP Service = "mcp/local"
)

type ProcessState string

const (
	Stopped  ProcessState = "STOPPED"
	Starting ProcessState = "STARTING"
	Running  ProcessState = "RUNNING"
	Stopping ProcessState = "STOPPING"
	Crashed  ProcessState = "CRASHED"
	External ProcessState = "EXTERNAL"
	Error    ProcessState = "ERROR"
)

type Ownership string

const (
	Unowned  Ownership = ""
	Managed  Ownership = "MANAGED"
	Attached Ownership = "EXTERNAL"
	Conflict Ownership = "CONFLICT"
)

var (
	ErrAlreadyRunning   = errors.New("runtime stack is already running")
	ErrEndpointConflict = errors.New("endpoint is occupied by an incompatible service")
)

// ErrWriterLocked mirrors Core's DataRoot single-writer conflict. Another live
// Dev Bridge Core already owns this DataRoot; force unlock is never attempted.
var ErrWriterLocked = errors.New("Dev Bridge DataRoot is already in use by another running Core")

// WriterLockConflictError reports that Core exited at startup because a second
// writer holds the DataRoot lock. It is a product ownership conflict, not a
// transport failure, so it must never surface as generic Core unavailability.
type WriterLockConflictError struct {
	Service Service
}

func (e *WriterLockConflictError) Error() string {
	return fmt.Sprintf("%s failed to start: %v", e.Service, ErrWriterLocked)
}

func (e *WriterLockConflictError) Unwrap() error { return ErrWriterLocked }

// IsWriterLockExitLine reports whether a Core startup failure line carries the
// writer-lock conflict so callers outside the supervisor can reuse the exact
// classification.
func IsWriterLockExitLine(line string) bool { return isWriterLockExitLine(line) }

// SynthesizeStartupError classifies captured Core startup failure output the
// same way waitReady does, so integration tests can prove the mapping from
// real child output to the typed product error.
func SynthesizeStartupError(which Service, output string) error {
	if IsWriterLockExitLine(output) {
		return &WriterLockConflictError{Service: which}
	}
	return fmt.Errorf("%s failed to start: %s", which, firstDetail(strings.TrimSpace(output), "child exited"))
}

// isWriterLockExitLine recognizes the writer-lock conflict Core prints on
// stderr before exiting (main's "devbridge failed:" line wrapping the dataroot
// writer-lock error).
func isWriterLockExitLine(line string) bool {
	return strings.Contains(line, "already open for writing")
}

// EndpointConflictError never implies permission to terminate the listener.
type EndpointConflictError struct {
	Service  Service
	Endpoint string
}

func (e *EndpointConflictError) Error() string {
	return fmt.Sprintf("%s endpoint %s: %v", e.Service, e.Endpoint, ErrEndpointConflict)
}
func (e *EndpointConflictError) Unwrap() error { return ErrEndpointConflict }

type Command struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

type ServiceConfig struct {
	Command   Command
	Endpoint  string
	ReadyPath string
}

type Config struct {
	Core            ServiceConfig
	MCP             ServiceConfig
	LocalMCP        ServiceConfig
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
	ProbeInterval   time.Duration
	// LocalMCPReadyPollInterval controls how often a Running managed Local
	// MCP adapter is re-probed so its projection stays truthful after start.
	LocalMCPReadyPollInterval time.Duration
	HTTPClient                *http.Client
}

// Process is the small ownership boundary around an operating-system child.
type Process interface {
	Wait() error
	Signal(os.Signal) error
	Kill() error
}
type ProcessFactory interface {
	Start(context.Context, Command, io.Writer, io.Writer) (Process, error)
}
type ProbeResult struct {
	Reachable, Compatible, Ready bool
	Build, DataRoot, Detail      string
}
type EndpointProber interface {
	Probe(context.Context, Service, string, string) (ProbeResult, error)
}

type ServiceStatus struct {
	Service   Service      `json:"service"`
	State     ProcessState `json:"state"`
	Ownership Ownership    `json:"ownership"`
	Endpoint  string       `json:"endpoint"`
	Build     string       `json:"build,omitempty"`
	DataRoot  string       `json:"dataRoot,omitempty"`
	Detail    string       `json:"detail,omitempty"`
	ExitError string       `json:"exitError,omitempty"`
}
type Status struct {
	Core          ServiceStatus `json:"core"`
	MCP           ServiceStatus `json:"mcp"`
	LocalMCP      ServiceStatus `json:"localMCP"`
	BuildMismatch bool          `json:"buildMismatch"`
}

type RuntimeSupervisor struct {
	config   Config
	factory  ProcessFactory
	prober   EndpointProber
	logs     *logbuffer.Buffer
	mu       sync.RWMutex
	services map[Service]*serviceRuntime
}
type serviceRuntime struct {
	status      ServiceStatus
	process     Process
	generation  uint64
	launchError string
}

func NewRuntimeSupervisor(config Config, factory ProcessFactory, prober EndpointProber, logs *logbuffer.Buffer) (*RuntimeSupervisor, error) {
	config = normalizedConfig(config)
	if factory == nil {
		factory = execFactory{}
	}
	if prober == nil {
		prober = HTTPProber{Client: config.HTTPClient}
	}
	if logs == nil {
		logs = logbuffer.New(512)
	}
	if err := validateServiceConfig(Core, config.Core); err != nil {
		return nil, err
	}
	if err := validateServiceConfig(MCP, config.MCP); err != nil {
		return nil, err
	}
	if err := validateServiceConfig(LocalMCP, config.LocalMCP); err != nil {
		return nil, err
	}
	return &RuntimeSupervisor{config: config, factory: factory, prober: prober, logs: logs, services: map[Service]*serviceRuntime{
		Core:     {status: ServiceStatus{Service: Core, State: Stopped, Endpoint: config.Core.Endpoint}},
		MCP:      {status: ServiceStatus{Service: MCP, State: Stopped, Endpoint: config.MCP.Endpoint}},
		LocalMCP: {status: ServiceStatus{Service: LocalMCP, State: Stopped, Endpoint: config.LocalMCP.Endpoint}},
	}}, nil
}

func validateServiceConfig(service Service, config ServiceConfig) error {
	// Local MCP is a managed component, not a required stack member: an
	// unconfigured endpoint simply leaves it stopped.
	if service == LocalMCP && strings.TrimSpace(config.Endpoint) == "" {
		return nil
	}
	if _, err := url.ParseRequestURI(config.Endpoint); err != nil {
		return fmt.Errorf("%s endpoint: %w", service, err)
	}
	if config.ReadyPath == "" || !strings.HasPrefix(config.ReadyPath, "/") {
		return fmt.Errorf("%s ready path must begin with /", service)
	}
	return nil
}
func normalizedConfig(c Config) Config {
	if c.Core.ReadyPath == "" {
		c.Core.ReadyPath = "/readyz"
	}
	if c.MCP.ReadyPath == "" {
		c.MCP.ReadyPath = "/readyz"
	}
	if c.LocalMCP.ReadyPath == "" {
		c.LocalMCP.ReadyPath = "/readyz"
	}
	if c.StartupTimeout <= 0 {
		c.StartupTimeout = 10 * time.Second
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 5 * time.Second
	}
	if c.ProbeInterval <= 0 {
		c.ProbeInterval = 100 * time.Millisecond
	}
	if c.LocalMCPReadyPollInterval <= 0 {
		c.LocalMCPReadyPollInterval = time.Second
	}
	return c
}

// Start attaches compatible pre-existing services or starts Core, MCP, and
// the Local MCP adapter in dependency order. A Local MCP startup failure is
// scoped to its own projection: it never tears the managed stack down.
func (s *RuntimeSupervisor) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start context is required")
	}
	if err := s.startOne(ctx, Core); err != nil {
		return err
	}
	if err := s.startOne(ctx, MCP); err != nil {
		// Do not leave a partially started, Desktop-owned stack behind. Attached
		// Core services are deliberately untouched by Stop.
		_ = s.Stop(context.Background())
		return err
	}
	if err := s.startOne(ctx, LocalMCP); err != nil {
		s.logs.AppendLine(string(LocalMCP), logbuffer.Stderr, "local mcp startup failed: "+err.Error())
	}
	return nil
}
func (s *RuntimeSupervisor) startOne(ctx context.Context, which Service) error {
	s.mu.RLock()
	state := s.services[which].status.State
	s.mu.RUnlock()
	if state == Running || state == External {
		return nil
	}
	if state == Starting || state == Stopping {
		return ErrAlreadyRunning
	}
	config := s.serviceConfig(which)
	if which == LocalMCP && strings.TrimSpace(config.Endpoint) == "" {
		return nil
	}
	probe, err := s.probe(ctx, which, config)
	if err == nil && probe.Reachable {
		if !probe.Compatible {
			s.setStatus(which, Error, Conflict, probe.Build, "", firstDetail(probe.Detail, "incompatible listener"), "")
			return &EndpointConflictError{Service: which, Endpoint: config.Endpoint}
		}
		if !probe.Ready {
			s.setStatus(which, Error, Attached, probe.Build, "", firstDetail(probe.Detail, "service is not ready"), "")
			return fmt.Errorf("%s service at %s is not ready", which, config.Endpoint)
		}
		s.setStatus(which, External, Attached, probe.Build, probe.DataRoot, probe.Detail, "")
		return nil
	}
	if strings.TrimSpace(config.Command.Path) == "" {
		s.setStatus(which, Error, Unowned, "", "", "no binary configured", "")
		return fmt.Errorf("%s binary is not configured", which)
	}
	s.setStatus(which, Starting, Managed, "", "", "starting", "")
	stdout := &serviceLogWriter{buffer: s.logs, service: which, stream: logbuffer.Stdout, onLine: func(line string) { s.observeLine(which, line) }}
	stderr := &serviceLogWriter{buffer: s.logs, service: which, stream: logbuffer.Stderr, onLine: func(line string) { s.observeErrorLine(which, line) }}
	s.mu.Lock()
	s.services[which].launchError = ""
	s.mu.Unlock()
	process, err := s.factory.Start(ctx, config.Command, stdout, stderr)
	if err != nil {
		s.setStatus(which, Error, Unowned, "", "", err.Error(), "")
		return fmt.Errorf("start %s: %w", which, err)
	}
	s.mu.Lock()
	item := s.services[which]
	item.process = process
	item.generation++
	generation := item.generation
	s.mu.Unlock()
	go s.observeExit(which, generation, process)
	if probe, err = s.waitReady(ctx, which, config); err != nil {
		readyErr := err
		_ = s.stopOne(context.Background(), which)
		s.mu.RLock()
		build := s.services[which].status.Build
		s.mu.RUnlock()
		s.setStatus(which, Error, Unowned, build, "", readyErr.Error(), "")
		return readyErr
	}
	if probe.Build == "" {
		s.mu.RLock()
		probe.Build = s.services[which].status.Build
		s.mu.RUnlock()
	}
	s.setStatus(which, Running, Managed, probe.Build, probe.DataRoot, probe.Detail, "")
	if which == LocalMCP {
		go s.pollLocalReadiness(generation)
	}
	return nil
}

func (s *RuntimeSupervisor) waitReady(parent context.Context, which Service, config ServiceConfig) (ProbeResult, error) {
	ctx, cancel := context.WithTimeout(parent, s.config.StartupTimeout)
	defer cancel()
	var lastProbe string
	for {
		result, err := s.probe(ctx, which, config)
		if err == nil && result.Reachable && result.Compatible && result.Ready {
			return result, nil
		}
		if detail := strings.TrimSpace(result.Detail); detail != "" {
			lastProbe = detail
		} else if err != nil && strings.TrimSpace(err.Error()) != "" {
			lastProbe = err.Error()
		}
		s.mu.RLock()
		status := s.services[which].status
		launchError := s.services[which].launchError
		s.mu.RUnlock()
		if status.State == Crashed {
			if which == Core && isWriterLockExitLine(launchError) {
				return ProbeResult{}, &WriterLockConflictError{Service: which}
			}
			return ProbeResult{}, fmt.Errorf("%s failed to start: %s", which, firstDetail(launchError, status.ExitError, status.Detail))
		}
		select {
		case <-ctx.Done():
			if lastProbe != "" {
				return ProbeResult{}, fmt.Errorf("%s readiness timeout: %w; last probe: %s", which, ctx.Err(), lastProbe)
			}
			return ProbeResult{}, fmt.Errorf("%s readiness timeout: %w", which, ctx.Err())
		case <-time.After(s.config.ProbeInterval):
		}
	}
}
func (s *RuntimeSupervisor) probe(ctx context.Context, service Service, config ServiceConfig) (ProbeResult, error) {
	probeCtx, cancel := context.WithTimeout(ctx, minDuration(time.Second, s.config.StartupTimeout))
	defer cancel()
	return s.prober.Probe(probeCtx, service, config.Endpoint, config.ReadyPath)
}

// pollLocalReadiness keeps the managed Local MCP projection truthful: a
// Running adapter whose readiness endpoint stops answering is reported as
// not ready instead of staying Ready forever. The poller exits as soon as
// its generation is superseded or the process stops running.
func (s *RuntimeSupervisor) pollLocalReadiness(generation uint64) {
	for {
		s.mu.RLock()
		item := s.services[LocalMCP]
		active := item.generation == generation && item.status.State == Running && item.process != nil
		config := s.config.LocalMCP
		s.mu.RUnlock()
		if !active {
			return
		}
		if result, err := s.probe(context.Background(), LocalMCP, config); err == nil {
			detail := "not ready"
			if result.Reachable && result.Compatible && result.Ready {
				detail = firstDetail(result.Detail, "ready")
			}
			s.mu.Lock()
			item := s.services[LocalMCP]
			if item.generation == generation && item.status.State == Running {
				item.status.Detail = detail
			}
			s.mu.Unlock()
		}
		time.Sleep(s.config.LocalMCPReadyPollInterval)
	}
}

func (s *RuntimeSupervisor) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop context is required")
	}
	var result error
	for _, which := range []Service{LocalMCP, MCP, Core} {
		result = errors.Join(result, s.stopOne(ctx, which))
	}
	return result
}
func (s *RuntimeSupervisor) Shutdown(ctx context.Context) error { return s.Stop(ctx) }
func (s *RuntimeSupervisor) Restart(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.Start(ctx)
}
func (s *RuntimeSupervisor) stopOne(ctx context.Context, which Service) error {
	s.mu.RLock()
	item := s.services[which]
	status, process := item.status, item.process
	s.mu.RUnlock()
	if status.Ownership != Managed || process == nil {
		return nil
	}
	s.setStatus(which, Stopping, Managed, status.Build, status.DataRoot, "stopping", "")
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		s.logs.AppendLine(string(which), logbuffer.Stderr, "graceful stop: "+err.Error())
	}
	deadline := s.config.ShutdownTimeout
	if remaining, ok := ctx.Deadline(); ok {
		deadline = minDuration(deadline, time.Until(remaining))
	}
	if deadline <= 0 {
		deadline = time.Millisecond
	}
	select {
	case <-s.doneFor(which, process):
		s.setStatus(which, Stopped, Unowned, status.Build, status.DataRoot, "", "")
		return nil
	case <-time.After(deadline):
	}
	s.logs.AppendLine(string(which), logbuffer.Stderr, "graceful shutdown timed out; terminating owned child")
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("terminate owned %s: %w", which, err)
	}
	select {
	case <-s.doneFor(which, process):
		s.setStatus(which, Stopped, Unowned, status.Build, status.DataRoot, "", "")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
		return fmt.Errorf("owned %s did not exit after termination", which)
	}
}
func (s *RuntimeSupervisor) doneFor(which Service, process Process) <-chan struct{} { // wait is idempotently observed through a short-lived channel
	ch := make(chan struct{})
	go func() {
		s.mu.RLock()
		same := s.services[which].process == process
		s.mu.RUnlock()
		if !same {
			close(ch)
			return
		}
		for {
			s.mu.RLock()
			current := s.services[which].process
			s.mu.RUnlock()
			if current != process {
				close(ch)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return ch
}
func (s *RuntimeSupervisor) observeExit(which Service, generation uint64, process Process) {
	err := process.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.services[which]
	if item.generation != generation || item.process != process {
		return
	}
	prior := item.status
	item.process = nil
	if prior.State == Stopping {
		item.status.State, item.status.Ownership, item.status.Detail = Stopped, Unowned, ""
		return
	}
	item.status.State, item.status.Ownership, item.status.Detail = Crashed, Managed, firstDetail(item.launchError, "child exited")
	if err != nil {
		item.status.ExitError = err.Error()
	}
}

func (s *RuntimeSupervisor) Snapshot() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	core, mcp, local := s.services[Core].status, s.services[MCP].status, s.services[LocalMCP].status
	builds := []string{core.Build, mcp.Build, local.Build}
	known := 0
	for _, build := range builds {
		if build != "" {
			known++
		}
	}
	distinct := map[string]bool{}
	for _, build := range builds {
		if build != "" {
			distinct[build] = true
		}
	}
	return Status{Core: core, MCP: mcp, LocalMCP: local, BuildMismatch: known > 1 && len(distinct) > 1}
}
func (s *RuntimeSupervisor) Logs() []logbuffer.Entry { return s.logs.Snapshot() }
func (s *RuntimeSupervisor) serviceConfig(which Service) ServiceConfig {
	if which == Core {
		return s.config.Core
	}
	if which == LocalMCP {
		return s.config.LocalMCP
	}
	return s.config.MCP
}
func (s *RuntimeSupervisor) setStatus(which Service, state ProcessState, owner Ownership, build, dataRoot, detail, exit string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := &s.services[which].status
	status.State, status.Ownership, status.Build, status.DataRoot, status.Detail, status.ExitError = state, owner, build, dataRoot, detail, exit
}
func (s *RuntimeSupervisor) setDetail(which Service, detail string) {
	s.mu.Lock()
	s.services[which].status.Detail = detail
	s.mu.Unlock()
}
func (s *RuntimeSupervisor) observeLine(which Service, line string) {
	if which != Core || !strings.HasPrefix(line, "DEVBRIDGE_READY ") {
		return
	}
	_, build, found := strings.Cut(line, "build=")
	if !found || strings.TrimSpace(build) == "" {
		return
	}
	s.mu.Lock()
	s.services[which].status.Build = strings.Fields(build)[0]
	s.mu.Unlock()
}
func (s *RuntimeSupervisor) observeErrorLine(which Service, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if which == Core && isWriterLockExitLine(line) {
		s.setDetail(which, ErrWriterLocked.Error())
	}
	s.mu.Lock()
	s.services[which].launchError = line
	s.mu.Unlock()
}
func firstDetail(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

type execFactory struct{}

func (execFactory) Start(ctx context.Context, command Command, stdout, stderr io.Writer) (Process, error) {
	child := exec.CommandContext(ctx, command.Path, command.Args...)
	child.Dir, child.Stdout, child.Stderr = command.Dir, stdout, stderr
	if len(command.Env) > 0 {
		child.Env = append(os.Environ(), command.Env...)
	}
	if err := child.Start(); err != nil {
		return nil, err
	}
	return execProcess{child}, nil
}

type execProcess struct{ command *exec.Cmd }

func (p execProcess) Wait() error                   { return p.command.Wait() }
func (p execProcess) Signal(signal os.Signal) error { return p.command.Process.Signal(signal) }
func (p execProcess) Kill() error                   { return p.command.Process.Kill() }

// HTTPProber verifies Core through its readiness endpoint and Console title;
// MCP build identity comes from its existing bridge_diagnostics operation.
type HTTPProber struct{ Client *http.Client }

func (p HTTPProber) Probe(ctx context.Context, service Service, endpoint, readyPath string) (ProbeResult, error) {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+readyPath, nil)
	if err != nil {
		return ProbeResult{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return ProbeResult{}, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
	if response.StatusCode != http.StatusOK {
		return ProbeResult{Reachable: true, Detail: fmt.Sprintf("HTTP %d", response.StatusCode)}, nil
	}
	text := string(body)
	if service == Core {
		if !strings.Contains(text, `"ready":true`) {
			return ProbeResult{Reachable: true, Detail: "unexpected readiness response"}, nil
		}
		ui, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/ui/", nil)
		if err != nil {
			return ProbeResult{}, err
		}
		uiResponse, err := client.Do(ui)
		if err != nil {
			return ProbeResult{Reachable: true, Detail: "Core Console unavailable"}, nil
		}
		defer uiResponse.Body.Close()
		if uiResponse.StatusCode != http.StatusOK {
			return ProbeResult{Reachable: true, Detail: fmt.Sprintf("Core Console HTTP %d", uiResponse.StatusCode)}, nil
		}
		identity, identityErr := p.coreIdentity(ctx, client, endpoint)
		if identityErr != nil {
			return ProbeResult{Reachable: true, Compatible: true, Ready: true, Detail: "ready; runtime identity readback failed: " + identityErr.Error()}, nil
		}
		return ProbeResult{Reachable: true, Compatible: true, Ready: true, DataRoot: identity, Detail: "ready"}, nil
	}
	if !strings.Contains(text, `"status":"ready"`) {
		return ProbeResult{Reachable: true, Detail: "unexpected readiness response"}, nil
	}
	if service == LocalMCP {
		compatible, build, err := p.localMCPIdentity(ctx, client, endpoint)
		if err != nil {
			return ProbeResult{Reachable: true, Detail: "Local MCP adapter identity unavailable"}, nil
		}
		if !compatible {
			return ProbeResult{Reachable: true, Detail: "endpoint is not a Dev Bridge Local MCP adapter"}, nil
		}
		return ProbeResult{Reachable: true, Compatible: true, Ready: true, Build: build, Detail: "ready"}, nil
	}
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bridge_diagnostics","arguments":{}}}`)
	call, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/mcp", bytes.NewReader(payload))
	if err != nil {
		return ProbeResult{}, err
	}
	call.Header.Set("Content-Type", "application/json")
	call.Header.Set("Accept", "application/json, text/event-stream")
	callResponse, err := client.Do(call)
	if err != nil {
		return ProbeResult{Reachable: true, Detail: "MCP diagnostics unavailable"}, nil
	}
	defer callResponse.Body.Close()
	diagnostic, _ := io.ReadAll(io.LimitReader(callResponse.Body, 64<<10))
	build := diagnosticBuild(diagnostic)
	if callResponse.StatusCode != http.StatusOK || build == "" {
		return ProbeResult{Reachable: true, Detail: "MCP diagnostics did not identify Dev Bridge"}, nil
	}
	return ProbeResult{Reachable: true, Compatible: true, Ready: true, Build: build, Detail: "ready"}, nil
}

// coreIdentity reads the Core-owned DataRoot only after the readiness and
// Console availability checks pass. A configured launch argument is never
// used as a substitute for this authoritative runtime identity.
func (p HTTPProber) coreIdentity(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/_adapters/identity", nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("identity returned HTTP %d", response.StatusCode)
	}
	var identity struct {
		DataRoot string `json:"dataRoot"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&identity); err != nil {
		return "", errors.New("identity response is invalid")
	}
	identity.DataRoot = strings.TrimSpace(identity.DataRoot)
	if identity.DataRoot == "" {
		return "", errors.New("identity omitted DataRoot")
	}
	return identity.DataRoot, nil
}

func diagnosticBuild(body []byte) string {
	var envelope struct {
		Result struct {
			StructuredContent struct {
				ApplicationName    string `json:"applicationName"`
				ApplicationVersion string `json:"applicationVersion"`
				Ready              bool   `json:"ready"`
				Protocol           string `json:"protocol"`
				ServerName         string `json:"serverName"`
				ServerVersion      string `json:"serverVersion"`
				Transport          string `json:"transport"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	info := envelope.Result.StructuredContent
	if info.ApplicationName != "Dev Bridge" || !info.Ready || info.Protocol != "MCP" ||
		info.ServerName != "devbridge" || info.Transport != "streamable-http" ||
		strings.TrimSpace(info.ApplicationVersion) == "" || strings.TrimSpace(info.ServerVersion) == "" {
		return ""
	}
	return info.ServerVersion
}

// localMCPIdentity verifies that a ready listener is a compatible Local MCP
// adapter, not another Dev Bridge MCP server or an unrelated HTTP service.
// Compatibility requires the Local MCP adapter's unique decision capability
// the build version is read from that identity.
func (p HTTPProber) localMCPIdentity(ctx context.Context, client *http.Client, endpoint string) (bool, string, error) {
	tools, err := p.mcpCall(ctx, client, endpoint, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if err != nil {
		return false, "", err
	}
	if !strings.Contains(string(tools), `"decision_respond"`) {
		return false, "", nil
	}
	diagnostics, err := p.mcpCall(ctx, client, endpoint, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"bridge_diagnostics","arguments":{}}}`))
	if err != nil {
		return false, "", err
	}
	build := diagnosticBuild(diagnostics)
	if build == "" {
		return false, "", nil
	}
	return true, build, nil
}

func (p HTTPProber) mcpCall(ctx context.Context, client *http.Client, endpoint string, payload []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/mcp", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MCP call returned HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 64<<10))
}
