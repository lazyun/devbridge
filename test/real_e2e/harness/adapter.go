package harness

import (
	"context"
	"time"
)

type Checkpoint string

const (
	CheckpointCodexCompleted     Checkpoint = "CODEX_COMPLETED"
	CheckpointEvidenceCollecting Checkpoint = "EVIDENCE_COLLECTING"
	CheckpointRunSucceeded       Checkpoint = "RUN_SUCCEEDED"
	CheckpointWaitingApproval    Checkpoint = "WAITING_APPROVAL"
	CheckpointApprovalDecided    Checkpoint = "APPROVAL_DECIDED"
	CheckpointApprovalDelivered  Checkpoint = "APPROVAL_DELIVERED"
	CheckpointApprovalDeclined   Checkpoint = "APPROVAL_DECLINED"
)

type GateRequest struct {
	GateID          GateID
	SourceRepo      string
	BaseCommit      string
	DataRoot        string
	WorktreeRoot    string
	ArtifactDir     string
	ExpectedPath    string
	ExpectedContent string
}

type NormalObservation struct {
	RunID          string
	ThreadID       string
	WorktreePath   string
	Timeline       []Checkpoint
	AgentClaim     string
	BaseCommit     string
	HeadCommit     string
	Dirty          bool
	ChangedFiles   []string
	Diff           string
	DiffArtifactID string
	TokenStatus    string
}

type ApprovalObservation struct {
	RunID           string
	ApprovalID      string
	CorrelatedRunID string
	Decision        string
	FinalStatus     string
	Timeline        []Checkpoint
}

type RecoveryObservation struct {
	RunID              string
	FinalStatus        string
	FailureCode        string
	DataRoot           string
	WorktreePath       string
	ThreadID           string
	FinishedAt         string
	TokenStatus        string
	LiveSession        bool
	RecoveryEventCount int
	ApprovalStatus     string
	ApprovalDecision   string
}

type ParallelObservation struct {
	RunAID                 string
	RunBID                 string
	TaskAID                string
	TaskBID                string
	WorktreeA              string
	WorktreeB              string
	ThreadA                string
	ThreadB                string
	RunAStatusWhileBActive string
	RunAFinalStatus        string
	RunBFinalStatus        string
	SameTaskRejectionCode  string
	RunATokenStatus        string
	RunBTokenStatus        string
	RunAChangedFiles       []string
	RunBChangedFiles       []string
}

type ContinuityObservation struct {
	TaskID             string
	RunOneID           string
	RunTwoID           string
	WorkspaceOne       string
	WorkspaceTwo       string
	WorktreeOne        string
	WorktreeTwo        string
	WorktreePathOne    string
	WorktreePathTwo    string
	ThreadOne          string
	ThreadTwo          string
	TurnOne            string
	TurnTwo            string
	StartSnapshotOne   string
	EndSnapshotOne     string
	StartSnapshotTwo   string
	EndSnapshotTwo     string
	RunOneStatus       string
	RunTwoStatus       string
	RunOneTokenStatus  string
	RunTwoTokenStatus  string
	RunOneTokenTotal   int64
	RunTwoTokenTotal   int64
	RunOneChangedFiles []string
	RunTwoChangedFiles []string
	Restarted          bool
	OneActiveRejection string
}

type PlanRunObservation struct {
	ID                          string
	TaskID                      string
	Purpose                     string
	ExecutionProposalID         string
	WorkspaceID                 string
	WorktreeID                  string
	WorktreePath                string
	ThreadID                    string
	TurnID                      string
	Status                      string
	StartSnapshot               string
	EndSnapshot                 string
	StartedAt                   *time.Time
	FinishedAt                  *time.Time
	TokenStatus                 string
	TokenTotal                  int64
	TokenCachedInput            int64
	ChangedFiles                []string
	DiffArtifactID              string
	FailureCode                 string
	FailureMessage              string
	VerificationStatus          string
	VerificationChecks          int
	VerificationCheckStatus     string
	VerificationObservedCommand string
	VerificationExitCode        *int
	VerificationFailureReason   string
	AgentClaim                  string
}

type TaskFirstScopeReviewObservation struct {
	TaskID                           string
	InitialTaskStatus                string
	InitialRunCount                  int
	SearchPath                       string
	SearchLine                       int
	SearchSnippet                    string
	SearchTruncated                  bool
	ReviewResult                     string
	ReviewResultAfter                string
	Review                           PlanRunObservation
	TaskPlanID                       string
	PendingTaskPlanStatus            string
	PendingTaskPlanApplyRouteClosed  bool
	AppliedTaskPlanStatus            string
	AppliedTaskStatus                string
	RunsAfterPlanContinuation        int
	RepeatedPlanConverged            bool
	ExecutionID                      string
	PendingExecutionStartRouteClosed bool
	RepeatedExecuteConverged         bool
	ExecutionOutcome                 string
	CompletedTaskStatus              string
	Implementation                   PlanRunObservation
	Verification                     PlanRunObservation
	RejectedTaskID                   string
	RejectedTaskStatus               string
	RejectedTaskPlanStatus           string
	RejectedTaskRunCount             int
}

// ReviewSafetyTaskHandoffObservation carries exact-prompt, trusted-inspection,
// Document, and structural authorization facts for REAL-E2E-11 without importing
// Bridge's internal domain types into the black-box suite.

