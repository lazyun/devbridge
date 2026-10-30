// Package scopeexpansion owns explicit, durable user authorization for an
// exact proposed addition to a Review's current read scope.
package scopeexpansion

import (
	"time"

	"github.com/lazyun/devbridge/internal/run"
)

type Status string

const (
	StatusPendingConfirmation Status = "PENDING_CONFIRMATION"
	StatusAllowed             Status = "ALLOWED"
	StatusRejected            Status = "REJECTED"
)

// Expansion is domain authority, not persisted UI state. ALLOWED means only
// the exact AdditionalScope shown to the user was authorized; it does not
// itself start Codex, create a Run, or change the Task goal.
type Expansion struct {
	ID              string
	AISessionID     string
	ProjectID       string
	ReviewID        string
	FeatureAreaID   string
	RepositoryID    string
	LegacyTaskID    string
	CurrentScope    run.ReviewScope
	AdditionalScope run.ReviewScope
	Reason          string
	Status          Status
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DecidedAt       *time.Time
}

func Clone(value Expansion) Expansion {
	value.CurrentScope.Paths = append([]string(nil), value.CurrentScope.Paths...)
	value.AdditionalScope.Paths = append([]string(nil), value.AdditionalScope.Paths...)
	if value.DecidedAt != nil {
		copy := *value.DecidedAt
		value.DecidedAt = &copy
	}
	return value
}
