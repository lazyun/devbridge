package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
)

// NotConfigured marks the ChatGPT-integration tunnel as intentionally absent.
// Unlike Core and MCP, the tunnel is optional: NOT_CONFIGURED is a healthy
// projection and never blocks the managed stack.
const NotConfigured ProcessState = "NOT_CONFIGURED"

var ErrTunnelNotConfigured = errors.New("OpenAI tunnel-client is not configured")

// TunnelStatus is the local supervision projection for the OpenAI
// tunnel-client. It is deliberately separate from Core/MCP ServiceStatus:
// tunnel state never participates in the Bridge domain or stack readiness.
type TunnelStatus struct {
	State      ProcessState `json:"state"`
	Ownership  Ownership    `json:"ownership"`
	Executable string       `json:"executable,omitempty"`
	PID        int          `json:"pid,omitempty"`
	StartedAt  string       `json:"startedAt,omitempty"`
	ExitCode   *int         `json:"exitCode,omitempty"`
	LastError  string       `json:"lastError,omitempty"`
	Version    string       `json:"version,omitempty"`
	Ready      bool         `json:"ready"`
	Detail     string       `json:"detail,omitempty"`
}

// TunnelReadyFunc reports the daemon's own readiness signal. err != nil means
// the operator surface has not answered yet (the process may still be alive
// and starting); err == nil means the surface answered, and ready carries its
// verdict. A merely live process is never reported ready.
type TunnelReadyFunc func(ctx context.Context) (ready bool, detail string, err error)

// TunnelAttachFunc reports a compatible daemon that this Desktop instance
// does not own. External daemons are never terminated; conflict means the
// recorded operator surface is occupied by an incompatible service.
type TunnelAttachFunc func() (external bool, conflict bool, pid int, detail string)

type TunnelConfig struct {
	Command           Command
	Version           string
	Readiness         TunnelReadyFunc
	Attach            TunnelAttachFunc
	StartupTimeout    time.Duration
	ShutdownTimeout   time.Duration
	ReadyPollInterval time.Duration
}

// TunnelSupervisor supervises only tunnel-client processes this Desktop
// instance started. It reuses the Core/MCP ownership model: MANAGED children
// may be signalled and terminated; EXTERNAL daemons are attached read-only;
// CONFLICT is projected and never resolved by force.
type TunnelSupervisor struct {
	config  TunnelConfig
	factory ProcessFactory
	logs    *logbuffer.Buffer
	mu      sync.RWMutex
	status  TunnelStatus
	process Process
	// generation invalidates stale exit observers and readiness pollers.
	generation uint64
}

func NewTunnelSupervisor(config TunnelConfig, factory ProcessFactory, logs *logbuffer.Buffer) *TunnelSupervisor {
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = 20 * time.Second
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 5 * time.Second
	}
	if config.ReadyPollInterval <= 0 {
		config.ReadyPollInterval = 2 * time.Second
	}
	if factory == nil {
		factory = execFactory{}
	}
	if logs == nil {
		logs = logbuffer.New(512)
	}
	supervisor := &TunnelSupervisor{config: config, factory: factory, logs: logs}
	if strings.TrimSpace(config.Command.Path) == "" {
		supervisor.status = TunnelStatus{State: NotConfigured, Detail: "not configured"}
	} else {
		supervisor.status = TunnelStatus{State: Stopped, Executable: config.Command.Path, Version: config.Version}
	}
	return supervisor
}

