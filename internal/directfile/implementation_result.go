package directfile

import (
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/workset"
)

// ImplementationResultReadInput selects one existing result without replaying a
// submission or requiring its historical grant to remain active.
type ImplementationResultReadInput struct {
	AISessionID              string `json:"aiSessionId"`
	TaskID                   string `json:"taskId"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId"`
	WorkflowID               string `json:"workflowId"`
}

// ImplementationResultInput reports file work; it never asserts command execution.
type ImplementationResultInput struct {
	WorkerClaim              *WorkerClaimIdentity `json:"workerClaim,omitempty"`
	AISessionID              string               `json:"aiSessionId"`
	TaskID                   string               `json:"taskId"`
	TaskDefinitionRevisionID string               `json:"taskDefinitionRevisionId"`
	WorkflowID               string               `json:"workflowId"`
	RequestID                string               `json:"requestId"`
	Summary                  string               `json:"summary"`
	VerificationNotes        string               `json:"verificationNotes"`
	ProtocolVersion          int                  `json:"protocolVersion,omitempty"`
	RuntimeEpoch             string               `json:"runtimeEpoch,omitempty"`
	GrantGeneration          int64                `json:"grantGeneration,omitempty"`
	// Members are advisory completion statements. The result store obtains all
	// snapshots, paths and identities itself from the frozen authorization.
	Members []MemberResult `json:"members,omitempty"`
}
type ImplementationResult struct {
	ID                       string           `json:"id"`
	AISessionID              string           `json:"aiSessionId"`
	TaskID                   string           `json:"taskId"`
	TaskDefinitionRevisionID string           `json:"taskDefinitionRevisionId"`
	WorkflowID               string           `json:"workflowId"`
	RequestID                string           `json:"requestId"`
	Summary                  string           `json:"summary"`
	VerificationNotes        string           `json:"verificationNotes"`
	ExecutionID              string           `json:"executionId,omitempty"`
	VerificationStartError   string           `json:"verificationStartError,omitempty"`
	VerificationRunID        string           `json:"verificationRunId,omitempty"`
	VerificationStatus       string           `json:"verificationStatus"`
	Snapshot                 string           `json:"snapshot"`
	Accepted                 bool             `json:"accepted"`
	Replay                   bool             `json:"replay"`
	CommitError              string           `json:"commitError,omitempty"`
	ProtocolVersion          int              `json:"protocolVersion,omitempty"`
	RuntimeEpoch             string           `json:"runtimeEpoch,omitempty"`
	GrantGeneration          int64            `json:"grantGeneration,omitempty"`
	Members                  []MemberResult   `json:"members,omitempty"`
	RepositoryTargets        []workset.Target `json:"repositoryTargets,omitempty"`
}
type ImplementationAcceptInput struct {
	CompletionPlan           *delivery.TaskCompletionPlan `json:"-"`
	AISessionID              string                       `json:"aiSessionId"`
	TaskID                   string                       `json:"taskId"`
	TaskDefinitionRevisionID string                       `json:"taskDefinitionRevisionId"`
	WorkflowID               string                       `json:"workflowId"`
	ResultID                 string                       `json:"resultId"`
	RequestID                string                       `json:"requestId"`
	ProtocolVersion          int                          `json:"protocolVersion,omitempty"`
	RuntimeEpoch             string                       `json:"runtimeEpoch,omitempty"`
	GrantGeneration          int64                        `json:"grantGeneration,omitempty"`
}
