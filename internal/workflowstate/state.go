// Package workflowstate derives the bounded product workflow projection. The
// phase is intentionally never persisted; durable domain state remains the
// only source of truth.
package workflowstate

import "github.com/lazyun/devbridge/internal/projectcontext"

type Phase string

const (
	PhaseProjectSelectionRequired       Phase = "PROJECT_SELECTION_REQUIRED"
	PhaseProjectContextRequired         Phase = "PROJECT_CONTEXT_REQUIRED"
	PhaseProjectContextRunning          Phase = "PROJECT_CONTEXT_RUNNING"
	PhaseReadyForFeatureArea            Phase = "READY_FOR_FEATURE_AREA"
	PhaseFeatureAreaAnalysis            Phase = "FEATURE_AREA_ANALYSIS"
	PhaseFeatureAreaReviewRunning       Phase = "FEATURE_AREA_REVIEW_RUNNING"
	PhaseTaskRequired                   Phase = "TASK_REQUIRED"
	PhaseTaskAwaitingConfirmation       Phase = "TASK_AWAITING_CONFIRMATION"
	PhaseExecutionAwaitingAuthorization Phase = "EXECUTION_AWAITING_AUTHORIZATION"
	PhaseExecutionRunning               Phase = "EXECUTION_RUNNING"
	PhaseWaitingApproval                Phase = "WAITING_APPROVAL"
	PhaseResultAvailable                Phase = "RESULT_AVAILABLE"
	PhaseBlocked                        Phase = "BLOCKED"
)

type Objective string

const (
	ObjectiveSelectProject            Objective = "SELECT_PROJECT"
	ObjectiveAcquireProjectContext    Objective = "ACQUIRE_PROJECT_CONTEXT"
	ObjectiveWaitForEngineeringGoal   Objective = "WAIT_FOR_ENGINEERING_GOAL"
	ObjectiveUnderstandFeatureArea    Objective = "UNDERSTAND_FEATURE_AREA"
	ObjectiveFormExecutableTask       Objective = "FORM_EXECUTABLE_TASK"
	ObjectiveWaitForTaskDecision      Objective = "WAIT_FOR_TASK_DECISION"
	ObjectiveWaitForExecutionDecision Objective = "WAIT_FOR_EXECUTION_DECISION"
	ObjectiveObserveExecution         Objective = "OBSERVE_EXECUTION"
	ObjectiveResolveUserDecision      Objective = "RESOLVE_USER_DECISION"
	ObjectivePresentResult            Objective = "PRESENT_RESULT"
	ObjectiveResolveBlocker           Objective = "RESOLVE_BLOCKER"
)

type Workflow struct {
	Phase     Phase     `json:"phase"`
	Objective Objective `json:"objective"`
}

type ReadinessState string

const (
	ReadinessReady                  ReadinessState = "READY"
	ReadinessReadyNoProjectGuidance ReadinessState = "READY_NO_PROJECT_GUIDANCE"
	ReadinessContextRequired        ReadinessState = "CONTEXT_REQUIRED"
	ReadinessContextRunning         ReadinessState = "CONTEXT_RUNNING"
	ReadinessContextFailed          ReadinessState = "CONTEXT_FAILED"
)

type Readiness struct {
	State                ReadinessState `json:"state"`
	Usable               bool           `json:"usable"`
	CanCreateFeatureArea bool           `json:"canCreateFeatureArea"`
	CanStartReview       bool           `json:"canStartReview"`
	CanCreateTask        bool           `json:"canCreateTask"`
	CanExecute           bool           `json:"canExecute"`
	Summary              string         `json:"summary"`
	Blockers             []string       `json:"blockers"`
	ContextState         string         `json:"contextState"`
	UserMeaning          string         `json:"userMeaning"`
}

