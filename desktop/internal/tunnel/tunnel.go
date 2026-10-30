// Package tunnel isolates every assumption about the external OpenAI
// tunnel-client CLI: its launch flags, its version probe, and its local
// operator surfaces (/healthz, /readyz, the health URL file, and the PID
// file). No flag here is invented; all come from "tunnel-client run --help"
// of the distribution documented for this product. The package has no
// dependency on Dev Bridge Core and never touches credentials themselves.
package tunnel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// HealthURLFileName is written by "tunnel-client run --health.url-file".
	HealthURLFileName = "health.url"
	// PIDFileName is written by "tunnel-client run --pid.file".
	PIDFileName = "pid"
	// healthzBody is the exact liveness body a live tunnel-client operator
	// surface reports on /healthz.
	healthzBody = "live"
	// readyzBody is the exact readiness body a ready tunnel-client reports
	// on /readyz.
	readyzBody = "ready"

	// DefaultProbeTimeout bounds a local --version invocation.
	DefaultProbeTimeout = 5 * time.Second
	// maxVersionOutput bounds version probe output.
	maxVersionOutput = 4096
	// maxDetail bounds sanitized readiness/health detail strings.
	maxDetail = 512
)

// RuntimeFiles are the machine-local files a managed daemon writes under the
// Desktop state directory (never inside DataRoot).
type RuntimeFiles struct {
	HealthURLFile string
	PIDFile       string
}

// RuntimeFilesIn returns the runtime file paths for a tunnel state directory.
func RuntimeFilesIn(dir string) RuntimeFiles {
	return RuntimeFiles{
		HealthURLFile: filepath.Join(dir, HealthURLFileName),
		PIDFile:       filepath.Join(dir, PIDFileName),
	}
}

// LaunchConfig is the authoritative Desktop configuration for one managed
// tunnel-client daemon. TunnelID and MCPEndpoint are required; Executable is
// validated by the caller.
type LaunchConfig struct {
	Executable       string
	MCPEndpoint      string
	TunnelID         string
	APIKeyReference  string
	HealthListenAddr string
	RuntimeFiles     RuntimeFiles
}

// BuildCommand constructs the "tunnel-client run" command. The daemon binds
// its operator surface on an ephemeral loopback port (no fixed listener that
// could collide with Core or MCP) and reports the resolved address through
// the health URL file. The API key, when configured, is passed only as the
// tunnel-client's own env:/file: reference form; the secret itself stays
// outside Desktop.
func BuildCommand(config LaunchConfig) (Command, error) {
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		return Command{}, errors.New("tunnel-client executable is not configured")
	}
	if !filepath.IsAbs(executable) {
		return Command{}, errors.New("tunnel-client executable must be an absolute path")
	}
	if strings.TrimSpace(config.TunnelID) == "" {
		return Command{}, errors.New("OpenAI tunnel ID is not configured")
	}
	mcpURL := strings.TrimRight(strings.TrimSpace(config.MCPEndpoint), "/")
	if mcpURL == "" {
		return Command{}, errors.New("MCP endpoint is not configured")
	}
	listen := strings.TrimSpace(config.HealthListenAddr)
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	if config.RuntimeFiles.HealthURLFile == "" || config.RuntimeFiles.PIDFile == "" {
		return Command{}, errors.New("tunnel runtime files are not configured")
	}
	args := []string{
		"run",
		"--mcp.server-url", mcpURL + "/mcp",
		"--control-plane.tunnel-id", config.TunnelID,
		"--health.listen-addr", listen,
		"--health.url-file", config.RuntimeFiles.HealthURLFile,
		"--pid.file", config.RuntimeFiles.PIDFile,
	}
	if reference := strings.TrimSpace(config.APIKeyReference); reference != "" {
		args = append(args, "--control-plane.api-key", reference)
	}
	return Command{Path: executable, Args: args}, nil
}

// Command is a plain launch description, kept local so this package does not
// import the runtime supervisor package.
type Command struct {
	Path string
	Args []string
}

// Version runs only "<executable> --version" with bounded duration and output
// and returns the first whitespace-delimited token of the output.
func Version(ctx context.Context, executable string) (string, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return "", errors.New("tunnel-client executable is not configured")
	}
	probeCtx, cancel := context.WithTimeout(ctx, DefaultProbeTimeout)
	defer cancel()
	command := exec.CommandContext(probeCtx, executable, "--version") // #nosec G204 -- validated local executable
	var output cappedBuffer
	output.limit = maxVersionOutput
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			return "", errors.New("tunnel-client version probe timed out")
		}
		return "", fmt.Errorf("tunnel-client version probe failed: %s", sanitize(err.Error()))
	}
	if output.truncated || output.Len() == maxVersionOutput {
		return "", errors.New("tunnel-client version output exceeded limit")
	}
	version := strings.TrimSpace(output.String())
	if version == "" {
		return "", errors.New("tunnel-client version probe returned no version")
	}
	return strings.Fields(version)[0], nil
}

