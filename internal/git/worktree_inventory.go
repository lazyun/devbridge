package git

import (
	"context"
	"fmt"
	"strings"
)

// RegisteredWorktree is Git registration, not a statement about directory
// existence. Porcelain -z preserves paths containing spaces and newlines.
type RegisteredWorktree struct {
	Path, Head, BranchRef            string
	Detached, Bare, Locked, Prunable bool
}

func (c *Client) RegisteredWorktrees(ctx context.Context, repositoryPath string) ([]RegisteredWorktree, error) {
	out, err := c.readBranchFacts(ctx, repositoryPath, false, "read worktree registrations", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	result := []RegisteredWorktree{}
	var item *RegisteredWorktree
	for _, field := range strings.Split(string(out), "\x00") {
		key, value, _ := strings.Cut(field, " ")
		if key == "worktree" {
			result = append(result, RegisteredWorktree{Path: value})
			item = &result[len(result)-1]
			continue
		}
		if item == nil {
			continue
		}
		switch key {
		case "HEAD":
			item.Head = value
		case "branch":
			item.BranchRef = value
		case "detached":
			item.Detached = true
		case "bare":
			item.Bare = true
		case "locked":
			item.Locked = true
		case "prunable":
			item.Prunable = true
		}
	}
	return result, nil
}

// InventoryStatus verifies the checkout root before reading status so a
// leftover directory cannot accidentally inherit its parent's repository.
func (c *Client) InventoryStatus(ctx context.Context, path string) (string, Status, error) {
	out, err := c.readBranchFacts(ctx, path, false, "read worktree root", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", Status{}, err
	}
	root := strings.TrimSuffix(string(out), "\n")
	out, err = c.readBranchFacts(ctx, path, false, "read worktree status", "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return root, Status{}, err
	}
	status, err := parseStatus(out)
	return root, status, err
}

func (c *Client) InventoryCommit(ctx context.Context, path, ref string) (string, error) {
	out, err := c.readBranchFacts(ctx, path, false, "read inventory commit", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if !isObjectID(sha) {
		return "", fmt.Errorf("invalid inventory commit")
	}
	return sha, nil
}

func (c *Client) InventoryAncestor(ctx context.Context, path, candidate, head string) (bool, error) {
	if !isObjectID(candidate) || !isObjectID(head) {
		return false, fmt.Errorf("invalid inventory ancestry target")
	}
	// --is-ancestor's exit code is significant; use merge-base output instead
	// so the read-only environment can retain the existing bounded helper.
	out, err := c.readBranchFacts(ctx, path, true, "read inventory ancestry", "merge-base", candidate, head)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == candidate, nil
}
