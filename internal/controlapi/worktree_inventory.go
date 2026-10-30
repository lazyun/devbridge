package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/worktreeinventory"
)

// WorktreeInventory is the same read model for local Console/JSON clients.
// It admits only a registered Project; no client supplies filesystem paths.
func (f *Facade) WorktreeInventory(ctx context.Context, projectID string) (worktreeinventory.Inventory, error) {
	if _, err := f.deps.Projects.Get(ctx, projectID); err != nil {
		return worktreeinventory.Inventory{}, MapError(err)
	}
	if f.deps.WorktreeInventory == nil {
		return worktreeinventory.Inventory{}, &Error{Code: CodeInfrastructure, Message: "Worktree inventory unavailable"}
	}
	out, err := f.deps.WorktreeInventory.Read(ctx, projectID)
	if err != nil {
		return out, MapError(err)
	}
	return out, nil
}
