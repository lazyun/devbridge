package appserver

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func listenUnixSocket(t *testing.T, path string) net.Listener {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "db-si-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestSocketIdentityDirectAndSymlink(t *testing.T) {
	dir := shortSocketDir(t)
	direct := filepath.Join(dir, "direct.sock")
	listenUnixSocket(t, direct)
	id, err := socketIdentity(direct)
	if err != nil || id == "" || strings.HasPrefix(id, "symlink-v1:") {
		t.Fatalf("direct identity=%q err=%v", id, err)
	}

	targetDir := filepath.Join(dir, "private")
	if err := os.Mkdir(targetDir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDir, "rpc.sock")
	listenUnixSocket(t, target)
	alias := filepath.Join(dir, "alias.sock")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	aliasID, err := socketIdentity(alias)
	if err != nil || !strings.HasPrefix(aliasID, "symlink-v1:") {
		t.Fatalf("symlink identity=%q err=%v", aliasID, err)
	}
}

func TestSocketIdentityRejectsAliasAndTargetReplacement(t *testing.T) {
	dir := shortSocketDir(t)
	firstDir := filepath.Join(dir, "first")
	if err := os.Mkdir(firstDir, 0700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(firstDir, "rpc.sock")
	firstListener := listenUnixSocket(t, first)
	alias := filepath.Join(dir, "alias.sock")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	old, err := socketIdentity(alias)
	if err != nil {
		t.Fatal(err)
	}
	newAlias := filepath.Join(dir, "alias-new.sock")
	if err := os.Symlink(first, newAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newAlias, alias); err != nil {
		t.Fatal(err)
	}
	if next, err := socketIdentity(alias); err != nil || next == old {
		t.Fatalf("replacement alias was not distinguished: old=%q next=%q err=%v", old, next, err)
	}
	baseline, err := socketIdentity(alias)
	if err != nil {
		t.Fatal(err)
	}

	// Keep the alias inode and pathname unchanged while replacing its target
	// socket at the same target path.
	if unix, ok := firstListener.(*net.UnixListener); !ok {
		t.Fatal("expected Unix listener")
	} else {
		unix.SetUnlinkOnClose(false)
		if err := unix.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	replacementPath := filepath.Join(dir, "replacement.sock")
	replacement := listenUnixSocket(t, replacementPath)
	defer replacement.Close()
	if err := os.Rename(replacementPath, first); err != nil {
		t.Fatal(err)
	}
	if next, err := socketIdentity(alias); err != nil || next == baseline {
		t.Fatalf("replacement target was not distinguished with stable alias: old=%q next=%q err=%v", baseline, next, err)
	}
}

func TestSocketIdentityRejectsInvalidAliases(t *testing.T) {
	dir := shortSocketDir(t)
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("not a socket"), 0600); err != nil {
		t.Fatal(err)
	}
	regularAlias := filepath.Join(dir, "regular.sock")
	if err := os.Symlink(regular, regularAlias); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling.sock")
	if err := os.Symlink(filepath.Join(dir, "missing.sock"), dangling); err != nil {
		t.Fatal(err)
	}
	unsafe := filepath.Join(dir, "unsafe")
	if err := os.Mkdir(unsafe, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(unsafe, "rpc.sock")
	listenUnixSocket(t, socket)
	unsafeAlias := filepath.Join(dir, "unsafe.sock")
	if err := os.Symlink(socket, unsafeAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unsafe, 0700) })

	for name, path := range map[string]string{"regular": regularAlias, "dangling": dangling, "unsafe parent": unsafeAlias} {
		t.Run(name, func(t *testing.T) {
			if id, err := socketIdentity(path); err == nil || id != "" {
				t.Fatalf("invalid alias accepted: id=%q err=%v", id, err)
			}
		})
	}
}
