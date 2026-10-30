// Package execresolve centralizes Desktop runtime executable discovery for
// devbridge, devbridge-mcp, and devbridge-local-mcp. Resolution is derived
// from the running Desktop executable location, never from the current
// working directory, so a packaged application works under Finder launch.
package execresolve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Default binary names shipped in the root build/bin directory next to the
// packaged Desktop application bundle.
const (
	CoreBinary     = "devbridge"
	MCPBinary      = "devbridge-mcp"
	LocalMCPBinary = "devbridge-local-mcp"
)

// Selection names one supervised runtime executable.
type Selection string

const (
	Core     Selection = "core"
	MCP      Selection = "mcp"
	LocalMCP Selection = "local-mcp"
)

func (s Selection) defaultName() string {
	switch s {
	case Core:
		return CoreBinary
	case MCP:
		return MCPBinary
	case LocalMCP:
		return LocalMCPBinary
	}
	return string(s)
}

// DefaultName returns the canonical binary filename for a selection.
func DefaultName(selection Selection) string { return selection.defaultName() }

// NotExecutableError reports an adjacent build candidate that exists but
// cannot be executed. This indicates a broken installation, so it fails fast
// instead of silently falling back to an unrelated PATH binary.
type NotExecutableError struct {
	Path string
}

func (e *NotExecutableError) Error() string {
	return fmt.Sprintf("runtime executable %s exists but is not executable; the adjacent build is broken", e.Path)
}

// NotFoundError reports that no configured, adjacent, or PATH candidate
// resolved for a runtime executable.
type NotFoundError struct {
	Selection Selection
	Name      string
	Detail    string
}

func (e *NotFoundError) Error() string {
	message := fmt.Sprintf("%s executable %s not found", e.Selection, e.Name)
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

// Options configure resolution. ExecutablePath and Env exist for tests.
type Options struct {
	// ExecutablePath overrides the running Desktop executable location.
	ExecutablePath string
	// Env overrides the process environment for PATH lookup.
	Env []string
	// LookPath replaces PATH probing entirely when non-nil.
	LookPath func(env []string, name string) (string, bool)
}

// Resolve returns the executable path for a selection following precedence:
// the default bare name (or an empty configuration), an explicit configured
// path, then bundle/build-adjacent bin/<name>, repository development build
// bin/<name>, and finally PATH.
// The source string describes where the executable was found ("configured",
// "adjacent build", "bundle", "dev build", or "PATH") and is meant for
// diagnostics logs. When nothing resolves, the returned path is the bare
// name and the error is a *NotFoundError, so callers can still attempt a
// spawn and surface exec's own failure while treating the miss as
// non-fatal.
func Resolve(selection Selection, configured string, options Options) (path, source string, err error) {
	name := selection.defaultName()
	configured = strings.TrimSpace(configured)
	env := options.Env
	if env == nil {
		env = os.Environ()
	}
	switch {
	case configured == "" || configured == name:
		// The default bare name (shipped in persisted settings) and an empty
		// configuration both mean "the standard build layout": run the
		// adjacent/dev/PATH discovery chain below so a packaged application
		// finds build/bin next to its bundle even under Finder launch.
	case filepath.IsAbs(configured) || strings.ContainsRune(configured, os.PathSeparator):
		// An explicit user path is authoritative.
		return configured, "configured", nil
	default:
		// Bare custom names (e.g. "custom-devbridge") keep PATH semantics so
		// existing developer overrides keep working.
		if path, ok := lookPath(env, configured, options.LookPath); ok {
			return path, "PATH", nil
		}
		return configured, "PATH", nil
	}

	candidate, source, adjacentErr := adjacentCandidate(name, options.ExecutablePath)
	if adjacentErr != nil {
		return "", "", adjacentErr
	}
	if candidate != "" {
		return candidate, source, nil
	}

	if path, ok := lookPath(env, name, options.LookPath); ok {
		return path, "PATH", nil
	}
	return name, "PATH", &NotFoundError{Selection: selection, Name: name}
}

// adjacentCandidate probes bin/<name> relative to each search root derived
// from the Desktop executable location. It returns the first executable
// candidate, or a nil path with nil error when nothing exists. A
// non-executable adjacent file is a broken installation and is reported as
// NotExecutableError instead of silently falling back.
func adjacentCandidate(name, executablePath string) (string, string, error) {
	for _, dir := range searchRoots(executablePath) {
		candidate := filepath.Join(dir, "bin", name)
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		if info.Mode()&0111 == 0 {
			return "", "", &NotExecutableError{Path: candidate}
		}
		return candidate, rootSource(dir), nil
	}
	return "", "", nil
}

// rootSource labels where an adjacent candidate was found for diagnostics.
func rootSource(root string) string {
	switch {
	case strings.HasSuffix(root, ".app"):
		return "bundle"
	case isRepositoryRoot(root):
		return "dev build"
	default:
		return "adjacent build"
	}
}

func lookPath(env []string, name string, probe func([]string, string) (string, bool)) (string, bool) {
	if probe != nil {
		return probe(env, name)
	}
	return lookPathInEnv(env, name)
}

// lookPathInEnv mirrors exec.LookPath but against an explicit environment so
// tests can simulate Finder-like minimal PATH without mutating process state.
func lookPathInEnv(env []string, name string) (string, bool) {
	if strings.ContainsRune(name, '/') {
		if findExecutable(name) {
			return name, true
		}
		return "", false
	}
	for _, dir := range strings.Split(envPath(env), string(os.PathListSeparator)) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		if findExecutable(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func envPath(env []string) string {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			return value
		}
	}
	return ""
}

func findExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Mode()&0111 != 0
}

