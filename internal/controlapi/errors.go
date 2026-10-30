package controlapi

import (
	"context"
	"errors"
	"fmt"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"os"
	"strings"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/classification"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"github.com/lazyun/devbridge/internal/projectselection"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/repositoryunlink"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/taskplan"
)

type ErrorCode string

const (
	CodeFeatureAreaRetiring      ErrorCode = "FEATURE_AREA_RETIRING"
	CodeInvalidArgument          ErrorCode = "INVALID_ARGUMENT"
	CodeNotFound                 ErrorCode = "NOT_FOUND"
	CodeConflict                 ErrorCode = "CONFLICT"
	CodeInvalidState             ErrorCode = "INVALID_STATE"
	CodeTaskAlreadyHasActiveRun  ErrorCode = "TASK_ALREADY_HAS_ACTIVE_RUN"
	CodeFeatureAreaBusy          ErrorCode = "FEATURE_AREA_BUSY"
	CodeInfrastructure           ErrorCode = "INFRASTRUCTURE_ERROR"
	CodeCanceled                 ErrorCode = "CANCELED"
	CodePermissionDenied         ErrorCode = "PERMISSION_DENIED"
	CodeReviewScopeForbidden     ErrorCode = "REVIEW_SCOPE_FORBIDDEN"
	CodeInternal                 ErrorCode = "INTERNAL_ERROR"
	CodeInvalidPath              ErrorCode = "INVALID_PATH"
	CodeNotGitRepository         ErrorCode = "NOT_GIT_REPOSITORY"
	CodeProjectAlreadyRegistered ErrorCode = "PROJECT_ALREADY_REGISTERED"
)

