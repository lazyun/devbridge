package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const fixtureCommitDate = "2001-02-03T04:05:06Z"

type Fixture struct {
	Root       string
	SourceRepo string
	BaseCommit string
	DataRoot   string
	Artifacts  string
}

type RepositorySnapshot struct {
	Path         string   `json:"path"`
	HEAD         string   `json:"head"`
	Status       string   `json:"status"`
	ChangedFiles []string `json:"changed_files"`
	Diff         string   `json:"diff"`
}

type ExecutionTarget struct {
	Name       string
	Path       string
	Branch     string
	BaseCommit string
}

// NewFixture creates a self-contained repository and durable-state directory.
// The caller owns root and decides whether to clean or preserve it.
func NewFixture(ctx context.Context, root string) (*Fixture, error) {
	if root == "" {
		return nil, errors.New("fixture root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve fixture root: %w", err)
	}
	fixture := &Fixture{
		Root:       absolute,
		SourceRepo: filepath.Join(absolute, "source"),
		DataRoot:   filepath.Join(absolute, "data"),
		Artifacts:  filepath.Join(absolute, "artifacts"),
	}
	if _, err := os.Stat(fixture.SourceRepo); err == nil {
		return nil, fmt.Errorf("fixture source path already exists: %s", fixture.SourceRepo)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect fixture source path: %w", err)
	}
	for _, directory := range []string{fixture.SourceRepo, fixture.DataRoot, fixture.Artifacts, filepath.Join(absolute, "targets")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", directory, err)
		}
	}
	if _, err := fixture.git(ctx, fixture.SourceRepo, "init", "-b", "main"); err != nil {
		return nil, err
	}
	if _, err := fixture.git(ctx, fixture.SourceRepo, "config", "user.name", "Dev Bridge Real E2E"); err != nil {
		return nil, err
	}
	if _, err := fixture.git(ctx, fixture.SourceRepo, "config", "user.email", "real-e2e@devbridge.invalid"); err != nil {
		return nil, err
	}
	files := map[string]string{
		"README.md":                         "# Disposable Dev Bridge target\n",
		"tracked.txt":                       "base line\n",
		"tests/review_policy_test.txt":      "base line\n",
		"config/settings.txt":               "mode=fixture\n",
		"allowed/target.txt":                "allowed base\n",
		"allowed/scope_fixture_mutation.sh": "#!/bin/sh\nset -eu\nprintf 'allowed portion of violation\\n' > allowed/target.txt\nprintf 'deliberate unauthorized mutation\\n' > outside/protected.txt\n",
		"outside/protected.txt":             "protected base\n",
	}
	for name, contents := range files {
		path := filepath.Join(fixture.SourceRepo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			return nil, err
		}
	}
	symlinkTarget := filepath.Join(absolute, "targets", "outside-repository.txt")
	if err := os.WriteFile(symlinkTarget, []byte("outside repository\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.Symlink("../targets/outside-repository.txt", filepath.Join(fixture.SourceRepo, "escape-link")); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Join(fixture.SourceRepo, "allowed", "scope_fixture_mutation.sh"), 0o700); err != nil {
		return nil, err
	}
	if _, err := fixture.git(ctx, fixture.SourceRepo, "add", "."); err != nil {
		return nil, err
	}
	if _, err := fixture.git(ctx, fixture.SourceRepo, "commit", "-m", "deterministic fixture base"); err != nil {
		return nil, err
	}
	base, err := fixture.git(ctx, fixture.SourceRepo, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	fixture.BaseCommit = strings.TrimSpace(base)
	snapshot, err := fixture.Inspect(ctx, fixture.SourceRepo)
	if err != nil {
		return nil, err
	}
	if snapshot.Status != "" {
		return nil, fmt.Errorf("new fixture is dirty: %s", snapshot.Status)
	}
	return fixture, nil
}

func (f *Fixture) NewExecutionTarget(ctx context.Context, name string) (ExecutionTarget, error) {
	if f == nil {
		return ExecutionTarget{}, errors.New("fixture is required")
	}
	if !safeTargetName(name) {
		return ExecutionTarget{}, fmt.Errorf("unsafe target name %q", name)
	}
	target := ExecutionTarget{
		Name:       name,
		Path:       filepath.Join(f.Root, "targets", name),
		Branch:     "real-e2e/" + name,
		BaseCommit: f.BaseCommit,
	}
	if _, err := f.git(ctx, f.SourceRepo, "worktree", "add", "-b", target.Branch, target.Path, f.BaseCommit); err != nil {
		return ExecutionTarget{}, err
	}
	return target, nil
}

func (f *Fixture) TwoExecutionTargets(ctx context.Context) ([2]ExecutionTarget, error) {
	var targets [2]ExecutionTarget
	first, err := f.NewExecutionTarget(ctx, "task-a-run-a")
	if err != nil {
		return targets, err
	}
	second, err := f.NewExecutionTarget(ctx, "task-b-run-b")
	if err != nil {
		return targets, err
	}
	targets[0], targets[1] = first, second
	return targets, nil
}

func (f *Fixture) SourceStatus(ctx context.Context) (RepositorySnapshot, error) {
	return f.Inspect(ctx, f.SourceRepo)
}

func (f *Fixture) WorktreeStatus(ctx context.Context, target ExecutionTarget) (RepositorySnapshot, error) {
	return f.Inspect(ctx, target.Path)
}

func (f *Fixture) Inspect(ctx context.Context, path string) (RepositorySnapshot, error) {
	head, err := f.git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return RepositorySnapshot{}, err
	}
	status, err := f.git(ctx, path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return RepositorySnapshot{}, err
	}
	changed, err := f.git(ctx, path, "diff", "--name-only", f.BaseCommit, "--")
	if err != nil {
		return RepositorySnapshot{}, err
	}
	untracked, err := f.git(ctx, path, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return RepositorySnapshot{}, err
	}
	files := append(nonemptyLines(changed), nonemptyLines(untracked)...)
	sort.Strings(files)
	files = compact(files)
	diff, err := f.git(ctx, path, "diff", "--no-ext-diff", "--binary", f.BaseCommit, "--")
	if err != nil {
		return RepositorySnapshot{}, err
	}
	for _, name := range nonemptyLines(untracked) {
		untrackedDiff, err := f.diffUntracked(ctx, path, name)
		if err != nil {
			return RepositorySnapshot{}, err
		}
		diff += untrackedDiff
	}
	return RepositorySnapshot{Path: path, HEAD: strings.TrimSpace(head), Status: status, ChangedFiles: files, Diff: diff}, nil
}

func (f *Fixture) diffUntracked(ctx context.Context, directory, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-index", "--no-ext-diff", "--binary", "--", "/dev/null", name)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	output, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return "", fmt.Errorf("diff untracked file %s: %w: %s", name, err, strings.TrimSpace(string(output)))
		}
	}
	return string(output), nil
}

func (f *Fixture) git(ctx context.Context, directory string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+fixtureCommitDate,
		"GIT_COMMITTER_DATE="+fixtureCommitDate,
		"LC_ALL=C",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), directory, err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func safeTargetName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func nonemptyLines(value string) []string {
	var result []string
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func compact(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
