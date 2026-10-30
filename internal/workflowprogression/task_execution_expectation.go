package workflowprogression

import "github.com/lazyun/devbridge/internal/task"

// TaskExecutionExpectation binds a submitted current action to its observed
// definition, candidate and phase. It is not a request receipt or authority.
type TaskExecutionExpectation struct {
	FeatureAreaID            string     `json:"featureAreaId"`
	TaskDefinitionRevisionID string     `json:"taskDefinitionRevisionId"`
	ExecutionID              string     `json:"executionId"`
	Phase                    task.Phase `json:"phase"`
	// These optional fields bind a v77 multi-repository Coding start to the
	// proposal layout shown to the caller. They remain empty for legacy runs.
	PrimaryMemberID     string `json:"primaryMemberId,omitempty"`
	ApprovedWorksetHash string `json:"approvedWorksetHash,omitempty"`
}
