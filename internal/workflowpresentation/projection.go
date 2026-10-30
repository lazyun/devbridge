// Package workflowpresentation defines the client-independent FeatureArea read
// contract. Renderers consume these facts mechanically; they never query domain
// stores, resolve decisions, or infer workflow state from display text.
package workflowpresentation

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
)

type Identity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type RepositoryContext struct {
	Repository         Identity `json:"repository"`
	DevelopmentCycleID string   `json:"developmentCycleId,omitempty"`
	DevelopmentRef     string   `json:"developmentRef"`
	Branch             string   `json:"branch"`
	Revision           string   `json:"revision"`
	SelectedRevision   string   `json:"selectedRevision"`
	Status             string   `json:"status"`
	SelectionRequired  bool     `json:"selectionRequired"`
	RefChanged         *bool    `json:"refChanged,omitempty"`
	CheckoutMatches    *bool    `json:"checkoutMatches,omitempty"`
}

type DevelopmentContext struct {
	Project           Identity            `json:"project"`
	SelectionRequired bool                `json:"selectionRequired"`
	Ready             bool                `json:"ready"`
	Repositories      []RepositoryContext `json:"repositories"`
}

type PhaseProgress struct {
	Phase    string `json:"phase"`
	Status   string `json:"status"`
	SourceID string `json:"sourceId,omitempty"`
	Ordinal  int    `json:"ordinal,omitempty"`
}

type AgentOperation struct {
	Status         string     `json:"status"`
	Message        string     `json:"message,omitempty"`
	ConversationID string     `json:"conversationId,omitempty"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
}

type Operation struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	RunID  string `json:"runId,omitempty"`
	Status string `json:"status"`
}

type Decision struct {
	// CreatedAt belongs to this exact decision, never its Task or Review.
	CreatedAt      *time.Time      `json:"createdAt,omitempty"`
	Kind           string          `json:"kind"`
	ID             string          `json:"id"`
	SubjectID      string          `json:"subjectId"`
	Summary        string          `json:"summary"`
	AllowedActions []string        `json:"allowedActions"`
	Guarantee      string          `json:"guarantee"`
	CancelEffect   string          `json:"cancelEffect,omitempty"`
	Permissions    json.RawMessage `json:"permissions,omitempty"`
}

// ActionTarget contains exact observed subject identity, never user-editable
// workflow state. Every authoritative action revalidates this target.
type ActionTarget struct {
	TaskActionGeneration     int64                           `json:"taskActionGeneration,omitempty"`
	RepositoryID             string                          `json:"repositoryId,omitempty"`
	PrimaryMemberID          string                          `json:"primaryMemberId,omitempty"`
	ApprovedWorksetHash      string                          `json:"approvedWorksetHash,omitempty"`
	ExpectedReviewRequest    *wp.TaskReviewRequestTarget     `json:"expectedReviewRequest,omitempty"`
	ExpectedUpperVersion     *agentconversation.UpperVersion `json:"expectedUpperVersion,omitempty"`
	TaskDefinitionRevisionID string                          `json:"taskDefinitionRevisionId,omitempty"`
	TaskID                   string                          `json:"taskId,omitempty"`
	FeatureAreaID            string                          `json:"featureAreaId,omitempty"`
	DecisionID               string                          `json:"decisionId,omitempty"`
	SubjectID                string                          `json:"subjectId,omitempty"`
	ExecutionID              string                          `json:"executionId,omitempty"`
	OperationKind            string                          `json:"operationKind,omitempty"`
	OperationID              string                          `json:"operationId,omitempty"`
	RunID                    string                          `json:"runId,omitempty"`
	AgentConversationID      string                          `json:"agentConversationId,omitempty"`
}

type Cancellation struct {
	State            string `json:"state"`
	RunID            string `json:"runId,omitempty"`
	RecoveryRequired bool   `json:"recoveryRequired"`
}

type Recovery struct {
	Reason       string `json:"reason,omitempty"`
	Prerequisite string `json:"prerequisite,omitempty"`
	Status       string `json:"status"`
	Required     bool   `json:"required"`
	Summary      string `json:"summary"`
}

type Capability struct {
	RequiredReviewDepth *task.ReviewDepth  `json:"requiredReviewDepth,omitempty"`
	AllowedReviewDepths []task.ReviewDepth `json:"allowedReviewDepths,omitempty"`
	RequiredInputs      []string           `json:"requiredInputs,omitempty"`
	Action              string             `json:"action"`
	// Available means observed Bridge eligibility, never adapter wiring or an
	// authorization grant. Command owners must revalidate their exact subjects.
	Available      bool          `json:"available"`
	DecisionOption string        `json:"decisionOption,omitempty"`
	Reason         string        `json:"reason,omitempty"`
	Target         *ActionTarget `json:"target,omitempty"`
}

// Details retains exact immutable identities; it is navigation metadata, never
// a new history model or an authorization payload.
type Details struct {
	View                     string `json:"view"`
	ProjectID                string `json:"projectId"`
	FeatureAreaID            string `json:"featureAreaId"`
	ReviewID                 string `json:"reviewId,omitempty"`
	TaskID                   string `json:"taskId,omitempty"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId,omitempty"`
	TaskPlanID               string `json:"taskPlanId,omitempty"`
	ExecutionID              string `json:"executionId,omitempty"`
	RunID                    string `json:"runId,omitempty"`
	Baseline                 string `json:"baseline,omitempty"`
}

