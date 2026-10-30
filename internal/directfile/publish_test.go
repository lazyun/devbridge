package directfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishFailuresKeepTargetAndRemoveTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := publishCreate(root, "target", []byte("new")); errorCode(t, err) != AlreadyExists {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "target")); string(got) != "original" {
		t.Fatalf("create changed target to %q", got)
	}
	if err := publishReplaceWith(root, "target", []byte("new"), func(_, _ string) error { return errors.New("injected rename failure") }); errorCode(t, err) != OperationFailed {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "target")); string(got) != "original" {
		t.Fatalf("replace changed target to %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".directfile-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestPublishReplaceDirectoryKeepsDirectoryAndCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishReplace(root, "directory", []byte("new")); err == nil {
		t.Fatal("directory replacement unexpectedly succeeded")
	}
	info, err := os.Stat(filepath.Join(dir, "directory"))
	if err != nil || !info.IsDir() {
		t.Fatalf("directory changed: %v %+v", err, info)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".directfile-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestPublishReplacePreservesExistingPermissionBits(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := os.WriteFile(filepath.Join(dir, "target"), []byte("old"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(dir, "target"), mode); err != nil {
				t.Fatal(err)
			}
			if err := publishReplace(root, "target", []byte("new")); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(dir, "target"))
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("mode=%#o err=%v", info.Mode().Perm(), err)
			}
		})
	}
}

func TestPublishReplaceTempCreationFailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	path := filepath.Join(dir, "target")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := publishReplace(root, "target", []byte("new")); err == nil {
		t.Fatal("temporary creation unexpectedly succeeded")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("target=%q err=%v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".directfile-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestFileRemovalCannotRemoveDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := removeFile(root, "empty"); err == nil {
		t.Fatal("file-only unlink removed directory")
	}
	if info, err := root.Stat("empty"); err != nil || !info.IsDir() {
		t.Fatalf("directory lost: %v", err)
	}
}
