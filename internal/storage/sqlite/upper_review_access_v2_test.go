package sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

// The v2 grant pins both the resolved root and its device/inode identity.
// A symlink replacement may preserve the apparent member path, so this is a
// separate regression from the strict child-path tests.
func TestUpperV2RootPinRejectsSymlinkRootSwap(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	for _, p := range []string{first, second} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	member := filepath.Join(dir, "member")
	if err := os.Symlink(first, member); err != nil {
		t.Fatal(err)
	}
	path, err := upperV2CanonicalRoot(member)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := upperV2RootIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(member); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, member); err != nil {
		t.Fatal(err)
	}
	next, err := upperV2CanonicalRoot(member)
	if err != nil {
		t.Fatal(err)
	}
	nextIdentity, err := upperV2RootIdentity(next)
	if err != nil {
		t.Fatal(err)
	}
	if path == next || identity == nextIdentity {
		t.Fatalf("root replacement escaped pin: old=%q/%q new=%q/%q", path, identity, next, nextIdentity)
	}
}