// TaskStages is an exact selected Task's read-only execution-stage view.
// Unlike Progress, these facts retain terminal Run outcomes during recovery.
type TaskStages struct {
	Recovery *Recovery       `json:"recovery,omitempty"`
	ReadOnly bool            `json:"readOnly"`
	TaskID   string          `json:"taskId"`
	Title    string          `json:"title"`
	Phase    string          `json:"phase"`
	Status   string          `json:"status"`
	Approved bool            `json:"approved"`
	Stages   []PhaseProgress `json:"stages"`
}

type TaskOptions struct {
	Models           []string                              `json:"models"`
	ReasoningEfforts []string                              `json:"reasoningEfforts"`
	ModelConfigs     []codexruntime.CodingAgentModelConfig `json:"modelConfigs,omitempty"`
	// RuntimeOptions is the closed, runtime-authoritative option set for a new
	// Coding Agent successor. It carries Fast support explicitly rather than
	// asking a renderer to infer it from a model name or service tier.
	RuntimeOptions            []codexruntime.RuntimeOption         `json:"runtimeOptions,omitempty"`
	BackendCandidates         []codexconfig.BackendSelection       `json:"backendCandidates,omitempty"`
	BackendInventoryComplete  bool                                 `json:"backendInventoryComplete"`
	BackendInventoryReadError string                               `json:"backendInventoryReadError,omitempty"`
	LockedBackend             *codexconfig.BackendSelection        `json:"lockedBackend,omitempty"`
	BackendContinuity         *agentconversation.BackendContinuity `json:"backendContinuity,omitempty"`
	ConversationModelDefault  *agentconversation.ModelDefault      `json:"conversationModelDefault,omitempty"`
	// PiAgentEnabled mirrors the server's PI Agent runtime availability so
	// renderers only offer the PI_AGENT executor when it can actually run.
	PiAgentEnabled bool `json:"piAgentEnabled,omitempty"`
}

type Projection struct {
	VerificationDecisions *VerificationDecisions        `json:"verificationDecisions,omitempty"`
	NextStep              *TaskNextStep                 `json:"nextStep,omitempty"`
	DirectFile            *directfile.State             `json:"directFile,omitempty"`
	CommitStatus          string                        `json:"commitStatus,omitempty"`
	MergedCommit          string                        `json:"mergedCommit,omitempty"`
	CanCommit             bool                          `json:"canCommit"`
	CommitView            *CommitView                   `json:"commitView,omitempty"`
	Commit                *featurearea.CommitCapability `json:"commit,omitempty"`

	LatestTaskView        *TaskView                    `json:"latestTaskView,omitempty"`
	TaskOptions           *TaskOptions                 `json:"taskOptions,omitempty"`
	TaskView              *TaskView                    `json:"taskView,omitempty"`
	Product               FeatureAreaProductProjection `json:"product"`
	CurrentTask           *wp.TaskFact                 `json:"currentTask,omitempty"`
	LatestTask            *wp.TaskFact                 `json:"latestTask,omitempty"`
	CurrentWorkspaceID    string                       `json:"currentWorkspaceId,omitempty"`
	AgentExecution        *agentexecution.Projection   `json:"agentExecution,omitempty"`
	ProductProgress       []PhaseProgress              `json:"productProgress,omitempty"`
	TaskStages            *TaskStages                  `json:"taskStages,omitempty"`
	AgentOperation        AgentOperation               `json:"agentOperation"`
	SourceGeneration      string                       `json:"-"`
	SourceSequence        int64                        `json:"sourceSequence"`
	Generation            string                       `json:"generation"`
	WorkflowMode          string                       `json:"workflowMode"`
	LatestReviewStatus    string                       `json:"latestReviewStatus,omitempty"`
	CurrentWorkflowReview *wp.ReviewFact               `json:"currentWorkflowReview,omitempty"`
	AgentRuntime          *agentruntime.Summary        `json:"agentRuntime,omitempty"`
	Guidance              *wp.Guidance                 `json:"workflowGuidance,omitempty"`
	CurrentState          string                       `json:"currentState,omitempty"`
	NextAction            wp.Action                    `json:"nextAction"`
	Retirement            *featurearea.Retirement      `json:"retirement,omitempty"`
	SchemaVersion         string                       `json:"schemaVersion"`
	FeatureArea           Identity                     `json:"featureArea"`
	DevelopmentContext    DevelopmentContext           `json:"developmentContext"`
	LifecycleStatus       string                       `json:"lifecycleStatus"`
	WorkflowStatus        string                       `json:"workflowStatus"`
	CurrentPhase          string                       `json:"currentPhase"`
	Progress              []PhaseProgress              `json:"progress"`
	CurrentOperation      *Operation                   `json:"currentOperation,omitempty"`
	PendingDecision       *Decision                    `json:"pendingDecision,omitempty"`
	Recovery              *Recovery                    `json:"recovery,omitempty"`
	Cancellation          *Cancellation                `json:"cancellation,omitempty"`
	Blockers              []string                     `json:"blockers"`
	Capabilities          []Capability                 `json:"capabilities"`
	Details               Details                      `json:"details"`
	UpdatedAt             time.Time                    `json:"updatedAt"`
}
