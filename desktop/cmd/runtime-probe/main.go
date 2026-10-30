// Command runtime-probe performs the bounded, local Desktop integration gate.
// It is a development harness, not a second runtime or product health protocol.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	codexprobe "github.com/lazyun/devbridge/desktop/internal/codex"
	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
	runtimecontrol "github.com/lazyun/devbridge/desktop/internal/runtime"
	"github.com/lazyun/devbridge/internal/codexlaunch"
)

type result struct {
	CoreState               runtimecontrol.ProcessState `json:"coreState"`
	MCPState                runtimecontrol.ProcessState `json:"mcpState"`
	LocalMCPState           runtimecontrol.ProcessState `json:"localMCPState"`
	CoreBuild               string                      `json:"coreBuild"`
	MCPBuild                string                      `json:"mcpBuild"`
	LocalMCPBuild           string                      `json:"localMCPBuild"`
	BuildMismatch           bool                        `json:"buildMismatch"`
	ConsoleResponded        bool                        `json:"consoleResponded"`
	CodexStatus             string                      `json:"codexStatus"`
	CodexExecutable         string                      `json:"codexExecutable"`
	CodexVersion            string                      `json:"codexVersion"`
	OwnedPortsFreed         bool                        `json:"ownedPortsFreed"`
	ForeignConflictReported bool                        `json:"foreignConflictReported"`
	ForeignListenerSurvived bool                        `json:"foreignListenerSurvived"`
}

