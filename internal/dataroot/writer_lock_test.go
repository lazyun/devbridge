package dataroot

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWriterLockRejectsSecondDescriptorAndReleasesOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-writer.lock")
	first, err := AcquireWriterLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Held() {
		t.Fatal("new writer lock is not held")
	}
	if second, err := AcquireWriterLock(path); !errors.Is(err, ErrWriterLocked) || second != nil {
		t.Fatalf("second lock=%v err=%v", second, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if first.Held() {
		t.Fatal("closed writer lock is still held")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	second, err := AcquireWriterLock(path)
	if err != nil {
		t.Fatalf("acquire after close: %v", err)
	}
	defer second.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock artifact was removed: %v", err)
	}
}

func TestWriterLockIgnoresStaleFilename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-writer.lock")
	if err := os.WriteFile(path, []byte("stale artifact\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireWriterLock(path)
	if err != nil {
		t.Fatalf("stale filename blocked lock: %v", err)
	}
	defer lock.Close()
}

func TestWriterLockDifferentPathsCanBeHeldConcurrently(t *testing.T) {
	root := t.TempDir()
	paths := []string{filepath.Join(root, "a.lock"), filepath.Join(root, "b.lock")}
	locks := make(chan *WriterLock, len(paths))
	errs := make(chan error, len(paths))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, path := range paths {
		path := path
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lock, err := AcquireWriterLock(path)
			if err != nil {
				errs <- err
				return
			}
			locks <- lock
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(locks)
	for err := range errs {
		t.Error(err)
	}
	count := 0
	for lock := range locks {
		count++
		if !lock.Held() {
			t.Error("concurrent lock is not held")
		}
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}
	if count != len(paths) {
		t.Fatalf("held locks=%d want=%d", count, len(paths))
	}
}

func TestWriterLockProcessDeathReleasesKernelLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-writer.lock")
	command := exec.Command(os.Args[0], "-test.run=^TestWriterLockSubprocessHelper$", "--", path)
	command.Env = append(os.Environ(), "DEVBRIDGE_WRITER_LOCK_HELPER=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || line != "locked\n" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("child readiness=%q err=%v", line, err)
	}
	if lock, err := AcquireWriterLock(path); !errors.Is(err, ErrWriterLocked) || lock != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("parent acquired child lock: lock=%v err=%v", lock, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		lock, acquireErr := AcquireWriterLock(path)
		if acquireErr == nil {
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			break
		}
		if !errors.Is(acquireErr, ErrWriterLocked) || time.Now().After(deadline) {
			t.Fatalf("acquire after child death: %v", acquireErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWriterLockSubprocessHelper(t *testing.T) {
	if os.Getenv("DEVBRIDGE_WRITER_LOCK_HELPER") != "1" {
		return
	}
	if len(os.Args) == 0 {
		os.Exit(2)
	}
	path := os.Args[len(os.Args)-1]
	lock, err := AcquireWriterLock(path)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "acquire helper lock: %v\n", err)
		os.Exit(2)
	}
	defer lock.Close()
	_, _ = fmt.Fprintln(os.Stdout, "locked")
	_ = os.Stdout.Sync()
	for {
		time.Sleep(time.Hour)
	}
}

func TestWriterLockRejectsNonRegularPaths(t *testing.T) {
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lock")
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if lock, err := AcquireWriterLock(path); err == nil {
				lock.Close()
				t.Fatal("non-regular lock accepted")
			}
		})
	}
}
