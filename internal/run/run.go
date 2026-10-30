package run

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codexusage"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/workset"
)

type Status string

// CodexTurnInterruptedCode records a correlated terminal native interruption.
// It conveys no continuation authority; that belongs to the lifecycle guards.
const CodexTurnInterruptedCode = "CODEX_TURN_INTERRUPTED"

const (
	StatusCreated              Status = "CREATED"
	StatusPreparing            Status = "PREPARING"
	StatusRunning              Status = "RUNNING"
	StatusWaitingApproval      Status = "WAITING_APPROVAL"
	StatusCollecting           Status = "COLLECTING"
	StatusSucceeded            Status = "SUCCEEDED"
	StatusFailed               Status = "FAILED"
	StatusInfrastructureFailed Status = "INFRASTRUCTURE_FAILED"
	StatusCanceled             Status = "CANCELED"
)

type Purpose string

const (
	PurposeRecoveryStatus Purpose = "RECOVERY_STATUS"
	PurposeInspection     Purpose = "INSPECTION"
	PurposeScopeReview    Purpose = "SCOPE_REVIEW"
	PurposeImplementation Purpose = "IMPLEMENTATION"
	PurposeVerification   Purpose = "VERIFICATION"
)

type FailureKind string

const (
	FailureTask           FailureKind = "TASK_FAILED"
	FailureInfrastructure FailureKind = "INFRASTRUCTURE_FAILED"
	FailureCanceled       FailureKind = "CANCELED"
)

type Failure struct {
	Kind    FailureKind
	Stage   string
	Code    string
	Message string
}

type Result struct {
	AgentClaim  evidence.AgentClaim
	GitEvidence evidence.GitEvidence
	Artifacts   []evidence.Artifact
}

// LegacyVerificationResultStatus is retained only for decoding v1 rows. It is not a
// Task-acceptance or v2 report status.
type LegacyVerificationResultStatus string

const (
	LegacyVerificationPassed          LegacyVerificationResultStatus = "PASSED"
	LegacyVerificationFailed          LegacyVerificationResultStatus = "FAILED"
	LegacyVerificationMutatedWorktree LegacyVerificationResultStatus = "VERIFICATION_MUTATED_WORKTREE"
)

type VerificationCheckStatus string

const (
	VerificationCheckPassed  VerificationCheckStatus = "PASSED"
	VerificationCheckFailed  VerificationCheckStatus = "FAILED"
	VerificationCheckBlocked VerificationCheckStatus = "BLOCKED"
	VerificationCheckNotRun  VerificationCheckStatus = "NOT_RUN"
	VerificationCheckUnknown VerificationCheckStatus = "UNKNOWN"
	// LegacyVerificationCheckSemanticResult is recognized only by the v1 decoder
	// and is immediately projected as UNKNOWN evidence.
	LegacyVerificationCheckSemanticResult VerificationCheckStatus = "SEMANTIC_RESULT_AVAILABLE"
)

type VerificationReportAvailability string

const (
	VerificationReportAvailable    VerificationReportAvailability = "AVAILABLE"
	VerificationReportUnavailable  VerificationReportAvailability = "UNAVAILABLE"
	VerificationReportInsufficient VerificationReportAvailability = "INSUFFICIENT"
)

// VerificationObservedFact contains only Bridge-observed command or Git facts.
// It is never populated from AgentClaim or from the agent's structured report.
type VerificationObservedFact struct {
	Source            *VerificationObservationSource `json:"source,omitempty"`
	Status            VerificationCheckStatus        `json:"status"`
	Command           string                         `json:"command,omitempty"`
	ExitCode          *int                           `json:"exitCode,omitempty"`
	Evidence          string                         `json:"evidence,omitempty"`
	EvidenceTruncated bool                           `json:"evidenceTruncated"`
	StartedAt         *time.Time                     `json:"startedAt,omitempty"`
	FinishedAt        *time.Time                     `json:"finishedAt,omitempty"`
	DurationMillis    *int64                         `json:"durationMillis,omitempty"`
	Reason            string                         `json:"reason,omitempty"`
	Summary           string                         `json:"summary,omitempty"`
}

// VerificationObservationSource binds a fact to the exact frozen Run member.
// Older reports remain readable without this field, but cannot substantiate a
// new repository-qualified acceptance without independently observed provenance.
type VerificationObservationSource struct {
	Kind               string `json:"kind"`
	RunID              string `json:"runId"`
	RuntimeWorksetHash string `json:"runtimeWorksetHash"`
	RepositoryID       string `json:"repositoryId"`
	MemberID           string `json:"memberId"`
	MemberVersion      int    `json:"memberVersion"`
	CWD                string `json:"cwd"`
	ItemID             string `json:"itemId,omitempty"`
	CommandStatus      string `json:"commandStatus,omitempty"`
	TaskPermissionHash string `json:"taskPermissionHash,omitempty"`
}

