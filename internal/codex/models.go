package codex

import (
	"context"
	"fmt"

	"github.com/lazyun/devbridge/internal/codexruntime"
)

type modelListPage struct {
	Data []struct {
		Model                     string `json:"model"`
		Hidden                    bool   `json:"hidden"`
		DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
		SupportedReasoningEfforts []struct {
			ReasoningEffort string `json:"reasoningEffort"`
		} `json:"supportedReasoningEfforts"`
		ServiceTiers []struct {
			ID string `json:"id"`
		} `json:"serviceTiers"`
		AdditionalSpeedTiers []string `json:"additionalSpeedTiers"`
	} `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

// ListModels reads the installed App Server catalog without starting a turn.
// The catalog may be cached by Codex and is not an account entitlement check.
func (c *Client) ListModels(ctx context.Context) ([]codexruntime.CodingAgentModelConfig, error) {
	var models []codexruntime.CodingAgentModelConfig
	var cursor *string
	seenCursors := make(map[string]bool)
	seenModels := make(map[string]bool)
	for page := 0; page < 128; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var response modelListPage
		if err := c.rpc.Call(ctx, "model/list", map[string]any{"cursor": cursor, "limit": 100, "includeHidden": false}, &response); err != nil {
			return nil, fmt.Errorf("read Codex model catalog: %w", err)
		}
		for _, model := range response.Data {
			if model.Hidden || seenModels[model.Model] {
				continue
			}
			config := codexruntime.CodingAgentModelConfig{ModelID: model.Model, Enabled: true, DefaultReasoningEffort: model.DefaultReasoningEffort}
			for _, effort := range model.SupportedReasoningEfforts {
				config.SupportedReasoningEfforts = append(config.SupportedReasoningEfforts, effort.ReasoningEffort)
			}
			tier := ""
			for _, option := range model.ServiceTiers {
				if option.ID == "priority" {
					tier = "priority"
				}
			}
			for _, option := range model.AdditionalSpeedTiers {
				if option == "priority" {
					tier = "priority"
				}
			}
			config.FastServiceTier = &tier
			if err := (codexruntime.Allowlist{ModelConfigs: []codexruntime.CodingAgentModelConfig{config}}).Validate(); err != nil {
				return nil, fmt.Errorf("invalid Codex model catalog: %w", err)
			}
			config.Enabled = false
			models = append(models, config)
			seenModels[model.Model] = true
			if len(models) > codexruntime.MaxModels {
				return nil, fmt.Errorf("Codex model catalog exceeds %d models", codexruntime.MaxModels)
			}
		}
		if response.NextCursor == nil || *response.NextCursor == "" {
			if len(models) == 0 {
				return nil, fmt.Errorf("Codex returned an empty model catalog")
			}
			return models, nil
		}
		if seenCursors[*response.NextCursor] {
			return nil, fmt.Errorf("Codex model catalog repeated a pagination cursor")
		}
		seenCursors[*response.NextCursor] = true
		cursor = response.NextCursor
	}
	return nil, fmt.Errorf("Codex model catalog exceeded the pagination limit")
}
