package event

import (
	"context"
	"encoding/json"
	"time"
)

const PayloadVersion = 1

const (
	ProjectCreated                   = "project.created"
	FeatureAreaCreated               = "feature_area.created"
	FeatureAreaStatusChanged         = "feature_area.status_changed"
	DocumentCreated                  = "document.created"
	RepositoryRegistered             = "repository.registered"
	ProjectRepositoryUnlinked        = "project.repository_unlinked"
	ModuleCreated                    = "module.created"
	TaskClassified                   = "task.classified"
	ProjectContextChanged            = "project_context.changed"
	ContextProbeCreated              = "context_probe.created"
	ContextProbeStatusChanged        = "context_probe.status_changed"
	CodexConfigCreated               = "codex_config.created"
	TaskCreated                      = "task.created"
	TaskStatusChanged                = "task.status_changed"
	TaskPlanCreated                  = "task_plan.created"
	TaskPlanConfirmed                = "task_plan.confirmed"
	TaskPlanRejected                 = "task_plan.rejected"
	TaskPlanApplied                  = "task_plan.applied"
	TaskProposalCreated              = "task_proposal.created"
	TaskProposalConfirmed            = "task_proposal.confirmed"
	TaskProposalRejected             = "task_proposal.rejected"
	TaskProposalConsumed             = "task_proposal.consumed"
	ExecutionProposalCreated         = "execution_proposal.created"
	ExecutionProposalConfirmed       = "execution_proposal.confirmed"
	ExecutionProposalRejected        = "execution_proposal.rejected"
	ExecutionProposalConsumed        = "execution_proposal.consumed"
	ScopeExpansionCreated            = "scope_expansion.created"
	ScopeExpansionAllowed            = "scope_expansion.allowed"
	ScopeExpansionRejected           = "scope_expansion.rejected"
	ExecutionImplementationStarted   = "execution.implementation_started"
	ExecutionImplementationCompleted = "execution.implementation_completed"
	ExecutionVerificationStarted     = "execution.verification_started"
	ExecutionVerificationCompleted   = "execution.verification_completed"
	ExecutionCompleted               = "execution.completed"
	VerificationWaived               = "verification.waived"
	TaskAcceptanceResolved           = "task.acceptance_resolved"
	CodexThreadNameFailed            = "codex.thread_name_failed"
	RunAnalysisRecorded              = "run.analysis_recorded"
	RunCreated                       = "run.created"
	RunStatusChanged                 = "run.status_changed"
	ApprovalRequested                = "approval.requested"
	ApprovalDecided                  = "approval.decided"
	ApprovalDelivered                = "approval.delivered"
	ApprovalCanceled                 = "approval.canceled"
	ApprovalExpired                  = "approval.expired"
	WorktreeCreated                  = "worktree.created"
	WorktreeStatusChanged            = "worktree.status_changed"
	WorkspaceCreated                 = "workspace.created"
	WorkspaceUpdated                 = "workspace.updated"
	ArtifactRecorded                 = "artifact.recorded"
	EvidenceRecorded                 = "evidence.recorded"
	RecoveryRunInterrupted           = "recovery.run_interrupted"
	RecoveryWorktreeMissing          = "recovery.worktree_missing"
)

type Event struct {
	ID         string
	Sequence   int64
	Type       string
	ProjectID  string
	TaskID     string
	RunID      string
	EntityType string
	EntityID   string
	Payload    json.RawMessage
	CreatedAt  time.Time
}

type Filter struct {
	ProjectID string
	TaskID    string
	RunID     string
}

type Store interface {
	List(context.Context, Filter) ([]Event, error)
}

func Payload(fields map[string]any) (json.RawMessage, error) {
	copy := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		copy[key] = value
	}
	copy["version"] = PayloadVersion
	return json.Marshal(copy)
}
