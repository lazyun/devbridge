package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Only ignored output paths without protected source may be writable.
// End-of-run hashes alone would allow source edits followed by restoration.
func prepareVerificationOutputs(ctx context.Context, root string, requested []string) ([]string, error) {
	defaults := []string{"dist", "build", "out", "coverage", "target", "node_modules", ".cache", ".pytest_cache", ".vitest", ".vite", ".next", ".nuxt", ".dart_tool", "frontend/dist", "frontend/node_modules", "frontend/coverage", "frontend/.vite", "frontend/.cache", "frontend/.next", "frontend/build"}
	// TypeScript incremental builds write these exact cache files beside config.
	// They must be ignored and untracked; never grant their containing source directory.
	files := map[string]bool{}
	for _, prefix := range []string{"", "frontend/"} {
		for _, name := range []string{"tsconfig.tsbuildinfo", "tsconfig.app.tsbuildinfo", "tsconfig.node.tsbuildinfo"} {
			files[prefix+name] = true
		}
	}
	candidates := map[string]bool{}
	for _, path := range defaults {
		candidates[path] = false
	}
	for path := range files {
		candidates[path] = false
	}
	for _, path := range requested {
		candidates[path] = true
	}
	paths := make([]string, 0, len(candidates))
	for path := range candidates {
		if path == "" || path == "." || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == ".." || strings.HasPrefix(path, "../") || strings.ContainsAny(path, "\\\x00") {
			return nil, errors.New("TASK_PERMISSION_OUTPUT_PATH_INVALID")
		}
		for _, part := range strings.Split(path, "/") {
			if part == ".git" || part == ".codex" || part == ".agents" {
				return nil, errors.New("TASK_PERMISSION_OUTPUT_METADATA_FORBIDDEN")
			}
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	git := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0")
		return cmd
	}
	protected, err := git("ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, err
	}
	var input bytes.Buffer
	for _, path := range paths {
		if files[path] {
			input.WriteString(path + "\x00")
		} else {
			input.WriteString(path + "/\x00")
		}
	}
	cmd := git("check-ignore", "--no-index", "-z", "--stdin")
	cmd.Stdin = &input
	ignored, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, path := range bytes.Split(ignored, []byte{0}) {
		allowed[strings.TrimSuffix(string(path), "/")] = true
	}
	var result []string
	for _, path := range paths {
		safe := allowed[path]
		for _, name := range bytes.Split(protected, []byte{0}) {
			if string(name) == path || strings.HasPrefix(string(name), path+"/") {
				safe = false
				break
			}
		}
		// Ancestors must be directories; known cache-file leaves must be regular files.
		target := filepath.Join(root, filepath.FromSlash(path))
		for candidate := target; candidate != root; candidate = filepath.Dir(candidate) {
			info, e := os.Lstat(candidate)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return nil, e
			}
			validKind := info.IsDir()
			if files[path] && candidate == target {
				validKind = info.Mode().IsRegular()
			}
			if !validKind || info.Mode()&os.ModeSymlink != 0 {
				safe = false
				break
			}
		}
		if !safe {
			if candidates[path] {
				return nil, fmt.Errorf("TASK_PERMISSION_UNSAFE_VERIFICATION_OUTPUT: %s", path)
			}
			continue
		}
		directory := target
		if files[path] {
			directory = filepath.Dir(target)
		}
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, err
		}
		result = append(result, path)
	}
	return result, nil
}
