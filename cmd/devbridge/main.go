package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/buildinfo"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/dataroot"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/piagent"
	"github.com/lazyun/devbridge/internal/runtimequalification"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

const (
	responsePrompt = "Reply with exactly: DEV_BRIDGE_OK"
	responseText   = "DEV_BRIDGE_OK"
	mutationText   = "hello from dev bridge"
)

func main() { os.Exit(mainExitCode()) }

// Return through defers before exiting so logs close before writer ownership.
func mainExitCode() int {
	mode := flag.String("mode", "all", "mode: server, qualify-runtime, response, mutation, or all")
	qualificationBackend := flag.String("qualification-backend", "", "exact registered backend ID for qualify-runtime")
	qualificationRequest := flag.String("qualification-request", "", "idempotency request ID for qualify-runtime")
	qualificationRole := flag.String("qualification-role", "IMPLEMENTATION", "runtime role for qualify-runtime")
	cwd := flag.String("cwd", "", "safe working directory for the response smoke test (default: temporary directory)")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall smoke timeout")
	dataRoot := flag.String("data-root", "", "Dev Bridge data directory (default: platform application data directory)")
	listen := flag.String("listen", "127.0.0.1:0", "explicit loopback listen address (server mode)")
	adminTest := flag.Bool("admin-test", false, "enable loopback-only fixture provisioning (server mode)")
	allowedModels := flag.String("allowed-models", "", "comma-separated allowlisted Codex models (server mode)")
	allowedReasoningEfforts := flag.String("allowed-reasoning-efforts", "", "comma-separated allowlisted reasoning efforts (server mode)")
	codingAgentModelConfigs := flag.String("coding-agent-model-configs", "", "JSON Coding Agent model configs (server mode)")
	piAgentEnabled := flag.Bool("pi-agent", false, "enable the PI Agent review executor (server mode)")
	piAgentPath := flag.String("pi-agent-path", "", "PI Agent executable path (server mode, default: auto-resolve)")
	piAgentModel := flag.String("pi-agent-model", "", "PI Agent model selection (server mode, default: PI's default)")
	piAgentThinking := flag.String("pi-agent-thinking", "", "PI Agent thinking level: off, minimal, low, medium, high, xhigh, max (server mode)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var serverLogDirectory string
	var serverDataRoot dataroot.DataRoot
	var processLogger *observability.Logger
	if *mode == "server" {
		opened, openErr := dataroot.Open(*dataRoot)
		if openErr != nil {
			fmt.Fprintln(os.Stderr, "devbridge failed:", openErr)
			return 1
		}
		serverDataRoot = opened
		defer opened.Close()
		logger := observability.New(observability.Options{
			Process:   "devbridge",
			Build:     buildinfo.Version(),
			Directory: opened.Paths.Logs,
			Filename:  observability.CoreLogFilename,
		})
		serverLogDirectory = opened.Paths.Logs
		observability.SetDefault(logger)
		processLogger = logger
		defer logger.Close()
		rootEvent := "data_root.opened"
		message := "opened existing Dev Bridge data directory"
		if opened.Initialized {
			rootEvent = "data_root.initialized"
			message = "initialized Dev Bridge data directory"
		}
		logger.Info(ctx, "bootstrap", rootEvent, message, map[string]any{"dataRoot": opened.Paths.Root, "formatVersion": opened.FormatVersion})
		logger.Info(ctx, "bootstrap", "core.bootstrap.started", "starting Core service", nil)
	}

	var err error
	if *mode == "qualify-runtime" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
		err = qualifyRuntime(ctx, *dataRoot, *qualificationBackend, *qualificationRole, *qualificationRequest)
	} else if *mode == "server" {
		err = runServer(ctx, serverDataRoot, serverLogDirectory, *listen, *adminTest, *allowedModels, *allowedReasoningEfforts, *codingAgentModelConfigs, piAgentConfig(*piAgentEnabled, *piAgentPath, *piAgentModel, *piAgentThinking))
	} else {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
		err = run(ctx, *mode, *cwd)
	}
	if err != nil {
		if processLogger != nil {
			processLogger.Error(ctx, "bootstrap", "core.bootstrap.failed", "Core service failed", map[string]any{"errorCode": "CORE_PROCESS_FAILED", "errorSummary": err.Error()})
		}
		fmt.Fprintln(os.Stderr, "devbridge failed:", err)
		return 1
	}
	if processLogger != nil {
		processLogger.Info(ctx, "bootstrap", "core.bootstrap.stopped", "Core service stopped", nil)
	}
	return 0
}

