// Package workflowprogression defines the client-neutral development contract.
// Configured upper LLMs express user intent; only Core decides legal progression.
package workflowprogression

import (
	"context"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/directfile"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

const MaxReviewFeedbackBytes = 8192

// ValidReviewFeedback preserves every user byte that is accepted for the
// immutable successor instruction. Whitespace is meaningful and is never
// trimmed by this boundary.
func ValidReviewFeedback(value string) bool {
	return value != "" && len(value) <= MaxReviewFeedbackBytes && strings.IndexByte(value, 0) < 0 && utf8.ValidString(value)
}

const KindReviewApproval = "REVIEW_APPROVAL"
const KindInvalidReviewResultResolution = "INVALID_REVIEW_RESULT_RESOLUTION"

const (
	KindTaskAcceptance              = decisionresolution.KindTaskAcceptance
	AcceptanceAccept                = "ACCEPT"
	AcceptanceReject                = "REJECT"
	AcceptanceRequestChanges        = "REQUEST_CHANGES"
	AcceptanceRetryImplementation   = "RETRY_IMPLEMENTATION"
	AcceptanceRequestReverification = "REQUEST_REVERIFICATION"
)

type StartEffect string

const (
	StartEffectNoStart         StartEffect = "NO_START"
	StartEffectStartAuthorized StartEffect = "START_AUTHORIZED"
)

func (e StartEffect) Valid() bool {
	return e == StartEffectNoStart || e == StartEffectStartAuthorized
}

func DecisionID(reviewID string) string { return KindReviewApproval + ":" + reviewID }
func InvalidResultDecisionID(reviewID string) string {
	return KindInvalidReviewResultResolution + ":" + reviewID
}

type Action struct {
	Code     string `json:"code"`
	Required bool   `json:"required"`
	Message  string `json:"message"`
}
type Approval struct {
	// CreatedAt is the durable decision creation fact; nil means unavailable.
	CreatedAt *time.Time                   `json:"createdAt,omitempty"`
	Proposal  *review.ImplementationResult `json:"proposal,omitempty"`
	// DecisionVersion is the Core-frozen identity token for this Decision's durable
	// status and ownership facts. It is never the DecisionID and changes whenever
	// the Decision's status, resolution or owning Review changes.
	DecisionVersion string `json:"decisionVersion,omitempty"`
	// ReasonCode is the reducer's precise refusal code once a Decision is no
	// longer actionable: DECISION_NOT_PENDING, DECISION_SUPERSEDED or
	// DECISION_OWNER_CHANGED. It is empty while the Decision may be resolved.
	ReasonCode       string   `json:"reasonCode,omitempty"`
	DecisionID       string   `json:"decisionId"`
	ReviewID         string   `json:"reviewId"`
	TaskID           string   `json:"taskId"`
	StartRequestID   string   `json:"startRequestId"`
	ProvenanceSHA256 string   `json:"provenanceSha256"`
	ResultSHA256     string   `json:"resultSha256"`
	Summary          string   `json:"summary"`
	Status           string   `json:"status"`
	AllowedActions   []string `json:"allowedActions"`
}
type ReviewFact struct {
	ExecutorKind      review.ExecutorKind `json:"executorKind"`
	AccessState       string              `json:"accessState,omitempty"`
	ReviewDepth       task.ReviewDepth    `json:"reviewDepth"`
	ReviewDisposition review.Disposition  `json:"reviewDisposition,omitempty"`
	ID                string              `json:"id"`
	Status            string              `json:"status"`
}
type ReviewMaterializationFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type TaskFact struct {
	Phase  task.Phase `json:"phase"`
	ID     string     `json:"id"`
	Status string     `json:"status"`
}

type Guidance struct {
	RequiredReviewDepth      *task.ReviewDepth  `json:"requiredReviewDepth"`
	AllowedReviewDepths      []task.ReviewDepth `json:"allowedReviewDepths"`
	TaskPhase                task.Phase         `json:"taskPhase,omitempty"`
	FeatureAreaStatus        string             `json:"featureAreaStatus,omitempty"`
	CurrentTask              *TaskFact          `json:"currentTask,omitempty"`
	LatestTask               *TaskFact          `json:"latestTask,omitempty"`
	CurrentWorkspaceID       string             `json:"currentWorkspaceId,omitempty"`
	LatestReviewStatus       string             `json:"latestReviewStatus,omitempty"`
	CurrentWorkflowReview    *ReviewFact        `json:"currentWorkflowReview,omitempty"`
	WorkflowMode             string             `json:"workflowMode"`
	RuntimeApprovalID        string             `json:"runtimeApprovalId,omitempty"`
	RunID                    string             `json:"runId,omitempty"`
	SourceSequence           int64              `json:"sourceSequence"`
	Status                   string             `json:"status"`
	Message                  string             `json:"message"`
	CurrentState             string             `json:"currentState"`
	NextAction               Action             `json:"nextAction"`
	ProjectID                string             `json:"projectId"`
	FeatureAreaID            string             `json:"featureAreaId,omitempty"`
	ReviewID                 string             `json:"reviewId,omitempty"`
	DecisionID               string             `json:"decisionId,omitempty"`
	TaskID                   string             `json:"taskId,omitempty"`
	TaskDefinitionRevisionID string             `json:"taskDefinitionRevisionId,omitempty"`
	ExecutionID              string             `json:"executionId,omitempty"`
	Generation               string             `json:"generation"`
	Approval                 *Approval          `json:"reviewApproval,omitempty"`
	// ReviewConfirmation is the product name for the same durable Review
	// approval authority. It is an alias, not a second decision record.
	ReviewConfirmation       *Approval                     `json:"reviewConfirmation,omitempty"`
	ReviewRuntimeStatus      string                        `json:"reviewRuntimeStatus,omitempty"`
	RawResultAvailable       bool                          `json:"rawResultAvailable"`
	FormalResultAvailable    bool                          `json:"formalResultAvailable"`
	FormalResultStatus       string                        `json:"formalResultStatus,omitempty"`
	ReviewDisposition        review.Disposition            `json:"reviewDisposition,omitempty"`
	ReviewConfirmationStatus string                        `json:"reviewConfirmationStatus,omitempty"`
	MaterializationFailure   *ReviewMaterializationFailure `json:"materializationFailure,omitempty"`
	RequiredAction           Action                        `json:"requiredAction"`
	AvailableActions         []string                      `json:"availableActions"`
	Legacy                   bool                          `json:"legacyWorkflow,omitempty"`
	RefreshRequired          bool                          `json:"refreshRequired,omitempty"`
}

// CanCreateTask exposes independent intent creation in an available FeatureArea.
func (g Guidance) CanCreateTask() bool {
	return g.FeatureAreaStatus == "ACTIVE" || g.FeatureAreaStatus == "DISCOVERING"
}

func (g *Guidance) Set(state, action, message string) {
	g.CurrentState, g.Message = state, message
	g.NextAction = Action{Code: action, Required: action != "WAIT_FOR_USER_TASK" && action != "WAIT_FOR_IMPLEMENTATION_START" && action != "WORKFLOW_COMPLETE" && action != "WORKFLOW_CLOSED" && action != "WORKFLOW_REJECTED", Message: message}
	g.RequiredAction = g.NextAction
}

type Error struct {
	Code             string `json:"code"`
	Outcome          string `json:"outcome,omitempty"`
	RetryDisposition string `json:"retryDisposition,omitempty"`
	Guidance
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func Reject(code string, g Guidance) *Error {
	g.Status = "REJECTED"
	return &Error{Code: code, Guidance: g}
}

type ResolveInput struct {
	// Internal consumed confirmation reference; never accepted as a public tool input.
	ConfirmationIntentID  string                      `json:"-"`
	ExecutionInstructions *task.ExecutionInstructions `json:"executionInstructions,omitempty"`
	TaskID                string                      `json:"taskId"`
	AISessionID           string                      `json:"aiSessionId"`
	// OperatorPrincipalID is internal-only and comes from localoperator, never
	// a browser payload or a caller supplied header.
	OperatorPrincipalID  string                             `json:"-"`
	FeatureAreaID        string                             `json:"featureAreaId"`
	ReviewID             string                             `json:"-"`
	DecisionID           string                             `json:"decisionId"`
	Option               string                             `json:"option"`
	RequestID            string                             `json:"requestId"`
	NextRunRuntimeConfig *codexruntime.NextRunRuntimeConfig `json:"nextRunRuntimeConfig,omitempty"`
	BackendSelection     *codexconfig.BackendSelection      `json:"backendSelection,omitempty"`
	StartEffect          StartEffect                        `json:"-"`
	StartRequestID       string                             `json:"-"`
	// ReviewFeedback is accepted only for the REVISE confirmation decision.
	// It is retained verbatim by Core as the immutable instruction for the
	// successor Review; clients cannot supply successor identities or scope.
	ReviewFeedback string `json:"reviewFeedback,omitempty"`
}
type Resolution struct {
	DirectFile *directfile.State `json:"directFile,omitempty"`
	Guidance
	Receipt              decisionresolution.Receipt `json:"receipt"`
	Task                 *task.Task                 `json:"task,omitempty"`
	Replay               bool                       `json:"replay"`
	StartEffect          StartEffect                `json:"startEffect,omitempty"`
	SuccessorReviewID    string                     `json:"successorReviewId,omitempty"`
	SuccessorRunID       string                     `json:"successorRunId,omitempty"`
	ReviewStartRequestID string                     `json:"reviewStartRequestId,omitempty"`
}

type TaskAcceptanceInput struct {
	// Only the explicit completion confirmation can supply this Core-frozen plan.
	CompletionPlan                  *delivery.TaskCompletionPlan `json:"-"`
	RecoveryStatusID                string                       `json:"recoveryStatusId,omitempty"`
	RecoveryContinuationInstruction string                       `json:"recoveryContinuationInstruction,omitempty"`
	ExecutionInstructions           *task.ExecutionInstructions  `json:"executionInstructions,omitempty"`
	TaskID                          string                       `json:"taskId,omitempty"`
	FeatureAreaID                   string                       `json:"featureAreaId,omitempty"`
	TaskDefinitionRevisionID        string                       `json:"taskDefinitionRevisionId,omitempty"`
	AISessionID                     string                       `json:"aiSessionId"`
	// OperatorPrincipalID is internal-only; see ResolveInput.
	OperatorPrincipalID   string                             `json:"-"`
	DecisionID            string                             `json:"decisionId"`
	ExecutionID           string                             `json:"executionId"`
	Option                string                             `json:"option"`
	RequestID             string                             `json:"requestId"`
	NextWorkSpec          *task.WorkSpec                     `json:"nextWorkSpec,omitempty"`
	NextVerificationSpec  *task.VerificationSpec             `json:"nextVerificationSpec,omitempty"`
	NextRepositoryTargets []workset.Target                   `json:"nextRepositoryTargets,omitempty"`
	NextPrimaryMemberID   string                             `json:"nextPrimaryMemberId,omitempty"`
	ConfirmationIntentID  string                             `json:"-"`
	NextRunRuntimeConfig  *codexruntime.NextRunRuntimeConfig `json:"nextRunRuntimeConfig,omitempty"`
	// BackendSelection is Core-frozen evidence. Successors currently inherit
	// the predecessor backend; a different supplied identity is a conflict.
	BackendSelection *codexconfig.BackendSelection `json:"backendSelection,omitempty"`
	// StartEffect is derived by the trusted command/orchestration layer. It is
	// never caller-selectable independently of the business action.
	StartEffect StartEffect `json:"-"`
}

type TaskAcceptanceResolution struct {
	CommitError          string                     `json:"commitError,omitempty"`
	Receipt              decisionresolution.Receipt `json:"receipt"`
	Task                 task.Task                  `json:"task"`
	SuccessorExecutionID string                     `json:"successorExecutionId,omitempty"`
	StartEffect          StartEffect                `json:"startEffect,omitempty"`
	Replay               bool                       `json:"replay"`
}

func TaskAcceptanceDecisionID(executionID string) string {
	return decisionresolution.TaskAcceptanceDecisionID(executionID)
}

type TaskAcceptanceStore interface {
	ResolveTaskAcceptance(context.Context, TaskAcceptanceInput) (TaskAcceptanceResolution, error)
}
type Store interface {
	Read(context.Context, string, string) (Guidance, error)
	Resolve(context.Context, ResolveInput) (Resolution, error)
}

// ResourceScope locates command-owned facts; it is not a workflow classifier.
type ResourceScope struct {
	AISessionID, ProjectID, FeatureAreaID, TaskID, ReviewID, ExecutionID, RunID, OwnerType, OwnerID string
}
