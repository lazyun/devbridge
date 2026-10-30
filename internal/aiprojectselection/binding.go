package aiprojectselection

import (
	"errors"
	"strings"
)

// ClientBindingReference is adapter-scoped correlation, never authorization.
// CallerScope comes from the adapter's established caller/service boundary;
// HostReference is optional and SelectionHandle is an opaque exact entry ID.
type ClientBindingReference struct {
	CallerScope     string
	HostReference   string
	SelectionHandle string
}

const (
	BindingBound       = "BOUND"
	BindingUnbound     = "UNBOUND"
	BindingStale       = "STALE"
	BindingUnavailable = "UNAVAILABLE"
)

var (
	ErrBindingConflict    = errors.New("client binding references conflict")
	ErrBindingInvalid     = errors.New("invalid client binding reference")
	ErrBindingUnavailable = errors.New("client binding store unavailable")
)

type BindingResult struct {
	State           string `json:"state"`
	SelectionHandle string `json:"selectionHandle,omitempty"`
	AISessionID     string `json:"aiSessionId,omitempty"`
	ProjectID       string `json:"projectId,omitempty"`
	Version         int64  `json:"version"`
	Source          string `json:"source"`
	Reason          string `json:"reason,omitempty"`
}

func (r ClientBindingReference) Valid() bool {
	return validBindingText(r.CallerScope, 256, false) && validBindingText(r.HostReference, 512, true) && validBindingText(r.SelectionHandle, 128, true)
}

func ValidPresentationRequestID(value string) bool { return validBindingText(value, 128, false) }

func validBindingText(value string, maximum int, optional bool) bool {
	return (optional || value != "") && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}