// VerificationReportedFact is the agent's schema-validated factual report.
// It is evidence for GPT/user assessment, not Core acceptance authority.
type VerificationReportedFact struct {
	Status   VerificationCheckStatus `json:"status"`
	Summary  string                  `json:"summary"`
	Evidence string                  `json:"evidence,omitempty"`
	Reason   string                  `json:"reason,omitempty"`
}

type VerificationCheckOutcome struct {
	ID                      string                    `json:"id"`
	Index                   int                       `json:"index"`
	Kind                    string                    `json:"kind"`
	Required                bool                      `json:"required"`
	Status                  VerificationCheckStatus   `json:"status"`
	DeclaredCommand         string                    `json:"declaredCommand,omitempty"`
	ObservedCommand         string                    `json:"observedCommand,omitempty"`
	ExitCode                *int                      `json:"exitCode,omitempty"`
	Evidence                string                    `json:"evidence,omitempty"`
	EvidenceTruncated       bool                      `json:"evidenceTruncated"`
	StartedAt               *time.Time                `json:"startedAt,omitempty"`
	FinishedAt              *time.Time                `json:"finishedAt,omitempty"`
	DurationMillis          *int64                    `json:"durationMillis,omitempty"`
	FailureReason           string                    `json:"failureReason,omitempty"`
	SemanticResultAvailable bool                      `json:"semanticResultAvailable,omitempty"`
	Summary                 string                    `json:"summary,omitempty"`
	Observed                *VerificationObservedFact `json:"observed,omitempty"`
	Reported                VerificationReportedFact  `json:"reported"`
}

type VerificationCriterionOutcome struct {
	Index     int                      `json:"index"`
	Criterion string                   `json:"criterion"`
	Reported  VerificationReportedFact `json:"reported"`
}

// VerificationReport is the v2 evidence envelope. Availability means a valid
// report was delivered and persisted; it says nothing about Task acceptance.
type VerificationReport struct {
	Version            int                            `json:"version"`
	Availability       VerificationReportAvailability `json:"availability"`
	Summary            string                         `json:"summary"`
	Checks             []VerificationCheckOutcome     `json:"checks"`
	AcceptanceCriteria []VerificationCriterionOutcome `json:"acceptanceCriteria"`
	LegacyResultStatus LegacyVerificationResultStatus `json:"legacyResultStatus,omitempty"`
	// Status exists only as a source-compatible view for old callers. New v2
	// reports leave it empty and never derive it from item facts.
	Status LegacyVerificationResultStatus `json:"-"`
}

// ValidateVerificationReportV2 validates the durable evidence envelope. Exact
// coverage of the confirmed VerificationSpec is additionally enforced by the
// execution coordinator before attachment.
func ValidateVerificationReportV2(value VerificationReport) error {
	if value.Version != 2 || (value.Availability != VerificationReportAvailable && value.Availability != VerificationReportInsufficient) || strings.TrimSpace(value.Summary) == "" || len(value.Summary) > 4096 || len(value.Checks) > 32 || len(value.AcceptanceCriteria) > 32 {
		return errors.New("invalid VerificationReport v2")
	}
	validFact := func(status VerificationCheckStatus, summary, evidence, reason string) bool {
		switch status {
		case VerificationCheckPassed, VerificationCheckFailed, VerificationCheckBlocked, VerificationCheckNotRun, VerificationCheckUnknown:
		default:
			return false
		}
		return strings.TrimSpace(summary) != "" && len(summary) <= 2048 && len(evidence) <= 32<<10 && len(reason) <= 2048
	}
	for index, check := range value.Checks {
		if check.ID != fmt.Sprintf("check-%d", index+1) || check.Index != index || strings.TrimSpace(check.Kind) == "" || !validFact(check.Reported.Status, check.Reported.Summary, check.Reported.Evidence, check.Reported.Reason) {
			return errors.New("invalid VerificationReport v2 check")
		}
		if check.Observed != nil && !validFact(check.Observed.Status, check.Observed.Summary, check.Observed.Evidence, check.Observed.Reason) {
			return errors.New("invalid VerificationReport v2 observed check")
		}
	}
	for index, criterion := range value.AcceptanceCriteria {
		if criterion.Index != index || strings.TrimSpace(criterion.Criterion) == "" || !validFact(criterion.Reported.Status, criterion.Reported.Summary, criterion.Reported.Evidence, criterion.Reported.Reason) {
			return errors.New("invalid VerificationReport v2 acceptance criterion")
		}
	}
	return nil
}

type TokenUsageStatus string

const (
	TokenUsagePending     TokenUsageStatus = "PENDING"
	TokenUsageAvailable   TokenUsageStatus = "AVAILABLE"
	TokenUsageUnavailable TokenUsageStatus = "UNAVAILABLE"
)

