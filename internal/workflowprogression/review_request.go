package workflowprogression

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/task"
)

const ActionRequestTaskReview = "REQUEST_TASK_REVIEW"

// Every observed field is explicit. Empty identities mean no object existed;
// they are compared exactly and never act as wildcards.
type TaskReviewRequestTarget struct {
	Phase                    task.Phase `json:"phase"`
	TaskDefinitionRevisionID string     `json:"taskDefinitionRevisionId"`
	CurrentReviewID          string     `json:"currentReviewId"`
	CurrentExecutionID       string     `json:"currentExecutionId"`
	PendingDecisionID        string     `json:"pendingDecisionId"`
}

// Transport decoding preserves absence as an error instead of silently turning
// it into an empty identity. In-process callers still use the same typed target.
func (t *TaskReviewRequestTarget) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields) != 5 {
		return fmt.Errorf("complete Review request target requires exactly five fields")
	}
	values := map[string]string{}
	for _, name := range []string{"phase", "taskDefinitionRevisionId", "currentReviewId", "currentExecutionId", "pendingDecisionId"} {
		raw, present := fields[name]
		if !present {
			return fmt.Errorf("Review request target is missing %s", name)
		}
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return fmt.Errorf("Review request target %s must be an explicit string", name)
		}
		values[name] = *value
	}
	*t = TaskReviewRequestTarget{Phase: task.Phase(values["phase"]), TaskDefinitionRevisionID: values["taskDefinitionRevisionId"], CurrentReviewID: values["currentReviewId"], CurrentExecutionID: values["currentExecutionId"], PendingDecisionID: values["pendingDecisionId"]}
	return nil
}

type TaskReviewRequestInput struct {
	AISessionID string `json:"aiSessionId"`
	// OperatorPrincipalID is bound by the trusted local operator ingress. It
	// is never decoded from a client payload and is mutually exclusive with an
	// AI Session source.
	OperatorPrincipalID string                   `json:"-"`
	TaskID              string                   `json:"taskId"`
	FeatureAreaID       string                   `json:"featureAreaId"`
	RequestID           string                   `json:"requestId"`
	Purpose             string                   `json:"purpose"`
	ExpectedTarget      *TaskReviewRequestTarget `json:"expectedTarget"`
}

type TaskReviewRequestResolution struct {
	ID                   string                   `json:"id"`
	ProjectID            string                   `json:"projectId"`
	TaskID               string                   `json:"taskId"`
	FeatureAreaID        string                   `json:"featureAreaId"`
	RequestID            string                   `json:"requestId"`
	Purpose              string                   `json:"purpose"`
	SourceRevisionID     string                   `json:"sourceRevisionId"`
	SourceReviewID       string                   `json:"sourceReviewId"`
	SourceExecutionID    string                   `json:"sourceExecutionId"`
	SupersededDecisionID string                   `json:"supersededDecisionId"`
	Phase                task.Phase               `json:"phase"`
	ExpectedTarget       *TaskReviewRequestTarget `json:"expectedTarget"`
	RequestedAt          time.Time                `json:"requestedAt"`
	Replay               bool                     `json:"replay"`
}

type TaskReviewRequestEligibility struct {
	SupersededExecutions map[string]string            `json:"supersededExecutions,omitempty"`
	Available            bool                         `json:"available"`
	Reason               string                       `json:"reason,omitempty"`
	Target               *TaskReviewRequestTarget     `json:"target,omitempty"`
	CurrentRequest       *TaskReviewRequestResolution `json:"currentRequest,omitempty"`
}

type TaskReviewRequestStore interface {
	RequestTaskReview(context.Context, TaskReviewRequestInput) (TaskReviewRequestResolution, error)
	TaskReviewRequestEligibility(context.Context, string, string) (TaskReviewRequestEligibility, error)
}

// TaskReviewRequestReplayStore is intentionally separate from eligibility:
// retrying an already accepted request must not depend on its old live action
// still being available.
type TaskReviewRequestReplayStore interface {
	TaskReviewRequestReplayForOperator(context.Context, string, TaskReviewRequestInput) (bool, error)
}
