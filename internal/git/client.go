package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lazyun/devbridge/internal/reviewaccess"
)

const DefaultMaxOutput = 8 << 20

type Config struct {
	Executable string
	MaxOutput  int
}

type Client struct {
	executable     string
	maxOutput      int
	beforeFileOpen func(string)
}

type SearchMatch struct {
	Path    string
	Line    int
	Snippet string
}

// SearchFixed performs a literal search over live tracked-file contents.
func (c *Client) SearchFixed(ctx context.Context, repoPath, query string, paths []string) ([]SearchMatch, error) {
	root, err := reviewaccess.OpenRoot(repoPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return c.SearchFixedRoot(ctx, root, query, paths)
}

// SearchRepository adapts the Git-native tracked-file search to the trusted
// Codex Review capability without exposing Git process details to that layer.
// It applies no semantic filename exclusions.
func (c *Client) SearchRepository(ctx context.Context, root *reviewaccess.Root, query string, paths []string) ([]reviewaccess.GitSearchMatch, error) {
	values, err := c.SearchFixedRoot(ctx, root, query, paths)
	if err != nil {
		return nil, err
	}
	result := make([]reviewaccess.GitSearchMatch, 0, len(values))
	for _, value := range values {
		result = append(result, reviewaccess.GitSearchMatch{Path: value.Path, Line: value.Line, Snippet: value.Snippet})
	}
	return result, nil
}

// SearchFixedRoot searches the live contents of tracked regular files through
// an already-opened Repository root capability. Git supplies only untrusted
// index path metadata; every content open remains handle-relative to root.
func (c *Client) SearchFixedRoot(ctx context.Context, root *reviewaccess.Root, query string, paths []string) ([]SearchMatch, error) {
	if query == "" || strings.ContainsRune(query, '\x00') {
		return nil, errors.New("fixed search query is required")
	}
	if root == nil || root.Path() == "" {
		return nil, errors.New("Repository root capability is required")
	}
	filters := make([]searchFilter, 0, len(paths))
	for _, supplied := range paths {
		normalized, directory, err := root.Resolve(reviewaccess.OperationSearch, supplied)
		if err != nil {
			return nil, err
		}
		filters = append(filters, searchFilter{path: normalized, directory: directory})
	}
	out, err := c.run(ctx, root.Path(), "list tracked repository files", "ls-files", "-s", "-z", "--")
	if err != nil {
		return nil, err
	}
	tracked, err := parseTrackedRegularFiles(out)
	if err != nil {
		return nil, err
	}
	result := make([]SearchMatch, 0)
	outputSize := 0
	for _, path := range tracked {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !matchesSearchFilters(path, filters) {
			continue
		}
		if c.beforeFileOpen != nil {
			c.beforeFileOpen(path)
		}
		file, err := root.OpenTrackedFile(reviewaccess.OperationSearch, path)
		if err != nil {
			return nil, err
		}
		matches, scanErr := scanFixed(file, path, query, c.maxOutput-outputSize, c.maxOutput)
		_ = file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		for _, match := range matches {
			outputSize += len(match.Path) + len(match.Snippet) + 32
			if outputSize > c.maxOutput {
				return nil, &OutputLimitError{Operation: "fixed repository search", Limit: c.maxOutput}
			}
			result = append(result, match)
		}
	}
	return result, nil
}

type searchFilter struct {
	path      string
	directory bool
}

func matchesSearchFilters(path string, filters []searchFilter) bool {
	if len(filters) == 0 {
		return true
	}
	for _, filter := range filters {
		if path == filter.path || filter.directory && strings.HasPrefix(path, filter.path+"/") {
			return true
		}
	}
	return false
}

func parseTrackedRegularFiles(out []byte) ([]string, error) {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, entry := range splitNUL(out) {
		metadata, pathBytes, ok := bytes.Cut([]byte(entry), []byte{'\t'})
		fields := strings.Fields(string(metadata))
		if !ok || len(fields) != 3 {
			return nil, errors.New("parse tracked Repository entry")
		}
		if fields[0] != "100644" && fields[0] != "100755" {
			continue
		}
		path := filepath.ToSlash(string(pathBytes))
		if err := reviewaccess.RepositoryBoundary().ValidatePaths(reviewaccess.OperationSearch, []string{path}); err != nil {
			return nil, err
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result, nil
}

func scanFixed(file *os.File, path, query string, remaining, outputLimit int) ([]SearchMatch, error) {
	scanner := bufio.NewScanner(file)
	bufferLimit := max(outputLimit, 64<<10)
	scanner.Buffer(make([]byte, 64<<10), bufferLimit)
	result := make([]SearchMatch, 0)
	line := 0
	binary := false
	for scanner.Scan() {
		line++
		value := scanner.Bytes()
		if bytes.IndexByte(value, 0) >= 0 {
			binary = true
		}
		if !binary && bytes.Contains(value, []byte(query)) {
			if len(value)+len(path)+32 > remaining {
				return nil, &OutputLimitError{Operation: "fixed repository search", Limit: outputLimit}
			}
			remaining -= len(value) + len(path) + 32
			result = append(result, SearchMatch{Path: path, Line: line, Snippet: string(value)})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if binary {
		return nil, nil
	}
	return result, nil
}

func NewClient(config Config) *Client {
	executable := config.Executable
	if executable == "" {
		executable = "git"
	}
	limit := config.MaxOutput
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	return &Client{executable: executable, maxOutput: limit}
}

func (c *Client) ResolveHEAD(ctx context.Context, repoPath string) (string, error) {
	return c.ResolveCommit(ctx, repoPath, "HEAD")
}

// RepositoryRoot returns the canonical working-tree root reported by Git.
// Callers may supply a directory below the root; bare repositories are not a
// valid source workspace and therefore do not have a top-level directory.
func (c *Client) RepositoryRoot(ctx context.Context, path string) (string, error) {
	out, err := c.run(ctx, path, "resolve repository root", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("Git returned an invalid repository root")
	}
	return filepath.Clean(root), nil
}

func (c *Client) ResolveCommit(ctx context.Context, repoPath, revision string) (string, error) {
	if revision == "" {
		return "", errors.New("git revision is required")
	}
	out, err := c.run(ctx, repoPath, "resolve commit", "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(out))
	if !isObjectID(commit) {
		return "", fmt.Errorf("resolve commit returned invalid object id %q", commit)
	}
	return commit, nil
}

func (c *Client) CurrentBranch(ctx context.Context, repoPath string) (string, error) {
	out, err := c.run(ctx, repoPath, "current branch", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RepositoryState is the exact observable source-worktree state used when an
// operation freezes an immutable commit baseline. BranchRef is a full symbolic
// ref when Detached is false; a detached HEAD is represented explicitly and is
// never guessed to be main or master.
type RepositoryState struct {
	HeadCommit       string
	BranchRef        string
	Detached         bool
	WorkingTreeDirty bool
}

// InspectRepositoryState resolves HEAD, symbolic-ref identity, and dirty state
// from the same registered working tree. A successful empty symbolic-ref is
// the documented detached-HEAD result (exit status 1 is accepted).
func (c *Client) InspectRepositoryState(ctx context.Context, repoPath string) (RepositoryState, error) {
	head, err := c.ResolveHEAD(ctx, repoPath)
	if err != nil {
		return RepositoryState{}, err
	}
	ref, err := c.runAllowExitOne(ctx, repoPath, "current symbolic ref", "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return RepositoryState{}, err
	}
	status, err := c.Status(ctx, repoPath)
	if err != nil {
		return RepositoryState{}, err
	}
	branchRef := strings.TrimSpace(string(ref))
	return RepositoryState{HeadCommit: head, BranchRef: branchRef, Detached: branchRef == "", WorkingTreeDirty: status.Dirty()}, nil
}

func (c *Client) Status(ctx context.Context, repoPath string) (Status, error) {
	out, err := c.run(ctx, repoPath, "status", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return Status{}, err
	}
	return parseStatus(out)
}

func (c *Client) IsClean(ctx context.Context, repoPath string) (bool, error) {
	status, err := c.Status(ctx, repoPath)
	return !status.Dirty(), err
}

func (c *Client) ChangedFiles(ctx context.Context, repoPath, baseCommit string) ([]string, error) {
	if _, err := c.ResolveCommit(ctx, repoPath, baseCommit); err != nil {
		return nil, err
	}
	out, err := c.run(ctx, repoPath, "changed files", "diff", "--name-only", "-z", "--no-renames", baseCommit, "--")
	if err != nil {
		return nil, err
	}
	paths := splitNUL(out)
	status, err := c.Status(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	for _, file := range status.Files {
		if file.Untracked {
			paths = append(paths, file.Path)
		}
	}
	return uniqueSorted(paths), nil
}

func (c *Client) BranchExists(ctx context.Context, repoPath, branch string) (bool, error) {
	_, err := c.run(ctx, repoPath, "check branch", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) {
		var exitErr *exec.ExitError
		if errors.As(commandErr.Err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
	}
	return false, err
}

func (c *Client) WorktreeAdd(ctx context.Context, repoPath, path, branch, baseCommit string) error {
	_, err := c.run(ctx, repoPath, "add worktree", "worktree", "add", "-b", branch, path, baseCommit)
	return err
}

func (c *Client) WorktreeRemove(ctx context.Context, repoPath, path string) error {
	_, err := c.run(ctx, repoPath, "remove worktree", "worktree", "remove", "--force", path)
	return err
}

func (c *Client) DeleteBranch(ctx context.Context, repoPath, branch string) error {
	_, err := c.run(ctx, repoPath, "delete branch", "branch", "-D", branch)
	return err
}

func (c *Client) Diff(ctx context.Context, repoPath, baseCommit string) (string, error) {
	if _, err := c.ResolveCommit(ctx, repoPath, baseCommit); err != nil {
		return "", err
	}
	tracked, err := c.run(ctx, repoPath, "diff", "diff", "--binary", "--no-ext-diff", "--no-renames", baseCommit, "--")
	if err != nil {
		return "", err
	}
	status, err := c.Status(ctx, repoPath)
	if err != nil {
		return "", err
	}
	var combined bytes.Buffer
	if err := appendBounded(&combined, tracked, c.maxOutput, "diff"); err != nil {
		return "", err
	}
	for _, file := range status.Files {
		if !file.Untracked {
			continue
		}
		patch, diffErr := c.runAllowExitOne(ctx, repoPath, "diff untracked file", "diff", "--no-index", "--binary", "--", "/dev/null", filepath.FromSlash(file.Path))
		if diffErr != nil {
			return "", diffErr
		}
		if err := appendBounded(&combined, patch, c.maxOutput, "diff"); err != nil {
			return "", err
		}
	}
	return combined.String(), nil
}

// Snapshot writes the complete tracked and non-ignored working-tree state as
// an immutable Git tree using an isolated temporary index. It never changes
// the worktree's real index or files.
func (c *Client) Snapshot(ctx context.Context, repoPath string) (string, error) {
	if _, err := c.ResolveHEAD(ctx, repoPath); err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "devbridge-git-index-")
	if err != nil {
		return "", fmt.Errorf("create temporary Git index: %w", err)
	}
	indexPath := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(indexPath)
		return "", fmt.Errorf("close temporary Git index: %w", err)
	}
	if err := os.Remove(indexPath); err != nil {
		return "", fmt.Errorf("prepare temporary Git index: %w", err)
	}
	defer os.Remove(indexPath)
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := c.runEnv(ctx, repoPath, "initialize snapshot index", env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := c.runEnv(ctx, repoPath, "stage snapshot state", env, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	out, err := c.runEnv(ctx, repoPath, "write snapshot tree", env, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if !isObjectID(tree) {
		return "", fmt.Errorf("write snapshot tree returned invalid object id %q", tree)
	}
	return tree, nil
}

func (c *Client) ChangedFilesBetween(ctx context.Context, repoPath, startTree, endTree string) ([]string, error) {
	if err := c.verifyTree(ctx, repoPath, startTree); err != nil {
		return nil, err
	}
	if err := c.verifyTree(ctx, repoPath, endTree); err != nil {
		return nil, err
	}
	out, err := c.run(ctx, repoPath, "changed files between snapshots", "diff", "--name-only", "-z", "--no-renames", startTree, endTree, "--")
	if err != nil {
		return nil, err
	}
	return uniqueSorted(splitNUL(out)), nil
}

func (c *Client) DiffSnapshots(ctx context.Context, repoPath, startTree, endTree string) (string, error) {
	if err := c.verifyTree(ctx, repoPath, startTree); err != nil {
		return "", err
	}
	if err := c.verifyTree(ctx, repoPath, endTree); err != nil {
		return "", err
	}
	out, err := c.run(ctx, repoPath, "diff snapshots", "diff", "--binary", "--no-ext-diff", "--no-renames", startTree, endTree, "--")
	return string(out), err
}

func (c *Client) verifyTree(ctx context.Context, repoPath, value string) error {
	if !isObjectID(value) {
		return fmt.Errorf("invalid Git tree object id %q", value)
	}
	out, err := c.run(ctx, repoPath, "verify snapshot tree", "rev-parse", "--verify", value+"^{tree}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != value {
		return fmt.Errorf("snapshot %q did not resolve to itself", value)
	}
	return nil
}

func (c *Client) run(ctx context.Context, repoPath, operation string, args ...string) ([]byte, error) {
	return c.runWithExitEnv(ctx, repoPath, operation, false, nil, args...)
}

func (c *Client) runAllowExitOne(ctx context.Context, repoPath, operation string, args ...string) ([]byte, error) {
	return c.runWithExitEnv(ctx, repoPath, operation, true, nil, args...)
}

func (c *Client) runEnv(ctx context.Context, repoPath, operation string, env []string, args ...string) ([]byte, error) {
	return c.runWithExitEnv(ctx, repoPath, operation, false, env, args...)
}

func (c *Client) runWithExitEnv(ctx context.Context, repoPath, operation string, allowOne bool, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.executable, args...)
	cmd.Dir = repoPath
	cmd.Env = []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.Env = append(cmd.Env, env...)
	stdout := &limitedBuffer{limit: c.maxOutput}
	stderr := &limitedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if stdout.exceeded {
		return nil, &OutputLimitError{Operation: operation, Limit: c.maxOutput}
	}
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		var exitErr *exec.ExitError
		if !(allowOne && errors.As(err, &exitErr) && exitErr.ExitCode() == 1) {
			return nil, &CommandError{Operation: operation, Args: append([]string(nil), args...), Stderr: strings.TrimSpace(stderr.String()), Err: err}
		}
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit + 1 - b.Len()
	if remaining > 0 {
		if remaining < len(p) {
			_, _ = b.Buffer.Write(p[:remaining])
		} else {
			_, _ = b.Buffer.Write(p)
		}
	}
	if b.Len() > b.limit {
		b.exceeded = true
	}
	return n, nil
}

func appendBounded(dst *bytes.Buffer, value []byte, limit int, operation string) error {
	if dst.Len()+len(value) > limit {
		return &OutputLimitError{Operation: operation, Limit: limit}
	}
	_, _ = dst.Write(value)
	return nil
}

func parseStatus(out []byte) (Status, error) {
	fields := splitNUL(out)
	result := Status{}
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 3 || entry[2] != ' ' {
			return Status{}, fmt.Errorf("parse git status entry %q", entry)
		}
		file := FileStatus{Index: entry[0], Worktree: entry[1], Path: filepath.ToSlash(entry[3:])}
		file.Untracked = file.Index == '?' && file.Worktree == '?'
		if file.Index == 'R' || file.Index == 'C' {
			i++
			if i >= len(fields) {
				return Status{}, errors.New("parse git status rename without source")
			}
			file.Original = filepath.ToSlash(fields[i])
		}
		result.Files = append(result.Files, file)
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}

func splitNUL(value []byte) []string {
	parts := bytes.Split(value, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			result = append(result, string(part))
		}
	}
	return result
}

func uniqueSorted(paths []string) []string {
	set := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		set[filepath.ToSlash(path)] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for path := range set {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func isObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
