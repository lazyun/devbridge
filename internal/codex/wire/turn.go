package wire

import "encoding/json"

type TurnStartParams struct {
	CollaborationMode *CollaborationMode `json:"collaborationMode,omitempty"`
	Permissions       string             `json:"permissions,omitempty"`
	ApprovalPolicy    string             `json:"approvalPolicy,omitempty"`
	ThreadID          string             `json:"threadId"`
	Input             []UserInput        `json:"input"`
	OutputSchema      json.RawMessage    `json:"outputSchema,omitempty"`
	Model             string             `json:"model,omitempty"`
	Effort            string             `json:"effort,omitempty"`
	// ServiceTier is deliberately not omitted: nil explicitly clears an
	// inherited tier on resumed FeatureArea threads.
	ServiceTier           *string        `json:"serviceTier"`
	SandboxPolicy         *SandboxPolicy `json:"sandboxPolicy,omitempty"`
	RuntimeWorkspaceRoots []string       `json:"runtimeWorkspaceRoots,omitempty"`
}

// CollaborationMode installs developer instructions in the current turn's
// context. Thread resume configuration alone does not replace instructions
// retained in a persisted or compacted conversation.
type CollaborationMode struct {
	Mode     string                `json:"mode"`
	Settings CollaborationSettings `json:"settings"`
}

type CollaborationSettings struct {
	Model                 string  `json:"model"`
	ReasoningEffort       *string `json:"reasoning_effort"`
	DeveloperInstructions string  `json:"developer_instructions"`
}

// SandboxPolicy is the pinned Codex 0.149.0 turn/start policy. Dev Bridge
// binds it per operation so a resumed Thread cannot carry authority across an
// operation boundary.
type SandboxPolicy struct {
	Type                string   `json:"type"`
	NetworkAccess       bool     `json:"networkAccess,omitempty"`
	WritableRoots       []string `json:"writableRoots,omitempty"`
	ExcludeSlashTmp     bool     `json:"excludeSlashTmp,omitempty"`
	ExcludeTmpdirEnvVar bool     `json:"excludeTmpdirEnvVar,omitempty"`
}

type UserInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type TurnStartResponse struct {
	Turn Turn `json:"turn"`
}

type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type TurnInterruptResponse struct{}

type Turn struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Items  []json.RawMessage `json:"items"`
	Error  *TurnFailure      `json:"error,omitempty"`
}

type TurnFailure struct {
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo,omitempty"`
	AdditionalDetails json.RawMessage `json:"additionalDetails,omitempty"`
	Message           string          `json:"message"`
}

type TurnCompletedParams struct {
	ThreadID string `json:"threadId"`
	Turn     Turn   `json:"turn"`
}

type ItemCompletedParams struct {
	ThreadID      string          `json:"threadId"`
	TurnID        string          `json:"turnId"`
	Item          json.RawMessage `json:"item"`
	CompletedAtMS *int64          `json:"completedAtMs"`
}

type ItemStartedParams struct {
	ThreadID    string          `json:"threadId"`
	TurnID      string          `json:"turnId"`
	Item        json.RawMessage `json:"item"`
	StartedAtMS *int64          `json:"startedAtMs"`
}

type AgentMessage struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"`
	Text  string  `json:"text"`
	Phase *string `json:"phase,omitempty"`
}

// CommandExecution is the authoritative command item emitted by the pinned
// Codex App Server. AggregatedOutput combines stdout and stderr; the protocol
// does not expose them as separate streams on the completed item.
type CommandExecution struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Command          string          `json:"command"`
	CWD              string          `json:"cwd"`
	Status           string          `json:"status"`
	CommandActions   []CommandAction `json:"commandActions"`
	AggregatedOutput *string         `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`
	DurationMS       *int64          `json:"durationMs"`
}

type CommandAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}