func main() {
	var coreBinary, mcpBinary, localMCPBinary, codexExecutable, dataRoot, expectedBuild, coreAddress, mcpAddress, localMCPAddress string
	flag.StringVar(&coreBinary, "core", "", "path to the devbridge binary")
	flag.StringVar(&mcpBinary, "mcp", "", "path to the devbridge-mcp binary")
	flag.StringVar(&localMCPBinary, "local-mcp", "", "path to the devbridge-local-mcp binary")
	flag.StringVar(&codexExecutable, "codex", "codex", "Codex executable or path")
	flag.StringVar(&dataRoot, "data-root", "", "isolated probe Dev Bridge data directory")
	flag.StringVar(&expectedBuild, "expected-build", "", "expected running service build")
	flag.StringVar(&coreAddress, "core-address", "127.0.0.1:8081", "isolated Core loopback address")
	flag.StringVar(&mcpAddress, "mcp-address", "127.0.0.1:8080", "isolated MCP loopback address")
	flag.StringVar(&localMCPAddress, "local-mcp-address", "127.0.0.1:8090", "isolated Local MCP loopback address")
	flag.Parse()
	if coreBinary == "" || mcpBinary == "" || localMCPBinary == "" || dataRoot == "" || expectedBuild == "" {
		fatal(errors.New("-core, -mcp, -local-mcp, -data-root, and -expected-build are required"))
	}

	for _, address := range []string{coreAddress, mcpAddress, localMCPAddress} {
		if err := verifyFree(address); err != nil {
			fatal(fmt.Errorf("preflight %s: %w", address, err))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	logs := logbuffer.New(500)
	config := runtimecontrol.Config{
		Core: runtimecontrol.ServiceConfig{
			Command:  runtimecontrol.Command{Path: coreBinary, Args: []string{"-mode", "server", "-data-root", dataRoot, "-listen", coreAddress}},
			Endpoint: "http://" + coreAddress,
		},
		MCP: runtimecontrol.ServiceConfig{
			Command:  runtimecontrol.Command{Path: mcpBinary, Args: []string{"-data-root", dataRoot, "-core-url", "http://" + coreAddress, "-listen", mcpAddress}},
			Endpoint: "http://" + mcpAddress,
		},
		LocalMCP: runtimecontrol.ServiceConfig{
			Command:  runtimecontrol.Command{Path: localMCPBinary, Args: []string{"-data-root", dataRoot, "-core-url", "http://" + coreAddress, "-listen", localMCPAddress}},
			Endpoint: "http://" + localMCPAddress,
		},
		StartupTimeout:  15 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		ProbeInterval:   50 * time.Millisecond,
	}
	selectionDir, err := os.MkdirTemp("", "devbridge-codex-selection-")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(selectionDir)
	selectionFile := filepath.Join(selectionDir, "selection.json")
	codex := codexprobe.Probe(ctx, codexExecutable)
	if err := codexlaunch.Write(selectionFile, codexlaunch.Selection{Configured: codexExecutable, Executable: codex.Executable, Error: codex.Error}); err != nil {
		fatal(err)
	}
	config.Core.Command.Env = append(config.Core.Command.Env, codexlaunch.SelectionEnv+"="+selectionFile)
	supervisor, err := runtimecontrol.NewRuntimeSupervisor(config, nil, nil, logs)
	if err != nil {
		fatal(err)
	}
	defer supervisor.Shutdown(context.Background())
	var snapshot runtimecontrol.Status
	var consoleOK bool
	probeErr := func() error {
		if err := supervisor.Start(ctx); err != nil {
			return fmt.Errorf("start managed stack: %w", err)
		}
		snapshot = supervisor.Snapshot()
		if snapshot.Core.State != runtimecontrol.Running || snapshot.MCP.State != runtimecontrol.Running || snapshot.LocalMCP.State != runtimecontrol.Running {
			return fmt.Errorf("stack not ready: %+v", snapshot)
		}
		if snapshot.Core.Ownership != runtimecontrol.Managed || snapshot.MCP.Ownership != runtimecontrol.Managed || snapshot.LocalMCP.Ownership != runtimecontrol.Managed {
			return fmt.Errorf("stack not managed: %+v", snapshot)
		}
		if snapshot.Core.Build != expectedBuild || snapshot.MCP.Build != expectedBuild || snapshot.LocalMCP.Build != expectedBuild || snapshot.BuildMismatch {
			return fmt.Errorf("unexpected running builds: core=%q mcp=%q localMcp=%q expected=%q mismatch=%t", snapshot.Core.Build, snapshot.MCP.Build, snapshot.LocalMCP.Build, expectedBuild, snapshot.BuildMismatch)
		}
		var err error
		consoleOK, err = consoleResponds(ctx, "http://"+coreAddress+"/ui/")
		if err != nil || !consoleOK {
			return fmt.Errorf("Console probe: %w", err)
		}
		codex = codexprobe.VerifyCore("http://"+coreAddress, selectionFile, codex)
		if codex.Status != codexprobe.Available {
			return fmt.Errorf("Codex probe: %s: %s", codex.Status, codex.Error)
		}
		return nil
	}()
	if stopErr := supervisor.Stop(ctx); probeErr == nil && stopErr != nil {
		probeErr = fmt.Errorf("stop managed stack: %w", stopErr)
	}
	if probeErr != nil {
		fatal(probeErr)
	}
	for _, address := range []string{coreAddress, mcpAddress, localMCPAddress} {
		if err := waitFree(ctx, address); err != nil {
			fatal(err)
		}
	}

	foreign, err := net.Listen("tcp", coreAddress)
	if err != nil {
		fatal(fmt.Errorf("start foreign listener: %w", err))
	}
	foreignServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "foreign listener\n")
	})}
	go func() { _ = foreignServer.Serve(foreign) }()
	conflictSupervisor, err := runtimecontrol.NewRuntimeSupervisor(config, nil, nil, logbuffer.New(20))
	if err != nil {
		fatal(err)
	}
	conflictErr := conflictSupervisor.Start(ctx)
	conflictReported := errors.Is(conflictErr, runtimecontrol.ErrEndpointConflict)
	_ = conflictSupervisor.Stop(ctx)
	connection, dialErr := net.DialTimeout("tcp", coreAddress, time.Second)
	listenerSurvived := dialErr == nil
	if connection != nil {
		_ = connection.Close()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = foreignServer.Shutdown(shutdownCtx)
	shutdownCancel()
	if !conflictReported || !listenerSurvived {
		fatal(fmt.Errorf("foreign listener safety failed: conflict=%t survived=%t error=%v", conflictReported, listenerSurvived, conflictErr))
	}

	value := result{
		CoreState:               snapshot.Core.State,
		MCPState:                snapshot.MCP.State,
		LocalMCPState:           snapshot.LocalMCP.State,
		CoreBuild:               snapshot.Core.Build,
		MCPBuild:                snapshot.MCP.Build,
		LocalMCPBuild:           snapshot.LocalMCP.Build,
		BuildMismatch:           snapshot.BuildMismatch,
		ConsoleResponded:        consoleOK,
		CodexStatus:             string(codex.Status),
		CodexVersion:            codex.Version,
		CodexExecutable:         codex.Executable,
		OwnedPortsFreed:         true,
		ForeignConflictReported: conflictReported,
		ForeignListenerSurvived: listenerSurvived,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fatal(err)
	}
}

func verifyFree(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errors.New("endpoint is already occupied")
	}
	return listener.Close()
}

func waitFree(ctx context.Context, address string) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := verifyFree(address); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("port %s did not become free: %w", address, ctx.Err())
		case <-ticker.C:
		}
	}
}

func consoleResponds(ctx context.Context, endpoint string) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "runtime-probe:", err)
	os.Exit(1)
}
