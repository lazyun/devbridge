package workflowpresentation

import "time"

// References only: the formal Card independently re-reads its exact frozen
// identity and current options before offering a confirmation.
type TaskConfirmationFeed struct {
	AISessionID  string                 `json:"aiSessionId"`
	ProjectID    string                 `json:"projectId"`
	TaskID       string                 `json:"taskId"`
	Availability string                 `json:"availability"`
	ReadError    string                 `json:"readError,omitempty"`
	Truncated    bool                   `json:"truncated"`
	Items        []TaskConfirmationItem `json:"items"`
}
type TaskConfirmationItem struct {
	AISessionID string    `json:"aiSessionId"`
	IntentID    string    `json:"intentId"`
	Version     int       `json:"version"`
	ContentHash string    `json:"contentHash"`
	Purpose     string    `json:"purpose"`
	Action      string    `json:"action"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}
