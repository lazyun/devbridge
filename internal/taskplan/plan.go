// Package taskplan owns the human-confirmable implementation and verification
// plan for an already-existing Bridge Task.
package taskplan

import (
	"time"

	"github.com/lazyun/devbridge/internal/task"
)

type Status string

const (
	StatusPendingConfirmation Status = "PENDING_CONFIRMATION"
	StatusConfirmed           Status = "CONFIRMED"
	StatusRejected            Status = "REJECTED"
	StatusApplied             Status = "APPLIED"
)

type Plan struct {
	ID               string
	AISessionID      string
	ProjectID        string
	TaskID           string
	Title            string
	WorkSpec         task.WorkSpec
	VerificationSpec task.VerificationSpec
	Status           Status
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ConfirmedAt      *time.Time
	RejectedAt       *time.Time
	AppliedAt        *time.Time
}

func Clone(value Plan) Plan {
	value.WorkSpec = task.CloneWorkSpec(value.WorkSpec)
	value.VerificationSpec = task.CloneVerificationSpec(value.VerificationSpec)
	value.ConfirmedAt = cloneTime(value.ConfirmedAt)
	value.RejectedAt = cloneTime(value.RejectedAt)
	value.AppliedAt = cloneTime(value.AppliedAt)
	return value
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
