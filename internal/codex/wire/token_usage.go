package wire

import "encoding/json"

type TokenUsageBreakdown struct {
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

type ThreadTokenUsage struct {
	Last               json.RawMessage `json:"last"`
	Total              json.RawMessage `json:"total"`
	ModelContextWindow *int64          `json:"modelContextWindow"`
}

type ThreadTokenUsageUpdatedParams struct {
	ThreadID   string           `json:"threadId"`
	TurnID     string           `json:"turnId"`
	TokenUsage ThreadTokenUsage `json:"tokenUsage"`
}
