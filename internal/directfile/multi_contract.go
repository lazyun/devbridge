package directfile

import "github.com/lazyun/devbridge/internal/workset"

// StartInput is consumed only by the exact purpose-scoped confirmation step.
// Recovery explicitly names the previous epoch/generation; reads never start.
type StartInput struct {
	AISessionID              string `json:"aiSessionId"`
	TaskID                   string `json:"taskId"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId"`
	DecisionID               string `json:"decisionId"`
	RequestID                string `json:"requestId"`
	ConfirmationIntentID     string `json:"confirmationIntentId"`
	Action                   string `json:"action"`
	WorkflowID               string `json:"workflowId,omitempty"`
	RuntimeEpoch             string `json:"runtimeEpoch,omitempty"`
	GrantGeneration          int64  `json:"grantGeneration,omitempty"`
}
type MemberGrant struct {
	workset.Target
	WorkspaceID   string `json:"workspaceId"`
	WorktreeID    string `json:"worktreeId,omitempty"`
	State         string `json:"state"`
	StartHead     string `json:"startHead,omitempty"`
	StartSnapshot string `json:"startSnapshot,omitempty"`
	Failure       string `json:"failure,omitempty"`
}

// MemberResult is observed by Core, never supplied as a claimed filesystem result.
type MemberResult struct {
	workset.Target
	WorkspaceID   string   `json:"workspaceId"`
	WorktreeID    string   `json:"worktreeId"`
	WorktreePath  string   `json:"-"`
	Branch        string   `json:"branch"`
	RootIdentity  string   `json:"-"`
	StartHead     string   `json:"startHead"`
	StartSnapshot string   `json:"startSnapshot"`
	EndSnapshot   string   `json:"endSnapshot"`
	ChangedPaths  []string `json:"changedPaths"`
	State         string   `json:"state"`
}
