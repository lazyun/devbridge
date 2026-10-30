package main

import (
	"context"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/desktop/internal/piagent"
	"github.com/lazyun/devbridge/desktop/internal/settings"
)

// ReadPiModels returns a proposed inventory for the existing settings-save
// flow. Reading alone never changes persisted settings.
func (a *App) ReadPiModels() ([]settings.PiAgentModelConfig, error) {
	a.mu.RLock()
	ctx, executable := a.ctx, a.settings.PiExecutable
	a.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	models, err := piagent.ListModels(ctx, executable)
	if err != nil {
		return nil, fmt.Errorf("read PI Agent models: %w", err)
	}

	// Convert to settings format
	result := make([]settings.PiAgentModelConfig, len(models))
	for i, model := range models {
		result[i] = settings.PiAgentModelConfig{
			ModelID:                   model.ModelID,
			Provider:                  model.Provider,
			Enabled:                   model.Enabled,
			SupportedReasoningEfforts: model.SupportedReasoningEfforts,
			DefaultReasoningEffort:    model.DefaultReasoningEffort,
			ContextWindow:             model.ContextWindow,
			MaxOutput:                 model.MaxOutput,
			SupportsThinking:          model.SupportsThinking,
			SupportsImages:            model.SupportsImages,
		}
	}

	return result, nil
}
