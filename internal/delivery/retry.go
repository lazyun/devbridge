package delivery

import "errors"

var (
	ErrRejected        = errors.New("DELIVERY_REPREPARE_REQUIRED")
	ErrPlanReleased    = errors.New("MERGE_PLAN_RELEASED")
	ErrAbandoned       = errors.New("DELIVERY_ABANDONED")
	ErrRetryConflict   = errors.New("DELIVERY_RETRY_ALREADY_EXISTS")
	ErrRequestConflict = errors.New("DELIVERY_RETRY_REQUEST_CONFLICT")
)

// RetryInput identifies one rejected operation. A new command authorizes one
// successor; observing an existing delivery never creates this authority.
type RetryInput struct {
	TaskID                 string `json:"taskId"`
	RepositoryID           string `json:"repositoryId"`
	PredecessorOperationID string `json:"predecessorOperationId"`
	RequestID              string `json:"requestId"`
}

type RetryReceipt struct {
	ProjectID              string `json:"projectId"`
	TaskID                 string `json:"taskId"`
	RepositoryID           string `json:"repositoryId"`
	PredecessorOperationID string `json:"predecessorOperationId"`
	OperationID            string `json:"operationId"`
	RequestID              string `json:"requestId"`
}

type RetryCandidate struct {
	RepositoryID string `json:"repositoryId"`
	OperationID  string `json:"operationId"`
	Error        string `json:"error"`
}

// Merge retries retain the exact confirmed plan and rejected member identity.
// ReleasePlan explicitly closes a provably settled plan without creating a retry
// or executing Git; a new plan requires separate confirmation.
type MergeRetryInput struct {
	ReleasePlan            bool   `json:"releasePlan,omitempty"`
	FeatureAreaID          string `json:"featureAreaId"`
	MergeRequestID         string `json:"mergeRequestId"`
	RepositoryID           string `json:"repositoryId"`
	PredecessorOperationID string `json:"predecessorOperationId"`
	RequestID              string `json:"requestId"`
}
type MergeRetryReceipt struct {
	PlanReleased bool   `json:"planReleased,omitempty"`
	ProjectID    string `json:"projectId"`
	MergeRetryInput
	OperationID string `json:"operationId"`
}
type MergeRetryCandidate struct {
	CanRelease     bool   `json:"canRelease,omitempty"`
	MergeRequestID string `json:"mergeRequestId"`
	RetryCandidate
}
