package workflowprogression

// StopAndReviseNoRunTask returns an exact canceled-before-Run owner to Review.
// It carries no new definition or runtime authority. The later Review approval
// owns revision creation; this command preserves the predecessor and its facts.
type StopAndReviseNoRunTask struct {
	ProjectID                    string `json:"projectId,omitempty"`
	TaskID                       string `json:"taskId"`
	PredecessorExecutionID       string `json:"predecessorExecutionId"`
	ExpectedDefinitionRevisionID string `json:"expectedDefinitionRevisionId"`
	ActorAuthority               string `json:"-"`
}

const NoRunStopAndReviseAction = "STOP_AND_REVISE"
