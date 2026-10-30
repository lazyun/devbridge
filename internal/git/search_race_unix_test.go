//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/reviewaccess"
)

func TestSearchFixedRejectsDeterministicIntermediateSwap(t *testing.T) {
	repo, _ := makeRepo(t)
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "directory", "target.txt"), "inside needle\n")
	writeFile(t, filepath.Join(outside, "target.txt"), "outside needle\n")
	gitRun(t, repo, "add", "directory/target.txt")
	gitRun(t, repo, "commit", "-m", "tracked nested search file")
	client := NewClient(Config{})
	client.beforeFileOpen = func(path string) {
		if path != "directory/target.txt" {
			return
		}
		client.beforeFileOpen = nil
		if err := os.Rename(filepath.Join(repo, "directory"), filepath.Join(repo, "original-directory")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(repo, "directory")); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := client.SearchFixed(context.Background(), repo, "needle", nil)
	if !errors.Is(err, reviewaccess.ErrScopeForbidden) || len(matches) != 0 {
		t.Fatalf("search matches=%+v err=%v, want fail-closed boundary rejection", matches, err)
	}
}

func TestSearchFixedRejectsDeterministicFinalSwap(t *testing.T) {
	repo, _ := makeRepo(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(repo, "target.txt"), "inside needle\n")
	writeFile(t, filepath.Join(outside, "target.txt"), "outside needle\n")
	gitRun(t, repo, "add", "target.txt")
	gitRun(t, repo, "commit", "-m", "tracked search file")
	client := NewClient(Config{})
	client.beforeFileOpen = func(path string) {
		if path != "target.txt" {
			return
		}
		client.beforeFileOpen = nil
		if err := os.Rename(filepath.Join(repo, "target.txt"), filepath.Join(repo, "original.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(repo, "target.txt")); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := client.SearchFixed(context.Background(), repo, "needle", nil)
	if !errors.Is(err, reviewaccess.ErrScopeForbidden) || len(matches) != 0 {
		t.Fatalf("search matches=%+v err=%v, want fail-closed boundary rejection", matches, err)
	}
}
