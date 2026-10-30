package executionproposal

import (
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/task"
)

type Status string

const (
	// RuntimeAdapterCodexAppServerV1 identifies the production Client adapter
	// that serializes a frozen RuntimeWorkset to the pinned App Server wire.
	RuntimeAdapterCodexAppServerV1 = "codex-app-server-workset-v1"
	RuntimeProtocolCodex0149       = "codex-app-server-0.149.0"
)

const (
	StatusPendingConfirmation Status = "PENDING_CONFIRMATION"
	StatusConfirmed           Status = "CONFIRMED"
	StatusAdmitted            Status = "ADMITTED"
	StatusRejected            Status = "REJECTED"
	StatusConsumed            Status = "CONSUMED"
)

type Outcome string

const (
	OutcomePending   Outcome = "PENDING"
	OutcomeRunning   Outcome = "RUNNING"
	OutcomeSucceeded Outcome = "SUCCEEDED"
	OutcomeFailed    Outcome = "FAILED"
	OutcomeCanceled  Outcome = "CANCELED"
)

// Proposal is one durable, single-use authorization for an implementation
// Run and its confirmed optional verification Run.
// ProjectID and TaskID are derived from the bound AI session and the exact
// applied Task Plan; callers never supply either as authority.
type Proposal struct {
	ReviewApprovalID         string
	TaskDefinitionRevisionID string
	ProvenanceKind           string
	ID                       string
	AISessionID              string
	ProjectID                string
	TaskID                   string
	TaskPlanID               string
	WorkSpec                 task.WorkSpec
	VerificationSpec         task.VerificationSpec
	RuntimeConfig            codexruntime.NextRunRuntimeConfig
	BackendSelection         *codexconfig.BackendSelection
	// Runtime layout is frozen with the approved definition. Empty values retain
	// historical records, but must never authorize a new multi-member Coding run.
	PrimaryMemberID         string
	ApprovedWorksetHash     string
	RuntimeAdapterIdentity  string
	RuntimeProtocolIdentity string
	Status                  Status
	RunID                   string
	ImplementationRunID     string
	VerificationRunID       string
	Outcome                 Outcome
	FailureCode             string
	FailureMessage          string
	CreatedAt               time.Time
	UpdatedAt               time.Time
	ConfirmedAt             *time.Time
	RejectedAt              *time.Time
	ConsumedAt              *time.Time
}

func clone(value Proposal) Proposal {
	value.WorkSpec = task.CloneWorkSpec(value.WorkSpec)
	value.VerificationSpec = task.CloneVerificationSpec(value.VerificationSpec)
	value.ConfirmedAt = cloneTime(value.ConfirmedAt)
	value.RejectedAt = cloneTime(value.RejectedAt)
	value.ConsumedAt = cloneTime(value.ConsumedAt)
	return value
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