func ProjectReadiness(status projectcontext.Status) Readiness {
	value := Readiness{CanCreateFeatureArea: true, ContextState: string(status), Blockers: []string{}}
	switch status {
	case projectcontext.StatusReady:
		value.State, value.Usable, value.Summary = ReadinessReady, true, "Project is ready for engineering work."
	case projectcontext.StatusNoGuidance:
		value.State, value.Usable, value.Summary = ReadinessReadyNoProjectGuidance, true, "Project is usable; no project-level guidance was found."
	case projectcontext.StatusRunning:
		value.State, value.Summary = ReadinessContextRunning, "The engineering goal can be recorded while Project context acquisition is in progress."
		value.Blockers = []string{"Feature Area Review waits for Project Context acquisition to finish."}
	case projectcontext.StatusFailed:
		value.State, value.Summary = ReadinessContextFailed, "The engineering goal can be recorded, but Project context acquisition failed."
		value.Blockers = []string{"Feature Area Review is blocked until the Project Context failure is resolved."}
	default:
		value.State, value.Summary = ReadinessContextRequired, "The engineering goal can be recorded before Project context is acquired."
		value.Blockers = []string{"Feature Area Review requires Project Context first."}
	}
	value.UserMeaning = value.Summary
	return value
}

// WorkflowReadiness adds product capabilities to Project context readiness.
// It describes outcomes a user can reach, never a next-tool instruction.
func WorkflowReadiness(input Input, workflow Workflow) Readiness {
	value := ProjectReadiness(input.ContextStatus)
	contextUsable := contextUsable(input)
	if contextUsable && input.ContextStatus == projectcontext.StatusRunning {
		value.State, value.Usable = ReadinessReady, true
		value.Summary = "Project is usable while its context refresh runs in the background."
		value.Blockers = []string{}
	}
	if contextUsable && input.ContextStatus == projectcontext.StatusFailed {
		value.State, value.Usable = ReadinessReady, true
		value.Summary = "The previous Project Context snapshot remains usable after a refresh failure."
		value.Blockers = []string{}
	}
	value.CanStartReview = contextUsable && input.FeatureAreaExists && !input.FeatureAreaRetired && (input.Review == ReviewNone || input.Review == ReviewFailed || input.Review == "") && (input.Task == TaskNone || input.Task == "")
	value.CanCreateTask = contextUsable && input.Review == ReviewSucceeded && (input.Task == TaskNone || input.Task == "")
	value.CanExecute = input.Execution == ExecutionPendingAuthorization
	switch workflow.Phase {
	case PhaseReadyForFeatureArea:
		value.Summary = "Project is ready for a Feature Area or engineering goal."
	case PhaseFeatureAreaAnalysis:
		if input.Review == ReviewFailed {
			value.Summary = "The previous Feature Area Review failed; the same Feature Area remains available for retry."
		} else {
			value.Summary = "The Feature Area is recorded and ready for Feature Area Review."
		}
	case PhaseFeatureAreaReviewRunning:
		value.Summary = "Feature Area Review is in progress."
	case PhaseTaskRequired:
		value.Summary = "Feature Area Review is complete and a concrete Task can be created."
	case PhaseTaskAwaitingConfirmation:
		value.Summary = "The Task is ready for the user's confirmation."
		value.Blockers = append(value.Blockers, "Task confirmation is required before execution.")
	case PhaseExecutionAwaitingAuthorization:
		value.Summary = "The confirmed Task is ready to execute."
	case PhaseExecutionRunning:
		value.Summary = "Execution is in progress."
	case PhaseWaitingApproval:
		value.Summary = "Execution is waiting for a user decision."
		value.Blockers = append(value.Blockers, "A runtime approval decision is required.")
	case PhaseResultAvailable:
		value.Summary = "The current Execution result is available."
	case PhaseBlocked:
		if input.Review == ReviewFailed {
			value.Blockers = append(value.Blockers, "Feature Area Review failed and must be resolved.")
		}
	}
	value.UserMeaning = value.Summary
	return value
}

type ReviewState string

