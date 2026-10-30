package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

type CodexHistoryReader func(context.Context, string, string, string, string, int) (codex.HistoryPage, error)

func nativeHistoryReader(plane *controlplane.ControlPlane) CodexHistoryReader {
	return func(ctx context.Context, runID, thread, turn, cursor string, limit int) (codex.HistoryPage, error) {
		binding, err := sqlite.NewAgentRuntimeStore(plane.DB).AppServerBinding(ctx, runID)
		if err != nil {
			return codex.HistoryPage{}, err
		}
		return codex.ReadPersistedHistory(ctx, binding, plane.DataPaths.Root, thread, turn, cursor, limit)
	}
}

// ReadCodexHistory is an internal, trusted Project read used by Console after
// its owner checks. It has no Session alias and is not a model-visible command.
func (f *Facade) ReadCodexHistory(ctx context.Context, project, task, runID, cursor string, limit int) (codex.HistoryPage, error) {
	value, err := f.deps.Runs.Get(ctx, runID)
	if err != nil || value.ProjectID != project || task != "" && value.TaskID != task {
		return codex.HistoryPage{}, notFound()
	}
	if value.ThreadID == "" || value.TurnID == "" || f.deps.CodexHistoryReader == nil {
		return codex.HistoryPage{}, notFound()
	}
	return f.deps.CodexHistoryReader(ctx, runID, value.ThreadID, value.TurnID, cursor, limit)
}
