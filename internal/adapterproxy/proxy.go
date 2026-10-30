// Package adapterproxy serves a public MCP adapter endpoint by forwarding it
// to the corresponding handler embedded in the single Core process.
package adapterproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

const IdentityPath = "/_adapters/identity"

type Config struct {
	CoreURL     string
	DataRoot    string
	AdapterPath string
	HTTPClient  *http.Client
}

type identity struct {
	DataRoot string `json:"dataRoot"`
}

// Proxy forwards one adapter's /mcp, /healthz, and /readyz paths to Core.
// Construction verifies that Core is local, available, and owns DataRoot.
type Proxy struct{ handler http.Handler }

func New(ctx context.Context, config Config) (*Proxy, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	target, err := parseCoreURL(config.CoreURL)
	if err != nil {
		return nil, err
	}
	if config.DataRoot == "" {
		return nil, errors.New("data root is required")
	}
	if !strings.HasPrefix(config.AdapterPath, "/_adapters/") {
		return nil, errors.New("adapter path must be a Core adapter path")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	identityURL := *target
	identityURL.Path = IdentityPath
	identityURL.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, identityURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("construct Core identity request: %w", err)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Core unavailable at %s: %w", target.String(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Core unavailable at %s: identity returned HTTP %d", target.String(), response.StatusCode)
	}
	var got identity
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		return nil, fmt.Errorf("read Core identity: %w", err)
	}
	if got.DataRoot != config.DataRoot {
		return nil, fmt.Errorf("Core data root mismatch: Core owns %q, adapter expects %q", got.DataRoot, config.DataRoot)
	}
	upstream := *target
	upstream.Path = strings.TrimSuffix(config.AdapterPath, "/")
	proxy := httputil.NewSingleHostReverseProxy(&upstream)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "Core unavailable: "+err.Error(), http.StatusBadGateway)
	}
	return &Proxy{handler: proxy}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) { p.handler.ServeHTTP(w, r) }

func parseCoreURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("-core-url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid -core-url: %w", err)
	}
	if u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("-core-url must be a bare loopback http URL")
	}
	host := u.Hostname()
	if !isLoopbackHost(host) {
		return nil, errors.New("-core-url must use a loopback host")
	}
	return u, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