type TokenCounts struct {
	InputTokens           int64
	CachedInputTokens     int64
	CacheWriteInputTokens int64
	OutputTokens          int64
	ReasoningOutputTokens int64
	TotalTokens           int64
}

// StartCumulative and EndCumulative use Bridge logical coordinates for epoch-aware
// observations. Legacy rows retain their original same-epoch coordinates.
type TokenUsageAccounting struct {
	Status             TokenUsageStatus
	StartCumulative    TokenCounts
	EndCumulative      *TokenCounts
	Usage              *TokenCounts
	ModelContextWindow *int64
}

type TokenUsageObservation struct {
	Attribution        *codexusage.RunUsageAttribution
	StartCumulative    TokenCounts
	EndCumulative      TokenCounts
	ModelContextWindow *int64
	Reliable           bool
}

type OperationKind string

const (
	OperationInspection OperationKind = "INSPECTION"
	OperationReview     OperationKind = "REVIEW"
	OperationExecution  OperationKind = "EXECUTION"
)

type Run struct {
	// NativeResultState is a read-only projection of durable native terminal
	// observations. A Bridge failure or a final chat message alone is not one.
	NativeResultState   string `json:"nativeResultState,omitempty"`
	ResultProtocol      string `json:"resultProtocol,omitempty"`
	AnalysisState       string `json:"analysisState,omitempty"`
	AgentExecutionID    string
	AgentRuntime        *agentruntime.Snapshot `json:"agentRuntime,omitempty"`
	ProjectID           string
	FeatureAreaID       string
	OperationKind       OperationKind
	OperationID         string
	ID                  string
	RuntimeEpoch        string `json:"-"`
	ReviewID            string
	ContextProbeID      string
	TaskID              string
	Purpose             Purpose
	ExecutionProposalID string
	CodexInstruction    string
	FinalCodexPrompt    string
	PromptSHA256        string
	PromptBytes         int64
	PromptSentAt        *time.Time
	ReviewScope         ReviewScope
	ReviewPlan          ReviewPlan
	Attempt             int
	Status              Status
	WorkspaceID         string
	WorktreeID          string
	ThreadID            string
	TurnID              string
	StartSnapshot       string
	EndSnapshot         string
	StartedAt           *time.Time
	FinishedAt          *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	TokenUsage          TokenUsageAccounting
	// RuntimeWorkset is present only for Trusted Executor Model v1 Coding runs.
	// Its hash deliberately excludes RunID so serial verification can prove it
	// uses the same immutable target content.
	RuntimeWorkset     *workset.RuntimeWorkset `json:"runtimeWorkset,omitempty"`
	Result             *Result
	Failure            *Failure
	VerificationReport *VerificationReport
}

// WorkflowOwned distinguishes durable engineering operations from the legacy
// standalone Run compatibility surface. FeatureArea membership alone is not an
// operation owner.
func (v Run) WorkflowOwned() bool {
	return v.ExecutionProposalID != "" || v.OperationKind != "" || v.OperationID != "" || v.ReviewID != ""
}

// SameOwner compares the immutable execution identity. Mutable runtime
// observations cannot reassign a Run to another engineering operation.
func (v Run) SameOwner(other Run) bool {
	return v.AgentExecutionID == other.AgentExecutionID && v.ID == other.ID && v.ProjectID == other.ProjectID && v.FeatureAreaID == other.FeatureAreaID && v.OperationKind == other.OperationKind && v.OperationID == other.OperationID && v.TaskID == other.TaskID && v.ReviewID == other.ReviewID && v.ContextProbeID == other.ContextProbeID && v.ExecutionProposalID == other.ExecutionProposalID && v.RuntimeEpoch == other.RuntimeEpoch && v.Purpose == other.Purpose && v.Attempt == other.Attempt
}

// BindThread applies the Run-side invariant shared by the ordinary Run service
// and the first AgentConversation binding transaction. A backend Thread is an
// immutable observation: an exact replay is harmless, but a replacement is a
// conflict rather than a retry.
func BindThread(value *Run, threadID string, now time.Time) (bool, error) {
	if value == nil {
		return false, errors.New("run is required")
	}
	if value.Status != StatusPreparing {
		return false, &TransitionError{From: value.Status, To: StatusRunning}
	}
	if threadID == "" {
		return false, errors.New("thread id is required")
	}
	if value.ThreadID != "" {
		if value.ThreadID == threadID {
			return false, nil
		}
		return false, errors.New("run thread is immutable")
	}
	value.ThreadID = threadID
	value.UpdatedAt = now.UTC()
	return true, nil
}