type WorkVerificationObservation struct {
	RunTaskID             string
	RunExecutionID        string
	RunOutcome            string
	RunTaskStatus         string
	RunScope              string
	RunCheckKind          string
	RunCheckCommand       string
	Implementation        PlanRunObservation
	Verification          PlanRunObservation
	FailTaskID            string
	FailExecutionID       string
	FailOutcome           string
	FailTaskStatus        string
	FailImplementation    PlanRunObservation
	FailVerification      PlanRunObservation
	NoneTaskID            string
	NoneExecutionID       string
	NoneOutcome           string
	NoneTaskStatus        string
	NoneMode              string
	NoneWaiverSource      string
	NoneWaiverReason      string
	NoneVerificationRunID string
	NoneImplementation    PlanRunObservation
}

type ExecutionScopeCaseObservation struct {
	TaskID                    string
	ExecutionID               string
	Outcome                   string
	TaskStatus                string
	ScopeKind                 string
	ScopePaths                []string
	VerificationRunID         string
	ApprovedProtocolActionIDs []string
	Implementation            PlanRunObservation
	Verification              PlanRunObservation
}

type ExecutionScopeObservation struct {
	ValidPaths ExecutionScopeCaseObservation
	Violation  ExecutionScopeCaseObservation
	Repository ExecutionScopeCaseObservation
}

// Adapter is the stable black-box seam for a future executable or API client.
// Implementations should invoke Dev Bridge as an external system, not import its
// coordinator, stores, or domain models.
type Adapter interface {
	Name() string
	NormalExecution(context.Context, GateRequest) (NormalObservation, error)
	RuntimeApproval(context.Context, GateRequest, string) (ApprovalObservation, error)
	ActiveInterruptionCommand(context.Context, GateRequest) (CommandSpec, error)
	WaitUntilActive(context.Context, GateRequest) error
	RestartCommand(context.Context, GateRequest) (CommandSpec, error)
	ObserveActiveRecovery(context.Context, GateRequest, *ChildProcess) (RecoveryObservation, error)
	ApprovalInterruptionCommand(context.Context, GateRequest) (CommandSpec, error)
	WaitUntilWaitingApproval(context.Context, GateRequest) error
	ObserveApprovalRecovery(context.Context, GateRequest, *ChildProcess) (RecoveryObservation, error)
	ParallelIsolation(context.Context, GateRequest) (ParallelObservation, error)
	WorkspaceContinuity(context.Context, GateRequest) (ContinuityObservation, error)
	WorkVerification(context.Context, GateRequest) (WorkVerificationObservation, error)
	ExecutionScope(context.Context, GateRequest) (ExecutionScopeObservation, error)
	TaskFirstScopeReview(context.Context, GateRequest) (TaskFirstScopeReviewObservation, error)
}

type DeferredAdapter struct {
	Reasons map[GateID]string
}

func (a DeferredAdapter) Name() string { return "deferred" }

func (a DeferredAdapter) reason(id GateID) error {
	reason := a.Reasons[id]
	if reason == "" {
		reason = "no stable black-box Dev Bridge executable/API adapter is available"
	}
	return &CapabilityUnavailableError{Reason: reason}
}

func (a DeferredAdapter) NormalExecution(context.Context, GateRequest) (NormalObservation, error) {
	return NormalObservation{}, a.reason(GateNormalExecution)
}
func (a DeferredAdapter) RuntimeApproval(context.Context, GateRequest, string) (ApprovalObservation, error) {
	return ApprovalObservation{}, a.reason(GateRuntimeApproval)
}
func (a DeferredAdapter) ActiveInterruptionCommand(context.Context, GateRequest) (CommandSpec, error) {
	return CommandSpec{}, a.reason(GateActiveInterruption)
}
func (a DeferredAdapter) WaitUntilActive(context.Context, GateRequest) error {
	return a.reason(GateActiveInterruption)
}
func (a DeferredAdapter) RestartCommand(context.Context, GateRequest) (CommandSpec, error) {
	return CommandSpec{}, a.reason(GateActiveInterruption)
}
func (a DeferredAdapter) ObserveActiveRecovery(context.Context, GateRequest, *ChildProcess) (RecoveryObservation, error) {
	return RecoveryObservation{}, a.reason(GateActiveInterruption)
}
func (a DeferredAdapter) ApprovalInterruptionCommand(context.Context, GateRequest) (CommandSpec, error) {
	return CommandSpec{}, a.reason(GateApprovalInterruption)
}
func (a DeferredAdapter) WaitUntilWaitingApproval(context.Context, GateRequest) error {
	return a.reason(GateApprovalInterruption)
}
func (a DeferredAdapter) ObserveApprovalRecovery(context.Context, GateRequest, *ChildProcess) (RecoveryObservation, error) {
	return RecoveryObservation{}, a.reason(GateApprovalInterruption)
}
func (a DeferredAdapter) ParallelIsolation(context.Context, GateRequest) (ParallelObservation, error) {
	return ParallelObservation{}, a.reason(GateParallelIsolation)
}
func (a DeferredAdapter) WorkspaceContinuity(context.Context, GateRequest) (ContinuityObservation, error) {
	return ContinuityObservation{}, a.reason(GateWorkspaceContinuity)
}
func (a DeferredAdapter) WorkVerification(context.Context, GateRequest) (WorkVerificationObservation, error) {
	return WorkVerificationObservation{}, a.reason(GateWorkVerification)
}
func (a DeferredAdapter) ExecutionScope(context.Context, GateRequest) (ExecutionScopeObservation, error) {
	return ExecutionScopeObservation{}, a.reason(GateExecutionScope)
}
func (a DeferredAdapter) TaskFirstScopeReview(context.Context, GateRequest) (TaskFirstScopeReviewObservation, error) {
	return TaskFirstScopeReviewObservation{}, a.reason(GateTaskFirstScopeReview)
}

type CapabilityUnavailableError struct{ Reason string }

func (e *CapabilityUnavailableError) Error() string { return e.Reason }

type Clock func() time.Time