type Error struct {
	Code    ErrorCode
	Message string
	cause   error
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func invalid(message string) error { return &Error{Code: CodeInvalidArgument, Message: message} }

func MapError(err error) *Error {
	var actionFailure *confirmation.Failure
	if errors.As(err, &actionFailure) && actionFailure != nil {
		switch actionFailure.Code {
		case "TASK_ACTION_PENDING", "TASK_ACTION_STALE", "TASK_ACTION_CONFLICT":
			return &Error{Code: ErrorCode(actionFailure.Code), Message: actionFailure.Message, cause: err}
		}
	}
	if errors.Is(err, task.ErrUnobservableRequiredCheck) {
		return &Error{Code: ErrorCode("UNOBSERVABLE_REQUIRED_CHECK"), Message: err.Error(), cause: err}
	}
	if code := runtimeAuthorityError(err); code != "" {
		return &Error{Code: ErrorCode(code), Message: err.Error(), cause: err}
	}
	var fileErr *directfile.Error
	if errors.As(err, &fileErr) {
		return &Error{Code: ErrorCode(fileErr.Code), Message: fileErr.Message, cause: err}
	}
	var depthErr *review.DepthTooShallowError
	if errors.As(err, &depthErr) {
		return &Error{Code: ErrorCode("REVIEW_DEPTH_TOO_SHALLOW"), Message: depthErr.Error(), cause: err}
	}
	if errors.Is(err, task.ErrInstructionRequired) {
		return &Error{Code: CodeInvalidArgument, Message: err.Error(), cause: err}
	}
	if errors.Is(err, executionproposal.ErrNoStartRecorded) {
		return &Error{Code: ErrorCode("EXECUTION_NO_START_RECORDED"), Message: executionproposal.ErrNoStartRecorded.Error(), cause: err}
	}
	if errors.Is(err, executionproposal.ErrStartAuthorizationUnavailable) {
		return &Error{Code: ErrorCode("EXECUTION_START_AUTHORIZATION_UNAVAILABLE"), Message: executionproposal.ErrStartAuthorizationUnavailable.Error(), cause: err}
	}
	if errors.Is(err, executionproposal.ErrStartEffectConflict) {
		return &Error{Code: ErrorCode("EXECUTION_START_EFFECT_CONFLICT"), Message: executionproposal.ErrStartEffectConflict.Error(), cause: err}
	}
	var progressionErr *wp.Error
	if errors.As(err, &progressionErr) && progressionErr != nil {
		return &Error{Code: ErrorCode(progressionErr.Code), Message: progressionErr.Message, cause: err}
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, aisession.ErrProjectUnbound) {
		return &Error{Code: ErrorCode("AI_SESSION_PROJECT_UNBOUND"), Message: "select and bind a Project to this Session before Project operations", cause: err}
	}
	if errors.Is(err, localoperator.ErrAccessDenied) {
		return &Error{Code: CodePermissionDenied, Message: "Trusted local operator access is required for this Project", cause: err}
	}
	if errors.Is(err, aisession.ErrScopeMismatch) {
		return &Error{Code: CodePermissionDenied, Message: "Session is outside the caller's access scope", cause: err}
	}
	if errors.Is(err, aisession.ErrInitializationRequired) {
		return &Error{Code: ErrorCode("SESSION_INITIALIZATION_REQUIRED"), Message: "initialize a Session before binding a Project", cause: err}
	}
	if errors.Is(err, aisession.ErrInvalidInitialization) {
		return &Error{Code: CodeInvalidArgument, Message: "invalid Session initialization or binding input", cause: err}
	}
	if errors.Is(err, agentexecution.ErrNotFound) {
		return &Error{Code: CodeNotFound, Message: "Agent Execution not found", cause: err}
	}
	if errors.Is(err, agentexecution.ErrInvalidAssociation) {
		return &Error{Code: CodeConflict, Message: "Agent Execution ownership could not be proven", cause: err}
	}
	if errors.Is(err, recoverystatus.ErrNotFound) {
		return &Error{Code: CodeNotFound, Message: err.Error(), cause: err}
	}
	var purposeErr *recoverystatus.PurposeError
	if errors.As(err, &purposeErr) {
		return &Error{Code: ErrorCode(purposeErr.ReasonCode), Message: purposeErr.Error(), cause: err}
	}
	if errors.Is(err, recoverystatus.ErrConflict) || errors.Is(err, recoverystatus.ErrStale) || errors.Is(err, recoverystatus.ErrIneligible) {
		return &Error{Code: CodeConflict, Message: err.Error(), cause: err}
	}
	if errors.Is(err, agentruntime.ErrRequestConflict) {
		return &Error{Code: ErrorCode("REQUEST_CONFLICT"), Message: err.Error(), cause: err}
	}
	if errors.Is(err, agentruntime.ErrResponseDispositionUnknown) {
		return &Error{Code: ErrorCode("RESPONSE_DISPOSITION_UNKNOWN"), Message: "你的回答已经记录。Codex 可能已经收到该回答，但当前协议无法确认。Dev Bridge 不会自动重发。", cause: err}
	}
	if errors.Is(err, agentruntime.ErrStale) {
		return &Error{Code: CodeConflict, Message: err.Error(), cause: err}
	}
	if errors.Is(err, agentruntime.ErrInvalid) || errors.Is(err, agentruntime.ErrUnsupported) {
		return &Error{Code: CodeInvalidArgument, Message: err.Error(), cause: err}
	}
	if errors.Is(err, featurearea.ErrRetiring) || strings.Contains(err.Error(), "FEATURE_AREA_RETIRING") {
		return &Error{Code: CodeFeatureAreaRetiring, Message: "FeatureArea is closing; engineering advancement is unavailable", cause: err}
	}
	if errors.Is(err, bridgegit.ErrBranchConflict) {
		return &Error{Code: CodeConflict, Message: bridgegit.ErrBranchConflict.Error(), cause: err}
	}
	if errors.Is(err, bridgegit.ErrInvalidBranchArgument) {
		return &Error{Code: CodeInvalidArgument, Message: err.Error(), cause: err}
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	for _, domainErr := range []error{delivery.ErrRejected, delivery.ErrRequestConflict, delivery.ErrRetryConflict, delivery.ErrAbandoned, delivery.ErrPlanReleased, featurearea.ErrCommitScopeConflict, bridgegit.ErrSnapshotDrift, bridgegit.ErrCommitIncomplete, bridgegit.ErrRecoveryRequired, bridgegit.ErrMergeConflict, executioncancel.ErrInvalidArgument, executioncancel.ErrExecutionNotRunning, executioncancel.ErrRunNotActive, executioncancel.ErrRunExecutionMismatch, executioncancel.ErrClaimMismatch, executioncancel.ErrRecoveryRequired, executioncancel.ErrRequestConflict, executioncancel.ErrProjectMismatch, executioncancel.ErrOutcomeUnknown, executioncancel.ErrLifecycleProjectionPending, developmentcycle.ErrFinalizationPending, developmentcycle.ErrNotSelected, developmentcycle.ErrBranchNotFound, developmentcycle.ErrRefMissing, developmentcycle.ErrRepositoryMismatch, developmentcycle.ErrRequestConflict} {
		if errors.Is(err, domainErr) {
			return &Error{Code: ErrorCode(domainErr.Error()), Message: domainErr.Error(), cause: err}
		}
	}
	if errors.Is(err, developmentcycle.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: err.Error(), cause: err}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeCanceled, Message: "request canceled", cause: err}
	}
	if errors.Is(err, os.ErrPermission) {
		return &Error{Code: CodePermissionDenied, Message: "Repository path permission denied.", cause: err}
	}
	if errors.Is(err, repository.ErrHEADUnavailable) {
		return &Error{Code: ErrorCode("HEAD_UNAVAILABLE"), Message: "Repository HEAD has no available commit.", cause: err}
	}
	if errors.Is(err, repository.ErrRepositoryUnlinked) {
		return &Error{Code: ErrorCode("REPOSITORY_UNLINKED"), Message: "Repository is retained as history and is unavailable for new work.", cause: err}
	}
	if errors.Is(err, repository.ErrUnlinkedHistoryExists) {
		return &Error{Code: ErrorCode("REPOSITORY_UNLINKED_HISTORY_EXISTS"), Message: "This Repository path is retained as unlinked history; relink and transfer are not supported.", cause: err}
	}
	if errors.Is(err, repository.ErrProjectMismatch) {
		return &Error{Code: ErrorCode("REPOSITORY_PROJECT_MISMATCH"), Message: "Repository does not belong to the requested Project.", cause: err}
	}
	if errors.Is(err, projectregistration.ErrRequestConflict) {
		return &Error{Code: CodeConflict, Message: err.Error(), cause: err}
	}
	if errors.Is(err, projectregistration.ErrAtomicStoreUnavailable) {
		return &Error{Code: ErrorCode("UNSUPPORTED_OPERATION"), Message: err.Error(), cause: err}
	}
	for _, domainErr := range []error{repositoryunlink.ErrBlocked, repositoryunlink.ErrStale, repositoryunlink.ErrRequestConflict, repositoryunlink.ErrAlreadyUnlinked, repositoryunlink.ErrRepositoryMismatch} {
		if errors.Is(err, domainErr) {
			return &Error{Code: ErrorCode(domainErr.Error()), Message: domainErr.Error(), cause: err}
		}
	}
	if errors.Is(err, repository.ErrInvalidPath) {
		return &Error{Code: CodeInvalidPath, Message: "The selected directory cannot be accessed.", cause: err}
	}
	if errors.Is(err, repository.ErrNotGit) {
		return &Error{Code: CodeNotGitRepository, Message: "Selected folder is not a usable Git repository.", cause: err}
	}
	if errors.Is(err, projectregistration.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: err.Error(), cause: err}
	}
	if errors.Is(err, projectregistration.ErrAlreadyRegistered) {
		return &Error{Code: CodeProjectAlreadyRegistered, Message: "Repository is already registered.", cause: err}
	}
	if errors.Is(err, reviewaccess.ErrScopeForbidden) {
		message := "Feature Area Review access is outside the authorized Repository boundary"
		var violation *reviewaccess.Violation
		if errors.As(err, &violation) && len(violation.Paths) != 0 {
			message += ": " + strings.Join(violation.Paths, ", ")
		}
		return &Error{Code: CodeReviewScopeForbidden, Message: message, cause: err}
	}
	if errors.Is(err, executionproposal.ErrActiveExecution) {
		return &Error{Code: CodeTaskAlreadyHasActiveRun, Message: "the task already has an active run", cause: err}
	}
	if errors.Is(err, projectselection.ErrNotSelected) {
		return &Error{Code: CodeInvalidState, Message: "no project is selected", cause: err}
	}
	for _, e := range []error{review.ErrExecutorMismatch, review.ErrReportInvalid, review.ErrEvidenceMismatch, review.ErrTargetStale, review.ErrResultFinalized, review.ErrFinalizing, review.ErrPreparationFailed} {
		if errors.Is(err, e) {
			return &Error{Code: ErrorCode(e.Error()), Message: err.Error(), cause: err}
		}
	}
	if errors.Is(err, review.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: "invalid Review request", cause: err}
	}
	if errors.Is(err, taskplan.ErrInvalidArgument) || errors.Is(err, scopereview.ErrInvalidArgument) || errors.Is(err, reviewgrant.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: "invalid Task planning request", cause: err}
	}

	if errors.Is(err, task.ErrGoalsAlreadyDefined) {
		return &Error{Code: CodeConflict, Message: "task goals are already defined", cause: err}
	}
	if errors.Is(err, agentconversation.ErrBusy) {
		message := "the Feature Area has an active agent operation; wait for that operation to finish"
		var busy *review.AdmissionBusyError
		if errors.As(err, &busy) {
			message = fmt.Sprintf("the Feature Area is owned by active %s operation %s (Run %s); wait for it to finish", busy.Owner.Kind, busy.Owner.OperationID, busy.Owner.RunID)
		}
		return &Error{Code: CodeFeatureAreaBusy, Message: message, cause: err}
	}
	if errors.Is(err, review.ErrRequestConflict) {
		return &Error{Code: CodeConflict, Message: "Review requestId was already used with a different payload", cause: err}
	}
	var goalTransition *taskgoal.ErrInvalidTransition
	if errors.As(err, &goalTransition) {
		return &Error{Code: CodeInvalidState, Message: "the goal progress status transition is not permitted", cause: err}
	}
	if errors.Is(err, featurearea.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: "invalid Feature Area request", cause: err}
	}
	if errors.Is(err, document.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: "invalid Document request", cause: err}
	}
	if errors.Is(err, featurearea.ErrUnavailable) {
		return &Error{Code: CodeInvalidState, Message: "Feature Area is not available for continuation in this Project", cause: err}
	}
	if errors.Is(err, scopeexpansion.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: "invalid scope expansion", cause: err}
	}
	if errors.Is(err, executionproposal.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: "invalid execution proposal", cause: err}
	}
	if errors.Is(err, contextprobe.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: "invalid context probe", cause: err}
	}
	if errors.Is(err, module.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: "invalid module", cause: err}
	}
	if errors.Is(err, classification.ErrInvalidArgument) {
		return &Error{Code: CodeInvalidArgument, Message: "invalid task classification", cause: err}
	}
	if errors.Is(err, aisession.ErrNotFound) || errors.Is(err, document.ErrNotFound) || errors.Is(err, project.ErrNotFound) || errors.Is(err, projectcontext.ErrNotFound) || errors.Is(err, repository.ErrNotFound) || errors.Is(err, review.ErrNotFound) || errors.Is(err, module.ErrNotFound) || errors.Is(err, codexconfig.ErrNotFound) || errors.Is(err, featurearea.ErrNotFound) || errors.Is(err, task.ErrNotFound) || errors.Is(err, taskplan.ErrNotFound) || errors.Is(err, scopereview.ErrNotFound) || errors.Is(err, reviewgrant.ErrNotFound) || errors.Is(err, scopeexpansion.ErrNotFound) || errors.Is(err, executionproposal.ErrNotFound) || errors.Is(err, contextprobe.ErrNotFound) || errors.Is(err, classification.ErrNotFound) || errors.Is(err, run.ErrNotFound) || errors.Is(err, approval.ErrNotFound) || errors.Is(err, taskgoal.ErrNotFound) || errors.Is(err, sqlite.ErrNotFound) {
		return &Error{Code: CodeNotFound, Message: "requested control-plane entity was not found", cause: err}
	}
	if errors.Is(err, aisession.ErrConflict) || errors.Is(err, document.ErrConflict) || errors.Is(err, project.ErrConflict) || errors.Is(err, projectcontext.ErrConflict) || errors.Is(err, review.ErrConflict) || errors.Is(err, module.ErrConflict) || errors.Is(err, codexconfig.ErrConflict) || errors.Is(err, repository.ErrPathConflict) || errors.Is(err, repository.ErrIDExists) || errors.Is(err, featurearea.ErrConflict) || errors.Is(err, agentconversation.ErrConflict) || errors.Is(err, task.ErrCodexConfigImmutable) || errors.Is(err, task.ErrBaseCommitImmutable) || errors.Is(err, task.ErrTopicImmutable) || errors.Is(err, task.ErrConflict) || errors.Is(err, task.ErrLegacyReadOnly) || errors.Is(err, taskplan.ErrConflict) || errors.Is(err, scopereview.ErrConflict) || errors.Is(err, reviewgrant.ErrConflict) || errors.Is(err, scopeexpansion.ErrConflict) || errors.Is(err, executionproposal.ErrConflict) || errors.Is(err, contextprobe.ErrConflict) || errors.Is(err, classification.ErrConflict) || errors.Is(err, sqlite.ErrConflict) {
		return &Error{Code: CodeConflict, Message: "the request conflicts with durable control-plane state", cause: err}
	}
	var taskPlanTransition *taskplan.TransitionError
	var executionProposalTransition *executionproposal.TransitionError
	var scopeExpansionTransition *scopeexpansion.TransitionError
	var runTransition *run.TransitionError
	var notExecutable *execution.TaskNotExecutableError
	var activeRun *execution.TaskAlreadyHasActiveRunError
	if errors.As(err, &activeRun) {
		return &Error{Code: CodeTaskAlreadyHasActiveRun, Message: "the task already has an active run", cause: err}
	}
	if errors.As(err, &taskPlanTransition) || errors.As(err, &executionProposalTransition) || errors.As(err, &scopeExpansionTransition) || errors.As(err, &runTransition) || errors.As(err, &notExecutable) {
		return &Error{Code: CodeInvalidState, Message: "the entity is not in a state that permits this action", cause: err}
	}
	if errors.Is(err, approval.ErrAlreadyResolved) || errors.Is(err, approval.ErrInvalidDecision) {
		return &Error{Code: CodeInvalidState, Message: "the approval is not in a state that permits this decision", cause: err}
	}
	var delivery *approval.DeliveryError
	if errors.As(err, &delivery) {
		return &Error{Code: CodeInfrastructure, Message: "the approval decision could not be delivered to Codex", cause: err}
	}
	var infrastructure *execution.InfrastructureError
	if errors.As(err, &infrastructure) || errors.Is(err, execution.ErrSupervisorShuttingDown) || errors.Is(err, execution.ErrRunSessionNotLive) {
		return &Error{Code: CodeInfrastructure, Message: "managed execution failed because of local infrastructure", cause: err}
	}
	return &Error{Code: CodeInternal, Message: "internal control-plane error", cause: err}
}
