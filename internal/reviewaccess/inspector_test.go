package reviewaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type inspectorSearch struct {
	root  string
	paths []string
}

func (s *inspectorSearch) SearchRepository(_ context.Context, root *Root, _ string, paths []string) ([]GitSearchMatch, error) {
	s.root, s.paths = root.Path(), append([]string(nil), paths...)
	return []GitSearchMatch{{Path: "internal/feature.go", Line: 3, Snippet: "feature"}, {Path: "internal/feature_test.go", Line: 4, Snippet: "test"}}, nil
}

func TestInspectorAllowsAuthorizedRepositoryTraversalRegardlessOfFilenameSemantics(t *testing.T) {
	root := t.TempDir()
	mustWriteInspectorFile(t, root, "module-a/start.go", "package a\n")
	mustWriteInspectorFile(t, root, "module-b/dependency.go", "line one\nline two\n")
	mustWriteInspectorFile(t, root, "tests/fixture.go", "test-like content\n")
	search := &inspectorSearch{}
	inspector, err := NewInspector(search)
	if err != nil {
		t.Fatal(err)
	}

	read, err := inspector.Read(context.Background(), root, "module-b/dependency.go", 2, 2)
	if err != nil || read.Content != "line two" || read.Path != "module-b/dependency.go" {
		t.Fatalf("cross-module production read = %+v err=%v", read, err)
	}
	testRead, err := inspector.Read(context.Background(), root, "tests/fixture.go", 1, 1)
	if err != nil || testRead.Content != "test-like content" {
		t.Fatalf("test-like repository path received semantic rejection: result=%+v err=%v", testRead, err)
	}
	listed, err := inspector.List(context.Background(), root, ".", 20)
	if err != nil {
		t.Fatal(err)
	}
	foundTests := false
	for _, entry := range listed.Entries {
		foundTests = foundTests || entry.Path == "tests"
	}
	if !foundTests {
		t.Fatalf("test-like directory was semantically filtered from list: %+v", listed)
	}
	matched, err := inspector.Search(context.Background(), root, "feature", []string{"module-b"}, 20)
	if err != nil || len(matched.Matches) != 2 || matched.Matches[1].Path != "internal/feature_test.go" {
		t.Fatalf("search = %+v err=%v", matched, err)
	}
	if len(search.paths) != 1 || search.paths[0] != "module-b" {
		t.Fatalf("validated search paths = %v", search.paths)
	}
}

func TestInspectorRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteInspectorFile(t, outside, "outside.go", "secret")
	if err := os.Symlink(filepath.Join(outside, "outside.go"), filepath.Join(root, "escape.go")); err != nil {
		t.Fatal(err)
	}
	inspector, err := NewInspector(&inspectorSearch{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside.go", "escape.go"} {
		if _, err := inspector.Read(context.Background(), root, path, 1, 1); !errors.Is(err, ErrScopeForbidden) {
			t.Fatalf("read %q structural error=%v", path, err)
		}
	}
}

func mustWriteInspectorFile(t *testing.T, root, relative, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