func clone(value Run) Run {
	value.ReviewScope.Paths = append([]string(nil), value.ReviewScope.Paths...)
	value.ReviewPlan.InitialScope.Paths = append([]string(nil), value.ReviewPlan.InitialScope.Paths...)
	value.ReviewPlan.SelectedModules = append([]ReviewModuleSelection(nil), value.ReviewPlan.SelectedModules...)
	if value.StartedAt != nil {
		v := *value.StartedAt
		value.StartedAt = &v
	}
	if value.FinishedAt != nil {
		v := *value.FinishedAt
		value.FinishedAt = &v
	}
	if value.PromptSentAt != nil {
		v := *value.PromptSentAt
		value.PromptSentAt = &v
	}
	if value.RuntimeWorkset != nil {
		v := *value.RuntimeWorkset
		v.TaskPermissions = v.TaskPermissions.Clone()
		v.Members = append([]workset.RuntimeMemberBinding(nil), value.RuntimeWorkset.Members...)
		for i := range v.Members {
			v.Members[i].Write.Paths = append([]string(nil), value.RuntimeWorkset.Members[i].Write.Paths...)
		}
		value.RuntimeWorkset = &v
	}
	if value.Failure != nil {
		v := *value.Failure
		value.Failure = &v
	}
	if value.TokenUsage.EndCumulative != nil {
		v := *value.TokenUsage.EndCumulative
		value.TokenUsage.EndCumulative = &v
	}
	if value.TokenUsage.Usage != nil {
		v := *value.TokenUsage.Usage
		value.TokenUsage.Usage = &v
	}
	if value.TokenUsage.ModelContextWindow != nil {
		v := *value.TokenUsage.ModelContextWindow
		value.TokenUsage.ModelContextWindow = &v
	}
	if value.Result != nil {
		v := *value.Result
		v.GitEvidence.ChangedFiles = append([]string(nil), v.GitEvidence.ChangedFiles...)
		v.Artifacts = append([]evidence.Artifact(nil), v.Artifacts...)
		value.Result = &v
	}
	if value.VerificationReport != nil {
		v := *value.VerificationReport
		v.Checks = append([]VerificationCheckOutcome(nil), v.Checks...)
		v.AcceptanceCriteria = append([]VerificationCriterionOutcome(nil), v.AcceptanceCriteria...)
		for index := range v.Checks {
			if v.Checks[index].Observed != nil {
				observed := *v.Checks[index].Observed
				if observed.Source != nil {
					source := *observed.Source
					observed.Source = &source
				}
				if observed.ExitCode != nil {
					value := *observed.ExitCode
					observed.ExitCode = &value
				}
				if observed.StartedAt != nil {
					value := *observed.StartedAt
					observed.StartedAt = &value
				}
				if observed.FinishedAt != nil {
					value := *observed.FinishedAt
					observed.FinishedAt = &value
				}
				if observed.DurationMillis != nil {
					value := *observed.DurationMillis
					observed.DurationMillis = &value
				}
				v.Checks[index].Observed = &observed
			}
			if v.Checks[index].ExitCode != nil {
				exitCode := *v.Checks[index].ExitCode
				v.Checks[index].ExitCode = &exitCode
			}
			if v.Checks[index].StartedAt != nil {
				startedAt := *v.Checks[index].StartedAt
				v.Checks[index].StartedAt = &startedAt
			}
			if v.Checks[index].FinishedAt != nil {
				finishedAt := *v.Checks[index].FinishedAt
				v.Checks[index].FinishedAt = &finishedAt
			}
			if v.Checks[index].DurationMillis != nil {
				duration := *v.Checks[index].DurationMillis
				v.Checks[index].DurationMillis = &duration
			}
		}
		value.VerificationReport = &v
	}
	return value
}

type ReviewScopeKind string

const (
	ReviewScopePaths      ReviewScopeKind = "PATHS"
	ReviewScopeRepository ReviewScopeKind = "REPOSITORY"
)

// ReviewScope is the maximum read boundary for a SCOPE_REVIEW Run. It is
// intentionally distinct from the later confirmed mutation ExecutionScope.
type ReviewScope struct {
	Kind  ReviewScopeKind `json:"kind"`
	Paths []string        `json:"paths,omitempty"`
}

type ReviewMode string

const (
	ReviewModeFocused    ReviewMode = "FOCUSED"
	ReviewModeRepository ReviewMode = "REPOSITORY"
)

type ReviewModuleRole string

const (
	ReviewModulePrimary    ReviewModuleRole = "PRIMARY"
	ReviewModuleSupporting ReviewModuleRole = "SUPPORTING"
)

type ReviewModuleSelection struct {
	Key  string           `json:"key"`
	Role ReviewModuleRole `json:"role"`
}

type ReviewPlan struct {
	Mode            ReviewMode              `json:"mode"`
	InitialScope    ReviewScope             `json:"initialScope"`
	SelectedModules []ReviewModuleSelection `json:"selectedModules"`
}
