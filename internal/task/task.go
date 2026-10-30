package task

import (
	"github.com/lazyun/devbridge/internal/workset"
	"time"
)

type Status string

type Type string
type Scope string

const (
	TypeDevelopment  Type  = "DEVELOPMENT"
	TypeInspection   Type  = "INSPECTION"
	ScopeFeatureArea Scope = "FEATURE_AREA"
	ScopeProject     Scope = "PROJECT"
)

// Work describes durable work ownership independently of organizational WorkKind.
type Work struct {
	ExecutionID                 string    `json:"executionId,omitempty"`
	ExecutionProposalID         string    `json:"executionProposalId,omitempty"`
	ExecutionKind               string    `json:"executionKind,omitempty"`
	ExecutionStatus             string    `json:"executionStatus,omitempty"`
	DomainStatus                string    `json:"domainStatus,omitempty"`
	DomainResult                string    `json:"domainResult,omitempty"`
	DevelopmentBranches         []string  `json:"developmentBranches"`
	DevelopmentBranchProvenance string    `json:"developmentBranchProvenance"`
	ReadOnly                    bool      `json:"readOnly"`
	Provenance                  string    `json:"provenance"`
	Type                        Type      `json:"type"`
	Scope                       Scope     `json:"scope"`
	Phase                       string    `json:"phase"`
	Status                      string    `json:"status"`
	OwnerType                   string    `json:"ownerType"`
	OwnerID                     string    `json:"ownerId"`
	RunID                       string    `json:"runId,omitempty"`
	RunStatus                   string    `json:"runStatus,omitempty"`
	Agent                       string    `json:"agent"`
	TokenAvailability           string    `json:"tokenAvailability"`
	UpdatedAt                   time.Time `json:"updatedAt"`
	CancellationState           string    `json:"cancellationState,omitempty"`
	BackendDisposition          string    `json:"backendDisposition,omitempty"`
}

const (
	StatusPlanning Status = "PLANNING"
	// StatusDraft is retained only so migration-era callers can be diagnosed;
	// new Tasks are created in PLANNING and DRAFT is not a public lifecycle.
	StatusDraft     Status = "DRAFT"
	StatusReady     Status = "READY"
	StatusRunning   Status = "RUNNING"
	StatusSucceeded Status = "SUCCEEDED"
	StatusFailed    Status = "FAILED"
	StatusCanceled  Status = "CANCELED"
)

// WorkKind is an optional GPT-authored organization-only classification for a
// durable Task. It is validated against the closed enum and never inferred by
// Bridge from title, prompt, paths, diff, Module names, or any heuristic.
// It has no effect on ExecutionScope, authorization,
// model/reasoning selection, Verification, or runtime behavior.
type WorkKind string

const (
	WorkKindFeature       WorkKind = "FEATURE"
	WorkKindBugFix        WorkKind = "BUG_FIX"
	WorkKindRefactor      WorkKind = "REFACTOR"
	WorkKindMaintenance   WorkKind = "MAINTENANCE"
	WorkKindTestChange    WorkKind = "TEST_CHANGE"
	WorkKindDocumentation WorkKind = "DOCUMENTATION"
	WorkKindOther         WorkKind = "OTHER"
)

// ValidWorkKind reports whether value is null or one of the closed
// organization-only work kinds. The empty value means unclassified.
func ValidWorkKind(value WorkKind) bool {
	switch value {
	case "", WorkKindFeature, WorkKindBugFix, WorkKindRefactor, WorkKindMaintenance, WorkKindTestChange, WorkKindDocumentation, WorkKindOther:
		return true
	default:
		return false
	}
}

// OriginalRequirement is creation history, not an execution authorization.
// UNTRACEABLE never falls back to current Task or approved definition fields.
type OriginalRequirement struct {
	Status        string   `json:"status"`
	SourceEventID string   `json:"sourceEventId,omitempty"`
	Goal          string   `json:"goal,omitempty"`
	Constraints   []string `json:"constraints,omitempty"`
}

// RecoveryOrigin is read-only lifecycle history, never execution authority.
type RecoveryOrigin struct {
	Phase         Phase  `json:"phase"`
	SourceEventID string `json:"sourceEventId"`
}

type Task struct {
	RecoveryOrigin *RecoveryOrigin `json:"-"`
	// RepositoryTargets is populated by an immutable v2 definition, never by Project membership.
	RepositoryTargets   []workset.Target `json:"repositoryTargets,omitempty"`
	PrimaryMemberID     string           `json:"primaryMemberId,omitempty"`
	OriginalRequirement OriginalRequirement
	RequiredReviewDepth *ReviewDepth
	Title               string
	Phase               Phase
	CurrentReviewID     string
	CurrentExecutionID  string
	CommitSHA           string
	CommittedAt         *time.Time
	MergedCommit        string
	MergedAt            *time.Time

	ReviewApprovalID            string
	Type                        Type
	Scope                       Scope
	WorkOwnerID                 string
	Work                        Work
	ID                          string
	ProjectID                   string
	FeatureAreaID               string
	ModuleID                    string
	RepositoryID                string
	CodexConfigID               string
	WorkKind                    WorkKind
	Model                       string
	ReasoningEffort             string
	ProblemStatement            string
	ConfirmedTaskPlanID         string
	Goal                        string
	Constraints                 []string
	WorkSpec                    WorkSpec
	VerificationSpec            VerificationSpec
	BaseCommit                  string
	Status                      Status
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
	CurrentDefinitionRevisionID string
	LegacyReadOnly              bool
}

func clone(value Task) Task {
	value.RepositoryTargets = workset.CloneTargets(value.RepositoryTargets)
	if value.RequiredReviewDepth != nil {
		value.RequiredReviewDepth = ReviewDepthPointer(*value.RequiredReviewDepth)
	}
	value.Constraints = append([]string(nil), value.Constraints...)
	if value.RecoveryOrigin != nil {
		origin := *value.RecoveryOrigin
		value.RecoveryOrigin = &origin
	}
	value.OriginalRequirement.Constraints = append([]string(nil), value.OriginalRequirement.Constraints...)
	value.WorkSpec = CloneWorkSpec(value.WorkSpec)
	value.VerificationSpec = CloneVerificationSpec(value.VerificationSpec)
	return value
}

// CanAuthorPlan permits an independent plan proposal when no Task is executing.
// Storage enforces exact approval, cancellation and active authorization guards.
func CanAuthorPlan(status Status) bool {
	switch status {
	case StatusPlanning, StatusReady, StatusSucceeded, StatusFailed, StatusCanceled:
		return true
	}
	return false
}
