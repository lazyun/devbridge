package reviewaccess

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScopedSnapshotHonorsScopeAndStructuralGuards(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("allowed/a.txt", "one")
	write("forbidden/secret.txt", "secret")
	write(".git/config", "admin")
	write("outside.txt", "outside")
	if err := os.Symlink(filepath.Join(root, "outside.txt"), filepath.Join(root, "allowed", "link")); err != nil {
		t.Fatal(err)
	}
	first, err := ScopedSnapshot(context.Background(), root, []string{"allowed", "forbidden", ".git"}, []string{"forbidden"})
	if err != nil {
		t.Fatal(err)
	}
	write("forbidden/secret.txt", "changed")
	write(".git/config", "changed")
	second, err := ScopedSnapshot(context.Background(), root, []string{"allowed", "forbidden", ".git"}, []string{"forbidden"})
	if err != nil || second != first {
		t.Fatalf("unauthorized change digest=%q want=%q err=%v", second, first, err)
	}
	write("allowed/a.txt", "two")
	third, err := ScopedSnapshot(context.Background(), root, []string{"allowed", "forbidden", ".git"}, []string{"forbidden"})
	if err != nil || third == first {
		t.Fatalf("authorized change digest=%q old=%q err=%v", third, first, err)
	}
}

func TestScopedSnapshotDeduplicatesOverlappingRootsAndBoundsFiles(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a", "b.txt")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("same"), 0644); err != nil {
		t.Fatal(err)
	}
	one, err := ScopedSnapshot(context.Background(), root, []string{"a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	two, err := ScopedSnapshot(context.Background(), root, []string{"a", "a/b.txt"}, nil)
	if err != nil || one != two {
		t.Fatalf("overlap digest=%q want=%q err=%v", two, one, err)
	}
	reversed, err := ScopedSnapshot(context.Background(), root, []string{"a/b.txt", "a"}, nil)
	if err != nil || reversed != one {
		t.Fatalf("root order digest=%q want=%q err=%v", reversed, one, err)
	}
	big := filepath.Join(root, "a", "too-big")
	if err := os.WriteFile(big, make([]byte, maxSnapshotBytes+1), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScopedSnapshot(context.Background(), root, []string{"a"}, nil); err == nil {
		t.Fatal("oversize snapshot accepted")
	}
}
