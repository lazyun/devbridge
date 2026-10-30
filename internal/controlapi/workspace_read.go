package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/workspace"
)

// WorkspaceReadService exposes the persisted facts of an exact historical Run workspace.
type WorkspaceReadService interface {
	Get(context.Context, string) (workspace.Workspace, error)
}
