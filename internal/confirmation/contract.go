// Package confirmation stores purpose-specific user confirmation, not business
// lifecycle. Canonical Task/Review/Execution receipts remain authoritative.
package confirmation

import (
	"context"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

type Purpose string

const (
	ProjectSetup    Purpose = "PROJECT_SETUP"
	FeatureArea     Purpose = "FEATURE_AREA"
	Task            Purpose = "TASK"
	Review          Purpose = "REVIEW"
	Execution       Purpose = "EXECUTION"
	RecoveryPurpose Purpose = "RECOVERY_PURPOSE"
)

const ConfirmRecoveryPurpose = "CONFIRM_RECOVERY_PURPOSE"

type RecoveryPurposeBody struct {
	Proposal                       recoverystatus.PurposeProposal `json:"proposal"`
	ExpectedTaskStatus             string                         `json:"expectedTaskStatus"`
	ExpectedTaskPhase              string                         `json:"expectedTaskPhase"`
	PreparedTopologyRevision       int64                          `json:"preparedTopologyRevision"`
	PreparedDevelopmentContextHash string                         `json:"preparedDevelopmentContextHash"`
}

type ProjectBody struct {
	Summary string `json:"summary"`
}
type TaskBody struct {
	FeatureAreaID    string           `json:"featureAreaId"`
	RepositoryID     string           `json:"repositoryId"`
	Title            string           `json:"title"`
	ProblemStatement string           `json:"problemStatement"`
	Constraints      []string         `json:"constraints"`
	StartReview      bool             `json:"startReview"`
	ReviewDepth      task.ReviewDepth `json:"reviewDepth,omitempty"`
	ReviewPlan       run.ReviewPlan   `json:"reviewPlan"`
}
type AreaBody struct {
	CompletionTaskID string                    `json:"completionTaskId,omitempty"`
	FeatureAreaID    string                    `json:"featureAreaId,omitempty"`
	DeliveryPlan     *delivery.FeatureAreaPlan `json:"deliveryPlan,omitempty"`
	Title            string                    `json:"title"`
	ProblemStatement string                    `json:"problemStatement"`
	InitialTask      *TaskBody                 `json:"initialTask,omitempty"`
}
type ReviewBody struct {
	WorkspaceID                    string                      `json:"workspaceId,omitempty"`
	RepositoryTargets              []workset.Target            `json:"repositoryTargets,omitempty"`
	TaskID                         string                      `json:"taskId"`
	FeatureAreaID                  string                      `json:"featureAreaId"`
	RepositoryID                   string                      `json:"repositoryId,omitempty"`
	PreparedTopologyRevision       int64                       `json:"preparedTopologyRevision,omitempty"`
	PreparedDevelopmentContextHash string                      `json:"preparedDevelopmentContextHash,omitempty"`
	ReviewID                       string                      `json:"reviewId,omitempty"`
	DecisionID                     string                      `json:"decisionId,omitempty"`
	PreparedDecisionVersion        string                      `json:"preparedDecisionVersion,omitempty"`
	ReviewFeedback                 string                      `json:"reviewFeedback,omitempty"`
	ReviewDepth                    task.ReviewDepth            `json:"reviewDepth"`
	ReviewPlan                     run.ReviewPlan              `json:"reviewPlan"`
	SupplementalPurpose            string                      `json:"supplementalPurpose,omitempty"`
	ExpectedTarget                 *wp.TaskReviewRequestTarget `json:"expectedTarget,omitempty"`
}
type ExecutionBody struct {
	CompletionPlan                 *delivery.TaskCompletionPlan `json:"completionPlan,omitempty"`
	Note                           string                       `json:"note,omitempty"`
	RepositoryID                   string                       `json:"repositoryId,omitempty"`
	BaseCommit                     string                       `json:"baseCommit,omitempty"`
	PreparedTopologyRevision       int64                        `json:"preparedTopologyRevision,omitempty"`
	PreparedDevelopmentContextHash string                       `json:"preparedDevelopmentContextHash,omitempty"`

	WorkflowID               string                       `json:"workflowId,omitempty"`
	RuntimeEpoch             string                       `json:"runtimeEpoch,omitempty"`
	GrantGeneration          int64                        `json:"grantGeneration,omitempty"`
	Action                   string                       `json:"action"` // APPROVE_AND_START, START_APPROVED, RETRY_IMPLEMENTATION, REQUEST_CHANGES
	TaskID                   string                       `json:"taskId"`
	FeatureAreaID            string                       `json:"featureAreaId"`
	ReviewID                 string                       `json:"reviewId"`
	ExecutionID              string                       `json:"executionId"`
	TaskDefinitionRevisionID string                       `json:"taskDefinitionRevisionId"`
	DecisionID               string                       `json:"decisionId"`
	ExpectedTarget           *wp.TaskExecutionExpectation `json:"expectedTarget,omitempty"`
}
type Variant struct {
	RepositoryTargets     []workset.Target            `json:"repositoryTargets,omitempty"`
	PrimaryMemberID       string                      `json:"primaryMemberId,omitempty"`
	DisplaySummary        *DisplaySummary             `json:"displaySummary,omitempty"`
	ID                    string                      `json:"id"`
	Label                 string                      `json:"label"`
	Summary               string                      `json:"summary"`
	ExecutorKind          string                      `json:"executorKind,omitempty"`
	ImplementationMode    task.ImplementationMode     `json:"implementationMode,omitempty"`
	ReviewInstruction     string                      `json:"reviewInstruction,omitempty"`
	WorkSpec              *task.WorkSpec              `json:"workSpec,omitempty"`
	VerificationSpec      *task.VerificationSpec      `json:"verificationSpec,omitempty"`
	ExecutionInstructions *task.ExecutionInstructions `json:"executionInstructions,omitempty"`
}
type PrepareInput struct {
	TaskActionGeneration     int64                              `json:"taskActionGeneration,omitempty"`
	SubmittedHash            string                             `json:"-"`
	AISessionID              string                             `json:"aiSessionId"`
	RequestID                string                             `json:"requestId"`
	Purpose                  Purpose                            `json:"purpose"`
	Action                   string                             `json:"action"`
	SourceIntentID           string                             `json:"sourceIntentId,omitempty"`
	ProjectSetup             *ProjectBody                       `json:"projectSetup,omitempty"`
	FeatureArea              *AreaBody                          `json:"featureArea,omitempty"`
	Task                     *TaskBody                          `json:"task,omitempty"`
	Review                   *ReviewBody                        `json:"review,omitempty"`
	Execution                *ExecutionBody                     `json:"execution,omitempty"`
	RecoveryPurpose          *RecoveryPurposeBody               `json:"recoveryPurpose,omitempty"`
	Variants                 []Variant                          `json:"variants"`
	RecommendedRuntimeConfig *codexruntime.NextRunRuntimeConfig `json:"recommendedRuntimeConfig,omitempty"`
}

// ReprepareInput is the closed client request for replacing one confirmation
// intent that is stale before submission or has an exact no-effect rejection.
// It names only the source identity and this request: the replacement intentId,
// version, Decision version token, topology revision, development-context hash,
// Task/Review identity, variants and targets are all re-derived by Core from the
// current facts, so a client can never forge a replacement authority.
type ReprepareInput struct {
	SubmittedHash     string `json:"-"`
	AISessionID       string `json:"aiSessionId"`
	SourceIntentID    string `json:"sourceIntentId"`
	SourceVersion     int    `json:"sourceVersion"`
	SourceContentHash string `json:"sourceContentHash"`
	RequestID         string `json:"requestId"`
	// ReviewFeedback is the only mutable field an ordinary Decision replacement may carry,
	// and it stays bounded Review feedback rather than a new instruction body.
	ReviewFeedback string `json:"reviewFeedback,omitempty"`
	// RecoveryPurpose is a submitted candidate, never current owner authority.
	// Core revalidates its immutable source binding and freezes current context.
	RecoveryPurpose *recoverystatus.PurposeProposal `json:"recoveryPurpose,omitempty"`
}

type RepositoryDevelopmentSelection struct {
	RepositoryID   string `json:"repositoryId"`
	DevelopmentRef string `json:"developmentRef"`
}
type DevelopmentContextOption struct {
	RepositoryID   string   `json:"repositoryId"`
	Label          string   `json:"label"`
	DevelopmentRef string   `json:"developmentRef"`
	Available      bool     `json:"available"`
	Reason         string   `json:"reason,omitempty"`
	Branches       []Choice `json:"branches"`
}
type Selection struct {
	ReviewMode string `json:"reviewMode,omitempty"` // UPPER_LLM, CODING_AGENT, NONE; explicit user choice on Task creation.
	// StartReview may narrow a TASK creation to create-only. Nil preserves the
	// prepared action for existing clients; true never adds unprepared Review work.
	StartReview         *bool                              `json:"startReview,omitempty"`
	VerificationWaiver  *task.VerificationWaiver           `json:"verificationWaiver,omitempty"`
	DevelopmentContexts []RepositoryDevelopmentSelection   `json:"developmentContexts,omitempty"`
	ProjectID           string                             `json:"projectId,omitempty"`
	RepositoryID        string                             `json:"repositoryId,omitempty"`
	DevelopmentRef      string                             `json:"developmentRef,omitempty"`
	RuntimeConfig       *codexruntime.NextRunRuntimeConfig `json:"runtimeConfig,omitempty"`
	Backend             *codexconfig.BackendSelectionInput `json:"backend,omitempty"`
}
type CommitInput struct {
	SubmittedHash   string       `json:"-"`
	OriginalRequest *CommitInput `json:"-"`
	AISessionID     string       `json:"aiSessionId"`
	IntentID        string       `json:"intentId"`
	Version         int          `json:"version"`
	Purpose         Purpose      `json:"purpose"`
	Action          string       `json:"action"`
	RequestID       string       `json:"requestId"`
	ContentHash     string       `json:"contentHash"`
	VariantID       string       `json:"variantId"`
	Selection       Selection    `json:"selection"`
	// BackendSelection is Core-resolved frozen evidence. It is never part of a
	// client request or its original submission receipt.
	BackendSelection *codexconfig.BackendSelection `json:"backendSelection,omitempty"`
}
type Step struct {
	RepositoryID   string   `json:"repositoryId,omitempty"`
	DevelopmentRef string   `json:"developmentRef,omitempty"`
	Failure        *Failure `json:"failure,omitempty"`
	Effect         string   `json:"effect,omitempty"` // NOT_STARTED, COMMAND_COMPLETED, RESERVED, RUNTIME_HANDOFF_ACCEPTED, UNKNOWN
	Kind           string   `json:"kind"`
	RequestID      string   `json:"requestId"`
	StartRequestID string   `json:"startRequestId,omitempty"`
	State          string   `json:"state"` // PENDING, UNKNOWN, COMPLETED, REJECTED
	ProjectID      string   `json:"projectId,omitempty"`
	FeatureAreaID  string   `json:"featureAreaId,omitempty"`
	TaskID         string   `json:"taskId,omitempty"`
	ReviewID       string   `json:"reviewId,omitempty"`
	ExecutionID    string   `json:"executionId,omitempty"`
	RunID          string   `json:"runId,omitempty"`
	RevisionID     string   `json:"revisionId,omitempty"`
	WorkflowID     string   `json:"workflowId,omitempty"`
	Code           string   `json:"code,omitempty"`
	Message        string   `json:"message,omitempty"`
}
type Intent struct {
	ReviewStartPreparation    *ReviewStartPreparation `json:"reviewStartPreparation,omitempty"`
	ReplacementReceipt        *ReplacementReceipt     `json:"replacementReceipt,omitempty"`
	Submission                *Submission             `json:"submission,omitempty"`
	CurrentUpperReadAvailable bool                    `json:"currentUpperReadAvailable"`
	CurrentDirectFile         *directfile.State       `json:"currentDirectFile,omitempty"`
	CurrentUpperReview        *review.UpperAccess     `json:"currentUpperReview,omitempty"`
	CurrentAccessReadError    string                  `json:"currentAccessReadError,omitempty"`
	CanResume                 bool                    `json:"canResume"`
	IntentID                  string                  `json:"intentId"`
	Version                   int                     `json:"version"`
	AISessionID               string                  `json:"aiSessionId"`
	ProjectID                 string                  `json:"projectId"`
	Purpose                   Purpose                 `json:"purpose"`
	Action                    string                  `json:"action"`
	State                     string                  `json:"state"` // PENDING, CONFIRMING, COMPLETED, PARTIAL, UNKNOWN, REJECTED, SUPERSEDED, ABANDONED
	ContentHash               string                  `json:"contentHash"`
	Content                   PrepareInput            `json:"content"`
	Commit                    *CommitInput            `json:"commit,omitempty"`
	Steps                     []Step                  `json:"steps"`
	CreatedAt                 time.Time               `json:"createdAt"`
	ExpiresAt                 time.Time               `json:"expiresAt"`
}

// ReviewStartPreparation is detached current-state guidance, never a mutation
// of the consumed intent, proof of an old writer's effects, or start authority.
type ReviewStartPreparation struct {
	Action                  string                     `json:"action"`
	TaskID                  string                     `json:"taskId"`
	FeatureAreaID           string                     `json:"featureAreaId"`
	ExpectedTarget          wp.TaskReviewRequestTarget `json:"expectedTarget"`
	RequiredReviewDepth     *task.ReviewDepth          `json:"requiredReviewDepth"`
	AllowedReviewDepths     []task.ReviewDepth         `json:"allowedReviewDepths"`
	RequiresNewConfirmation bool                       `json:"requiresNewConfirmation"`
	ReasonCode              string                     `json:"reasonCode"`
	Summary                 string                     `json:"summary"`
}
type Store interface {
	ReplayPrepare(context.Context, string, string, string) (Intent, error)
	ReadSubmission(context.Context, ReadInput) (Submission, error)
	RejectCommit(context.Context, CommitInput, *Failure) (Intent, error)
	Prepare(context.Context, string, PrepareInput) (Intent, error)
	Get(context.Context, string, string) (Intent, error)
	BeginCommit(context.Context, CommitInput, []Step) (Intent, bool, error)
	RecordStep(context.Context, string, string, int, Step, string) (Intent, error)
	Resume(context.Context, string, string, int) (Intent, bool, error)
}

// ReadInput is the exact public read identity of one Confirmation. ContentHash is
// part of the identity, not a filter: a read is only ever answered for the frozen
// intent content it names, and Core re-verifies the supplied hash against durable
// state. intentId/version alone cannot address a read (§7).
type ReadInput struct {
	RequestID string `json:"requestId,omitempty"`
	// ReplacementRequestID selects a replacement receipt, including a typed
	// not-found result. It is mutually exclusive with a commit requestId.
	ReplacementRequestID string  `json:"replacementRequestId,omitempty"`
	AISessionID          string  `json:"aiSessionId"`
	IntentID             string  `json:"intentId"`
	Version              int     `json:"version"`
	Purpose              Purpose `json:"purpose"`
	ContentHash          string  `json:"contentHash"`
}
type OptionsInput struct {
	ReadInput
	StartReview  *bool  `json:"startReview,omitempty"`
	ProjectID    string `json:"projectId,omitempty"`
	RepositoryID string `json:"repositoryId,omitempty"`
}

// Options contains current read-only choices; it does not mutate intent content.
type Choice struct {
	Failures   []Failure `json:"failures,omitempty"`
	HeadCommit string    `json:"headCommit,omitempty"`
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	Available  bool      `json:"available"`
	Reason     string    `json:"reason,omitempty"`
}
type Options struct {
	// TaskContinuity is live creation guidance, never a command or write target.
	TaskContinuity *TaskContinuity `json:"taskContinuity,omitempty"`
	// StartReview echoes the TASK choice these options qualify, including false.
	// It is a read observation, not authorization or a change to frozen content.
	StartReview *bool `json:"startReview,omitempty"`
	// RuntimeSelectionMode distinguishes a new choice from an existing frozen
	// Execution whose missing configuration must never become editable defaults.
	RuntimeSelectionMode        string                     `json:"runtimeSelectionMode,omitempty"`
	DevelopmentContexts         []DevelopmentContextOption `json:"developmentContexts"`
	RepositoryInventoryComplete bool                       `json:"repositoryInventoryComplete"`
	// The options envelope echoes only the four-field identity it was read for. An
	// OptionsInput carries the frozen content hash that binds the read, but the
	// public options output never re-projects it, so this stays narrower than ReadInput.
	AISessionID               string                               `json:"aiSessionId"`
	IntentID                  string                               `json:"intentId"`
	Version                   int                                  `json:"version"`
	Purpose                   Purpose                              `json:"purpose"`
	ProjectID                 string                               `json:"projectId"`
	RepositoryID              string                               `json:"repositoryId"`
	Projects                  []Choice                             `json:"projects"`
	Repositories              []Choice                             `json:"repositories"`
	Branches                  []Choice                             `json:"branches"`
	Variants                  []Choice                             `json:"variants"`
	RuntimeOptions            []codexruntime.RuntimeOption         `json:"runtimeOptions"`
	RuntimeInventoryRevision  uint64                               `json:"runtimeInventoryRevision"`
	RuntimeInventoryReadError string                               `json:"runtimeInventoryReadError,omitempty"`
	RuntimeInventorySource    string                               `json:"runtimeInventorySource"`
	LockedRuntimeConfig       *codexruntime.NextRunRuntimeConfig   `json:"lockedRuntimeConfig,omitempty"`
	BackendCandidates         []codexconfig.BackendSelection       `json:"backendCandidates,omitempty"`
	BackendInventoryComplete  bool                                 `json:"backendInventoryComplete"`
	BackendInventoryReadError string                               `json:"backendInventoryReadError,omitempty"`
	LockedBackend             *codexconfig.BackendSelection        `json:"lockedBackend,omitempty"`
	BackendContinuity         *agentconversation.BackendContinuity `json:"backendContinuity,omitempty"`
	ConversationModelDefault  *agentconversation.ModelDefault      `json:"conversationModelDefault,omitempty"`
	Eligibility               IntentEligibility                    `json:"eligibility"`
}

// IntentEligibility is the single read-side admission result consumed before
// commit. The commit path performs the same validation again; these facts are
// never accepted from a client and never grant authority.
type IntentEligibility struct {
	Available                bool   `json:"available"`
	ReasonCode               string `json:"reasonCode,omitempty"`
	ReasonMessage            string `json:"reasonMessage,omitempty"`
	ReprepareAllowed         bool   `json:"reprepareAllowed"`
	CurrentGeneration        string `json:"currentGeneration,omitempty"`
	PreparedGeneration       string `json:"preparedGeneration,omitempty"`
	CurrentTopologyRevision  int64  `json:"currentTopologyRevision,omitempty"`
	PreparedTopologyRevision int64  `json:"preparedTopologyRevision,omitempty"`
	CurrentDecisionVersion   string `json:"currentDecisionVersion,omitempty"`
	PreparedDecisionVersion  string `json:"preparedDecisionVersion,omitempty"`
	AttemptState             string `json:"attemptState"`
	ReceiptState             string `json:"receiptState"`
	// NextAction and RetryDisposition mirror the intent's latest durable
	// submission. A surface that only reads options must still be told what the
	// user may do next; these are derived reads, never client input.
	NextAction       NextAction `json:"nextAction,omitempty"`
	RetryDisposition string     `json:"retryDisposition,omitempty"`
}
