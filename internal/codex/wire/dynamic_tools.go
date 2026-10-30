package wire

import "encoding/json"

// DynamicToolSpec mirrors the function variant of the pinned Codex 0.149.0
// api/codex-schema/v2/ThreadStartParams.json DynamicToolSpec contract.
// Namespace tools are deliberately not exposed by Dev Bridge yet.
type DynamicToolSpec struct {
	Type         string          `json:"type"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	DeferLoading bool            `json:"deferLoading,omitempty"`
}

// DynamicToolCallParams mirrors the pinned Codex 0.149.0
// DynamicToolCallParams contract for item/tool/call.
type DynamicToolCallParams struct {
	CallID    string          `json:"callId"`
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	Tool      string          `json:"tool"`
	Namespace *string         `json:"namespace"`
	Arguments json.RawMessage `json:"arguments"`
}

// DynamicToolCallResponse mirrors the pinned Codex 0.149.0
// DynamicToolCallResponse contract. Only inputText output is supported.
type DynamicToolCallResponse struct {
	Success      bool                               `json:"success"`
	ContentItems []DynamicToolCallOutputContentItem `json:"contentItems"`
}

type DynamicToolCallOutputContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
