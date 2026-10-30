package execution

import (
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/agent"
)

const BridgeShutdownInterruptedCode = "bridge_shutdown_interrupted"

var (
	ErrBridgeShutdownInterrupted = errors.New("Bridge shutdown interrupted owned execution")
	ErrRunCanceledByUser         = errors.New("run canceled by user")
	ErrRunSessionNotLive         = errors.New("run has no live supervisor session")
	ErrSupervisorShuttingDown    = errors.New("runtime supervisor is shutting down")
	ErrAgentExecutionUnproven    = errors.New("durable AgentExecution ownership is unproven")
)

type WorkspaceContinuityUnavailableError struct {
	TaskID      string
	ReviewID    string
	WorkspaceID string
	Reason      string
}

func (e *WorkspaceContinuityUnavailableError) Error() string {
	if e.ReviewID != "" {
		return fmt.Sprintf("workspace continuity is unavailable for Review %s (workspace %s): %s; start a new Review", e.ReviewID, e.WorkspaceID, e.Reason)
	}
	return fmt.Sprintf("workspace continuity is unavailable for task %s (workspace %s): %s; create a new Task", e.TaskID, e.WorkspaceID, e.Reason)
}

type TaskAlreadyHasActiveRunError struct {
	TaskID string
	RunID  string
}

func (e *TaskAlreadyHasActiveRunError) Error() string {
	if e.RunID == "" {
		return fmt.Sprintf("task %s already has an active run", e.TaskID)
	}
	return fmt.Sprintf("task %s already has active run %s", e.TaskID, e.RunID)
}

func (e *TaskAlreadyHasActiveRunError) ExistingRunID() string { return e.RunID }

type TaskNotExecutableError struct {
	TaskID string
	Status string
}

func (e *TaskNotExecutableError) Error() string {
	return fmt.Sprintf("task %s is not executable from status %s", e.TaskID, e.Status)
}

type InfrastructureError struct {
	Stage string
	Err   error
}

func (e *InfrastructureError) Error() string {
	return fmt.Sprintf("execution infrastructure failed during %s: %v", e.Stage, e.Err)
}
func (e *InfrastructureError) Unwrap() error { return e.Err }

// TaskExecutionError marks a terminal executor outcome that is not an
// infrastructure failure.
type TaskExecutionError = agent.ExecutionError

type ApprovalPersistenceError struct{ Err error }

func (e *ApprovalPersistenceError) Error() string {
	return "persist incoming approval: " + e.Err.Error()
}
func (e *ApprovalPersistenceError) Unwrap() error { return e.Err }
