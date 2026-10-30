//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package reviewaccess

import (
	"errors"
	"os"
)

type rootHandle struct{}

func openRootHandle(string) (*rootHandle, error) {
	return nil, errors.New("race-safe Repository inspection is unavailable on this platform")
}

func (*rootHandle) close() error { return nil }
func (*rootHandle) path() string { return "" }
func (*rootHandle) open(string, pathKind, func(string, string)) (*os.File, string, error) {
	return nil, "", errors.New("race-safe Repository inspection is unavailable on this platform")
}
