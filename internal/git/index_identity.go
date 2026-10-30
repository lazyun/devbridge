package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// IndexIdentity observes logical staged entries without refreshing or writing
// the real index. Worktree snapshots alone do not detect staging existing bytes.
func (c *Client) IndexIdentity(ctx context.Context, repoPath string) (string, error) {
	entries, err := c.run(ctx, repoPath, "observe staged entries", "ls-files", "--stage", "-z", "--")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(entries)
	return hex.EncodeToString(sum[:]), nil
}
