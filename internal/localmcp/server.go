package localmcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/observability"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpserver "github.com/lazyun/devbridge/internal/mcpserver"
)

const (
	// MCPPath is the Streamable HTTP MCP endpoint of the Local MCP adapter.
	MCPPath = "/mcp"
	// HealthPath is the liveness endpoint.
	HealthPath = "/healthz"
	// ReadinessPath is the readiness endpoint.
	ReadinessPath = "/readyz"

	// DefaultListenAddress binds loopback only. Docker or remote exposure is
	// an explicit operator decision made through -listen plus
	// -allow-non-loopback, never a default.
	DefaultListenAddress = "127.0.0.1:8090"

	ServerName = "devbridge-local-mcp"
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

// Config configures the local Local MCP adapter transport. Zero values
// receive bounded, production-safe defaults.
type Config struct {
	ListenAddress     string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	RequestTimeout    time.Duration
	ShutdownTimeout   time.Duration
	MaxRequestBytes   int64
}

// Server owns the Local MCP adapter's HTTP server and MCP lifecycle.
type Server struct {
	config     Config
	httpServer *http.Server
	listener   net.Listener

	startedMu sync.Mutex
	started   bool
	stopped   bool
	done      chan struct{}
	doneErr   error
}

// New builds the Local MCP adapter server over the shared Bridge application
// services. The service is the same port the GPT adapter uses
// (mcpserver.BridgeService, implemented by mcpserver.StaticService); this
// package adds only the Local MCP adapter's own registration and presentation.
func New(config Config, service mcpserver.BridgeService) (*Server, error) {
	if service == nil {
		return nil, errors.New("bridge service is required")
	}
	config = withDefaults(config)

	mcpServer := sdkmcp.NewServer(
		&sdkmcp.Implementation{
			Name:        ServerName,
			Title:       "Dev Bridge Local MCP Adapter",
			Description: "Local MCP adapter for the Dev Bridge control plane",
			Version:     mcpserver.ServerVersion,
		},
		&sdkmcp.ServerOptions{
			Instructions: LocalMCPServerInstructions,
			Capabilities: &sdkmcp.ServerCapabilities{
				Tools: &sdkmcp.ToolCapabilities{},
			},
		},
	)
	mcpServer.AddReceivingMiddleware(toolLoggingMiddleware())
	mcpServer.AddReceivingMiddleware(mcpserver.WorkflowGuidanceMiddleware(service))
	mcpServer.AddReceivingMiddleware(localSurfaceResultMiddleware())
	mcpserver.RegisterSurfaceBusinessTools(mcpServer, service)

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
	mux.Handle(MCPPath, noStore(http.TimeoutHandler(mcpHandler, config.RequestTimeout, "MCP request timed out")))
	mux.Handle(HealthPath, noStore(http.HandlerFunc(healthHandler)))
	mux.Handle(ReadinessPath, noStore(readinessHandler(service)))

	return &Server{
		config: config,
		httpServer: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: config.ReadHeaderTimeout,
			ReadTimeout:       config.ReadTimeout,
			WriteTimeout:      config.WriteTimeout,
			IdleTimeout:       config.IdleTimeout,
		},
		done: make(chan struct{}),
	}, nil
}

// Start creates the configured listener and begins serving in the background.
func (s *Server) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	s.startedMu.Lock()
	if s.started || s.stopped {
		s.startedMu.Unlock()
		return errors.New("Local MCP adapter server already started or stopped")
	}
	s.started = true
	s.startedMu.Unlock()
	listener, err := net.Listen("tcp", s.config.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for Local MCP adapter HTTP server: %w", err)
	}
	s.listener = listener
	go func() {
		defer close(s.done)
		if serveErr := s.httpServer.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			s.doneErr = serveErr
		}
	}()
	return nil
}

// Addr returns the bound listener address. It is valid after Start.
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Handler exposes the adapter HTTP surface for embedding in the Core server.
// It does not start a listener; callers retain ownership of transport lifecycle.
func (s *Server) Handler() http.Handler { return s.httpServer.Handler }

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.startedMu.Lock()
	if !s.started || s.stopped {
		s.startedMu.Unlock()
		return nil
	}
	s.stopped = true
	s.startedMu.Unlock()
	return s.httpServer.Shutdown(ctx)
}

// Wait blocks until the HTTP server stops.
func (s *Server) Wait() error {
	<-s.done
	return s.doneErr
}

func withDefaults(config Config) Config {
	if config.ListenAddress == "" {
		config.ListenAddress = DefaultListenAddress
	}
	if config.ReadHeaderTimeout <= 0 {
		config.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = defaultReadTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = defaultWriteTimeout
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = defaultIdleTimeout
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = defaultRequestTimeout
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = defaultShutdownTimeout
	}
	if config.MaxRequestBytes <= 0 {
		config.MaxRequestBytes = defaultMaxRequestBytes
	}
	return config
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("live"))
}

func readinessHandler(service mcpserver.BridgeService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), defaultRequestTimeout)
		defer cancel()
		info, err := service.BridgeInfo(ctx)
		if err != nil || !info.Ready {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"starting"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}
}

// toolLoggingMiddleware keeps the adapter's bounded structured diagnostics
// without logging any prompt or user content.
func toolLoggingMiddleware() sdkmcp.Middleware {
	return func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
			result, err := next(ctx, method, request)
			logCtx := observability.WithContext(ctx, observability.Correlation{})
			if err != nil {
				observability.Info(logCtx, "localmcp", "localmcp.tool.failed", "Local MCP tool call failed", map[string]any{"method": method, "errorSummary": observability.Fingerprint(err.Error())})
				return result, err
			}
			observability.Info(logCtx, "localmcp", "localmcp.tool.completed", "Local MCP tool call completed", map[string]any{"method": method})
			return result, nil
		}
	}
}
