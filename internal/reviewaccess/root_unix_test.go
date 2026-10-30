//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package reviewaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestInspectorPreservesInternalSymlinkSemantics(t *testing.T) {
	root := t.TempDir()
	mustWriteInspectorFile(t, root, "actual/nested.txt", "inside\n")
	if err := os.Symlink("actual", filepath.Join(root, "directory-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("actual/nested.txt", filepath.Join(root, "file-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "actual", "nested.txt"), filepath.Join(root, "absolute-file-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file-link", filepath.Join(root, "chain-link")); err != nil {
		t.Fatal(err)
	}
	inspector, _ := NewInspector(&inspectorSearch{})
	for _, path := range []string{"file-link", "absolute-file-link", "chain-link", "directory-link/nested.txt"} {
		result, err := inspector.Read(context.Background(), root, path, 1, 1)
		if err != nil || result.Content != "inside" || result.Path != "actual/nested.txt" {
			t.Fatalf("read %q = %+v err=%v", path, result, err)
		}
	}
	listed, err := inspector.List(context.Background(), root, "directory-link", 10)
	if err != nil || listed.Path != "actual" || len(listed.Entries) != 1 || listed.Entries[0].Path != "actual/nested.txt" {
		t.Fatalf("list through internal symlink = %+v err=%v", listed, err)
	}
}

func TestInspectorRejectsSymlinkBoundaryCases(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteInspectorFile(t, outside, "secret.txt", "outside\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "absolute-external")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../"+filepath.Base(outside), filepath.Join(root, "intermediate")); err != nil {
		t.Fatal(err)
	}
	inspector, _ := NewInspector(&inspectorSearch{})
	for _, path := range []string{"absolute-external", "dangling", "intermediate/secret.txt", "../secret.txt", filepath.Join(root, "actual")} {
		if _, err := inspector.Read(context.Background(), root, path, 1, 1); !errors.Is(err, ErrScopeForbidden) {
			t.Fatalf("read %q error=%v, want boundary rejection", path, err)
		}
	}
	if _, err := inspector.List(context.Background(), root, "absolute-external", 10); !errors.Is(err, ErrScopeForbidden) {
		t.Fatalf("list external final symlink error=%v", err)
	}
}

func TestReadFinalComponentSwapUsesOpenedFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteInspectorFile(t, root, "target.txt", "inside\n")
	mustWriteInspectorFile(t, outside, "target.txt", "outside\n")
	var once sync.Once
	inspector, _ := NewInspector(&inspectorSearch{})
	inspector.hook = func(event accessEvent) {
		if event.stage == "target-opened" && event.path == "target.txt" {
			once.Do(func() {
				if err := os.Rename(filepath.Join(root, "target.txt"), filepath.Join(root, "original.txt")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(root, "target.txt")); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	result, err := inspector.Read(context.Background(), root, "target.txt", 1, 1)
	if err != nil || result.Content != "inside" {
		t.Fatalf("final swap read = %+v err=%v", result, err)
	}
}

func TestReadIntermediateComponentSwapUsesOpenedDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteInspectorFile(t, root, "directory/target.txt", "inside\n")
	mustWriteInspectorFile(t, outside, "target.txt", "outside\n")
	inspector, _ := NewInspector(&inspectorSearch{})
	inspector.hook = func(event accessEvent) {
		if event.stage != "directory-opened" || event.path != "directory" {
			return
		}
		inspector.hook = nil
		if err := os.Rename(filepath.Join(root, "directory"), filepath.Join(root, "original-directory")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "directory")); err != nil {
			t.Fatal(err)
		}
	}
	result, err := inspector.Read(context.Background(), root, "directory/target.txt", 1, 1)
	if err != nil || result.Content != "inside" {
		t.Fatalf("intermediate swap read = %+v err=%v", result, err)
	}
}

func TestListDirectorySwapUsesOpenedDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteInspectorFile(t, root, "directory/inside.txt", "inside\n")
	mustWriteInspectorFile(t, outside, "outside.txt", "outside\n")
	inspector, _ := NewInspector(&inspectorSearch{})
	inspector.hook = func(event accessEvent) {
		if event.stage != "target-opened" || event.path != "directory" {
			return
		}
		inspector.hook = nil
		if err := os.Rename(filepath.Join(root, "directory"), filepath.Join(root, "original-directory")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "directory")); err != nil {
			t.Fatal(err)
		}
	}
	result, err := inspector.List(context.Background(), root, "directory", 10)
	if err != nil || len(result.Entries) != 1 || result.Entries[0].Path != "directory/inside.txt" {
		t.Fatalf("directory swap list = %+v err=%v", result, err)
	}
}

func TestRootReplacementAfterOpenKeepsAuthorizedObject(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repository")
	outside := t.TempDir()
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteInspectorFile(t, root, "target.txt", "inside\n")
	mustWriteInspectorFile(t, outside, "target.txt", "outside\n")
	capability, err := openRoot(root, func(event accessEvent) {
		if event.stage != "root-opened" {
			return
		}
		if err := os.Rename(root, filepath.Join(parent, "original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, root); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	file, _, err := capability.OpenFile(OperationRead, "target.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := os.ReadFile(filepath.Join(parent, "original", "target.txt"))
	if err != nil || string(data) != "inside\n" {
		t.Fatalf("fixture moved file=%q err=%v", data, err)
	}
	buffer := make([]byte, 32)
	n, err := file.Read(buffer)
	if err != nil || string(buffer[:n]) != "inside\n" {
		t.Fatalf("root replacement read=%q err=%v", buffer[:n], err)
	}
}

func TestRepositoryRootSymlinkFailsClosed(t *testing.T) {
	actual := t.TempDir()
	parent := t.TempDir()
	alias := filepath.Join(parent, "repository-link")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	if root, err := OpenRoot(alias); err == nil {
		_ = root.Close()
		t.Fatal("symlink Repository root unexpectedly opened")
	}
}

func TestConcurrentLegitimateReads(t *testing.T) {
	root := t.TempDir()
	mustWriteInspectorFile(t, root, "nested/target.txt", "inside\n")
	inspector, _ := NewInspector(&inspectorSearch{})
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for attempt := 0; attempt < 20; attempt++ {
				result, err := inspector.Read(context.Background(), root, "nested/target.txt", 1, 1)
				if err != nil || result.Content != "inside" {
					t.Errorf("concurrent read=%+v err=%v", result, err)
					return
				}
			}
		}()
	}
	wait.Wait()
}

func TestRapidSymlinkSwapsNeverReadOutsideRepository(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteInspectorFile(t, root, "inside.txt", "inside\n")
	mustWriteInspectorFile(t, outside, "outside.txt", "outside\n")
	target := filepath.Join(root, "target")
	if err := os.Symlink("inside.txt", target); err != nil {
		t.Fatal(err)
	}
	inspector, _ := NewInspector(&inspectorSearch{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for attempt := 0; attempt < 500; attempt++ {
			_ = os.Remove(target)
			_ = os.Symlink(filepath.Join(outside, "outside.txt"), target)
			_ = os.Remove(target)
			_ = os.Symlink("inside.txt", target)
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
			result, err := inspector.Read(context.Background(), root, "target", 1, 1)
			if err == nil && result.Content != "inside" {
				t.Fatalf("rapid swap exposed content %q", result.Content)
			}
			if err != nil && !errors.Is(err, ErrScopeForbidden) {
				t.Fatalf("rapid swap error=%v", err)
			}
		}
	}
}