// Start attaches an external daemon, or starts a managed one. It returns
// ErrTunnelNotConfigured when no executable is configured; this is not a
// stack failure. A startup failure never affects Core or MCP.
func (s *TunnelSupervisor) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start context is required")
	}
	s.mu.RLock()
	state := s.status.State
	s.mu.RUnlock()
	switch state {
	case Running, External:
		return nil
	case Starting, Stopping:
		return ErrAlreadyRunning
	case NotConfigured:
		return ErrTunnelNotConfigured
	}
	if strings.TrimSpace(s.config.Command.Path) == "" {
		s.setStatus(TunnelStatus{State: NotConfigured, Detail: "not configured"})
		return ErrTunnelNotConfigured
	}
	if s.config.Attach != nil {
		external, conflict, pid, detail := s.config.Attach()
		s.logs.AppendLine(tunnelLogSource, logbuffer.Stdout, "tunnel ownership check: "+sanitizeTunnelText(detail))
		if conflict {
			s.setStatus(TunnelStatus{State: Error, Ownership: Conflict, Executable: s.config.Command.Path, Detail: detail})
			return errors.New(detail)
		}
		if external {
			s.setStatus(TunnelStatus{State: External, Ownership: Attached, Executable: s.config.Command.Path, PID: pid, Detail: detail})
			return nil
		}
	}
	s.logLifecycle("tunnel process start: " + s.config.Command.Path)
	s.setStatus(TunnelStatus{State: Starting, Ownership: Managed, Executable: s.config.Command.Path, Version: s.config.Version, Detail: "starting"})
	stdout := &serviceLogWriter{buffer: s.logs, service: tunnelLogSource, stream: logbuffer.Stdout}
	stderr := &serviceLogWriter{buffer: s.logs, service: tunnelLogSource, stream: logbuffer.Stderr}
	// The daemon outlives the caller's start context: without WithoutCancel a
	// per-operation timeout context would SIGKILL the freshly started child
	// the moment Start returns. Stopping remains explicit (Stop/Shutdown).
	process, err := s.factory.Start(context.WithoutCancel(ctx), s.config.Command, stdout, stderr)
	if err != nil {
		failure := sanitizeTunnelText(err.Error())
		s.setStatus(TunnelStatus{State: Error, Executable: s.config.Command.Path, Version: s.config.Version, LastError: failure})
		s.logLifecycle("tunnel start failed: " + failure)
		return fmt.Errorf("start tunnel-client: %w", err)
	}
	s.mu.Lock()
	s.process = process
	s.generation++
	generation := s.generation
	s.status.StartedAt = time.Now().UTC().Format(time.RFC3339)
	s.mu.Unlock()
	go s.observeExit(generation, process)
	if err := s.waitLive(ctx, generation); err != nil {
		_ = s.stopOne(context.Background())
		s.mu.RLock()
		failed := s.status
		s.mu.RUnlock()
		failed.State, failed.Ownership, failed.Ready, failed.PID = Error, Unowned, false, 0
		failed.StartedAt = ""
		failed.LastError = sanitizeTunnelText(err.Error())
		s.setStatus(failed)
		s.logLifecycle("tunnel startup failed: " + sanitizeTunnelText(err.Error()))
		return err
	}
	s.mu.Lock()
	s.status.State, s.status.Ownership, s.status.Detail = Running, Managed, "running"
	s.mu.Unlock()
	go s.pollReadiness(generation)
	return nil
}

