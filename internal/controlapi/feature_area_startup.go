package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/run"
)

const CreateOnly = "CREATE_ONLY"
const CreateAndReview = "CREATE_AND_REVIEW"

type InitialTaskInput struct {
	Title            string   `json:"title"`
	ProblemStatement string   `json:"problemStatement"`
	Constraints      []string `json:"constraints,omitempty"`
	RepositoryID     string   `json:"repositoryId"`
}
type InitialReviewPlan struct {
	Mode            string                      `json:"mode"`
	SelectedModules []run.ReviewModuleSelection `json:"selectedModules,omitempty"`
}
type InitialReviewInput struct {
	ReviewDepth       string            `json:"reviewDepth"`
	ReviewPlan        InitialReviewPlan `json:"reviewPlan"`
	ReviewInstruction string            `json:"reviewInstruction"`
	Model             string            `json:"model"`
	ReasoningEffort   string            `json:"reasoningEffort,omitempty"`
}
type FeatureAreaCreateInput struct {
	AISessionID      string              `json:"aiSessionId"`
	Title            string              `json:"title"`
	ProblemStatement string              `json:"problemStatement"`
	Mode             string              `json:"mode,omitempty"`
	RequestID        string              `json:"requestId,omitempty"`
	InitialTask      *InitialTaskInput   `json:"initialTask,omitempty"`
	InitialReview    *InitialReviewInput `json:"initialReview,omitempty"`
}
type StartupError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type StartupRetry struct {
	Action    string `json:"action"`
	RequestID string `json:"requestId"`
}
type FeatureAreaStartup struct {
	Mode            string        `json:"mode"`
	RequestID       string        `json:"requestId"`
	CompletedSteps  []string      `json:"completedSteps"`
	FeatureAreaID   string        `json:"featureAreaId"`
	TaskID          string        `json:"taskId,omitempty"`
	ReviewID        string        `json:"reviewId,omitempty"`
	RunID           string        `json:"runId,omitempty"`
	ReviewAdmitted  bool          `json:"reviewAdmitted"`
	RunStatus       string        `json:"runStatus,omitempty"`
	FailedStep      string        `json:"failedStep,omitempty"`
	Error           *StartupError `json:"error,omitempty"`
	Retryable       bool          `json:"retryable"`
	Retry           *StartupRetry `json:"retry,omitempty"`
	RefreshRequired bool          `json:"refreshRequired"`
}
type FeatureAreaCreateResult struct {
	FeatureArea featurearea.FeatureArea
	Startup     *FeatureAreaStartup
}

// StartupContext is the same scoped inventory used by the standalone tools.
type StartupContext interface {
	GetSession(context.Context, string) (aicontext.SessionContext, error)
	ListRepositories(context.Context, string, aicontext.ListOptions) ([]aicontext.RepositoryContext, error)
	ListModules(context.Context, string, aicontext.ListOptions) ([]module.Module, error)
	ListProjectModuleMap(context.Context, string) ([]module.Module, error)
	ListCodexConfigs(context.Context, string, aicontext.ListOptions) ([]codexconfig.Config, error)
}
type creationReceipts interface {
	ReplayCreation(context.Context, string, string, string, string) (featurearea.FeatureArea, featurearea.CreationReceipt, error)
	CreateReceipted(context.Context, featurearea.CreateInput, featurearea.CreationReceipt) (featurearea.FeatureArea, featurearea.CreationReceipt, error)
	UpdateCreationReceipt(context.Context, featurearea.CreationReceipt) error
}
