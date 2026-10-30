// Command devbridge-mcp exposes Core's GPT MCP handler on a dedicated local port.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lazyun/devbridge/internal/adapterproxy"
	"github.com/lazyun/devbridge/internal/dataroot"
	"github.com/lazyun/devbridge/internal/mcpserver"
)

func main() {
	listen := flag.String("listen", mcpserver.DefaultListenAddress, "HTTP listen address")
	allowNonLoopback := flag.Bool("allow-non-loopback", false, "allow an unauthenticated non-loopback listener")
	dataRoot := flag.String("data-root", "", "Dev Bridge data directory used by Core")
	coreURL := flag.String("core-url", "", "required loopback Core HTTP URL")
	flag.Parse()
	if err := validateListenAddress(*listen, *allowNonLoopback); err != nil {
		fmt.Fprintln(os.Stderr, "devbridge-mcp:", err)
		os.Exit(2)
	}
	root, err := deriveDataRoot(*dataRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devbridge-mcp:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	proxy, err := adapterproxy.New(ctx, adapterproxy.Config{CoreURL: *coreURL, DataRoot: root, AdapterPath: "/_adapters/gpt"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "devbridge-mcp:", err)
		os.Exit(1)
	}
	if err := serve(ctx, *listen, proxy); err != nil {
		fmt.Fprintln(os.Stderr, "devbridge-mcp:", err)
		os.Exit(1)
	}
}

func deriveDataRoot(requested string) (string, error) {
	if requested == "" {
		var err error
		requested, err = dataroot.DefaultRoot()
		if err != nil {
			return "", err
		}
	}
	paths, err := dataroot.Derive(requested)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(paths.Root)
	if err != nil {
		return "", fmt.Errorf("resolve Core data root: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func serve(ctx context.Context, address string, handler http.Handler) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Printf("Dev Bridge MCP proxy listening at http://%s%s\n", listener.Addr(), mcpserver.MCPPath)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := server.Shutdown(shutdown)
		served := <-done
		if errors.Is(served, http.ErrServerClosed) {
			served = nil
		}
		return errors.Join(err, served)
	}
}

func validateListenAddress(address string, allowNonLoopback bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid -listen address: %w", err)
	}
	if allowNonLoopback || host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return errors.New("non-loopback listener requires -allow-non-loopback")
}
