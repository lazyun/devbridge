package execution

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/task"
)

func TestReviewFocusRetainsRepositoryContainment(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", "/absolute", "escape"} {
		if err := validateReviewScopeBoundary(root, task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: []string{path}}); err == nil {
			t.Fatalf("accepted escape %q", path)
		}
	}
	if err := validateReviewScopeBoundary(root, task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: []string{"inside.txt"}}); err != nil {
		t.Fatal(err)
	}
}
