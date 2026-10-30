package workflowprogression

import "github.com/lazyun/devbridge/internal/task"

// PrepareSuccessorExecution is the sole Core command envelope for the durable
// canceled-before-Run successor. RequestID is globally unique in this command
// namespace; Task and predecessor are immutable bindings, not key partitions.
type PrepareSuccessorExecution struct {
	RequestID                    string `json:"requestId"`
	PayloadSHA256                string `json:"payloadSha256"`
	ProjectID                    string `json:"projectId"`
	TaskID                       string `json:"taskId"`
	PredecessorExecutionID       string `json:"predecessorExecutionId"`
	ExpectedTaskState            string `json:"expectedTaskState"`
	ExpectedCurrentExecutionID   string `json:"expectedCurrentExecutionId"`
	ExpectedDefinitionRevisionID string `json:"expectedDefinitionRevisionId"`
	ActorAuthority               string `json:"actorAuthority"`
}

type SuccessorCommandReceipt struct {
	RequestID                     string `json:"requestId"`
	PayloadSHA256                 string `json:"payloadSha256"`
	TaskID                        string `json:"taskId"`
	PredecessorExecutionID        string `json:"predecessorExecutionId"`
	SuccessorEffectAuthorityID    string `json:"successorEffectAuthorityId"`
	SuccessorExecutionID          string `json:"successorExecutionId"`
	RuntimeScopePlanID            string `json:"runtimeScopePlanId"`
	RuntimePreparationAuthorityID string `json:"runtimePreparationAuthorityId"`
	Outcome                       string `json:"outcome"`
}

const (
	NoRunSuccessorState         = "TASK_NO_RUN_SUCCESSOR_REQUIRED"
	NoRunSuccessorAction        = "PREPARE_SUCCESSOR_EXECUTION"
	NoRunSuccessorCreated       = "SUCCESSOR_CREATED"
	NoRunSuccessorAlreadyExists = "ALREADY_HAS_SUCCESSOR"
)

func NoRunSuccessorGuidance(projectID, taskID, predecessor string) Guidance {
	g := Guidance{ProjectID: projectID, TaskID: taskID, ExecutionID: predecessor, TaskPhase: task.PhaseNoRunSuccessorRequired, Status: "ACTIVE", AvailableActions: []string{NoRunSuccessorAction, "STOP_AND_REVISE", "CANCEL_TASK", "READ"}}
	g.Set(NoRunSuccessorState, NoRunSuccessorAction, "前一执行已在创建 Run 前取消；需要准备后继执行。")
	return g
}
