// Package codexlaunch owns executable selection for Desktop and every Core Codex runtime.
package codexlaunch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Resolve supplements the GUI process PATH with deterministic,
// user-local installation locations. It intentionally does not start a login
// shell or source shell profiles: doing so can execute arbitrary interactive
// startup code and can hang application startup.
func Resolve(executable string) string {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		executable = "codex"
	}
	if executable != "codex" {
		if path, err := exec.LookPath(executable); err == nil {
			executable = path
		}
	} else {
		executable = resolveCodexExecutableFrom(executable, exec.LookPath, commonCodexCandidates())
	}
	if strings.ContainsAny(executable, `/\`) {
		if path, err := filepath.Abs(executable); err == nil {
			return path
		}
	}
	return executable
}

func resolveCodexExecutableFrom(executable string, lookPath func(string) (string, error), candidates []string) string {
	executable = strings.TrimSpace(executable)
	if executable != "codex" {
		return executable
	}
	if path, err := lookPath(executable); err == nil {
		return path
	}
	for _, candidate := range candidates {
		if isExecutable(candidate) {
			return candidate
		}
	}
	return executable
}

func commonCodexCandidates() []string {
	candidates := []string{}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/Applications/ChatGPT.app/Contents/Resources/codex")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return candidates
	}
	candidates = append(candidates,
		filepath.Join(home, ".local", "bin", "codex"),
		filepath.Join(home, ".codex", "bin", "codex"),
		filepath.Join(home, ".volta", "bin", "codex"),
	)
	// Keep the existing deterministic reverse directory-name order for NVM
	// discovery; no shell profile or active-version command is evaluated.
	nvm, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "codex"))
	sort.Sort(sort.Reverse(sort.StringSlice(nvm)))
	return append(candidates, nvm...)
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0111 != 0
}

func Environment(env []string, executable string) []string {
	if !filepath.IsAbs(executable) {
		return env
	}
	dir := filepath.Dir(executable)
	result := append([]string(nil), env...)
	for index, entry := range result {
		if path, ok := strings.CutPrefix(entry, "PATH="); ok {
			result[index] = "PATH=" + dir + string(os.PathListSeparator) + path
			return result
		}
	}
	return append(result, "PATH="+dir)
}

// Validate never substitutes another executable for a broken explicit selection.
func Validate(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("CODEX_EXECUTABLE_INVALID: expected absolute executable, got %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("CODEX_EXECUTABLE_MISSING: %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return fmt.Errorf("CODEX_EXECUTABLE_NOT_EXECUTABLE: %q", path)
	}
	return nil
}
