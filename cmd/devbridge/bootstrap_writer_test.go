package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/dataroot"
	"github.com/lazyun/devbridge/internal/observability"
)

// Runs the actual entrypoint in a separate process, including its exit/defer path.
func TestBootstrapCoreProcess(t *testing.T) {
	if os.Getenv("DEVBRIDGE_BOOTSTRAP_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	main()
}

func coreCommand(t *testing.T, root string, extra ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	args := append([]string{"-test.run=^TestBootstrapCoreProcess$", "--", "-mode", "server", "-data-root", root}, extra...)
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Env = append(os.Environ(), "DEVBRIDGE_BOOTSTRAP_PROCESS=1")
	return cmd
}

type rootEntry struct {
	Mode     fs.FileMode
	Size     int64
	Modified int64
	Hash     [32]byte
}

func snapshotRoot(t *testing.T, root string) map[string]rootEntry {
	t.Helper()
	entries := map[string]rootEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := rootEntry{Mode: info.Mode(), Size: info.Size(), Modified: info.ModTime().UnixNano()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value.Hash = sha256.Sum256(data)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func assertContenderDoesNotWrite(t *testing.T, root string) {
	t.Helper()
	before := snapshotRoot(t, root)
	output, err := coreCommand(t, root).CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "already open for writing") || strings.Contains(string(output), "DEVBRIDGE_READY") {
		t.Fatalf("contender err=%v output=%s", err, output)
	}
	if after := snapshotRoot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("contender mutated root:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestBootstrapContenderCannotInitializeOrRepair(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		name := "before-marker"
		if initialized {
			name = "missing-managed-directory"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if initialized {
				opened, err := dataroot.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				if err = opened.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(root, "logs")); err != nil {
					t.Fatal(err)
				}
			}
			// Deterministic pause at the ownership boundary, before layout mutation.
			owner, err := dataroot.AcquireWriterLock(filepath.Join(root, dataroot.WriterLockFilename))
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			assertContenderDoesNotWrite(t, root)
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := dataroot.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
		})
	}
}

func TestBootstrapLiveCoreConflictAndRestart(t *testing.T) {
	for _, crash := range []bool{false, true} {
		name := "graceful"
		if crash {
			name = "killed"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			cmd := coreCommand(t, root)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			reader := bufio.NewReader(stdout)
			line, err := reader.ReadString('\n')
			if err != nil || !strings.HasPrefix(line, "DEVBRIDGE_READY ") {
				t.Fatalf("ready=%q err=%v", line, err)
			}
			// Readiness is printed just before the last bootstrap log write. Wait for
			// that write before taking the full content/metadata snapshot.
			deadline := time.Now().Add(5 * time.Second)
			for {
				data, _ := os.ReadFile(filepath.Join(root, "logs", observability.CoreLogFilename))
				if strings.Contains(string(data), "core.bootstrap.ready") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("ready log missing: %s", data)
				}
				time.Sleep(10 * time.Millisecond)
			}
			assertContenderDoesNotWrite(t, root)
			if crash {
				err = cmd.Process.Kill()
			} else {
				err = cmd.Process.Signal(syscall.SIGTERM)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if !crash && err != nil {
				t.Fatalf("shutdown=%v stderr=%s", err, stderr.String())
			}
			opened, err := dataroot.Open(root)
			if err != nil {
				t.Fatalf("restart: %v", err)
			}
			defer opened.Close()
		})
	}
}

func TestBootstrapFailureReleasesOwnership(t *testing.T) {
	root := t.TempDir()
	// Fails after opening the logger, and must flush failure diagnostics before
	// releasing ownership through mainExitCode's defers.
	output, err := coreCommand(t, root, "-listen", "0.0.0.0:0").CombinedOutput()
	if err == nil || strings.Contains(string(output), "DEVBRIDGE_READY") {
		t.Fatalf("err=%v output=%s", err, output)
	}
	opened, err := dataroot.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	data, err := os.ReadFile(filepath.Join(root, "logs", observability.CoreLogFilename))
	if err != nil || !strings.Contains(string(data), "core.bootstrap.failed") {
		t.Fatalf("failure log=%s err=%v", data, err)
	}
}

func TestBootstrapConcurrentFreshProcessesHaveOneWriter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fresh")
	type child struct {
		cmd    *exec.Cmd
		reader *bufio.Reader
		stderr *strings.Builder
		waited bool
	}
	children := make([]*child, 2)
	for i := range children {
		cmd := coreCommand(t, root)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr := &strings.Builder{}
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		c := &child{cmd: cmd, reader: bufio.NewReader(stdout), stderr: stderr}
		children[i] = c
		defer func() {
			if !c.waited {
				_ = c.cmd.Process.Kill()
				_ = c.cmd.Wait()
			}
		}()
	}
	winners := 0
	for _, c := range children {
		line, err := c.reader.ReadString('\n')
		if strings.HasPrefix(line, "DEVBRIDGE_READY ") {
			winners++
			continue
		}
		if err == nil {
			t.Fatalf("unexpected output: %q", line)
		}
		err = c.cmd.Wait()
		c.waited = true
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(c.stderr.String(), "already open for writing") {
			t.Fatalf("loser err=%v stderr=%s", err, c.stderr.String())
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	for _, c := range children {
		if !c.waited {
			if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			err := c.cmd.Wait()
			c.waited = true
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "logs", observability.CoreLogFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"core.bootstrap.started", "data_root.initialized", "core.bootstrap.ready", "core.bootstrap.stopped"} {
		if count := strings.Count(string(data), event); count != 1 {
			t.Fatalf("%s count=%d logs=%s", event, count, data)
		}
	}
}
