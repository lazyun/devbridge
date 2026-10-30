package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	runtimecontrol "github.com/lazyun/devbridge/desktop/internal/runtime"
)

const desktopEmbedHeader = "X-Devbridge-Desktop-Embed"

// consoleEmbedMiddleware serves the existing Core UI inside the native Wails
// origin. It is not a listener, CORS exception, alternate Core or runtime owner.
// Wails v2.10.2 uses wails://wails on macOS/Linux and http://wails.localhost
// on Windows. API access requires an unguessable process-local document token,
// never a frontend assertion that it is a local operator.
func (a *App) consoleEmbedMiddleware(next http.Handler) http.Handler {
	secret := make([]byte, 32)
	_, secretErr := rand.Read(secret)
	token := hex.EncodeToString(secret)
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConnsPerHost:   8,
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		isAPI := strings.HasPrefix(path, "/ui/api/") || strings.HasPrefix(path, "/v1/")
		isUI := strings.HasPrefix(path, "/ui/")
		if !isAPI && !isUI && path != "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		deny := func(code int, message string) { http.Error(w, message, code) }
		if secretErr != nil || !desktopEmbedHost(r.Host) || !desktopEmbedPath(r.URL) {
			deny(http.StatusForbidden, "Desktop embed access denied")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "null" && origin != "wails://wails" && origin != "http://wails.localhost" {
			deny(http.StatusForbidden, "Desktop embed origin denied")
			return
		}
		// Opaque custom-scheme origins are admitted only with the token. An
		// external document cannot read this same-origin, non-CORS HTML response.
		if isAPI && subtle.ConstantTimeCompare([]byte(r.Header.Get(desktopEmbedHeader)), []byte(token)) != 1 {
			deny(http.StatusForbidden, "Desktop document authorization required")
			return
		}
		if !desktopEmbedMethod(r.Method, path) {
			deny(http.StatusMethodNotAllowed, "Unsupported embedded Console method")
			return
		}
		a.mu.RLock()
		supervisor, appCtx := a.supervisor, a.ctx
		a.mu.RUnlock()
		if supervisor == nil {
			deny(http.StatusServiceUnavailable, "Core runtime is unavailable")
			return
		}
		// Snapshot identifies the endpoint actually managed/attached, rather than
		// settings that may have changed while waiting for a restart.
		core := supervisor.Snapshot().Core
		if (core.State != runtimecontrol.Running && core.State != runtimecontrol.External) || (core.Ownership != runtimecontrol.Managed && core.Ownership != runtimecontrol.Attached) {
			deny(http.StatusServiceUnavailable, "Core is not attached or running")
			return
		}
		base, err := url.Parse(core.Endpoint)
		if err != nil || base.Scheme != "http" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" || base.Port() == "" || !net.ParseIP(base.Hostname()).IsLoopback() {
			deny(http.StatusServiceUnavailable, "Explicit loopback Core endpoint required")
			return
		}
		isStream := strings.HasSuffix(path, "/session/events")
		timeout := 20 * time.Second
		if isStream {
			timeout = 125 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if appCtx != nil {
			stop := context.AfterFunc(appCtx, cancel)
			defer stop()
		}
		var body io.Reader
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
			if err != nil {
				deny(http.StatusRequestEntityTooLarge, "Embedded request exceeds limit")
				return
			}
			body = bytes.NewReader(data)
		}
		base.Path, base.RawPath, base.RawQuery = path, "", r.URL.RawQuery
		request, err := http.NewRequestWithContext(ctx, r.Method, base.String(), body)
		if err != nil {
			deny(http.StatusBadRequest, "Invalid embedded request")
			return
		}
		// Rebuild native loopback transport facts. Never forward caller Cookie,
		// Authorization, Origin, Host, X-Forwarded-* or the document token to Core.
		request.Header.Set("Accept", "application/json")
		if isStream {
			request.Header.Set("Accept", "text/event-stream")
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(request)
		if err != nil {
			deny(http.StatusBadGateway, "Embedded Core connection unavailable")
			return
		}
		defer response.Body.Close()
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			deny(http.StatusBadGateway, "Embedded Core redirect refused")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'self'")
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		if isUI && !isAPI && (path == "/ui/" || path == "/ui/index.html") && response.StatusCode == http.StatusOK {
			data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
			if err != nil || len(data) > 2<<20 {
				deny(http.StatusBadGateway, "Embedded document unavailable")
				return
			}
			meta := []byte(`<meta name="devbridge-desktop-embed" content="` + token + `">`)
			data = bytes.Replace(data, []byte("<head>"), append([]byte("<head>"), meta...), 1)
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(data)
			return
		}
		w.WriteHeader(response.StatusCode)
		if isStream {
			// Stream bounded chunks; never buffer the full session in the native
			// bridge. Closing the iframe cancels this request, not its Bridge Run.
			flusher, canFlush := w.(http.Flusher)
			// Pinned Wails WebKit Write calls didReceiveData immediately and
			// deliberately has no http.Flusher; net/http transports do.
			if canFlush {
				flusher.Flush()
			}
			buffer := make([]byte, 32<<10)
			for {
				n, err := response.Body.Read(buffer)
				if n > 0 {
					if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
						return
					}
					if canFlush {
						flusher.Flush()
					}
				}
				if err != nil {
					return
				}
			}
		}
		_, _ = io.Copy(w, io.LimitReader(response.Body, 32<<20))
	})
}

func desktopEmbedHost(host string) bool {
	return host == "wails" || host == "wails.localhost"
}

// The browser can preserve encoded identifier characters such as ':'; only
// separators and traversal segments are rejected before forwarding.
func desktopEmbedPath(target *url.URL) bool {
	if strings.Contains(target.Path, "\\") {
		return false
	}
	escaped := strings.ToLower(target.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return false
	}
	for _, segment := range strings.Split(target.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func desktopEmbedMethod(method, path string) bool {
	if method == http.MethodGet {
		return true
	}
	if method == http.MethodPost {
		return strings.HasPrefix(path, "/ui/api/") || path == "/v1/project-selection" || path == "/v1/development-branch-selection"
	}
	return method == http.MethodPut && path == "/v1/runtime/codex-inventory"
}
