package git

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/reviewaccess"
)

// ReadCommittedFileRoot reads one exact regular Git blob, without checkout,
// filters, hooks, worktree creation or traversal. Only full object IDs enter.
func (c *Client) ReadCommittedFileRoot(ctx context.Context, root *reviewaccess.Root, commit, path string) ([]byte, error) {
	if root == nil || root.Path() == "" || !isObjectID(commit) {
		return nil, errors.New("invalid exact repository commit")
	}
	if err := reviewaccess.RepositoryBoundary().ValidatePaths(reviewaccess.OperationRead, []string{path}); err != nil {
		return nil, err
	}
	resolved, err := c.readFileCommit(ctx, root.Path(), commit)
	if err != nil {
		return nil, err
	}
	if resolved != commit {
		return nil, errors.New("repository commit identity mismatch")
	}
	out, err := c.runEnv(ctx, root.Path(), "read exact Git tree entry", fileReadGitEnv(), "--literal-pathspecs", "ls-tree", "-z", commit, "--", path)
	if err != nil {
		return nil, err
	}
	entries := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	if len(entries) != 1 {
		return nil, errors.New("repository file is not one exact tree entry")
	}
	pair := bytes.SplitN(entries[0], []byte{'\t'}, 2)
	if len(pair) != 2 || string(pair[1]) != path {
		return nil, errors.New("repository file not found in exact commit")
	}
	fields := strings.Fields(string(pair[0]))
	if len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" || !isObjectID(fields[2]) {
		return nil, errors.New("repository historical path is not a regular file")
	}
	return c.runEnv(ctx, root.Path(), "read exact Git blob", fileReadGitEnv(), "cat-file", "blob", fields[2])
}

func fileReadGitEnv() []string {
	return []string{"GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1"}
}
func (c *Client) readFileCommit(ctx context.Context, root, revision string) (string, error) {
	out, err := c.runEnv(ctx, root, "resolve exact file-read commit", fileReadGitEnv(), "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if !isObjectID(id) {
		return "", errors.New("invalid file-read commit identity")
	}
	return id, nil
}
func (c *Client) ReadFileHEAD(ctx context.Context, root string) (string, error) {
	return c.readFileCommit(ctx, root, "HEAD")
}
