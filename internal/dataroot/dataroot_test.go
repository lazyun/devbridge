package dataroot

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
)

func TestOpenInitializesLayoutAndReopensIdempotently(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	first, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Initialized || first.FormatVersion != 1 {
		t.Fatalf("first open = %+v", first)
	}
	want, _ := Derive(first.Paths.Root)
	if !reflect.DeepEqual(first.Paths, want) {
		t.Fatalf("paths = %+v, want %+v", first.Paths, want)
	}
	for _, path := range []string{want.State, want.Worktrees, want.Evidence, want.Logs, want.Runtime, want.Artifacts} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("managed path %q: %v", path, err)
		}
	}
	marker, err := os.ReadFile(filepath.Join(root, MarkerFilename))
	if err != nil || string(marker) != "{\n  \"formatVersion\": 1\n}\n" {
		t.Fatalf("marker = %q, %v", marker, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(root)
	defer second.Close()
	if err != nil || second.Initialized || second.Paths != first.Paths {
		t.Fatalf("second open = %+v, %v", second, err)
	}
}

func TestConcurrentFirstOpenHasExactlyOneOwner(t *testing.T) {
	const workers = 32
	for iteration := 0; iteration < 12; iteration++ {
		root := filepath.Join(t.TempDir(), "data")
		start := make(chan struct{})
		type result struct {
			root DataRoot
			err  error
		}
		results := make(chan result, workers)
		var ready sync.WaitGroup
		ready.Add(workers)
		for i := 0; i < workers; i++ {
			go func() {
				ready.Done()
				<-start
				opened, err := Open(root)
				results <- result{opened, err}
			}()
		}
		ready.Wait()
		close(start)
		var winner DataRoot
		owners := 0
		for i := 0; i < workers; i++ {
			r := <-results
			if r.err == nil {
				owners++
				winner = r.root
				defer r.root.Close()
			} else if !errors.Is(r.err, ErrWriterLocked) {
				t.Errorf("iteration %d: %v", iteration, r.err)
			}
		}
		if owners != 1 {
			t.Fatalf("owners=%d, want 1", owners)
		}
		if !winner.WriterHeld() {
			t.Fatal("winner lost ownership")
		}
		marker, err := os.ReadFile(filepath.Join(root, MarkerFilename))
		if err != nil || string(marker) != "{\n  \"formatVersion\": 1\n}\n" {
			t.Fatalf("iteration %d marker=%q err=%v", iteration, marker, err)
		}
		paths, _ := Derive(root)
		for _, path := range []string{paths.State, paths.Worktrees, paths.Evidence, paths.Logs, paths.Runtime, paths.Artifacts} {
			info, statErr := os.Lstat(path)
			if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("iteration %d managed path %q info=%v err=%v", iteration, path, info, statErr)
			}
		}
	}
}

func TestOpenRejectsInvalidRootsWithoutMutation(t *testing.T) {
	t.Run("non-empty arbitrary directory", func(t *testing.T) {
		root := t.TempDir()
		foreign := filepath.Join(root, "keep.txt")
		if err := os.WriteFile(foreign, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(root)
		assertCode(t, err, CodeNotRecognized)
		if got, readErr := os.ReadFile(foreign); readErr != nil || string(got) != "keep" {
			t.Fatalf("foreign content changed: %q, %v", got, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(root, MarkerFilename)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("marker unexpectedly created: %v", statErr)
		}
	})
	t.Run("malformed marker", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, MarkerFilename), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(root)
		assertCode(t, err, CodeMarkerMalformed)
	})
	t.Run("missing format version", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, MarkerFilename), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(root)
		assertCode(t, err, CodeMarkerMalformed)
	})
	t.Run("unsupported version", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, MarkerFilename), []byte(`{"formatVersion":2}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(root)
		assertCode(t, err, CodeFormatUnsupported)
		entries, _ := os.ReadDir(root)
		if len(entries) != 1 {
			t.Fatalf("unsupported root mutated: %v", entries)
		}
	})
	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "data")
		if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(path)
		assertCode(t, err, CodePathIsFile)
	})
	t.Run("managed child symlink", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, MarkerFilename), []byte(`{"formatVersion":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "worktrees")); err != nil {
			t.Fatal(err)
		}
		_, err := Open(root)
		assertCode(t, err, CodeChildInvalid)
	})
}

func TestInitializationFailureDoesNotCreateValidMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	_, err := Open(root)
	if err == nil {
		t.Skip("filesystem permits writes despite directory mode")
	}
	assertCode(t, err, CodeInitializeFailed)
	if _, statErr := os.Stat(filepath.Join(root, MarkerFilename)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed initialization left marker: %v", statErr)
	}
}

func assertCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	var target *Error
	if !errors.As(err, &target) || target.Code != want {
		t.Fatalf("error = %v (%T), want code %s", err, err, want)
	}
}

func TestRootAliasesShareBootstrapOwnership(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	first, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	for _, path := range []string{alias, filepath.Join(root, ".")} {
		if next, err := Open(path); !errors.Is(err, ErrWriterLocked) {
			next.Close()
			t.Fatalf("alias %s: %v", path, err)
		}
	}
}

func TestOpenRecoversAbandonedBootstrapLockArtifact(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, WriterLockFilename)
	if err := os.WriteFile(path, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Initialized {
		t.Fatal("partial bootstrap did not initialize")
	}
	first.Close()
	if data, err := os.ReadFile(path); err != nil || string(data) != "stale" {
		t.Fatalf("lock artifact changed: %q %v", data, err)
	}
}

func TestInitializationFailureAfterLockReleasesOwnership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics")
	}
	root := t.TempDir()
	lockPath := filepath.Join(root, WriterLockFilename)
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0700)
	opened, err := Open(root)
	if err == nil {
		opened.Close()
		t.Skip("filesystem permits writes despite directory mode")
	}
	assertCode(t, err, CodeInitializeFailed)
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	next, err := Open(root)
	if err != nil {
		t.Fatalf("retry after initialization failure: %v", err)
	}
	defer next.Close()
}