func qualifyRuntime(ctx context.Context, root, backend, role, request string) error {
	if backend == "" || request == "" {
		return errors.New("qualify-runtime requires -qualification-backend and -qualification-request")
	}
	opened, err := dataroot.Open(root)
	if err != nil {
		return err
	}
	defer opened.Close()
	db, err := sqlite.Open(ctx, opened.Paths.State)
	if err != nil {
		return err
	}
	defer db.Close()
	service := &runtimequalification.Service{Store: sqlite.NewRuntimePlatformStore(db), Configurations: codexconfig.NewService(sqlite.NewCodexConfigStore(db), nil, nil), EvidenceRoot: filepath.Join(opened.Paths.Root, "platform-evidence")}
	mapping, err := service.QualifyBackend(ctx, backend, role, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(mapping)
}

func run(ctx context.Context, mode, requestedCWD string) error {
	if mode != "response" && mode != "mutation" && mode != "all" {
		return fmt.Errorf("invalid -mode %q", mode)
	}
	client, err := codex.Start(ctx, codex.Config{})
	if err != nil {
		return err
	}
	defer client.Close()

	if mode == "response" || mode == "all" {
		workingDir, cleanup, err := safeWorkingDirectory(requestedCWD, "devbridge-response-")
		if err != nil {
			return err
		}
		defer cleanup()
		result, err := executeTurn(ctx, client, workingDir, responsePrompt)
		if err != nil {
			return fmt.Errorf("response smoke: %w", err)
		}
		if strings.TrimSpace(result.FinalMessage) != responseText {
			return fmt.Errorf("response smoke: expected %q, got %q", responseText, result.FinalMessage)
		}
		fmt.Printf("response smoke passed: thread=%s turn=%s response=%q\n", result.ThreadID, result.TurnID, result.FinalMessage)
	}

	if mode == "mutation" || mode == "all" {
		workingDir, err := os.MkdirTemp("", "devbridge-mutation-")
		if err != nil {
			return fmt.Errorf("create mutation directory: %w", err)
		}
		defer os.RemoveAll(workingDir)
		prompt := "Create hello.txt in the current working directory with exact contents (no trailing newline): " + mutationText
		result, err := executeTurn(ctx, client, workingDir, prompt)
		if err != nil {
			return fmt.Errorf("mutation smoke: %w", err)
		}
		contents, err := os.ReadFile(filepath.Join(workingDir, "hello.txt"))
		if err != nil {
			return fmt.Errorf("mutation smoke: read hello.txt: %w", err)
		}
		if string(contents) != mutationText {
			return fmt.Errorf("mutation smoke: hello.txt contains %q", contents)
		}
		fmt.Printf("mutation smoke passed: thread=%s turn=%s file=%q\n", result.ThreadID, result.TurnID, string(contents))
	}
	return nil
}

func piAgentConfig(enabled bool, path, model, thinking string) *piagent.Config {
	if !enabled && path == "" && model == "" && thinking == "" {
		return nil
	}
	return &piagent.Config{PiPath: path, Model: model, ThinkingLevel: thinking}
}

func runServer(ctx context.Context, opened dataroot.DataRoot, logDirectory, listenAddress string, adminTest bool, allowedModels, allowedReasoningEfforts, codingAgentModelConfigs string, piAgent *piagent.Config) error {
	if err := bridgehttp.ValidateLoopbackListen(listenAddress); err != nil {
		return err
	}
	paths := opened.Paths
	runtimeInventory, err := codexruntime.ParseStrict(allowedModels, allowedReasoningEfforts)
	if strings.TrimSpace(codingAgentModelConfigs) != "" {
		runtimeInventory, err = codexruntime.ParseModelConfigsStrict(codingAgentModelConfigs)
	}
	if err != nil {
		return fmt.Errorf("parse Codex runtime inventory: %w", err)
	}
	plane, err := controlplane.OpenWithDataRoot(ctx, controlplane.Config{DataRoot: paths.Root, CodexRuntime: runtimeInventory, AppServerContinuity: os.Getenv("DEVBRIDGE_CODEX_APP_SERVER_CONTINUITY") == "1", PiAgent: piAgent}, opened)
	if err != nil {
		return fmt.Errorf("open control plane: %w", err)
	}
	defer plane.Close()
	bridge, err := bridgehttp.NewWithLogDirectory(plane, adminTest, logDirectory)
	if err != nil {
		return fmt.Errorf("construct HTTP API: %w", err)
	}
	control, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		return fmt.Errorf("construct control facade: %w", err)
	}
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: buildinfo.Version(), Ready: plane.Ready}, plane.AIContext, control)
	if err != nil {
		return fmt.Errorf("construct adapter service: %w", err)
	}
	gpt, err := mcpserver.New(mcpserver.Config{}, service)
	if err != nil {
		return fmt.Errorf("construct GPT adapter: %w", err)
	}
	local, err := localmcp.New(localmcp.Config{}, service)
	if err != nil {
		return fmt.Errorf("construct Local MCP adapter: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/_adapters/gpt/", http.StripPrefix("/_adapters/gpt", gpt.Handler()))
	mux.Handle("/_adapters/local/", http.StripPrefix("/_adapters/local", local.Handler()))
	mux.HandleFunc("GET /_adapters/identity", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"dataRoot":%q}`, paths.Root)
	})
	mux.Handle("/", bridge.Handler())
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	fmt.Printf("DEVBRIDGE_READY http://%s build=%s dataRoot=%s\n", listener.Addr().String(), buildinfo.Version(), paths.Root)
	observability.Info(ctx, "bootstrap", "core.bootstrap.ready", "Core service is listening", map[string]any{"listenAddress": listener.Addr().String(), "dataRoot": paths.Root})
	select {
	case serveErr := <-serveDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownContext)
		serveErr := <-serveDone
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

func safeWorkingDirectory(requested, pattern string) (string, func(), error) {
	if requested != "" {
		absolute, err := filepath.Abs(requested)
		if err != nil {
			return "", nil, fmt.Errorf("resolve working directory: %w", err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return "", nil, fmt.Errorf("inspect working directory: %w", err)
		}
		if !info.IsDir() {
			return "", nil, errors.New("working directory is not a directory")
		}
		return absolute, func() {}, nil
	}
	directory, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", nil, fmt.Errorf("create temporary working directory: %w", err)
	}
	return directory, func() { _ = os.RemoveAll(directory) }, nil
}

// executeTurn is used only by the explicit CLI response/mutation smoke modes.
// Production serve-mode Agent work uses durable Task/owner/Run admission.
func executeTurn(ctx context.Context, client *codex.Client, cwd, prompt string) (codex.TurnResult, error) {
	thread, err := client.StartThread(ctx, codex.ThreadOptions{CWD: cwd})
	if err != nil {
		return codex.TurnResult{}, err
	}
	return client.StartTurn(ctx, codex.TurnOptions{ThreadID: thread.ID, Prompt: prompt})
}