// ValidateExecutablePath performs the pre-acceptance checks for a newly
// selected executable: it must exist, be a regular file, and be executable.
// The version probe itself is separate so callers control when it runs.
func ValidateExecutablePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("tunnel-client executable path is empty")
	}
	if !filepath.IsAbs(path) {
		return errors.New("tunnel-client executable path must be absolute")
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("tunnel-client executable does not exist")
	}
	if err != nil {
		return fmt.Errorf("inspect tunnel-client executable: %s", sanitize(err.Error()))
	}
	if info.IsDir() {
		return errors.New("tunnel-client executable is a directory, not a file")
	}
	if !info.Mode().IsRegular() {
		return errors.New("tunnel-client executable is not a regular file")
	}
	if info.Mode()&0111 == 0 {
		return errors.New("tunnel-client executable is not executable")
	}
	return nil
}

// AttachResult reports whether a compatible tunnel-client daemon that this
// Desktop instance does not own is already serving the operator surface.
type AttachResult struct {
	// External is true when a compatible live daemon exists. It must never
	// be terminated by this Desktop instance.
	External bool
	// Conflict is true when the recorded operator surface is occupied by an
	// incompatible service and cannot safely be reused.
	Conflict bool
	PID      int
	Detail   string
}

// pidAlive is a variable for test injection.
var pidAlive = defaultPIDAlive

func defaultPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// AttachCheck inspects the runtime files from a previous managed run. A live
// compatible daemon is projected as EXTERNAL (never killed by this instance);
// an occupied operator surface that is not a tunnel-client is CONFLICT; stale
// files from an exited daemon are removed so a fresh managed start is safe.
func AttachCheck(files RuntimeFiles) AttachResult {
	pid := readPID(files.PIDFile)
	healthURL, urlErr := readHealthURL(files.HealthURLFile)
	if urlErr != nil {
		if pidAlive(pid) {
			// A process from a previous instance is still alive; this
			// instance has no ownership over it and must not terminate it.
			return AttachResult{External: true, PID: pid, Detail: "process from a previous Desktop session is still running; its health endpoint is not responding"}
		}
		removeStaleFiles(files)
		return AttachResult{}
	}
	body, ok := probeSurface(context.Background(), healthURL, "/healthz")
	if !ok {
		if pidAlive(pid) {
			return AttachResult{External: true, PID: pid, Detail: "process from a previous Desktop session is still running; its health endpoint is not responding"}
		}
		removeStaleFiles(files)
		return AttachResult{}
	}
	if body != healthzBody {
		return AttachResult{Conflict: true, Detail: "the recorded tunnel operator surface is occupied by an incompatible service"}
	}
	return AttachResult{External: true, PID: pid, Detail: "compatible external tunnel-client daemon from a previous Desktop session is still running"}
}

// Readiness returns a probe function that reports the tunnel-client's own
// readiness. err != nil means the operator surface has not answered yet;
// err == nil means it answered, and ready carries its verdict. The returned
// detail is sanitized and bounded.
func Readiness(files RuntimeFiles) func(ctx context.Context) (ready bool, detail string, err error) {
	return func(ctx context.Context) (bool, string, error) {
		healthURL, err := readHealthURL(files.HealthURLFile)
		if err != nil {
			return false, "", err
		}
		body, ok := probeSurface(ctx, healthURL, "/readyz")
		if !ok {
			return false, "", errors.New("tunnel-client operator surface did not answer")
		}
		return body == readyzBody, sanitize(body), nil
	}
}

// probeSurface GETs one operator-surface path and returns the trimmed body.
func probeSurface(ctx context.Context, baseURL, path string) (string, bool) {
	requestCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return "", false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", false
	}
	defer response.Body.Close()
	var body bytes.Buffer
	_, _ = body.ReadFrom(response.Body)
	if response.StatusCode != http.StatusOK {
		return "", false
	}
	return strings.TrimSpace(body.String()), true
}

func readPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &value); err != nil {
		return 0
	}
	return value
}

func readHealthURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(string(data))
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return "", fmt.Errorf("invalid health URL file content")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("health URL is not loopback")
	}
	return raw, nil
}

func removeStaleFiles(files RuntimeFiles) {
	_ = os.Remove(files.HealthURLFile)
	_ = os.Remove(files.PIDFile)
}

// sanitize strips control characters and newlines from process-provided text
// and bounds its length before it reaches logs or status projection.
func sanitize(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > maxDetail {
		value = value[:maxDetail]
	}
	return value
}

type cappedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - len(b.data)
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.data = append(b.data, p[:remaining]...)
		b.truncated = true
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *cappedBuffer) Len() int       { return len(b.data) }
func (b *cappedBuffer) String() string { return string(b.data) }
