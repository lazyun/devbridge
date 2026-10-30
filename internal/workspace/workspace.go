package workspace

import (
	"context"
	"time"
)

type Status string

const (
	StatusReady                 Status = "READY"
	StatusContinuityUnavailable Status = "CONTINUITY_UNAVAILABLE"
)

// OwnerKind is a resource namespace. It is separate from business Task
// ownership so unrelated Project Inspection operations never share a mutable
// workspace merely because they belong to the same Project.
type OwnerKind string

const (
	OwnerFeatureArea       OwnerKind = "FEATURE_AREA"
	OwnerProjectInspection OwnerKind = "PROJECT_INSPECTION"
	OwnerRecoveryStatus    OwnerKind = "RECOVERY_STATUS"
)

type TokenCounts struct {
	InputTokens           int64
	CachedInputTokens     int64
	CacheWriteInputTokens int64
	OutputTokens          int64
	ReasoningOutputTokens int64
	TotalTokens           int64
}

// Workspace is the canonical durable working environment. AgentConversation
// owns backend continuity; legacy thread identity is audit only.
type Workspace struct {
	Members []Member `json:"members,omitempty"`
	// RuntimeQuarantined preserves an abandoned operation workspace as history.
	RuntimeQuarantined     bool
	ID                     string
	ProjectID              string
	RepositoryID           string
	FeatureAreaID          string
	OwnerKind              OwnerKind
	OwnerID                string
	InspectionTaskID       string
	InspectionID           string
	BaseCommit             string
	WorktreeID             string
	LegacyThreadID         string
	Status                 Status
	TokenBaseline          TokenCounts
	TokenBaselineAvailable bool
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type Store interface {
	Create(context.Context, Workspace) error
	Get(context.Context, string) (Workspace, error)
	GetByFeatureArea(context.Context, string) (Workspace, error)
	GetByOwner(context.Context, OwnerKind, string) (Workspace, error)
	List(context.Context) ([]Workspace, error)
	Update(context.Context, string, func(*Workspace) error) (Workspace, error)
}
