package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateManagedWorktree verifies Git identity without inspecting Agent behavior
// or treating prior uncommitted work as drift.
func (c *Client) ValidateManagedWorktree(ctx context.Context, source, path, branch string) error {
	common := func(root string) (string, error) {
		out, err := c.run(ctx, root, "workspace common directory", "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	}
	a, err := common(source)
	if err != nil {
		return err
	}
	b, err := common(path)
	if err != nil {
		return err
	}
	if a != b {
		return fmt.Errorf("workspace belongs to another repository")
	}
	out, err := c.run(ctx, path, "workspace branch", "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "refs/heads/"+branch {
		return fmt.Errorf("workspace branch identity changed")
	}
	return nil
}
