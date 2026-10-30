package piagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Resolve finds the pi executable using deterministic, user-local installation
// locations. It supplements the GUI process PATH without starting a login
// shell or sourcing shell profiles.
func Resolve(executable string) string {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		executable = "pi"
	}
	if executable != "pi" {
		if path, err := exec.LookPath(executable); err == nil {
			executable = path
		}
	} else {
		executable = resolvePiExecutableFrom(executable, exec.LookPath, commonPiCandidates())
	}
	if strings.ContainsAny(executable, `/\`) {
		if path, err := filepath.Abs(executable); err == nil {
			return path
		}
	}
	return executable
}

func resolvePiExecutableFrom(executable string, lookPath func(string) (string, error), candidates []string) string {
	executable = strings.TrimSpace(executable)
	if executable != "pi" {
		return executable
	}
	// First check PATH.
	if path, err := lookPath(executable); err == nil {
		return path
	}
	// Then check common installation locations.
	for _, candidate := range candidates {
		if isExecutable(candidate) {
			return candidate
		}
	}
	return executable
}

func commonPiCandidates() []string {
	candidates := []string{}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return candidates
	}

	// NPM global installations.
	candidates = append(candidates,
		filepath.Join(home, ".npm-global", "bin", "pi"),
		filepath.Join(home, ".local", "bin", "pi"),
	)

	// Volta installations.
	candidates = append(candidates,
		filepath.Join(home, ".volta", "bin", "pi"),
	)

	// NVM installations (reverse order for newest first).
	nvm, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "pi"))
	sort.Sort(sort.Reverse(sort.StringSlice(nvm)))
	candidates = append(candidates, nvm...)

	// Platform-specific locations.
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/usr/local/bin/pi",
			"/opt/homebrew/bin/pi",
		)
	} else if runtime.GOOS == "linux" {
		candidates = append(candidates,
			"/usr/local/bin/pi",
			"/usr/bin/pi",
		)
	}

	return candidates
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0111 != 0
}
