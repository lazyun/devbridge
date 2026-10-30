package execution

import (
	"encoding/json"
	"slices"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/recoverystatus"
)

// A retained backend history can be only a subset of the locally captured
// history. Replaying its earlier items at the end must not change which command
// ran last. Use the containing history's order; incomparable histories cannot
// establish a latest invocation and must not authorize recovery completion.
func mergeRecoverySourceItems(captured []agentruntime.Item, raw []json.RawMessage) ([]agentruntime.Item, error) {
	var refreshed []agentruntime.Item
	for _, value := range raw {
		item, include, err := recoverySourceItem(value)
		if err != nil {
			return nil, err
		}
		if include {
			refreshed = append(refreshed, item)
		}
	}
	commands := func(items []agentruntime.Item) ([]string, map[string]agentruntime.Item, error) {
		var order []string
		byID := make(map[string]agentruntime.Item)
		for _, item := range items {
			if item.Type != "commandExecution" {
				continue
			}
			if _, duplicate := byID[item.ID]; duplicate || item.ID == "" {
				return nil, nil, recoverystatus.ErrConflict
			}
			order = append(order, item.ID)
			byID[item.ID] = item
		}
		return order, byID, nil
	}
	oldOrder, old, err := commands(captured)
	if err != nil {
		return nil, err
	}
	newOrder, fresh, err := commands(refreshed)
	if err != nil {
		return nil, err
	}
	var oldCommon, newCommon []string
	for _, id := range oldOrder {
		if _, ok := fresh[id]; ok {
			oldCommon = append(oldCommon, id)
		}
	}
	for _, id := range newOrder {
		if _, ok := old[id]; ok {
			newCommon = append(newCommon, id)
		}
	}
	if !slices.Equal(oldCommon, newCommon) || (len(oldCommon) != len(oldOrder) && len(newCommon) != len(newOrder)) {
		return nil, recoverystatus.ErrConflict
	}
	order := oldOrder
	if len(newOrder) > len(oldOrder) {
		order = newOrder
	}
	var result []agentruntime.Item
	for _, id := range order {
		item, ok := fresh[id]
		if !ok {
			item = old[id]
		}
		result = append(result, item)
	}
	// Agent messages are a separate result channel; keep the refreshed message
	// last, as before, without using its position to reorder command evidence.
	for _, items := range [][]agentruntime.Item{captured, refreshed} {
		for _, item := range items {
			if item.Type != "commandExecution" {
				result = append(result, item)
			}
		}
	}
	return result, nil
}

// recoverySourceItem never silently drops uncertain command history. A missing
// later invocation could otherwise leave an earlier successful check in force.
func recoverySourceItem(raw json.RawMessage) (agentruntime.Item, bool, error) {
	var item struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Status string `json:"status"`
		Text   string `json:"text"`
		Output string `json:"aggregatedOutput"`
	}
	if json.Unmarshal(raw, &item) != nil || item.Type == "" {
		return agentruntime.Item{}, false, recoverystatus.ErrConflict
	}
	if item.Type != "commandExecution" && item.Type != "agentMessage" {
		return agentruntime.Item{}, false, nil
	}
	oversized := len(raw) > agentruntime.MaxPayload || len(item.Output) > agentruntime.MaxOutput
	if item.Type == "commandExecution" && (oversized || item.ID == "") {
		return agentruntime.Item{}, false, recoverystatus.ErrConflict
	}
	if oversized {
		return agentruntime.Item{}, false, nil
	}
	return agentruntime.Item{ID: item.ID, Type: item.Type, Status: item.Status, Text: item.Text, Source: "APP_SERVER", Metadata: append(json.RawMessage(nil), raw...), OutputTail: item.Output, OutputBytes: int64(len(item.Output))}, true, nil
}
