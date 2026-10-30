// Package mcpserver implements the AI-facing MCP transport adapter.
//
// This package depends only on a narrow typed service port. Dev Bridge domain,
// persistence, and control-plane packages must not depend on this adapter or
// on the MCP SDK.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/buildinfo"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// MCPPath is the Streamable HTTP MCP endpoint.
	MCPPath = "/mcp"
	// HealthPath is the liveness endpoint, separate from the MCP protocol.
	HealthPath = "/healthz"
	// ReadinessPath is the readiness endpoint, separate from the MCP protocol.
	ReadinessPath = "/readyz"

	DefaultListenAddress = "127.0.0.1:8080"
	ServerName           = "devbridge"
	TransportName        = "streamable-http"
)

var (
	// ServerVersion is the version identity of the running Dev Bridge build.
	ServerVersion     = buildinfo.Version()
	ErrAlreadyStarted = errors.New("MCP server already started")
	ErrNotStarted     = errors.New("MCP server not started")
)

const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 15 * time.Second
	defaultWriteTimeout      = 20 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultRequestTimeout    = 15 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
	defaultMaxRequestBytes   = 1 << 20
)

// Config configures the local HTTP adapter. Zero values receive bounded,
// production-safe defaults.
type Config struct {
	// Deprecated: production presentation is now registered unconditionally.
	EnableFeatureAreaPresentation bool

	ListenAddress     string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	RequestTimeout    time.Duration
	ShutdownTimeout   time.Duration
	MaxRequestBytes   int64
}

// BridgeInfoOutput is the structured diagnostic result backed by BridgeInfo.
type BridgeInfoOutput struct {
	BusinessCatalogSHA256     string `json:"businessCatalogSHA256"`
	UnifiedReviewSchemaSHA256 string `json:"unifiedReviewSchemaSHA256"`
	ApplicationName           string `json:"applicationName"`
	ApplicationVersion        string `json:"applicationVersion"`
	Ready                     bool   `json:"ready"`
	Protocol                  string `json:"protocol"`
	ProtocolVersion           string `json:"protocolVersion"`
	ServerName                string `json:"serverName"`
	ServerVersion             string `json:"serverVersion"`
	Transport                 string `json:"transport"`
}

type bridgeInfoInput struct{}

// Server owns an HTTP server and its MCP adapter lifecycle. A Server is
// single-use: call Start for an internally-created listener, or Serve for an
// existing listener, then Shutdown and Wait as needed.
type Server struct {
	config     Config
	httpServer *http.Server

	mu       sync.Mutex
	started  bool
	listener net.Listener
	done     chan struct{}
	serveErr error
}

// New constructs a stateless Streamable HTTP server with deterministic JSON
// responses for ordinary RPCs.
func New(config Config, service BridgeService) (*Server, error) {
	if service == nil {
		return nil, errors.New("bridge service is required")
	}
	config = withDefaults(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	mcpServer := sdkmcp.NewServer(
		&sdkmcp.Implementation{
			Name:        ServerName,
			Title:       "Dev Bridge",
			Description: "AI-facing transport adapter for the Dev Bridge control plane",
			Version:     ServerVersion,
		},
		&sdkmcp.ServerOptions{
			Instructions: ChatGPTConfirmationInstructions,
			Capabilities: &sdkmcp.ServerCapabilities{
				Tools:     &sdkmcp.ToolCapabilities{},
				Resources: &sdkmcp.ResourceCapabilities{},
			},
		},
	)
	mcpServer.AddReceivingMiddleware(toolLoggingMiddleware())
	mcpServer.AddReceivingMiddleware(cardToolVisibilityMiddleware())
	mcpServer.AddReceivingMiddleware(presentationResultMiddleware(service))
	mcpServer.AddReceivingMiddleware(WorkflowGuidanceMiddleware(service))
	RegisterSurfaceBusinessTools(mcpServer, service)
	registerUIWorkflowStateTool(mcpServer, service)
	registerUIDecisionTools(mcpServer, service)
	RegisterSurfacePresentationTools(mcpServer, service)
	registerPanelConfirmationFeed(mcpServer, service)
	RegisterDevelopmentContextCard(mcpServer)
	RegisterFeatureAreaPresentation(mcpServer, service)
	RegisterFeatureAreaDetails(mcpServer, service)
	registerChatGPTDecisionContinuation(mcpServer, service)
	RegisterConfirmationTools(mcpServer, service)

	mcpHandler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return mcpServer },
		&sdkmcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			CrossOriginProtection:        http.NewCrossOriginProtection(),
			MaxRequestBodyBytes:          config.MaxRequestBytes,
			PropagateRequestCancellation: true,
		},
	)

	mux := http.NewServeMux()
	var handler http.Handler = mcpHandler
	if provider, ok := service.(workflowEventProvider); ok {
		handler = workflowEventsHandler(handler, provider.WorkflowEvents(), config.MaxRequestBytes)
	}
	mux.Handle(MCPPath, noStore(http.TimeoutHandler(handler, config.RequestTimeout, "MCP request timed out")))
	mux.Handle(HealthPath, noStore(http.HandlerFunc(healthHandler)))
	mux.Handle(ReadinessPath, noStore(readinessHandler(service, config.RequestTimeout)))

	return &Server{
		config: config,
		httpServer: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: config.ReadHeaderTimeout,
			ReadTimeout:       config.ReadTimeout,
			WriteTimeout:      config.WriteTimeout,
			IdleTimeout:       config.IdleTimeout,
		},
	}, nil
}

