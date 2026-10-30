package task

import "time"

// Hold is an explicit user decision to retain an idle verification checkpoint.
// It changes neither engineering evidence nor the canonical business phase.
type Hold struct {
	TaskID      string    `json:"taskId"`
	ExecutionID string    `json:"executionId"`
	RevisionID  string    `json:"taskDefinitionRevisionId"`
	Note        string    `json:"note"`
	HeldAt      time.Time `json:"heldAt"`
}

type HoldInput struct {
	ProjectID     string `json:"projectId"`
	TaskID        string `json:"taskId"`
	FeatureAreaID string `json:"featureAreaId"`
	ExecutionID   string `json:"executionId"`
	RevisionID    string `json:"taskDefinitionRevisionId"`
	Action        string `json:"action"`
	RequestID     string `json:"requestId"`
	Note          string `json:"note"`
	Actor         string `json:"-"`
}