const (
	ReviewNone      ReviewState = "NONE"
	ReviewRunning   ReviewState = "RUNNING"
	ReviewSucceeded ReviewState = "SUCCEEDED"
	ReviewFailed    ReviewState = "FAILED"
)

type TaskState string

const (
	TaskNone      TaskState = "NONE"
	TaskPending   TaskState = "PENDING_CONFIRMATION"
	TaskConfirmed TaskState = "CONFIRMED"
	TaskRejected  TaskState = "REJECTED"
)

type ExecutionState string

const (
	ExecutionNone                 ExecutionState = "NONE"
	ExecutionPendingAuthorization ExecutionState = "PENDING_AUTHORIZATION"
	ExecutionRunning              ExecutionState = "RUNNING"
	ExecutionSucceeded            ExecutionState = "SUCCEEDED"
	ExecutionFailed               ExecutionState = "FAILED"
	ExecutionCanceled             ExecutionState = "CANCELED"
	ExecutionRejected             ExecutionState = "REJECTED"
)

type Input struct {
	SessionBound  bool
	ContextStatus projectcontext.Status
	// ContextUsable is true when a published snapshot remains authoritative
	// while a refresh is running or has failed.
	ContextUsable      bool
	FeatureAreaExists  bool
	FeatureAreaRetired bool
	Review             ReviewState
	Task               TaskState
	Execution          ExecutionState
	ApprovalPending    bool
	HardBlocker        bool
}

func contextUsable(input Input) bool {
	return input.ContextUsable || input.ContextStatus == projectcontext.StatusReady || input.ContextStatus == projectcontext.StatusNoGuidance
}

func Derive(input Input) Workflow {
	if !input.SessionBound {
		return Workflow{Phase: PhaseProjectSelectionRequired, Objective: ObjectiveSelectProject}
	}
	if input.HardBlocker || (input.ContextStatus == projectcontext.StatusFailed && !contextUsable(input)) || input.FeatureAreaRetired {
		return Workflow{Phase: PhaseBlocked, Objective: ObjectiveResolveBlocker}
	}
	switch input.ContextStatus {
	case projectcontext.StatusRequired, "":
		return Workflow{Phase: PhaseProjectContextRequired, Objective: ObjectiveAcquireProjectContext}
	case projectcontext.StatusRunning:
		if !contextUsable(input) {
			return Workflow{Phase: PhaseProjectContextRunning, Objective: ObjectiveAcquireProjectContext}
		}
	}
	if !input.FeatureAreaExists {
		return Workflow{Phase: PhaseReadyForFeatureArea, Objective: ObjectiveWaitForEngineeringGoal}
	}
	if input.ApprovalPending {
		return Workflow{Phase: PhaseWaitingApproval, Objective: ObjectiveResolveUserDecision}
	}
	switch input.Execution {
	case ExecutionSucceeded, ExecutionFailed, ExecutionCanceled, ExecutionRejected:
		return Workflow{Phase: PhaseResultAvailable, Objective: ObjectivePresentResult}
	case ExecutionRunning:
		return Workflow{Phase: PhaseExecutionRunning, Objective: ObjectiveObserveExecution}
	case ExecutionPendingAuthorization:
		return Workflow{Phase: PhaseExecutionAwaitingAuthorization, Objective: ObjectiveWaitForExecutionDecision}
	}
	if input.Task == TaskPending {
		return Workflow{Phase: PhaseTaskAwaitingConfirmation, Objective: ObjectiveWaitForTaskDecision}
	}

	if input.Review == ReviewRunning {
		return Workflow{Phase: PhaseFeatureAreaReviewRunning, Objective: ObjectiveUnderstandFeatureArea}
	}
	if input.Review == ReviewSucceeded {
		return Workflow{Phase: PhaseTaskRequired, Objective: ObjectiveFormExecutableTask}
	}
	return Workflow{Phase: PhaseFeatureAreaAnalysis, Objective: ObjectiveUnderstandFeatureArea}
}
