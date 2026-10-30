package consoleapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/worktreeinventory"
)

func (r *Reader) GetWorktreeInventory(ctx context.Context, projectID string) (worktreeinventory.Inventory, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return worktreeinventory.Inventory{}, err
	}
	return r.control.WorktreeInventory(ctx, projectID)
}
