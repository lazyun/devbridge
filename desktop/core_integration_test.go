package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	runtimecontrol "github.com/lazyun/devbridge/desktop/internal/runtime"
	harness "github.com/lazyun/devbridge/test/real_e2e/harness"
)

// Core exits with signal 15 on graceful SIGTERM shutdown; Go 1.26 removed
// syscall.Signal's Error method, so the matching wait error is this Errno.
var errCoreSIGTERM = syscall.Errno(int(syscall.SIGTERM))

// TestHTTPProjectCoreAPIAgainstRealCore builds the production devbridge
// binary, boots it against a temporary DataRoot, and drives the exact
// production Desktop client through register/list/restart/readback. It is the
// non-mock guard against Core route and wire-contract drift that unit mocks
// cannot catch.
func TestHTTPProjectCoreAPIAgainstRealCore(t *testing.T) {
	if testing.Short() {
		t.Skip("real Core integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := harness.NewFixture(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	binaryPath := filepath.Join(t.TempDir(), "devbridge")
	build := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "github.com/lazyun/devbridge/cmd/devbridge")
	build.Dir = repoRoot(t)
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build devbridge: %v: %s", buildErr, output)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	server := exec.CommandContext(ctx, binaryPath, "-mode", "server", "-data-root", fixture.DataRoot, "-listen", fmt.Sprintf("127.0.0.1:%d", port))
	server.Dir = fixture.Root
	server.Cancel = func() error { return errCoreSIGTERM }
	server.Stdout, server.Stderr = io.Discard, io.Discard
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = server.Process.Signal(syscall.SIGTERM)
	}()
	waitForReady(ctx, t, endpoint)

	api := newHTTPProjectCoreAPI(&http.Client{Timeout: 10 * time.Second})
	repositoryPath, err := filepath.EvalSymlinks(fixture.SourceRepo)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := api.RegisterProject(ctx, endpoint, RegisterProjectInput{Name: "Dev Bridge", RepositoryPath: repositoryPath})
	if err != nil || registered.ProjectID == "" || registered.RepositoryID == "" || registered.Name != "Dev Bridge" {
		t.Fatalf("register = %+v, err = %v", registered, err)
	}

	projects, err := api.ListProjects(ctx, endpoint)
	if err != nil || len(projects) != 1 || projects[0].ID != registered.ProjectID || projects[0].RepositoryCount != 1 {
		t.Fatalf("list = %+v, err = %v", projects, err)
	}

	duplicate, err := api.RegisterProject(ctx, endpoint, RegisterProjectInput{Name: "Duplicate", RepositoryPath: repositoryPath})
	var conflict *coreAPIError
	if err == nil || !errors.As(err, &conflict) || conflict.Code != desktopCodeProjectAlreadyRegistered || duplicate.ProjectID != registered.ProjectID || duplicate.RepositoryPath != repositoryPath {
		t.Fatalf("duplicate = %+v, err = %v", duplicate, err)
	}

	if _, err := api.RegisterProject(ctx, endpoint, RegisterProjectInput{Name: "Plain", RepositoryPath: t.TempDir()}); err == nil {
		t.Fatal("non-Git directory was accepted")
	}

	if err := server.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := server.Wait(); err != nil && !errors.Is(err, errCoreSIGTERM) && !isExitSuccess(err) {
		t.Fatalf("Core shutdown: %v", err)
	}
	restarted := exec.CommandContext(ctx, binaryPath, "-mode", "server", "-data-root", fixture.DataRoot, "-listen", fmt.Sprintf("127.0.0.1:%d", port))
	restarted.Dir = fixture.Root
	restarted.Cancel = func() error { return errCoreSIGTERM }
	restarted.Stdout, restarted.Stderr = io.Discard, io.Discard
	if err := restarted.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = restarted.Process.Kill()
		_, _ = restarted.Process.Wait()
	}()
	waitForReady(ctx, t, endpoint)
	projects, err = api.ListProjects(ctx, endpoint)
	if err != nil || len(projects) != 1 || projects[0].ID != registered.ProjectID {
		t.Fatalf("readback after restart = %+v, err = %v", projects, err)
	}
}

// TestSecondCoreOnSameDataRootFailsWithWriterLock boots two real Core
// processes against one DataRoot. The second writer must exit at startup with
// the writer-lock conflict on stderr before binding a port; the Desktop
// classifier maps that exact line to the distinct ownership conflict, never
// generic Core unavailability, and force unlock is never attempted.
func TestSecondCoreOnSameDataRootFailsWithWriterLock(t *testing.T) {
	if testing.Short() {
		t.Skip("real Core integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := harness.NewFixture(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	binaryPath := filepath.Join(t.TempDir(), "devbridge")
	build := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "github.com/lazyun/devbridge/cmd/devbridge")
	build.Dir = repoRoot(t)
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build devbridge: %v: %s", buildErr, output)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	first := exec.CommandContext(ctx, binaryPath, "-mode", "server", "-data-root", fixture.DataRoot, "-listen", fmt.Sprintf("127.0.0.1:%d", port))
	first.Dir = fixture.Root
	first.Cancel = func() error { return errCoreSIGTERM }
	first.Stdout, first.Stderr = io.Discard, io.Discard
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = first.Process.Signal(syscall.SIGTERM)
	}()
	// The first writer must hold the lock before the contender starts,
	// otherwise the two processes race for ownership.
	waitForReady(ctx, t, endpoint)

	secondStderr := &strings.Builder{}
	second := exec.CommandContext(ctx, binaryPath, "-mode", "server", "-data-root", fixture.DataRoot)
	second.Dir = fixture.Root
	second.Stdout, second.Stderr = io.Discard, secondStderr
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- second.Wait() }()
	select {
	case err := <-waitErr:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("second Core exit = %v, want exit code 1", err)
		}
	case <-ctx.Done():
		t.Fatalf("second Core did not exit: %v", ctx.Err())
	}
	output := secondStderr.String()
	if !strings.Contains(output, "already open for writing") {
		t.Fatalf("second Core stderr = %q, want writer-lock conflict", output)
	}
	if !runtimecontrol.IsWriterLockExitLine(output) {
		t.Fatalf("classifier rejected actual Core writer-lock output: %q", output)
	}
	var conflict *runtimecontrol.WriterLockConflictError
	synthetic := runtimecontrol.SynthesizeStartupError(runtimecontrol.Core, output)
	if !errors.As(synthetic, &conflict) || !errors.Is(synthetic, runtimecontrol.ErrWriterLocked) {
		t.Fatalf("classified startup error = %v, want WriterLockConflictError", synthetic)
	}
	if strings.Contains(synthetic.Error(), "force unlock") {
		t.Fatalf("classified error wording = %q", synthetic.Error())
	}

	// The first Core keeps serving: the lock conflict is an ownership fact,
	// not a transport failure of the healthy writer.
	waitForReady(ctx, t, endpoint)
}

func waitForReady(ctx context.Context, t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatalf("Core readiness: %v", ctx.Err())
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/readyz", nil)
		if err == nil {
			response, err := http.DefaultClient.Do(request)
			if err == nil {
				body, _ := io.ReadAll(response.Body)
				if response.StatusCode == http.StatusOK && strings.Contains(string(body), "true") {
					return
				}
				_ = response.Body.Close()
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("Core did not become ready within 30s")
}

func isExitSuccess(err error) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode() == 0
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(directory, ".."))
}
