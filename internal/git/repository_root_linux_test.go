package git

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Linux permits these filename bytes. Check actual Git's output before testing
// the client so that a fixture or output-framing assumption cannot hide a bug.
func TestRepositoryRootPreservesFilenameBytes(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	version := repositoryRootGitOutput(t, gitPath, t.TempDir(), "--version")
	t.Logf("actual Git executable=%q version=%q", gitPath, version)
	cases := []struct {
		name string
		base string
	}{
		{"ordinary", "repository"},
		{"leading_space", " repository"},
		{"internal_whitespace", "repo \t\n name"},
		{"trailing_spaces", "repository  "},
		{"trailing_tab", "repository\t"},
		{"trailing_newline", "repository\n"},
		{"trailing_newlines", "repository\n\n"},
		{"trailing_carriage_return", "repository\r"},
		{"trailing_crlf", "repository\r\n"},
		{"trailing_nbsp", "repository\u00a0"},
		{"trailing_ideographic_space", "repository\u3000"},
		{"trailing_mixed_whitespace", "repository \t\n\r\u00a0\u3000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", fixture)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(fixture, "config"))
			t.Setenv("DEVBRIDGE_TEST_GIT_EXECUTABLE", gitPath)
			// The client discards inherited GIT_* variables. This launcher
			// disables external config after that filtering, then runs Git.
			launcher := filepath.Join(fixture, "isolated-git")
			script := "#!/bin/sh\n" +
				"[ \"$#\" -eq 2 ] && [ \"$1\" = rev-parse ] && [ \"$2\" = --show-toplevel ] || exit 99\n" +
				"export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null\n" +
				"exec \"$DEVBRIDGE_TEST_GIT_EXECUTABLE\" \"$@\"\n"
			if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(fixture, tc.base)
			selected := filepath.Join(repo, "nested", "selected \t")
			if err := os.MkdirAll(selected, 0o700); err != nil {
				t.Fatal(err)
			}
			// This is an empty, unborn repository inside this test's private
			// temporary directory. No index or commit operations are needed.
			repositoryRootGitOutput(t, gitPath, repo, "init", "--quiet", "--template=", "--initial-branch=main")
			for _, dir := range []string{repo, selected} {
				raw := repositoryRootGitOutput(t, gitPath, dir, "rev-parse", "--show-toplevel")
				wantRaw := []byte(repo + "\n")
				if !bytes.Equal(raw, wantRaw) {
					t.Fatalf("actual Git output = %q (%x), want %q (%x)", raw, raw, wantRaw, wantRaw)
				}
				t.Logf("actual Git selected=%q stdout=%q hex=%x", dir, raw, raw)
				got, err := NewClient(Config{Executable: launcher}).RepositoryRoot(context.Background(), dir)
				if err != nil || got != repo {
					t.Errorf("RepositoryRoot(%q) = %q, %v; want %q", dir, got, err, repo)
				}
			}
		})
	}
}

func repositoryRootGitOutput(t *testing.T, executable, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(executable, args...)
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %q in %q: %v; stderr=%q", args, dir, err, stderr.String())
	}
	return out
}

func TestRepositoryRootValidatesAbsoluteOutput(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "git-output")
	script := "#!/bin/sh\n" +
		"[ \"$#\" -eq 2 ] && [ \"$1\" = rev-parse ] && [ \"$2\" = --show-toplevel ] || exit 99\n" +
		"printf '%s' \"$DEVBRIDGE_TEST_ROOT_OUTPUT\"\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"", "\n", "relative", "relative\n", " \t\n"} {
		t.Run("output="+output, func(t *testing.T) {
			t.Setenv("DEVBRIDGE_TEST_ROOT_OUTPUT", output)
			got, err := NewClient(Config{Executable: executable}).RepositoryRoot(context.Background(), dir)
			if got != "" || err == nil || err.Error() != "Git returned an invalid repository root" {
				t.Fatalf("RepositoryRoot output %q = %q, %v; want invalid root error", output, got, err)
			}
		})
	}
}
