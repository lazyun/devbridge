package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func createDriftRepo(t *testing.T) (string, string) {
	t.Helper()
	path := t.TempDir()
	run := func(args ...string) {
		command := exec.Command("git", args...)
		command.Dir = path
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	write := func(relative, contents string) {
		full := filepath.Join(path, relative)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write("interface.go", "package app\n\nconst Name = \"v1\"\n")
	write("implementation.go", "package app\n\nfunc Render() string { return \"v1\" }\n")
	write("docs/readme.md", "docs\n")
	run("add", ".")
	run("-c", "user.name=Drift Test", "-c", "user.email=drift@example.invalid", "commit", "-qm", "base")
	head, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return path, string(head[:len(head)-1])
}

func commitAll(t *testing.T, repoPath, message string) string {
	t.Helper()
	command := exec.Command("git", "-C", repoPath, "add", "-A")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	command = exec.Command("git", "-C", repoPath, "-c", "user.name=Drift Test", "-c", "user.email=drift@example.invalid", "commit", "-qm", message)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	head, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(head[:len(head)-1])
}

func TestChangedFilesNamedReportsObjectiveChangeKinds(t *testing.T) {
	repoPath, base := createDriftRepo(t)
	write := func(relative, contents string) {
		if err := os.WriteFile(filepath.Join(repoPath, relative), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// ADDED, MODIFIED, DELETED, RENAMED (tracked file rename keeps -M happy).
	write("added.go", "package app\n")
	write("implementation.go", "package app\n\nfunc Render() string { return \"v2\" }\n")
	if err := os.Remove(filepath.Join(repoPath, "docs/readme.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoPath, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repoPath, "interface.go"), filepath.Join(repoPath, "api", "interface.go")); err != nil {
		t.Fatal(err)
	}
	next := commitAll(t, repoPath, "team changes")

	client := NewClient(Config{})
	changes, err := client.ChangedFilesNamed(context.Background(), repoPath, base, next)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]FileChange{}
	for _, change := range changes {
		byPath[change.Path] = change
	}
	if got := byPath["added.go"]; got.Change != ChangeAdded || got.Path != "added.go" {
		t.Fatalf("added = %+v", got)
	}
	if got := byPath["implementation.go"]; got.Change != ChangeModified {
		t.Fatalf("modified = %+v", got)
	}
	if got := byPath["docs/readme.md"]; got.Change != ChangeDeleted {
		t.Fatalf("deleted = %+v", got)
	}
	renamed, ok := byPath["api/interface.go"]
	if !ok || renamed.Change != ChangeRenamed || renamed.OldPath != "interface.go" {
		t.Fatalf("renamed = %+v (all=%+v)", renamed, changes)
	}
}

func TestChangedFilesNamedSameCommitIsEmpty(t *testing.T) {
	repoPath, base := createDriftRepo(t)
	client := NewClient(Config{})
	changes, err := client.ChangedFilesNamed(context.Background(), repoPath, base, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("same-commit diff = %+v", changes)
	}
}

func TestParseNameStatusHandlesScoredAndUnknownStatuses(t *testing.T) {
	// R100 with new path first, old path second; unknown letter falls back to
	// MODIFIED without corrupting the stream.
	out := []byte("R100\x00old/path.go\x00new/path.go\x00M\x00kept.go\x00X\x00weird.go\x00")
	changes := parseNameStatus(out)
	if len(changes) != 3 {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[0].Change != ChangeRenamed || changes[0].Path != "new/path.go" || changes[0].OldPath != "old/path.go" {
		t.Fatalf("rename = %+v", changes[0])
	}
	if changes[1].Change != ChangeModified || changes[1].Path != "kept.go" {
		t.Fatalf("modified = %+v", changes[1])
	}
	if changes[2].Change != ChangeModified || changes[2].Path != "weird.go" {
		t.Fatalf("unknown = %+v", changes[2])
	}
}
