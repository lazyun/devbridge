package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/codexlaunch"
)

const (
	stderrCaptureLimit = 64 << 10
	stderrReadSize     = 16 << 10
)

type appServerProcess struct {
	startedAt time.Time
	endedAt   time.Time
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser

	done       chan struct{}
	stderrDone chan struct{}
	mu         sync.Mutex
	waitErr    error
	capture    limitedCapture
	closeOnce  sync.Once
}

func startAppServer(codexPath string, extra ...[]string) (*appServerProcess, error) {
	selection, err := codexlaunch.Runtime(codexPath)
	if err != nil {
		return nil, err
	}
	return startAppServerExecutable(selection.Executable, extra...)
}

func startVerifiedAppServer(ctx context.Context, executable codexlaunch.VerifiedExecutable, extra ...[]string) (*appServerProcess, error) {
	path, err := executable.Executable(ctx)
	if err != nil {
		return nil, err
	}
	// The verifier selected the native file. Never resolve Desktop Settings or
	// execute the package wrapper again after verifying that file's identity.
	if err = codexlaunch.Validate(path); err != nil {
		return nil, err
	}
	return startAppServerExecutable(path, extra...)
}

func startAppServerExecutable(path string, extra ...[]string) (*appServerProcess, error) {
	args := []string{"app-server"}
	for _, values := range extra {
		args = append(args, values...)
	}
	process, err := startOwnedProcess(path, codexlaunch.ExecutorArgs(args), codexlaunch.Environment(os.Environ(), path))
	if err != nil {
		return nil, fmt.Errorf("CODEX_APP_SERVER_START_FAILED: %q: %w", path, err)
	}
	if process.cmd.Path != path {
		_ = process.Close()
		return nil, fmt.Errorf("CODEX_EXECUTABLE_MISMATCH: selected %q, launched %q", path, process.cmd.Path)
	}
	return process, nil
}

func startOwnedProcess(path string, args, env []string) (*appServerProcess, error) {
	cmd := exec.Command(path, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	p := &appServerProcess{startedAt: time.Now().UTC(),
		cmd:        cmd,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		done:       make(chan struct{}),
		stderrDone: make(chan struct{}),
	}
	go func() {
		defer close(p.stderrDone)
		buffer := make([]byte, stderrReadSize)
		for {
			n, readErr := stderr.Read(buffer)
			if n > 0 {
				_, _ = p.capture.Write(buffer[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		p.endedAt = time.Now().UTC()
		p.mu.Lock()
		p.waitErr = err
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

func (p *appServerProcess) Close() error {
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		select {
		case <-p.done:
		case <-time.After(750 * time.Millisecond):
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Signal(os.Interrupt)
			}
			select {
			case <-p.done:
			case <-time.After(750 * time.Millisecond):
				if p.cmd.Process != nil {
					_ = p.cmd.Process.Kill()
				}
				<-p.done
			}
		}
		_ = p.stdout.Close()
		_ = p.stderr.Close()
		<-p.stderrDone
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(p.waitErr, &exitErr) {
			return p.waitErr
		}
	}
	return nil
}

func (p *appServerProcess) waitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *appServerProcess) stderrText() string { return p.capture.String() }

type limitedCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *limitedCapture) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := stderrCaptureLimit - w.buf.Len()
	if remaining > 0 {
		if len(data) < remaining {
			remaining = len(data)
		}
		_, _ = w.buf.Write(data[:remaining])
	}
	return len(data), nil
}

func (w *limitedCapture) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
