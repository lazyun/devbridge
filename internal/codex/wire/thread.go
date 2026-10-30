package wire

import "encoding/json"

type ThreadStartParams struct {
	Permissions           string            `json:"permissions,omitempty"`
	ApprovalPolicy        string            `json:"approvalPolicy,omitempty"`
	DeveloperInstructions string            `json:"developerInstructions,omitempty"`
	CWD                   string            `json:"cwd,omitempty"`
	DynamicTools          []DynamicToolSpec `json:"dynamicTools,omitempty"`
	Model                 string            `json:"model,omitempty"`
	ModelProvider         string            `json:"modelProvider,omitempty"`
	Config                map[string]any    `json:"config,omitempty"`
	Sandbox               string            `json:"sandbox,omitempty"`
	RuntimeWorkspaceRoots []string          `json:"runtimeWorkspaceRoots,omitempty"`
}

type ThreadStartResponse struct {
	ActivePermissionProfile *ExperimentalActivePermissionProfile `json:"activePermissionProfile,omitempty"`
	Thread                  Thread                               `json:"thread"`
	CWD                     string                               `json:"cwd"`
}

type ThreadResumeParams struct {
	Permissions           string         `json:"permissions,omitempty"`
	ApprovalPolicy        string         `json:"approvalPolicy,omitempty"`
	DeveloperInstructions string         `json:"developerInstructions,omitempty"`
	ThreadID              string         `json:"threadId"`
	ExcludeTurns          bool           `json:"excludeTurns,omitempty"`
	CWD                   string         `json:"cwd,omitempty"`
	Config                map[string]any `json:"config,omitempty"`
	Model                 string         `json:"model,omitempty"`
	ModelProvider         string         `json:"modelProvider,omitempty"`
	ServiceTier           string         `json:"serviceTier,omitempty"`
	Sandbox               string         `json:"sandbox,omitempty"`
	RuntimeWorkspaceRoots []string       `json:"runtimeWorkspaceRoots,omitempty"`
}

type ThreadResumeResponse struct {
	ActivePermissionProfile *ExperimentalActivePermissionProfile `json:"activePermissionProfile,omitempty"`
	Thread                  Thread                               `json:"thread"`
	CWD                     string                               `json:"cwd"`
}

type Thread struct {
	Name *string `json:"name"`
	ID   string  `json:"id"`
}

// ThreadSetNameParams mirrors the pinned Codex 0.149.0
// api/codex-schema/v2/ThreadSetNameParams.json contract.
type ThreadSetNameParams struct {
	ThreadID string `json:"threadId"`
	Name     string `json:"name"`
}

type ThreadSetNameResponse struct{}

// ThreadInjectItemsParams mirrors the pinned Codex
// api/codex-schema/v2/ThreadInjectItemsParams.json contract: raw Responses
// API items appended to the thread's model-visible history.
type ThreadInjectItemsParams struct {
	ThreadID string            `json:"threadId"`
	Items    []json.RawMessage `json:"items"`
}

type ThreadInjectItemsResponse struct{}

// ThreadTurnsListParams follows the Codex 0.163.0 paginated history contract.
type ThreadTurnsListParams struct {
	ThreadID      string `json:"threadId"`
	Cursor        string `json:"cursor,omitempty"`
	Limit         int    `json:"limit"`
	SortDirection string `json:"sortDirection"`
	ItemsView     string `json:"itemsView"`
}
type ThreadTurnsListResponse struct {
	Data       []Turn  `json:"data"`
	NextCursor *string `json:"nextCursor"`
}