// searchRoots returns the deterministic build/install root candidates derived
// from the running Desktop executable location: the macOS application bundle
// parent when packaged, the executable directory for raw development builds,
// and the repository root for wails dev layouts.
func searchRoots(executablePath string) []string {
	if executablePath == "" {
		resolved, err := os.Executable()
		if err != nil {
			return nil
		}
		executablePath = resolved
	}
	// Resolve symlink context without mutating the filesystem: a canonical
	// executable path keeps valid symlinked installs working even when leaf
	// directories do not exist yet.
	if resolvedDir, ok := resolveExistingDir(filepath.Dir(executablePath)); ok {
		executablePath = filepath.Join(resolvedDir, filepath.Base(executablePath))
	}
	runDir := filepath.Dir(executablePath)
	roots := []string{}
	add := func(root string) {
		if root != "" {
			roots = append(roots, root)
		}
	}
	if root, ok := bundleRoot(executablePath); ok {
		add(root)
	}
	add(runDir)
	add(devRoot(runDir))
	return roots
}

// bundleRoot detects the standard Wails macOS bundle executable layout
// <root>/<App>.app/Contents/MacOS/<executable> and returns <root>. It never
// returns a location inside the bundle, because the sibling bin directory
// lives next to the .app, not inside it.
func bundleRoot(executablePath string) (string, bool) {
	macosDir := filepath.Dir(executablePath)
	if filepath.Base(macosDir) != "MacOS" {
		return "", false
	}
	contents := filepath.Dir(macosDir)
	if filepath.Base(contents) != "Contents" {
		return "", false
	}
	bundle := filepath.Dir(contents)
	if !strings.HasSuffix(bundle, ".app") {
		return "", false
	}
	return filepath.Dir(bundle), true
}

// resolveExistingDir canonicalizes a directory that may not exist by
// resolving the deepest existing ancestor and rejoining the remainder.
func resolveExistingDir(dir string) (string, bool) {
	dir = filepath.Clean(dir)
	suffix := ""
	for {
		if _, err := os.Stat(dir); err == nil {
			resolved, err := filepath.EvalSymlinks(dir)
			if err != nil {
				return "", false
			}
			if suffix == "" {
				return resolved, true
			}
			return filepath.Join(resolved, suffix), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		suffix = filepath.Join(filepath.Base(dir), suffix)
		dir = parent
	}
}

// devRoot walks upward from the executable directory looking for a Go module
// root with a checked-out Desktop source layout, which covers `wails dev` and
// raw `go run` development executables living in temporary build directories.
func devRoot(from string) string {
	dir := from
	for {
		if isRepositoryRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isRepositoryRoot(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil || info.IsDir() {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "desktop", "main.go"))
	if err != nil || info.IsDir() {
		return false
	}
	return true
}
