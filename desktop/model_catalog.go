package main

import (
	"context"
	"fmt"
	"time"

	corecodex "github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/codexruntime"
)

// ReadCodexModels returns a proposed inventory for the existing settings-save
// flow. Reading alone never changes persisted settings or the running Core.
func (a *App) ReadCodexModels() ([]codexruntime.CodingAgentModelConfig, error) {
	a.mu.RLock()
	ctx, executable := a.ctx, a.settings.CodexExecutable
	current := codexruntime.Allowlist{ModelConfigs: a.settings.CodingAgentModels}.Clone()
	reader := a.modelCatalogReader
	a.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if selection, err := codexlaunch.Read(a.codexSelectionFile()); err == nil && selection.Configured == executable && selection.Executable != "" {
		executable = selection.Executable
	}
	if reader == nil {
		reader = readCodexModelCatalog
	}
	catalog, err := reader(ctx, executable)
	if err != nil {
		return nil, err
	}
	merged, err := current.MergeCatalog(catalog)
	if err != nil {
		return nil, fmt.Errorf("merge Codex model catalog: %w", err)
	}
	return merged.ModelConfigs, nil
}

func readCodexModelCatalog(ctx context.Context, executable string) ([]codexruntime.CodingAgentModelConfig, error) {
	client, err := corecodex.Start(ctx, corecodex.Config{CodexPath: executable})
	if err != nil {
		return nil, fmt.Errorf("connect to Codex to read models: %w", err)
	}
	defer client.Close()
	return client.ListModels(ctx)
}
