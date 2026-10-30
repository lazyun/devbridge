package appserver

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/lazyun/devbridge/internal/codexlaunch"
)

// ResolveExecutable preserves the configured installation. For the official
// npm launcher only, select its own platform package/native vendor executable;
// never silently substitute another installation from PATH.
func ResolveExecutable(configured string) (string, error) {
	selection, e := codexlaunch.Runtime(configured)
	if e != nil {
		return "", e
	}
	path, e := filepath.EvalSymlinks(selection.Executable)
	if e != nil {
		return "", e
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	if len(raw) >= 2 && string(raw[:2]) != "#!" {
		return path, nil
	}
	root := filepath.Dir(filepath.Dir(path))
	var pkg struct {
		Name string `json:"name"`
	}
	data, e := os.ReadFile(filepath.Join(root, "package.json"))
	if e != nil || json.Unmarshal(data, &pkg) != nil || pkg.Name != "@openai/codex" || filepath.Base(path) != "codex.js" {
		return "", errors.New("detached appserver requires native Codex or official npm launcher")
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	platform := map[string]string{"darwin": "apple-darwin", "linux": "unknown-linux-musl"}[runtime.GOOS]
	npmArch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if arch == "" || platform == "" {
		return "", errors.New("unsupported detached platform")
	}
	triple := arch + "-" + platform
	packageName := "codex-" + runtime.GOOS + "-" + npmArch
	candidates := []string{filepath.Join(root, "node_modules", "@openai", packageName, "vendor", triple, "bin", "codex"), filepath.Join(filepath.Dir(root), packageName, "vendor", triple, "bin", "codex"), filepath.Join(root, "vendor", triple, "bin", "codex")}
	for _, candidate := range candidates {
		if codexlaunch.Validate(candidate) == nil {
			return filepath.EvalSymlinks(candidate)
		}
	}
	return "", errors.New("configured npm installation has no native Codex")
}