// waitLive waits for the daemon's operator surface to answer. Reaching the
// surface (ready or not) proves the process is live; it does not claim
// connected. Timing out means the process never became observable, which is
// a managed startup failure.
func (s *TunnelSupervisor) waitLive(ctx context.Context, generation uint64) error {
	if s.config.Readiness == nil {
		// No readiness signal exists: process liveness is the only truth.
		return nil
	}
	deadline := time.Now().Add(s.config.StartupTimeout)
	for {
		if _, _, err := s.config.Readiness(ctx); err == nil {
			return nil
		}
		s.mu.RLock()
		gone := s.generation != generation || s.process == nil
		state := s.status.State
		lastError := s.status.LastError
		s.mu.RUnlock()
		if gone || state == Crashed {
			return fmt.Errorf("tunnel-client failed to start: %s", sanitizeTunnelText(firstDetail(lastError, "child exited")))
		}
		if time.Now().After(deadline) {
			return errors.New("tunnel-client operator surface did not come up before the startup timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// pollReadiness keeps the Running status truthful: Ready reflects the
// daemon's own readiness answer, never mere process existence. The poller
// exits as soon as its generation is superseded or the process is no longer
// Running.
func (s *TunnelSupervisor) pollReadiness(generation uint64) {
	if s.config.Readiness == nil {
		return
	}
	interval := s.config.ReadyPollInterval
	for {
		s.mu.RLock()
		active := s.generation == generation && s.status.State == Running && s.process != nil
		s.mu.RUnlock()
		if !active {
			return
		}
		ready, detail, err := s.config.Readiness(context.Background())
		s.mu.Lock()
		if s.generation == generation && s.status.State == Running {
			if err == nil {
				if ready && !s.status.Ready {
					s.logLifecycle("tunnel ready")
				}
				s.status.Ready = ready
				if ready {
					s.status.Detail = "ready"
				} else if detail != "" {
					s.status.Detail = sanitizeTunnelText(detail)
				}
			}
		}
		s.mu.Unlock()
		time.Sleep(interval)
	}
}

// Stop terminates only the MANAGED child. EXTERNAL daemons and CONFLICT
// listeners are left untouched by design.
func (s *TunnelSupervisor) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop context is required")
	}
	return s.stopOne(ctx)
}

func (s *TunnelSupervisor) stopOne(ctx context.Context) error {
	s.mu.RLock()
	status, process := s.status, s.process
	s.mu.RUnlock()
	if status.Ownership != Managed || process == nil {
		return nil
	}
	s.mu.Lock()
	s.generation++
	s.mu.Unlock()
	s.setStatus(TunnelStatus{State: Stopping, Ownership: Managed, Executable: status.Executable, Version: status.Version, Detail: "stopping"})
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		s.logs.AppendLine(tunnelLogSource, logbuffer.Stderr, "tunnel graceful stop: "+sanitizeTunnelText(err.Error()))
	}
	deadline := s.config.ShutdownTimeout
	if remaining, ok := ctx.Deadline(); ok {
		deadline = minDuration(deadline, time.Until(remaining))
	}
	if deadline <= 0 {
		deadline = time.Millisecond
	}
	if !awaitExit(s, process, deadline) {
		s.logs.AppendLine(tunnelLogSource, logbuffer.Stderr, "tunnel graceful shutdown timed out; terminating owned child")
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("terminate owned tunnel-client: %w", err)
		}
		if !awaitExit(s, process, time.Second) {
			return errors.New("owned tunnel-client did not exit after termination")
		}
	}
	s.mu.Lock()
	s.generation++
	stopped := TunnelStatus{State: Stopped, Executable: status.Executable, Version: status.Version}
	s.status = stopped
	s.mu.Unlock()
	s.logLifecycle("tunnel stopped")
	return nil
}

func awaitExit(s *TunnelSupervisor, process Process, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.RLock()
		exited := s.process != process
		s.mu.RUnlock()
		if exited {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Restart affects only the tunnel: callers keep Core and MCP untouched.
func (s *TunnelSupervisor) Restart(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.Start(ctx)
}

func (s *TunnelSupervisor) observeExit(generation uint64, process Process) {
	err := process.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	// The handle is dead regardless of which lifecycle owns the projection.
	if s.process == process {
		s.process = nil
	}
	// A newer lifecycle (stop/restart) owns the status projection now.
	if s.generation != generation {
		return
	}
	prior := s.status
	if prior.State == Stopping {
		s.status.State, s.status.Ownership, s.status.Detail = Stopped, Unowned, ""
		return
	}
	s.status.State, s.status.Ownership, s.status.Ready = Crashed, Managed, false
	s.status.ExitCode = exitCodeOf(err)
	s.status.StartedAt = ""
	s.status.Detail = sanitizeTunnelText(firstDetail(prior.Detail, "child exited"))
	if err != nil {
		s.status.LastError = sanitizeTunnelText(err.Error())
	}
	s.logs.AppendLine(tunnelLogSource, logbuffer.Stderr, "tunnel process exited: "+s.status.LastError)
}

func exitCodeOf(err error) *int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		return &code
	}
	return nil
}

func (s *TunnelSupervisor) setStatus(status TunnelStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func (s *TunnelSupervisor) logLifecycle(message string) {
	s.logs.AppendLine(tunnelLogSource, logbuffer.Stdout, message)
}

// Snapshot returns the current tunnel projection. Ready is true only when the
// daemon's own readiness signal answered; a merely live process reports
// Running, never Ready/Connected.
func (s *TunnelSupervisor) Snapshot() TunnelStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func sanitizeTunnelText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

const tunnelLogSource = "tunnel"
