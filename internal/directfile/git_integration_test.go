package directfile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFileOperationsOnlyDirtyGitWorktree(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init")
	git("config", "user.email", "directfile-test@example.invalid")
	git("config", "user.name", "Direct File Test")
	for path, content := range map[string]string{
		"replace.txt": "old replacement", "move.txt": "move me", "delete.txt": "delete me",
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-m", "initial")
	headBefore := git("rev-parse", "HEAD")
	refsBefore := git("show-ref", "--head")
	indexBefore, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	authority := &testAuthority{root: root}
	service := NewService(authority)
	ctx := context.Background()
	scope := Scope{AISessionID: "session", FeatureAreaID: "area", WorkflowID: "workflow"}
	if _, err := service.Enable(ctx, scope); err != nil {
		t.Fatal(err)
	}
	create := Input{Scope: scope, Path: "created.txt", Content: "created"}
	if _, err := service.Create(ctx, create); err != nil {
		t.Fatal(err)
	}
	replace := Input{Scope: scope, Path: "replace.txt", Content: "replaced"}
	if _, err := service.Replace(ctx, replace); err != nil {
		t.Fatal(err)
	}
	move := Input{Scope: scope, Path: "move.txt", Destination: "moved.txt"}
	if _, err := service.Move(ctx, move); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Delete(ctx, Input{Scope: scope, Path: "delete.txt"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".git/config", ".GiT/index"} {
		if _, err := service.Read(ctx, Input{Scope: scope, Path: path}); errorCode(t, err) != InvalidPath {
			t.Fatalf("%s: %v", path, err)
		}
	}

	if got := git("rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD changed: before %q after %q", headBefore, got)
	}
	if got := git("show-ref", "--head"); got != refsBefore {
		t.Fatalf("refs changed: before %q after %q", refsBefore, got)
	}
	indexAfter, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("Git index changed")
	}
	if clean := git("status", "--porcelain"); clean == "" {
		t.Fatal("expected Direct File operations to dirty the worktree")
	}
}
