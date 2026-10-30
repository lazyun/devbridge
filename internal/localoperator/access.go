// Package localoperator carries access admitted by the loopback control-plane
// transport. It never represents ownership of an AI Session.
package localoperator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

var ErrAccessDenied = errors.New("trusted local operator access required")

type contextKey struct{}
type admission struct{ principal string }

// Access cannot be built from request JSON, a caller-scope header, or a bool.
// Its private admission is created only after transport checks in AdmitHTTP.
type Access struct {
	entry   *admission
	project string
	area    string
	task    string
	action  string
}

func (a Access) PrincipalID() string {
	if a.entry == nil {
		return ""
	}
	return a.entry.principal
}
func (a Access) ProjectID() string { return a.project }
func (a Access) AreaID() string    { return a.area }
func (a Access) TaskID() string    { return a.task }
func (a Access) Action() string    { return a.action }
func (a Access) Check(projectID, taskID, action string) error {
	if a.entry == nil || !identifier(projectID) || a.project != projectID || a.task != taskID || a.action != action {
		return ErrAccessDenied
	}
	return nil
}

func FromContext(ctx context.Context) (Access, bool) {
	a, ok := ctx.Value(contextKey{}).(Access)
	return a, ok && a.entry != nil
}

func BindProject(ctx context.Context, projectID string) (context.Context, error) {
	a, ok := FromContext(ctx)
	if !ok || !identifier(projectID) || a.project != "" && a.project != projectID {
		return ctx, ErrAccessDenied
	}
	a.project = projectID
	return context.WithValue(ctx, contextKey{}, a), nil
}

func BindTask(ctx context.Context, projectID, taskID, action string) (context.Context, error) {
	if !identifier(taskID) || !identifier(action) {
		return ctx, ErrAccessDenied
	}
	ctx, err := BindProject(ctx, projectID)
	if err != nil {
		return ctx, err
	}
	a, _ := FromContext(ctx)
	if a.area != "" || a.task != "" && a.task != taskID || a.action != "" && a.action != action {
		return ctx, ErrAccessDenied
	}
	a.task, a.action = taskID, action
	return context.WithValue(ctx, contextKey{}, a), nil
}

// BindArea keeps FeatureArea identity separate from Task identity for the
// original project-native submission and idle context-reset commands.
func BindArea(ctx context.Context, projectID, areaID, action string) (context.Context, error) {
	if !identifier(areaID) || !identifier(action) {
		return ctx, ErrAccessDenied
	}
	ctx, err := BindProject(ctx, projectID)
	if err != nil {
		return ctx, err
	}
	a, _ := FromContext(ctx)
	if a.task != "" || a.area != "" && a.area != areaID || a.action != "" && a.action != action {
		return ctx, ErrAccessDenied
	}
	a.area, a.action = areaID, action
	return context.WithValue(ctx, contextKey{}, a), nil
}

func (a Access) CheckArea(projectID, areaID, action string) error {
	if a.Check(projectID, "", action) != nil || !identifier(areaID) || a.area != areaID {
		return ErrAccessDenied
	}
	return nil
}

func RequireProject(ctx context.Context, projectID string) error {
	a, ok := FromContext(ctx)
	if !ok || !identifier(projectID) || a.project != projectID {
		return ErrAccessDenied
	}
	return nil
}

// RequestID separates this deployment's operator receipts from AI Session
// requests while preserving the user's stable request identity within an action.
func RequestID(ctx context.Context, projectID, taskID, action, original string) (string, error) {
	a, ok := FromContext(ctx)
	if !ok || a.Check(projectID, taskID, action) != nil || !identifier(original) {
		return "", ErrAccessDenied
	}
	payload, err := json.Marshal([]string{"devbridge.local-operator.request.v1", a.PrincipalID(), projectID, a.area, taskID, action, original})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return "operator_" + hex.EncodeToString(hash[:]), nil
}

func identifier(s string) bool {
	return s != "" && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}

// Ingress is held by the trusted local HTTP server, never by MCP adapters.
// This deployment has one local-operator realm, not per-browser login/RBAC.
type Ingress struct{ entry *admission }

func NewIngress(deployment string) (*Ingress, error) {
	if strings.TrimSpace(deployment) == "" {
		return nil, ErrAccessDenied
	}
	hash := sha256.Sum256([]byte(deployment))
	return &Ingress{entry: &admission{principal: "local_operator_" + hex.EncodeToString(hash[:16])}}, nil
}

// AdmitHTTP requires facts installed by net/http for the actual listener as
// well as the remote connection and browser request protections. Host, Origin,
// client IP, or a missing AI caller scope alone cannot establish this access.
func (i *Ingress) AdmitHTTP(r *http.Request) (context.Context, error) {
	if i == nil || i.entry == nil || r == nil {
		return nil, ErrAccessDenied
	}
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || local == nil {
		return r.Context(), ErrAccessDenied
	}
	localHost, localPort, err := net.SplitHostPort(local.String())
	if err != nil || !loopback(localHost) {
		return r.Context(), ErrAccessDenied
	}
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !loopback(remoteHost) {
		return r.Context(), ErrAccessDenied
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != localPort || !loopback(host) && !strings.EqualFold(host, "localhost") {
		return r.Context(), ErrAccessDenied
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return r.Context(), ErrAccessDenied
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if err != nil || parsed.Scheme != scheme || !strings.EqualFold(parsed.Host, r.Host) || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return r.Context(), ErrAccessDenied
		}
	}
	return context.WithValue(r.Context(), contextKey{}, Access{entry: i.entry}), nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
