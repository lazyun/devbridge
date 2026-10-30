package worktree

import "errors"

var (
	ErrNotFound  = errors.New("worktree not found")
	ErrCollision = errors.New("worktree path or branch collision")
	ErrNotOwned  = errors.New("worktree is not Bridge-owned")
)