// Start creates the configured listener and begins serving in the background.
func (s *Server) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", s.config.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for MCP HTTP server: %w", err)
	}
	if err := s.begin(ctx, listener); err != nil {
		_ = listener.Close()
		return err
	}
	return nil
}

// Serve begins serving on listener and blocks until the server stops.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if listener == nil {
		return errors.New("listener is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.begin(ctx, listener); err != nil {
		return err
	}
	return s.Wait()
}

func (s *Server) begin(ctx context.Context, listener net.Listener) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return ErrAlreadyStarted
	}
	s.started = true
	s.listener = listener
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()

	go s.serve(listener, done)
	go s.stopOnContext(ctx, done)
	return nil
}

func (s *Server) serve(listener net.Listener, done chan struct{}) {
	err := s.httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	s.mu.Lock()
	s.serveErr = err
	close(done)
	s.mu.Unlock()
}

func (s *Server) stopOnContext(ctx context.Context, done <-chan struct{}) {
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		defer cancel()
		_ = s.Shutdown(shutdownCtx)
	case <-done:
	}
}

// Shutdown gracefully drains active requests until ctx expires. On expiry it
// forcibly closes remaining connections so lifecycle goroutines cannot leak.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return nil
	}

	err := s.httpServer.Shutdown(ctx)
	if err == nil {
		return nil
	}
	closeErr := s.httpServer.Close()
	if closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
		return errors.Join(err, closeErr)
	}
	return err
}

// Wait blocks until serving ends and returns any listener/server failure.
func (s *Server) Wait() error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return ErrNotStarted
	}
	done := s.done
	s.mu.Unlock()

	<-done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serveErr
}

// Addr returns the active listener address, or nil before Start/Serve.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Handler exposes the adapter HTTP surface for embedding in the Core server.
// It does not start a listener; callers retain ownership of transport lifecycle.
func (s *Server) Handler() http.Handler { return s.httpServer.Handler }

func withDefaults(config Config) Config {
	if config.ListenAddress == "" {
		config.ListenAddress = DefaultListenAddress
	}
	if config.ReadHeaderTimeout == 0 {
		config.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = defaultReadTimeout
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = defaultWriteTimeout
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = defaultIdleTimeout
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = defaultRequestTimeout
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = defaultShutdownTimeout
	}
	if config.MaxRequestBytes == 0 {
		config.MaxRequestBytes = defaultMaxRequestBytes
	}
	return config
}

func validateConfig(config Config) error {
	if config.MaxRequestBytes < 1 {
		return errors.New("max request bytes must be positive")
	}
	for _, item := range []struct {
		name  string
		value time.Duration
	}{
		{"read header timeout", config.ReadHeaderTimeout},
		{"read timeout", config.ReadTimeout},
		{"write timeout", config.WriteTimeout},
		{"idle timeout", config.IdleTimeout},
		{"request timeout", config.RequestTimeout},
		{"shutdown timeout", config.ShutdownTimeout},
	} {
		if item.value < 0 {
			return fmt.Errorf("%s must not be negative", item.name)
		}
	}
	return nil
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusOK, `{"status":"ok"}`)
}

func readinessHandler(service BridgeService, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), timeout)
		defer cancel()
		info, err := service.BridgeInfo(ctx)
		if err != nil || validateBridgeInfo(info) != nil || !info.Ready {
			writeStatus(w, http.StatusServiceUnavailable, `{"status":"not_ready"}`)
			return
		}
		writeStatus(w, http.StatusOK, `{"status":"ready"}`)
	})
}

func writeStatus(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, request)
	})
}
