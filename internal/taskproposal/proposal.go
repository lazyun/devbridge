package taskproposal

import (
	"time"

	"github.com/lazyun/devbridge/internal/task"
)

type Kind string

const (
	KindCreateTask   Kind = "CREATE_TASK"
	KindContinueTask Kind = "CONTINUE_TASK"
)

type Status string

const (
	StatusPendingConfirmation Status = "PENDING_CONFIRMATION"
	StatusConfirmed           Status = "CONFIRMED"
	StatusRejected            Status = "REJECTED"
	StatusConsumed            Status = "CONSUMED"
)

// Proposal preserves historical Task Proposal records. The authoring and
// consumption workflow is retired; current Task definitions use immutable
// revisions, Execution authorizations.
type Proposal struct {
	ID               string
	AISessionID      string
	ProjectID        string
	Kind             Kind
	ModuleID         string
	RepositoryID     string
	TargetTaskID     *string
	ResultTaskID     *string
	CodexConfigID    string
	Goal             string
	Constraints      []string
	WorkSpec         task.WorkSpec
	VerificationSpec task.VerificationSpec
	BaseRevision     string
	Status           Status
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ConfirmedAt      *time.Time
	RejectedAt       *time.Time
	ConsumedAt       *time.Time
}

func clone(value Proposal) Proposal {
	value.Constraints = append([]string(nil), value.Constraints...)
	value.WorkSpec = task.CloneWorkSpec(value.WorkSpec)
	value.VerificationSpec = task.CloneVerificationSpec(value.VerificationSpec)
	value.TargetTaskID = cloneString(value.TargetTaskID)
	value.ResultTaskID = cloneString(value.ResultTaskID)
	value.ConfirmedAt = cloneTime(value.ConfirmedAt)
	value.RejectedAt = cloneTime(value.RejectedAt)
	value.ConsumedAt = cloneTime(value.ConsumedAt)
	return value
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
