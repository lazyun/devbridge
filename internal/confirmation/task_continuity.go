package confirmation

// TaskContinuity describes unfinished Tasks in the exact creation FeatureArea.
// Selecting one requests scoped continuation in the conversation. For an existing
// authorized GPT Review this includes file review and formal report submission.
// The Card never consumes the creation intent or grants new workflow authority.
type TaskContinuity struct {
	Availability    string             `json:"availability"`
	ProjectID       string             `json:"projectId"`
	FeatureAreaID   string             `json:"featureAreaId"`
	Tasks           []ContinuationTask `json:"tasks"`
	Total           int                `json:"total"`
	ReadFailureCode string             `json:"readFailureCode,omitempty"`
}

type ContinuationTask struct {
	TaskID                   string `json:"taskId"`
	Title                    string `json:"title"`
	Status                   string `json:"status"`
	Phase                    string `json:"phase"`
	Stage                    string `json:"stage"`
	Summary                  string `json:"summary"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId,omitempty"`
}
