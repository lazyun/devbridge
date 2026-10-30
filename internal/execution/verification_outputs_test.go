package execution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestVerificationOutputsNeverGrantSourceOrSymlinks(t *testing.T) {
	_, w := taskPermissionFixture(t)
	root := w.Members[0].CanonicalRoot
	ctx := context.Background()
	paths, err := prepareVerificationOutputs(ctx, root, nil)
	if err != nil || !slices.Contains(paths, "dist") || !slices.Contains(paths, ".cache") {
		t.Fatalf("safe ignored outputs: %v %v", paths, err)
	}
	for _, path := range []string{"source.txt", "new-test.txt", "not-ignored", ".", "../escape", ".git/objects", "dist/../source.txt"} {
		if _, err := prepareVerificationOutputs(ctx, root, []string{path}); err == nil {
			t.Fatalf("unsafe grant accepted: %s", path)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("dist/\n.cache/\nlinked/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".cache"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareVerificationOutputs(ctx, root, []string{"linked"}); err == nil {
		t.Fatal("symlink output granted")
	}
	// Even force-tracked source under an ignored directory must remain protected.
	if err := os.WriteFile(filepath.Join(root, "dist", "source.txt"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", root, "add", "-f", "dist/source.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	if _, err := prepareVerificationOutputs(ctx, root, []string{"dist"}); err == nil {
		t.Fatal("tracked ignored source granted")
	}
}

func TestTaskPermissionCheckpointCannotChangeNativeBinding(t *testing.T) {
	c, w := taskPermissionFixture(t)
	w, err := c.prepareTaskPermissions(context.Background(), w, true)
	if err != nil {
		t.Fatal(err)
	}
	cwd, b, err := multiRuntimeClientBinding(w)
	if err != nil {
		t.Fatal(err)
	}
	cp := executionCheckpoint{MultiRuntime: &PreparedMultiRuntime{Workset: w, CWD: cwd, Binding: b}}
	if err := validateCheckpointTaskBinding(cp); err != nil {
		t.Fatal(err)
	}
	cp.MultiRuntime.Binding.WritableRoots = append(cp.MultiRuntime.Binding.WritableRoots, filepath.Dir(cwd))
	if validateCheckpointTaskBinding(cp) == nil {
		t.Fatal("checkpoint broadened native sandbox")
	}
}

func TestVerificationOutputFilesAreExactIgnoredCaches(t *testing.T) {
	_, w := taskPermissionFixture(t)
	root := w.Members[0].CanonicalRoot
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("dist/\n.cache/\n*.tsbuildinfo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := prepareVerificationOutputs(context.Background(), root, nil)
	if err != nil || !slices.Contains(paths, "tsconfig.tsbuildinfo") || !slices.Contains(paths, "frontend/tsconfig.tsbuildinfo") {
		t.Fatalf("missing exact caches: %v %v", paths, err)
	}
	if _, err := os.Stat(filepath.Join(root, "tsconfig.tsbuildinfo")); !os.IsNotExist(err) {
		t.Fatal("cache leaf incorrectly created as directory")
	}
	if err := os.WriteFile(filepath.Join(root, "tsconfig.tsbuildinfo"), []byte("protected tracked file"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "add", "-f", "tsconfig.tsbuildinfo").CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	if err := os.Symlink(filepath.Join(root, "source.txt"), filepath.Join(root, "frontend", "tsconfig.tsbuildinfo")); err != nil {
		t.Fatal(err)
	}
	paths, err = prepareVerificationOutputs(context.Background(), root, nil)
	if err != nil || slices.Contains(paths, "tsconfig.tsbuildinfo") || slices.Contains(paths, "frontend/tsconfig.tsbuildinfo") {
		t.Fatal("tracked file or symlink granted")
	}
}
