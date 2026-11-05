package git

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseNameStatusPreservesPathBytes(t *testing.T) {
	paths := []struct {
		name string
		path string
	}{
		{"leading_space", " leading.go"},
		{"trailing_space", "trailing.go "},
		{"both_spaces", " both.go "},
		{"tabs", "\ttab.go\t"},
		{"newlines", "\nline.go\r\n"},
		{"unicode_spaces", "\u0085\u00a0\u2003世界.go\u3000"},
		{"whitespace_only", " \t\n\u00a0"},
		{"embedded_whitespace", "dir/a b\tc\nd.go"},
		{"literal_quotes_backslash", "\"quoted\\name.go\""},
		{"non_utf8", " \xffname.go\xfe "},
	}
	statuses := []struct {
		status   string
		change   ChangeType
		twoPaths bool
	}{
		{"A", ChangeAdded, false},
		{"M", ChangeModified, false},
		{"M75", ChangeModified, false},
		{"D", ChangeDeleted, false},
		{"T", ChangeModified, false},
		{"X", ChangeModified, false},
		{"R100", ChangeRenamed, true},
		{"C75", ChangeAdded, true},
	}
	for _, status := range statuses {
		for _, path := range paths {
			t.Run(status.status+"/"+path.name, func(t *testing.T) {
				fields := []string{status.status, path.path}
				want := FileChange{Change: status.change, Path: path.path}
				if status.twoPaths {
					// Use different old/new byte strings to catch accidental swapping.
					newPath := path.path + " new\t\u3000"
					fields = append(fields, newPath)
					want.Path = newPath
					want.OldPath = path.path
				}
				out := []byte(strings.Join(fields, "\x00") + "\x00")
				if got := parseNameStatus(out); !reflect.DeepEqual(got, []FileChange{want}) {
					t.Fatalf("parseNameStatus(%q) = %#v, want %#v", out, got, []FileChange{want})
				}
			})
		}
	}
}

func TestParseNameStatusPreservesPathBytesAndStreamOrder(t *testing.T) {
	// Distinct names must remain distinct even when trimming would collapse them.
	out := []byte("A\x00same\x00M\x00 same\x00D\x00same \x00" +
		"R100\x00\told\n\x00\u2003new\u3000\x00" +
		"C75\x00 source\t\x00 copy\n\x00X\x00 \t\n\x00A\x00last\x00")
	want := []FileChange{
		{Change: ChangeAdded, Path: "same"},
		{Change: ChangeModified, Path: " same"},
		{Change: ChangeDeleted, Path: "same "},
		{Change: ChangeRenamed, Path: "\u2003new\u3000", OldPath: "\told\n"},
		{Change: ChangeAdded, Path: " copy\n", OldPath: " source\t"},
		{Change: ChangeModified, Path: " \t\n"},
		{Change: ChangeAdded, Path: "last"},
	}
	if got := parseNameStatus(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
}

func TestParseNameStatusMatchesGitNoIndexPathBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses filenames that are not supported on Windows")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	oldPath := " \told-\u3000\n"
	newPath := "\u2003new-\t "
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(oldPath, "before\n")
	write(newPath, "after\n")
	write("left/ old\t", "identical\n")
	write("right/\u3000new\n", "identical\n")

	cases := []struct {
		name string
		from string
		to   string
		wire string
		want FileChange
	}{
		{"addition", os.DevNull, newPath, "A\x00" + newPath + "\x00", FileChange{Change: ChangeAdded, Path: newPath}},
		{"modification", oldPath, newPath, "M\x00" + oldPath + "\x00", FileChange{Change: ChangeModified, Path: oldPath}},
		{"deletion", oldPath, os.DevNull, "D\x00" + oldPath + "\x00", FileChange{Change: ChangeDeleted, Path: oldPath}},
		{"rename", "left", "right", "R100\x00left/ old\t\x00right/\u3000new\n\x00", FileChange{Change: ChangeRenamed, Path: "right/\u3000new\n", OldPath: "left/ old\t"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// --no-index reads only these temporary files, without a repository.
			command := exec.Command(gitPath, "diff", "--no-index", "--name-status", "-z", "-M", "--no-ext-diff", "--no-textconv", "--", tc.from, tc.to)
			command.Dir = root
			// Ignore inherited repository and user configuration overrides.
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "XDG_CONFIG_HOME=") && !strings.HasPrefix(entry, "LC_ALL=") {
					command.Env = append(command.Env, entry)
				}
			}
			command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "HOME="+root, "XDG_CONFIG_HOME="+root, "LC_ALL=C")
			var stderr bytes.Buffer
			command.Stderr = &stderr
			out, err := command.Output()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("git diff --no-index: expected changed-files exit 1; got %v, stderr %q", err, stderr.String())
			}
			// Independently check Git's documented unquoted, NUL-terminated bytes
			// before testing the parser against them. R/C list source then target.
			if string(out) != tc.wire {
				t.Fatalf("git output = %q, want %q; stderr %q", out, tc.wire, stderr.String())
			}
			if got := parseNameStatus(out); !reflect.DeepEqual(got, []FileChange{tc.want}) {
				t.Fatalf("parseNameStatus(%q) = %#v, want %#v", out, got, []FileChange{tc.want})
			}
		})
	}
}
