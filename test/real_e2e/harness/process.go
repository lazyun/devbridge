package harness

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type CommandSpec struct {
	Path string
	Args []string
	Env  []string
	Dir  string
}

type ChildProcess struct {
	cmd        *exec.Cmd
	stdout     *os.File
	stderr     *os.File
	waitOnce   sync.Once
	waitErr    error
	StdoutPath string
	StderrPath string
}

func StartChild(spec CommandSpec, artifactDir, label string) (*ChildProcess, error) {
	if spec.Path == "" {
		return nil, errors.New("child command path is required")
	}
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return nil, fmt.Errorf("create child artifact directory: %w", err)
	}
	child := &ChildProcess{
		StdoutPath: filepath.Join(artifactDir, label+".stdout.log"),
		StderrPath: filepath.Join(artifactDir, label+".stderr.log"),
	}
	var err error
	child.stdout, err = os.OpenFile(child.StdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	child.stderr, err = os.OpenFile(child.StderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = child.stdout.Close()
		return nil, err
	}
	child.cmd = exec.Command(spec.Path, spec.Args...)
	child.cmd.Dir = spec.Dir
	child.cmd.Env = append(os.Environ(), spec.Env...)
	child.cmd.Stdout = child.stdout
	child.cmd.Stderr = child.stderr
	if err := child.cmd.Start(); err != nil {
		_ = child.stdout.Close()
		_ = child.stderr.Close()
		return nil, fmt.Errorf("start child: %w", err)
	}
	return child, nil
}

// KillNonGracefully deliberately uses the OS kill operation rather than a
// signal the Bridge can convert into an orderly shutdown.
func (c *ChildProcess) KillNonGracefully() error {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return errors.New("child is not running")
	}
	return c.cmd.Process.Kill()
}

func (c *ChildProcess) Wait() error {
	if c == nil || c.cmd == nil {
		return errors.New("child is not running")
	}
	c.waitOnce.Do(func() {
		c.waitErr = c.cmd.Wait()
		_ = c.stdout.Close()
		_ = c.stderr.Close()
	})
	return c.waitErr
}

func (c *ChildProcess) PID() int {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}
