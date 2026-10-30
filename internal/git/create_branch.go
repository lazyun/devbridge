package git

import (
	"context"
	"errors"
	"strings"
)

var ErrBranchConflict = errors.New("DEVELOPMENT_BRANCH_CREATE_CONFLICT")
var ErrInvalidBranchArgument = errors.New("invalid local branch creation argument")

// CreateLocalBranch creates exactly one ref at an immutable commit. The zero
// old object ID fences concurrent creation; neither HEAD nor the index moves.
// Exact existing entity state is replayable, but is not a durable request receipt.
func (c *Client) CreateLocalBranch(ctx context.Context, path, ref, commit string) (LocalBranch, error) {
	if !strings.HasPrefix(ref, "refs/heads/") || !isObjectID(commit) {
		return LocalBranch{}, ErrInvalidBranchArgument
	}
	if _, err := c.readBranchFacts(ctx, path, false, "validate branch ref", "check-ref-format", ref); err != nil {
		return LocalBranch{}, ErrInvalidBranchArgument
	}
	typ, err := c.readBranchFacts(ctx, path, false, "validate branch commit", "cat-file", "-t", commit)
	if err != nil || strings.TrimSpace(string(typ)) != "commit" {
		return LocalBranch{}, ErrInvalidBranchArgument
	}
	_, createErr := c.readBranchFacts(ctx, path, false, "create local branch", "update-ref", "--no-deref", ref, commit, strings.Repeat("0", len(commit)))
	inventory, err := c.LocalBranches(ctx, path)
	if err != nil {
		return LocalBranch{}, err
	}
	for _, branch := range inventory.Branches {
		if branch.FullRef == ref {
			if branch.HeadSHA != commit {
				return LocalBranch{}, ErrBranchConflict
			}
			return branch, nil
		}
	}
	if createErr != nil {
		return LocalBranch{}, createErr
	}
	return LocalBranch{}, ErrBranchConflict
}
