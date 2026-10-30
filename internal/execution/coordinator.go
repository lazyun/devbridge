package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

type RepositoryService interface {
	Get(context.Context, string) (repository.Repository, error)
}
type currentHEADRepositoryService interface {
	CurrentHEAD(context.Context, string) (string, error)
}
type CodexConfigService interface {
	Get(context.Context, string) (codexconfig.Config, error)
}
type TaskService interface {
	Get(context.Context, string) (task.Task, error)
}
type RunService interface {
	Create(context.Context, string) (run.Run, error)
	Get(context.Context, string) (run.Run, error)
	Transition(context.Context, string, run.Status) (run.Run, error)
	AttachWorktree(context.Context, string, string) (run.Run, error)
	AttachWorkspace(context.Context, string, string) (run.Run, error)
	AttachThread(context.Context, string, string) (run.Run, error)
	AttachTurn(context.Context, string, string) (run.Run, error)
	AttachStartSnapshot(context.Context, string, string) (run.Run, error)
	AttachEndSnapshot(context.Context, string, string) (run.Run, error)
	Succeed(context.Context, string, run.Result) (run.Run, error)
	Fail(context.Context, string, run.Failure) (run.Run, error)
}
type verificationReportRunService interface {
	AttachVerificationReport(context.Context, string, run.VerificationReport) (run.Run, error)
}
type resultRunService interface {
	AttachResult(context.Context, string, run.Result) (run.Run, error)
}
type promptRunService interface {
	RecordPrompt(context.Context, string, string, time.Time) (run.Run, error)
}

// taskGoalLister is the optional capability that reports whether a Task has
// ordered Goal definitions.
type taskGoalLister interface {
	ListGoals(context.Context, string) ([]task.Goal, error)
}
type usageAwareRunService interface {
	SucceedWithUsage(context.Context, string, run.Result, *run.TokenUsageObservation) (run.Run, error)
	FailWithUsage(context.Context, string, run.Failure, *run.TokenUsageObservation) (run.Run, error)
}
type WorktreeManager interface {
	Create(context.Context, worktree.CreateInput) (worktree.Worktree, error)
}
type worktreeGetter interface {
	Get(context.Context, string) (worktree.Worktree, error)
}
type EvidenceCollector interface {
	CaptureSnapshot(context.Context, string) (string, error)
	CollectRun(context.Context, string, string, string, string) (evidence.GitEvidence, error)
}

type WorkspaceService interface {
	EnsureForFeatureArea(context.Context, string, string, string, string) (workspace.Workspace, error)
	AttachWorktree(context.Context, string, string) (workspace.Workspace, error)
	RecordTokenBaseline(context.Context, string, *workspace.TokenCounts) (workspace.Workspace, error)
	MarkContinuityUnavailable(context.Context, string) (workspace.Workspace, error)
}
type plannedWorkspaceService interface {
	EnsurePlannedMembers(context.Context, string, string, string, []workset.Target) (workspace.Workspace, error)
}
type repositoryBindingStore interface {
	ListRepositoryBindings(context.Context, string) ([]run.RepositoryBinding, error)
	PutRepositoryBinding(context.Context, run.RepositoryBinding) error
}
type UpperReviewAccess interface {
	Prepare(context.Context, review.UpperTarget) (review.UpperAccess, error)
	UpperReviewAccessForSession(context.Context, string, string) (review.UpperAccess, error)
}

// CodexSession is retained for existing callers; new adapters use agent.Session.
type CodexSession = agent.Session
type InteractiveCodexSession interface {
	CodexSession
	NextApproval(context.Context) (codex.ApprovalRequest, error)
	ResolveApproval(context.Context, string, codex.ApprovalDecision) error
}
type ApprovalController interface {
	Register(string, approval.Resolver, context.CancelFunc) (func(), error)
	Request(context.Context, string, codex.ApprovalRequest) (approval.Approval, error)
	Wait(context.Context, string) error
}
type CodexFactory interface {
	Start(context.Context, codexconfig.Config) (CodexSession, error)
}
type modelAwareCodexFactory interface {
	StartWithSelection(context.Context, codexconfig.Config, string, string) (CodexSession, error)
}

type operationAwareCodexFactory interface {
	StartForOperation(context.Context, codexconfig.Config, string, string, codex.AuthorizationCeiling) (CodexSession, error)
}

type DiagnosticRecorder interface {
	RecordThreadNameFailure(context.Context, string, string, string, time.Time) error
}

// CodexTurnResult is the compatibility name for neutral observed turn facts.
type CodexTurnResult = agent.TurnResult

type RuntimeHandoffStore interface {
	ReadHandoff(context.Context, string, string) (executionproposal.Handoff, error)
	ClaimHandoff(context.Context, string, string, time.Time) (executionproposal.Handoff, bool, error)
	RecordHandoff(context.Context, string, string, executionproposal.HandoffState, string, time.Time) (executionproposal.Handoff, error)
}

type Coordinator struct {
	QualifiedRuntime interface {
		VerifyExecution(context.Context, taskgoal.Execution) error
	}
	AppServers AppServerContinuityStore

	Handoffs RuntimeHandoffStore
	// ProjectAuthorizations freezes explicit project-level trusted execution
	// grants into new Runs. Nil keeps every Run on legacy per-path behavior.
	ProjectAuthorizations interface {
		Get(context.Context, string) (project.ExecutionAuthorization, error)
	}
	RuntimeConfigProvider func() codexruntime.Allowlist
	Cancellations         executioncancel.Store
	Repositories          RepositoryService
	CodexConfigs          CodexConfigService
	Tasks                 TaskService
	Runs                  RunService
	Worktrees             WorktreeManager
	Workspaces            WorkspaceService
	Codex                 CodexFactory
	PiAgent               CodexFactory
	Agents                *agent.Registry[agent.Factory]
	Evidence              EvidenceCollector
	Results               ResultRecorder
	Approvals             ApprovalController
	Diagnostics           DiagnosticRecorder
	Reviews               reviewProductRuntime
	UpperReviews          UpperReviewAccess
	Goals                 *taskgoal.Service
	Executions            taskgoal.ExecutionLookup
	// Conversations owns FeatureArea-scoped Codex context when configured.
	// Workspaces remain a worktree lifecycle concern only.
	Conversations agentconversation.Runtime

	mu                      sync.Mutex
	taskLocks               map[string]*sync.Mutex
	upperReviewPreparations map[[2]string]*upperReviewPreparationGate
}

type upperReviewPreparationGate struct {
	available chan struct{}
	users     int
}

// StartUpperReview prepares the ordinary managed workspace for an externally
// driven Upper LLM review. It deliberately has no Codex dependency and never
// creates a conversation or thread.
func (c *Coordinator) StartUpperReview(ctx context.Context, value review.Review, reserved run.Run) (run.Run, error) {
	return c.startUpperReview(ctx, value, reserved, nil)
}

// PrepareUpperReview resumes environment preparation only for the explicit
// current target supplied to review_access_prepare. Old admission replay never
// acquires this recovered epoch/generation implicitly.
func (c *Coordinator) PrepareUpperReview(ctx context.Context, target review.UpperTarget) (review.UpperAccess, error) {
	if c == nil || c.Reviews == nil || c.Runs == nil || c.UpperReviews == nil {
		return review.UpperAccess{}, review.ErrInvalidArgument
	}
	access, err := c.UpperReviews.UpperReviewAccessForSession(ctx, target.AISessionID, target.ReviewID)
	if err != nil {
		return access, err
	}
	if access.Target != target {
		return review.UpperAccess{}, review.ErrTargetStale
	}
	checker, ok := c.UpperReviews.(interface {
		CheckPreparationTarget(context.Context, review.UpperTarget) error
	})
	if !ok {
		return review.UpperAccess{}, review.ErrTargetStale
	}
	if err = checker.CheckPreparationTarget(ctx, target); err != nil {
		return review.UpperAccess{}, err
	}
	value, err := c.Reviews.Get(ctx, target.ReviewID)
	if err != nil {
		return review.UpperAccess{}, err
	}
	reserved, err := c.Runs.Get(ctx, target.RunID)
	if err != nil {
		return review.UpperAccess{}, err
	}
	if _, err = c.startUpperReview(ctx, value, reserved, &target); err != nil {
		return review.UpperAccess{}, err
	}
	return c.UpperReviews.UpperReviewAccessForSession(ctx, target.AISessionID, target.ReviewID)
}

func (c *Coordinator) startUpperReview(ctx context.Context, value review.Review, reserved run.Run, explicit *review.UpperTarget) (out run.Run, err error) {
	initialPreparation := false
	var initialTarget review.UpperTarget
	var releasePreparation func()
	defer func() {
		// Failure projection belongs to this preparation attempt and must finish
		// before a waiting caller can observe or retry the same reservation.
		if releasePreparation != nil {
			defer releasePreparation()
		}
		if err != nil && initialPreparation && c != nil && c.UpperReviews != nil {
			if recorder, ok := c.UpperReviews.(interface {
				RecordTargetPreparationFailure(context.Context, review.UpperTarget, string) error
			}); ok {
				_ = recorder.RecordTargetPreparationFailure(context.WithoutCancel(ctx), initialTarget, err.Error())
			} else if initialTarget.GrantGeneration == 1 {
				if legacy, ok := c.UpperReviews.(interface {
					RecordPreparationFailure(context.Context, string, string, string) error
				}); ok {
					_ = legacy.RecordPreparationFailure(context.WithoutCancel(ctx), value.ID, reserved.ID, err.Error())
				}
			}
			next := "replay the original start request"
			if explicit != nil {
				next = "retry review_access_prepare with the same explicit target"
			}
			err = fmt.Errorf("%w: Upper Review %s Run %s; %s after resolving the cause: %w", review.ErrPreparationFailed, value.ID, reserved.ID, next, err)
		}
	}()
	if c == nil || c.Repositories == nil || c.Runs == nil || c.Workspaces == nil || c.Worktrees == nil || c.UpperReviews == nil {
		return run.Run{}, errors.New("Upper Review coordinator dependencies are incomplete")
	}
	if value.ExecutorKind.Effective() != review.ExecutorUpperLLM || reserved.ID == "" || reserved.ReviewID != value.ID || reserved.Purpose != run.PurposeScopeReview {
		return run.Run{}, review.ErrInvalidArgument
	}
	// The foreground start and the durable admission continuation can reach
	// physical preparation together. Serialize only this exact Review/Run,
	// without weakening worktree collision checks or caching authority.
	releasePreparation, err = c.lockUpperReviewPreparation(ctx, value.ID, reserved.ID)
	if err != nil {
		return run.Run{}, err
	}
	current, readErr := c.Runs.Get(ctx, reserved.ID)
	if readErr != nil {
		return run.Run{}, readErr
	}
	if current.ID != reserved.ID || current.RuntimeEpoch != reserved.RuntimeEpoch || current.ProjectID != reserved.ProjectID || current.TaskID != reserved.TaskID || current.FeatureAreaID != reserved.FeatureAreaID || current.OperationKind != reserved.OperationKind || current.OperationID != reserved.OperationID || current.ReviewID != value.ID || current.ProjectID != value.ProjectID || current.TaskID != value.TaskID || current.FeatureAreaID != value.FeatureAreaID || current.OperationKind != run.OperationReview || current.OperationID != value.ID || current.Purpose != run.PurposeScopeReview {
		return run.Run{}, review.ErrTargetStale
	}
	reserved = current
	// A start admission owns only generation 1. Replaying that old admission
	// must never substitute a recovered generation fetched from the latest
	// grant. Recovery activation uses explicit review_access_prepare instead.
	var accessState string
	resolveTarget := func() (review.UpperTarget, error) {
		access, err := c.UpperReviews.UpperReviewAccessForSession(ctx, value.CreatedByAISessionID, value.ID)
		if err != nil {
			return review.UpperTarget{}, err
		}
		target := access.Target
		if target.AISessionID != value.CreatedByAISessionID || target.FeatureAreaID != value.FeatureAreaID || target.TaskID != value.TaskID || target.ReviewID != value.ID || target.RunID != reserved.ID || target.RuntimeEpoch != reserved.RuntimeEpoch || ((explicit == nil && target.GrantGeneration != 1) || (explicit != nil && target != *explicit)) || (access.State != "PREPARING" && access.State != "ACTIVE" && access.State != "AWAITING_REPORT") {
			return review.UpperTarget{}, review.ErrTargetStale
		}
		accessState = access.State
		return target, nil
	}
	// Check before any Run/workspace mutation, then resolve again before the
	// final Prepare commit; the store independently fences epoch/generation.
	initialTarget, targetErr := resolveTarget()
	if targetErr != nil {
		return run.Run{}, targetErr
	}
	if reserved.Status == run.StatusRunning && (accessState == "ACTIVE" || accessState == "AWAITING_REPORT") {
		// ACTIVE alone is a projection: cancellation, claim release, or owner
		// advancement can precede its update. Reuse only after the same pure
		// lifecycle predicate used by explicit preparation accepts this target.
		if checker, ok := c.UpperReviews.(interface {
			CheckPreparationTarget(context.Context, review.UpperTarget) error
		}); ok {
			if err := checker.CheckPreparationTarget(ctx, initialTarget); err != nil {
				return run.Run{}, err
			}
			return reserved, nil
		}
	}
	// Preserve the initial observation through stores that reload the Run.
	// Their writer transactions must not adopt a later recovered epoch.
	ctx = runtimeepoch.WithFence(ctx, initialTarget.RuntimeEpoch, initialTarget.RunID)
	initialPreparation = true
	if reserved.Status == run.StatusCreated {
		reserved, err = c.Runs.Transition(ctx, reserved.ID, run.StatusPreparing)
		if err != nil {
			return run.Run{}, err
		}
	}
	if reserved.Status != run.StatusPreparing && reserved.Status != run.StatusRunning {
		return run.Run{}, &run.TransitionError{From: reserved.Status, To: run.StatusPreparing}
	}
	if reserved.Status == run.StatusRunning {
		if len(value.RepositoryTargets) > 0 {
			return c.startMultiUpperReview(ctx, value, reserved, resolveTarget)
		}
		target, targetErr := resolveTarget()
		if targetErr != nil {
			initialPreparation = false
			return run.Run{}, targetErr
		}
		_, err := c.UpperReviews.Prepare(ctx, target)
		if err != nil {
			return run.Run{}, err
		}
		return reserved, nil
	}
	if len(value.RepositoryTargets) > 0 {
		return c.startMultiUpperReview(ctx, value, reserved, resolveTarget)
	}
	repositoryValue, err := c.Repositories.Get(ctx, value.RepositoryID)
	if err != nil {
		return run.Run{}, err
	}
	workspaceValue, err := c.Workspaces.EnsureForFeatureArea(ctx, value.ProjectID, value.RepositoryID, value.FeatureAreaID, value.BaseCommit)
	if err != nil {
		return run.Run{}, err
	}
	if reserved.Status == run.StatusPreparing {
		reserved, err = c.Runs.AttachWorkspace(ctx, reserved.ID, workspaceValue.ID)
		if err != nil {
			return run.Run{}, err
		}
	}
	worktreeValue, _, err := c.workspaceWorktree(ctx, workspaceValue, value.BaseCommit, repositoryValue, reserved)
	if err != nil {
		return run.Run{}, err
	}
	if reserved.Status == run.StatusPreparing {
		reserved, err = c.Runs.AttachWorktree(ctx, reserved.ID, worktreeValue.ID)
		if err != nil {
			return run.Run{}, err
		}
	}
	if reserved.Status != run.StatusRunning {
		reserved, err = c.Runs.Transition(ctx, reserved.ID, run.StatusRunning)
		if err != nil {
			return run.Run{}, err
		}
	}
	target, targetErr := resolveTarget()
	if targetErr != nil {
		initialPreparation = false
		return run.Run{}, targetErr
	}
	_, err = c.UpperReviews.Prepare(ctx, target)
	if err != nil {
		return run.Run{}, err
	}
	return reserved, nil
}

// startMultiUpperReview prepares every immutable target recorded at admission.
// It never projects a member into Run.WorktreeID/WorkspaceID: bindings are the
// authoritative relationship, while those scalar fields stay v1 compatibility.
func (c *Coordinator) startMultiUpperReview(ctx context.Context, value review.Review, reserved run.Run, resolveTarget func() (review.UpperTarget, error)) (run.Run, error) {
	if preparation, found, err := c.recoveryReviewPreparation(ctx, reserved.ID); err != nil {
		return run.Run{}, err
	} else if found {
		return c.startPreparedPurposeUpperReview(ctx, value, reserved, preparation, resolveTarget)
	}
	planner, ok := c.Workspaces.(plannedWorkspaceService)
	if !ok || value.WorkspaceID == "" {
		return run.Run{}, errors.New("exact planned workspace preparation is unavailable")
	}
	bindings, ok := c.Runs.(repositoryBindingStore)
	if !ok {
		return run.Run{}, errors.New("durable repository bindings are unavailable")
	}
	workspaceValue, err := planner.EnsurePlannedMembers(ctx, value.ProjectID, value.FeatureAreaID, value.WorkspaceID, value.RepositoryTargets)
	if err != nil {
		return run.Run{}, err
	}
	persisted, err := bindings.ListRepositoryBindings(ctx, reserved.ID)
	if err != nil {
		return run.Run{}, err
	}
	if len(persisted) != len(value.RepositoryTargets) {
		return run.Run{}, review.ErrConflict
	}
	byRepo := make(map[string]run.RepositoryBinding, len(persisted))
	for _, binding := range persisted {
		if binding.RunID != reserved.ID || binding.ReviewID != value.ID || binding.ProjectID != value.ProjectID || binding.FeatureAreaID != value.FeatureAreaID || binding.WorkspaceID != value.WorkspaceID || binding.RepositoryID == "" || binding.MemberID == "" || binding.MemberVersion < 1 || !workset.ValidCommit(binding.OriginCommit) {
			return run.Run{}, review.ErrConflict
		}
		if _, exists := byRepo[binding.RepositoryID]; exists {
			return run.Run{}, review.ErrConflict
		}
		byRepo[binding.RepositoryID] = binding
	}
	for _, target := range value.RepositoryTargets {
		binding, exists := byRepo[target.RepositoryID]
		if !exists || binding.MemberID != target.MemberID || binding.MemberVersion != target.MemberVersion || binding.OriginCommit != target.OriginCommit {
			return run.Run{}, review.ErrConflict
		}
	}
	for _, target := range value.RepositoryTargets {
		repositoryValue, err := c.Repositories.Get(ctx, target.RepositoryID)
		if err != nil {
			return run.Run{}, err
		}
		worktreeValue, updatedWorkspace, err := c.workspaceWorktree(ctx, workspaceValue, target.OriginCommit, repositoryValue, reserved)
		workspaceValue = updatedWorkspace
		if err != nil {
			_ = bindings.PutRepositoryBinding(context.WithoutCancel(ctx), bindingFailure(byRepo[target.RepositoryID], err))
			return run.Run{}, err
		}
		binding := byRepo[target.RepositoryID]
		binding.WorktreeID, binding.Preparation, binding.Failure = worktreeValue.ID, "READY", ""
		if err := bindings.PutRepositoryBinding(ctx, binding); err != nil {
			return run.Run{}, fmt.Errorf("persist Upper Review member binding %s (%s -> %s): %w", target.RepositoryID, byRepo[target.RepositoryID].WorktreeID, worktreeValue.ID, err)
		}
	}
	if reserved.Status != run.StatusRunning {
		reserved, err = c.Runs.Transition(ctx, reserved.ID, run.StatusRunning)
		if err != nil {
			return run.Run{}, err
		}
	}
	target, err := resolveTarget()
	if err != nil {
		return run.Run{}, err
	}
	if _, err = c.UpperReviews.Prepare(ctx, target); err != nil {
		return run.Run{}, err
	}
	return reserved, nil
}

func bindingFailure(value run.RepositoryBinding, cause error) run.RepositoryBinding {
	value.Preparation, value.Failure = "FAILED", cause.Error()
	return value
}

func (c *Coordinator) StartRun(ctx context.Context, taskID string) (run.Run, error) {
	if c == nil {
		return run.Run{}, errors.New("execution coordinator is not initialized")
	}
	unlock := c.lockTask(taskID)
	defer unlock()
	prepared, err := c.PrepareRun(ctx, taskID)
	if err != nil {
		return prepared.Run, err
	}
	return c.ExecuteRun(ctx, prepared, nil)
}

// PreparedRun is the durable handoff boundary between validating/creating a
// run and executing it. The task, repository and Codex configuration snapshots
// are intentionally private: only the coordinator may consume them.
type PreparedRun struct {
	qualifiedExecution *taskgoal.Execution
	rejoin             *rejoinExecution

	multiRuntime     *PreparedMultiRuntime
	scalarRuntime    *PreparedMultiRuntime
	Run              run.Run
	task             task.Task
	review           *review.Review
	repository       repository.Repository
	config           codexconfig.Config
	backendSelection *codexconfig.BackendSelection
	instruction      Instruction
	executionScope   task.ExecutionScope
	purpose          run.Purpose
	verificationSpec *task.VerificationSpec
	reviewScope      run.ReviewScope
}

// PrepareScopeReview loads a reserved read-only Run and its direct Review owner.
func (c *Coordinator) PrepareScopeReview(ctx context.Context, runID string) (PreparedRun, error) {
	if c == nil || c.Repositories == nil || c.CodexConfigs == nil || c.Runs == nil || c.Worktrees == nil || c.Workspaces == nil || c.Codex == nil || c.Evidence == nil {
		return PreparedRun{}, errors.New("execution coordinator dependencies are incomplete")
	}
	runValue, err := c.Runs.Get(ctx, runID)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("load reserved Scope Review Run: %w", err)
	}
	prepared := PreparedRun{Run: runValue, purpose: run.PurposeScopeReview, instruction: Instruction{CodexInstruction: runValue.CodexInstruction}, reviewScope: runValue.ReviewScope}
	if runValue.Status != run.StatusCreated || runValue.Purpose != run.PurposeScopeReview || runValue.ReviewID == "" || runValue.TaskID == "" || runValue.ExecutionProposalID != "" || runValue.CodexInstruction == "" {
		return c.failReservedSetup(ctx, prepared, "Scope Review contract", "SCOPE_REVIEW_CONTRACT_INVALID", errors.New("invalid reserved Scope Review contract"))
	}
	return c.prepareProductReview(ctx, prepared)
}

func (c *Coordinator) PrepareRun(ctx context.Context, taskID string) (PreparedRun, error) {
	if c == nil || c.Repositories == nil || c.CodexConfigs == nil || c.Tasks == nil || c.Runs == nil || c.Worktrees == nil || c.Workspaces == nil || c.Codex == nil || c.Evidence == nil {
		return PreparedRun{}, errors.New("execution coordinator dependencies are incomplete")
	}
	taskValue, err := c.Tasks.Get(ctx, taskID)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("load task: %w", err)
	}
	if len(taskValue.RepositoryTargets) > 0 {
		if err := task.ValidateExecutablePrimary(task.Definition{Version: 2, RepositoryTargets: taskValue.RepositoryTargets, PrimaryMemberID: taskValue.PrimaryMemberID, WorkSpec: taskValue.WorkSpec}); err != nil {
			return PreparedRun{}, err
		}
		return PreparedRun{}, errors.New("multi-repository Coding tasks require an approved Execution reservation")
	}
	// Direct preparation is only legal for a non-terminal ACTIVE Development
	// Task whose stage authority has already been admitted by its owning
	// transaction. Terminal, legacy READY/RUNNING and any other Status is
	// rejected before a Run exists, so no side effect precedes the rejection.
	if !executableTaskStatus(taskValue.Status) {
		return PreparedRun{}, &TaskNotExecutableError{TaskID: taskID, Status: string(taskValue.Status)}
	}
	if err := task.ValidateWorkSpec(taskValue.WorkSpec); err != nil {
		return PreparedRun{}, fmt.Errorf("task has no executable WorkSpec: %w", err)
	}
	repositoryValue, err := c.Repositories.Get(ctx, taskValue.RepositoryID)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("load repository: %w", err)
	}
	configValue, err := c.CodexConfigs.Get(ctx, taskValue.CodexConfigID)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("load task Codex config: %w", err)
	}
	if !configValue.Enabled {
		return PreparedRun{}, errors.New("task Codex config is disabled")
	}
	if !fullCommit(taskValue.BaseCommit) {
		return PreparedRun{}, fmt.Errorf("task base commit is not an immutable full hash: %q", taskValue.BaseCommit)
	}
	runValue, err := c.Runs.Create(ctx, taskID)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("create run: %w", err)
	}
	prepared := PreparedRun{Run: runValue, task: taskValue, repository: repositoryValue, config: configValue, instruction: Instruction{CodexInstruction: taskValue.WorkSpec.CodexInstruction}, executionScope: task.CloneWorkSpec(taskValue.WorkSpec).ExecutionScope, purpose: run.PurposeImplementation}
	state := executionState{coordinator: c, prepared: &prepared, ctx: ctx}
	runValue, err = c.Runs.Transition(ctx, runValue.ID, run.StatusPreparing)
	if err != nil {
		if contextEnded(ctx, err) {
			value, failErr := state.failContext("run preparation state", err)
			prepared.Run = value
			return prepared, failErr
		}
		value, failErr := state.fail(run.FailureInfrastructure, "run preparation state", err)
		prepared.Run = value
		return prepared, failErr
	}
	prepared.Run = runValue
	if ctx.Err() != nil {
		value, failErr := state.failContext("run preparation", ctx.Err())
		prepared.Run = value
		return prepared, failErr
	}
	return prepared, nil
}

// PrepareReservedRun prepares the exact CREATED Run atomically reserved by an
// ExecutionProposal. Failures terminalize that Run without creating a hidden
// replacement attempt.
func (c *Coordinator) PrepareReservedRun(ctx context.Context, runID string, instruction Instruction) (PreparedRun, error) {
	return c.PrepareReservedExecution(ctx, runID, instruction, nil)
}

func (c *Coordinator) PrepareReservedExecution(ctx context.Context, runID string, instruction Instruction, verificationSpec *task.VerificationSpec) (PreparedRun, error) {
	if c == nil || c.Repositories == nil || c.CodexConfigs == nil || c.Tasks == nil || c.Runs == nil || c.Worktrees == nil || c.Workspaces == nil || c.Codex == nil || c.Evidence == nil {
		return PreparedRun{}, errors.New("execution coordinator dependencies are incomplete")
	}
	runValue, err := c.Runs.Get(ctx, runID)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("load reserved run: %w", err)
	}
	prepared := PreparedRun{Run: runValue, instruction: Instruction{Goal: instruction.Goal, Constraints: append([]string(nil), instruction.Constraints...), CodexInstruction: instruction.CodexInstruction, ExecutionScope: task.ExecutionScope{Kind: instruction.ExecutionScope.Kind, Paths: append([]string(nil), instruction.ExecutionScope.Paths...)}}, purpose: runValue.Purpose}
	// Task success is exclusively committed by TASK_ACCEPTANCE after delivery;
	// an Execution-owned Run only terminalizes its Execution transport.
	if prepared.purpose == "" {
		prepared.purpose = run.PurposeImplementation
	}
	if verificationSpec != nil {
		copy := task.CloneVerificationSpec(*verificationSpec)
		prepared.verificationSpec = &copy
	}
	if runValue.Status != run.StatusCreated && runValue.Status != run.StatusPreparing {
		return prepared, &run.TransitionError{From: runValue.Status, To: run.StatusPreparing}
	}
	if strings.TrimSpace(instruction.CodexInstruction) == "" || len(instruction.CodexInstruction) > task.MaxCodexInstructionBytes || strings.ContainsRune(instruction.CodexInstruction, '\x00') {
		return c.failReservedSetup(ctx, prepared, "authored instruction", "CONTRACT_INCOMPLETE", task.ErrInstructionRequired)
	}
	switch prepared.purpose {
	case run.PurposeImplementation:
		if prepared.verificationSpec != nil {
			return c.failReservedSetup(ctx, prepared, "execution purpose", "reserved_implementation_contract_invalid", errors.New("IMPLEMENTATION Run cannot carry a VerificationSpec"))
		}
	case run.PurposeVerification:
		if prepared.verificationSpec == nil || prepared.verificationSpec.Mode != task.VerificationModeRun {
			return c.failReservedSetup(ctx, prepared, "execution purpose", "reserved_verification_contract_invalid", errors.New("VERIFICATION Run requires a confirmed RUN VerificationSpec"))
		}
		if err := task.ValidateVerificationSpec(*prepared.verificationSpec); err != nil {
			return c.failReservedSetup(ctx, prepared, "execution purpose", "reserved_verification_contract_invalid", err)
		}
		if instruction.CodexInstruction != prepared.verificationSpec.CodexInstruction {
			return c.failReservedSetup(ctx, prepared, "execution purpose", "reserved_verification_contract_invalid", errors.New("verification Codex instruction differs from the confirmed VerificationSpec"))
		}
	default:
		return c.failReservedSetup(ctx, prepared, "execution purpose", "reserved_run_purpose_invalid", fmt.Errorf("unsupported Run purpose %q", prepared.purpose))
	}
	taskValue, err := c.Tasks.Get(ctx, runValue.TaskID)
	if err != nil {
		return c.failReservedSetup(ctx, prepared, "load task", "reserved_task_load_failed", fmt.Errorf("load task: %w", err))
	}
	var boundSelection *codexconfig.BackendSelection
	var boundConfig *codexconfig.Config
	var boundProposal *taskgoal.Execution
	if runValue.FeatureAreaID != "" {
		if c.Executions == nil || runValue.ExecutionProposalID == "" {
			return c.failReservedSetup(ctx, prepared, "Execution owner", "EXECUTION_CONTEXT_UNAVAILABLE", errors.New("execution owner unavailable"))
		}
		proposal, proposalErr := c.Executions.Get(ctx, runValue.ExecutionProposalID)
		boundProposal = &proposal
		boundSelection = proposal.BackendSelection
		if boundSelection != nil {
			selection := *boundSelection
			prepared.backendSelection = &selection
		}
		phaseRunID := proposal.RunID
		if runValue.Purpose == run.PurposeVerification {
			phaseRunID = proposal.VerificationRunID
		}
		if proposalErr != nil || proposal.Status != "CONSUMED" || proposal.Outcome != "RUNNING" || phaseRunID != runValue.ID || proposal.TaskID != taskValue.ID || proposal.ProjectID != runValue.ProjectID || taskValue.ProjectID != runValue.ProjectID || taskValue.FeatureAreaID != runValue.FeatureAreaID || runValue.OperationKind != run.OperationExecution || runValue.OperationID != proposal.ID {
			return c.failReservedSetup(ctx, prepared, "Execution owner", "EXECUTION_CONTEXT_UNAVAILABLE", errors.Join(proposalErr, errors.New("execution owner mismatch")))
		}
		taskValue, err = c.frozenTask(ctx, taskValue, proposal.TaskDefinitionRevisionID)
		if err != nil {
			return c.failReservedSetup(ctx, prepared, "Execution definition", "EXECUTION_CONTEXT_UNAVAILABLE", err)
		}
		if boundSelection != nil {
			if boundSelection.ConfigID == "" || boundSelection.ConfigFingerprint == "" {
				return c.failReservedSetup(ctx, prepared, "Execution backend", "EXECUTION_BACKEND_INVALID", errors.New("backend selection is incomplete"))
			}
			config, configErr := c.CodexConfigs.Get(ctx, boundSelection.ConfigID)
			if configErr != nil || !config.Enabled || config.Fingerprint() != boundSelection.ConfigFingerprint {
				return c.failReservedSetup(ctx, prepared, "Execution backend", "EXECUTION_BACKEND_INVALID", errors.Join(configErr, errors.New("backend selection is stale or disabled")))
			}
			taskValue.CodexConfigID = config.ID
			boundConfig = &config
		} else {
			return c.failReservedSetup(ctx, prepared, "Execution backend", "EXECUTION_BACKEND_EVIDENCE_UNAVAILABLE", errors.New("Execution has no frozen backend selection"))
		}

		if prepared.purpose == run.PurposeImplementation {
			expectedInstruction := taskValue.WorkSpec.CodexInstruction
			if reader, ok := c.Runs.(interface {
				RecoveryContinuationInstruction(context.Context, string) (string, error)
			}); ok {
				override, e := reader.RecoveryContinuationInstruction(ctx, runValue.ExecutionProposalID)
				if e != nil {
					return c.failReservedSetup(ctx, prepared, "recovery continuation", "RECOVERY_STATUS_CONFLICT", e)
				}
				if override != "" {
					expectedInstruction = override
				}
			}
			if instruction.CodexInstruction != expectedInstruction {
				return c.failReservedSetup(ctx, prepared, "immutable instruction", "CONTRACT_INCOMPLETE", errors.New("implementation instruction differs from exact approved or recovery continuation instruction"))
			}
		}
		if prepared.purpose == run.PurposeVerification && (instruction.CodexInstruction != taskValue.VerificationSpec.CodexInstruction || !reflect.DeepEqual(*prepared.verificationSpec, taskValue.VerificationSpec)) {
			return c.failReservedSetup(ctx, prepared, "immutable instruction", "CONTRACT_INCOMPLETE", errors.New("verification contract differs from TaskDefinitionRevision"))
		}

	}
	if prepared.purpose == run.PurposeVerification && taskValue.WorkSpec.ImplementationMode == task.ImplementationDirectFile && taskValue.PrimaryMemberID == "" && len(taskValue.RepositoryTargets) == 1 {
		taskValue.PrimaryMemberID = taskValue.RepositoryTargets[0].MemberID
	}

	if len(taskValue.RepositoryTargets) > 0 {
		if err := task.ValidateExecutablePrimary(task.Definition{Version: 2, RepositoryTargets: taskValue.RepositoryTargets, PrimaryMemberID: taskValue.PrimaryMemberID, WorkSpec: taskValue.WorkSpec}); err != nil {
			return c.failReservedSetup(ctx, prepared, "exact repository workset", "RUNTIME_WORKSET_INCOMPLETE", err)
		}
		validRuntimeIdentity := false
		if boundProposal != nil && agentpolicy.ValidateFrozenSelection(boundProposal.RuntimeConfig.AgentID, boundProposal.RuntimeConfig.ExecutionPolicy, boundProposal.RuntimeConfig.AgentConfigFingerprint) == nil {
			if boundProposal.RuntimeConfig.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 {
				validRuntimeIdentity = boundProposal.RuntimeAdapterIdentity == agentpolicy.CooperativeAdapterIdentity && boundProposal.RuntimeProtocolIdentity == agentpolicy.CooperativeProtocolIdentity
			} else {
				validRuntimeIdentity = boundProposal.RuntimeAdapterIdentity == executionproposal.RuntimeAdapterCodexAppServerV1 && boundProposal.RuntimeProtocolIdentity == executionproposal.RuntimeProtocolCodex0149 || runtimeplatform.QualifiedExecutorIdentity(boundProposal.RuntimeAdapterIdentity, boundProposal.RuntimeProtocolIdentity)
			}
		}
		if boundProposal == nil || boundProposal.PrimaryMemberID != taskValue.PrimaryMemberID || !validRuntimeIdentity {
			return c.failReservedSetup(ctx, prepared, "exact repository workset", "RUNTIME_WORKSET_INCOMPLETE", errors.New("Execution lacks its exact frozen agent policy and runtime identity"))
		}
	}
	if boundProposal != nil && runtimeplatform.QualifiedExecutorIdentity(boundProposal.RuntimeAdapterIdentity, boundProposal.RuntimeProtocolIdentity) {
		if c.QualifiedRuntime == nil {
			return c.failReservedSetup(ctx, prepared, "qualified runtime", "RUNTIME_PLATFORM_QUALIFICATION_MISSING", errors.New("qualified runtime verifier unavailable"))
		}
		if err := c.QualifiedRuntime.VerifyExecution(ctx, *boundProposal); err != nil {
			return c.failReservedSetup(ctx, prepared, "qualified runtime", "RUNTIME_PLATFORM_IDENTITY_MISMATCH", err)
		}
		prepared.qualifiedExecution = boundProposal
	}
	prepared.task = taskValue
	if prepared.purpose == run.PurposeImplementation {
		prepared.executionScope = instruction.ExecutionScope
		if prepared.executionScope.Kind == "" {
			prepared.executionScope = task.CloneWorkSpec(taskValue.WorkSpec).ExecutionScope
		}
		if len(taskValue.RepositoryTargets) == 0 {
			if err := task.ValidateWorkSpec(task.WorkSpec{Goal: "scope validation", CodexInstruction: "scope validation", ExecutionScope: prepared.executionScope}); err != nil {
				return c.failReservedSetup(ctx, prepared, "implementation execution scope", "IMPLEMENTATION_SCOPE_INVALID", err)
			}
		} else if prepared.executionScope.Kind != task.ExecutionScopeTargets || len(prepared.executionScope.Paths) != 0 {
			return c.failReservedSetup(ctx, prepared, "implementation execution scope", "IMPLEMENTATION_SCOPE_INVALID", errors.New("multi-repository Coding scope must reference repository targets"))
		}
	}
	// A non-terminal Development Task is always ACTIVE; its phase authority was
	// admitted by the owning transaction before this Run was reserved. Any
	// other Status is rejected without a lifecycle side effect.
	if !executableTaskStatus(taskValue.Status) {
		if active := c.activeRunForTask(ctx, taskValue.ID, runValue.ID); active != "" {
			cause := &TaskAlreadyHasActiveRunError{TaskID: taskValue.ID, RunID: active}
			return c.failReservedWithCause(ctx, prepared, "run preparation state", "task_already_has_active_run", cause)
		}
		cause := &TaskNotExecutableError{TaskID: taskValue.ID, Status: string(taskValue.Status)}
		return c.failReservedWithCause(ctx, prepared, "task eligibility", "task_not_executable", cause)
	}
	primaryRepositoryID := taskValue.RepositoryID
	if len(taskValue.RepositoryTargets) > 0 {
		for _, target := range taskValue.RepositoryTargets {
			if target.MemberID == taskValue.PrimaryMemberID {
				primaryRepositoryID = target.RepositoryID
				break
			}
		}
	}
	repositoryValue, err := c.Repositories.Get(ctx, primaryRepositoryID)
	if err != nil {
		return c.failReservedSetup(ctx, prepared, "load repository", "reserved_repository_load_failed", fmt.Errorf("load repository: %w", err))
	}
	prepared.repository = repositoryValue
	if runValue.Status == run.StatusPreparing {
		store, ok := c.Runs.(scalarRuntimePreparer)
		if !ok || prepared.purpose != run.PurposeImplementation || len(taskValue.RepositoryTargets) != 0 {
			return prepared, &run.TransitionError{From: runValue.Status, To: run.StatusPreparing}
		}
		if _, err := store.ReadExecutionPreparationPlan(ctx, runValue.ID); err != nil {
			return prepared, err
		}
	}
	var configValue codexconfig.Config
	if boundConfig != nil {
		configValue = *boundConfig
	} else {
		configValue, err = c.CodexConfigs.Get(ctx, taskValue.CodexConfigID)
		if err != nil {
			return c.failReservedSetup(ctx, prepared, "load task Codex config", "reserved_config_load_failed", fmt.Errorf("load task Codex config: %w", err))
		}
	}
	prepared.config = configValue
	if !configValue.Enabled {
		return c.failReservedSetup(ctx, prepared, "task Codex config", "reserved_config_disabled", errors.New("task Codex config is disabled"))
	}
	if len(taskValue.RepositoryTargets) == 0 && !fullCommit(taskValue.BaseCommit) {
		return c.failReservedSetup(ctx, prepared, "task base commit", "reserved_base_invalid", fmt.Errorf("task base commit is not an immutable full hash: %q", taskValue.BaseCommit))
	}
	if runValue.Status == run.StatusCreated {
		runValue, err = c.Runs.Transition(ctx, runValue.ID, run.StatusPreparing)
	}
	if err != nil {
		if active := c.activeRunForTask(context.WithoutCancel(ctx), taskValue.ID, runValue.ID); active != "" {
			cause := &TaskAlreadyHasActiveRunError{TaskID: taskValue.ID, RunID: active}
			return c.failReservedWithCause(ctx, prepared, "run preparation state", "task_already_has_active_run", cause)
		}
		if contextEnded(ctx, err) {
			return c.failReservedContext(ctx, prepared, "run preparation state", err)
		}
		return c.failReservedSetup(ctx, prepared, "run preparation state", "reserved_run_prepare_failed", err)
	}
	prepared.Run = runValue
	if ctx.Err() != nil {
		state := executionState{coordinator: c, prepared: &prepared, ctx: ctx}
		value, failErr := state.failContext("run preparation", ctx.Err())
		prepared.Run = value
		return prepared, failErr
	}
	if len(taskValue.RepositoryTargets) == 0 && prepared.purpose == run.PurposeImplementation {
		if err := c.prepareScalarRuntime(ctx, &prepared); err != nil {
			// Keep the committed admission, Run, claim and receipts for exact
			// readback. An unknown external outcome must not create a new attempt.
			return prepared, &InfrastructureError{Stage: "scalar RuntimePreparation", Err: err}
		}
	}
	if len(taskValue.RepositoryTargets) > 0 {
		if c.Reviews == nil || taskValue.CurrentReviewID == "" || boundProposal == nil {
			return c.failReservedSetup(ctx, prepared, "exact repository workset", "RUNTIME_WORKSET_INCOMPLETE", errors.New("approved Review workspace authority is unavailable"))
		}
		sourceReview, reviewErr := c.Reviews.Get(ctx, taskValue.CurrentReviewID)
		if reviewErr != nil || sourceReview.WorkspaceID == "" || sourceReview.ProjectID != taskValue.ProjectID || sourceReview.FeatureAreaID != taskValue.FeatureAreaID || !sameRuntimeTargetIdentity(sourceReview.RepositoryTargets, taskValue.RepositoryTargets) {
			return c.failReservedSetup(ctx, prepared, "exact repository workset", "RUNTIME_WORKSET_INCOMPLETE", errors.Join(reviewErr, errors.New("approved Review workspace differs from the frozen Task definition")))
		}
		runValue, err = c.Runs.AttachWorkspace(ctx, runValue.ID, sourceReview.WorkspaceID)
		if err != nil {
			return c.failReservedSetup(ctx, prepared, "exact repository workset", "RUNTIME_WORKSET_PREPARATION_FAILED", err)
		}
		prepared.Run = runValue
		multi, multiErr := c.PrepareMultiRuntime(ctx, MultiRuntimePrepareInput{
			Owner:       MultiRuntimeOwner{ProjectID: taskValue.ProjectID, FeatureAreaID: taskValue.FeatureAreaID, TaskID: taskValue.ID, TaskDefinitionRevisionID: boundProposal.TaskDefinitionRevisionID, ExecutionID: boundProposal.ID, RunID: runValue.ID},
			WorkspaceID: sourceReview.WorkspaceID, PrimaryMemberID: taskValue.PrimaryMemberID, Targets: taskValue.RepositoryTargets,
			Runtime:  MultiRuntimeIdentity{AgentConfigFingerprint: boundProposal.RuntimeConfig.AgentConfigFingerprint, AgentID: boundProposal.RuntimeConfig.AgentID, ExecutionPolicy: boundProposal.RuntimeConfig.ExecutionPolicy, BackendConfigID: boundSelection.ConfigID, BackendConfigFingerprint: boundSelection.ConfigFingerprint, Model: boundProposal.RuntimeConfig.Model, ReasoningEffort: boundProposal.RuntimeConfig.ReasoningEffort, ServiceTier: boundProposal.RuntimeConfig.ServiceTier, RuntimeAdapterIdentity: boundProposal.RuntimeAdapterIdentity, RuntimeProtocolIdentity: boundProposal.RuntimeProtocolIdentity, CreatedAt: runValue.CreatedAt},
			ReadOnly: prepared.purpose == run.PurposeVerification,
		})
		if multiErr != nil {
			return c.failReservedSetup(ctx, prepared, "exact repository workset", "RUNTIME_WORKSET_PREPARATION_FAILED", multiErr)
		}
		prepared.multiRuntime = &multi
		prepared.Run, err = c.Runs.Get(ctx, runValue.ID)
		if err != nil {
			return c.failReservedSetup(ctx, prepared, "exact repository workset", "RUNTIME_WORKSET_PREPARATION_FAILED", err)
		}
	}
	return prepared, nil
}

func sameRuntimeTargetIdentity(admitted, approved []workset.Target) bool {
	if len(admitted) != len(approved) {
		return false
	}
	byRepository := make(map[string]workset.Target, len(admitted))
	for _, target := range admitted {
		byRepository[target.RepositoryID] = target
	}
	for _, target := range approved {
		frozen, ok := byRepository[target.RepositoryID]
		if !ok || frozen.MemberID != target.MemberID || frozen.MemberVersion != target.MemberVersion || frozen.OriginCommit != target.OriginCommit || frozen.OriginRef != target.OriginRef || frozen.DevelopmentCycleID != target.DevelopmentCycleID || frozen.ObservedCommit != target.ObservedCommit || !target.Read {
			return false
		}
	}
	return true
}

func (c *Coordinator) workspaceWorktree(ctx context.Context, workspaceValue workspace.Workspace, baseCommit string, repositoryValue repository.Repository, runValue run.Run) (worktree.Worktree, workspace.Workspace, error) {
	member, memberErr := workspaceValue.MemberForRepository(repositoryValue.ID)
	// Old test adapters and pre-v73 read-only projections have one explicit
	// repository identity. Never infer a member from an unrelated scalar root.
	if memberErr != nil && len(workspaceValue.Members) == 0 && workspaceValue.RepositoryID == repositoryValue.ID {
		member = workspace.LegacyMember(workspaceValue)
		memberErr = nil
	}
	if memberErr != nil {
		return worktree.Worktree{}, workspaceValue, memberErr
	}
	if member.WorktreeID == "" {
		input := worktree.CreateInput{PreserveOnFailure: len(workspaceValue.Members) > 0, RepositoryID: repositoryValue.ID, RepositoryPath: repositoryValue.Path, FeatureAreaID: workspaceValue.FeatureAreaID, RunID: runValue.ID, WorkspaceID: workspaceValue.ID, BaseCommit: member.OriginCommit}
		if member.ID != "member_"+workspaceValue.ID {
			input.MemberID = member.ID
			input.MemberVersion = member.Version
			input.MemberAlias = member.Alias
		}
		value, err := c.Worktrees.Create(ctx, input)
		if err != nil {
			if recorder, ok := c.Workspaces.(interface {
				RecordMemberFailure(context.Context, string, string, string) (workspace.Workspace, error)
			}); ok {
				updated, recordErr := recorder.RecordMemberFailure(context.WithoutCancel(ctx), workspaceValue.ID, member.ID, err.Error())
				if recordErr == nil {
					workspaceValue = updated
				} else {
					err = errors.Join(err, recordErr)
				}
			}
			return worktree.Worktree{}, workspaceValue, err
		}
		if attacher, ok := c.Workspaces.(interface {
			AttachMemberWorktree(context.Context, string, string, string) (workspace.Workspace, error)
		}); ok {
			workspaceValue, err = attacher.AttachMemberWorktree(ctx, workspaceValue.ID, member.ID, value.ID)
		} else if len(workspaceValue.Members) <= 1 && workspaceValue.RepositoryID == repositoryValue.ID {
			workspaceValue, err = c.Workspaces.AttachWorktree(ctx, workspaceValue.ID, value.ID)
		} else {
			err = errors.New("exact workspace member attachment unavailable")
		}
		return value, workspaceValue, err
	}
	getter, ok := c.Worktrees.(worktreeGetter)
	if !ok {
		return worktree.Worktree{}, workspaceValue, errors.New("worktree manager cannot reload a persisted Workspace worktree")
	}
	value, err := getter.Get(ctx, member.WorktreeID)
	if err != nil {
		return worktree.Worktree{}, workspaceValue, err
	}
	if value.RepositoryID != repositoryValue.ID || value.BaseCommit != member.OriginCommit || value.Status != worktree.StatusReady {
		return worktree.Worktree{}, workspaceValue, errors.New("persisted workspace worktree identity is inconsistent with its operation")
	}
	info, err := os.Stat(value.Path)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("workspace worktree path is not a directory")
		}
		return worktree.Worktree{}, workspaceValue, fmt.Errorf("inspect workspace worktree: %w", err)
	}
	if validator, ok := c.Worktrees.(interface {
		Validate(context.Context, worktree.Worktree) error
	}); ok {
		if err := validator.Validate(ctx, value); err != nil {
			return worktree.Worktree{}, workspaceValue, err
		}
	}
	return value, workspaceValue, nil
}

// conversationUsageBaseline follows the exact safe conversation head. A Task's
// workspace totals can refer to another conversation or an older context head.
// Missing accounting evidence means unavailable usage, never a guessed zero.
func (c *Coordinator) conversationUsageBaseline(ctx context.Context, conversation agentconversation.Conversation) *run.TokenCounts {
	if conversation.CurrentBoundaryTurnID == "" {
		return &run.TokenCounts{} // Established empty Thread: no Turn dispatched.
	}
	lookup, ok := c.Runs.(interface {
		GetByInteraction(context.Context, string, string) (run.Run, error)
	})
	if !ok {
		return nil
	}
	previous, err := lookup.GetByInteraction(ctx, conversation.BackendThreadID, conversation.CurrentBoundaryTurnID)
	if err != nil || previous.TokenUsage.Status != run.TokenUsageAvailable || previous.TokenUsage.EndCumulative == nil {
		return nil
	}
	value := *previous.TokenUsage.EndCumulative
	return &value
}

func runTokenCounts(value workspace.TokenCounts) run.TokenCounts {
	return run.TokenCounts{InputTokens: value.InputTokens, CachedInputTokens: value.CachedInputTokens, CacheWriteInputTokens: value.CacheWriteInputTokens, OutputTokens: value.OutputTokens, ReasoningOutputTokens: value.ReasoningOutputTokens, TotalTokens: value.TotalTokens}
}

func workspaceTokenCounts(value run.TokenCounts) workspace.TokenCounts {
	return workspace.TokenCounts{InputTokens: value.InputTokens, CachedInputTokens: value.CachedInputTokens, CacheWriteInputTokens: value.CacheWriteInputTokens, OutputTokens: value.OutputTokens, ReasoningOutputTokens: value.ReasoningOutputTokens, TotalTokens: value.TotalTokens}
}

// ExecuteRun owns the live portion of a prepared run. When live is non-nil the
// newly-created Codex client is attached to that supervisor session.
func (c *Coordinator) ExecuteRun(ctx context.Context, prepared PreparedRun, live *RunSession) (returnedRun run.Run, resultErr error) {
	if prepared.Run.RuntimeEpoch != "" {
		ctx = runtimeepoch.WithFence(ctx, prepared.Run.RuntimeEpoch, prepared.Run.ID)
	}
	ctx = observability.WithContext(ctx, observability.Correlation{ReviewID: prepared.Run.ReviewID, TaskID: prepared.Run.TaskID, ExecutionID: prepared.Run.AgentExecutionID, RunID: prepared.Run.ID})
	state := executionState{coordinator: c, prepared: &prepared, ctx: ctx}
	if prepared.Run.ExecutionProposalID != "" && prepared.Run.Purpose == run.PurposeImplementation || prepared.Run.ReviewID != "" {
		if gate, ok := c.Runs.(interface {
			PreparationIdentity(context.Context, string) (string, string, string, error)
			ValidatePreparedStart(context.Context, string) error
		}); ok {
			if _, _, _, identityErr := gate.PreparationIdentity(ctx, prepared.Run.ID); identityErr == nil {
				validate := gate.ValidatePreparedStart
				stage := "RuntimePreparation start authority"
				if prepared.rejoin != nil {
					stage = "RuntimePreparation rejoin authority"
					recovery, ok := c.Runs.(interface {
						ValidatePreparedRejoin(context.Context, string) error
					})
					if !ok {
						return prepared.Run, &InfrastructureError{Stage: "RuntimePreparation rejoin authority", Err: errors.New("same-Turn preparation authority unavailable")}
					}
					validate = recovery.ValidatePreparedRejoin
				}
				if authorityErr := validate(ctx, prepared.Run.ID); authorityErr != nil {
					return prepared.Run, &InfrastructureError{Stage: stage, Err: authorityErr}
				}
			} else if !errors.Is(identityErr, run.ErrNotFound) {
				return prepared.Run, &InfrastructureError{Stage: "RuntimePreparation identity", Err: identityErr}
			}
		}
	}
	if err := c.validateRunExecution(context.WithoutCancel(ctx), prepared.Run); err != nil {
		// An unproven handoff grants no authority, including cleanup writes to
		// a potentially different owner or a read-only historical envelope.
		observability.Error(ctx, "runtime", "execution.ownership.rejected", "AgentExecution ownership proof rejected", map[string]any{"errorCode": "AGENT_EXECUTION_UNAVAILABLE", "errorSummary": err.Error()})
		return prepared.Run, &InfrastructureError{Stage: "AgentExecution ownership", Err: errors.Join(ErrAgentExecutionUnproven, err)}
	}
	if ctx.Err() != nil {
		return state.failContext("owned execution start", ctx.Err())
	}
	var tokenUsage *run.TokenUsageObservation
	state.tokenUsage = &tokenUsage
	taskValue, repositoryValue, configValue := prepared.task, prepared.repository, prepared.config
	runValue := prepared.Run
	productReviewRun := prepared.purpose == run.PurposeScopeReview && runValue.ReviewID != ""
	failCoded := state.failCoded
	fail := state.fail
	workspaceProjectID, workspaceAreaID, workspaceBaseCommit := taskValue.ProjectID, taskValue.FeatureAreaID, taskValue.BaseCommit
	if productReviewRun && prepared.review != nil {
		workspaceProjectID, workspaceAreaID, workspaceBaseCommit = prepared.review.ProjectID, prepared.review.FeatureAreaID, prepared.review.BaseCommit
	}
	// A recovered or malformed PreparedRun must not use canonical conversation
	// state to discover that its Workspace is wider than its frozen authority.
	// Membership is only a fail-closed guard here; it never supplies a workset.
	var workspaceValue workspace.Workspace
	var err error
	if prepared.rejoin != nil {
		workspaceValue = prepared.rejoin.Checkpoint.Workspace
	} else if prepared.multiRuntime != nil {
		planner, ok := c.Workspaces.(plannedWorkspaceService)
		if !ok {
			return failCoded(run.FailureInfrastructure, "exact repository workset", "RUNTIME_WORKSET_PREPARATION_FAILED", errors.New("planned workspace service unavailable"))
		}
		targets := prepared.task.RepositoryTargets
		if productReviewRun && prepared.review != nil {
			targets = prepared.review.RepositoryTargets
		}
		workspaceValue, err = planner.EnsurePlannedMembers(ctx, workspaceProjectID, workspaceAreaID, prepared.multiRuntime.Workset.WorkspaceID, targets)
	} else {
		workspaceValue, err = c.Workspaces.EnsureForFeatureArea(ctx, workspaceProjectID, repositoryValue.ID, workspaceAreaID, workspaceBaseCommit)
	}
	if err != nil {
		return fail(run.FailureInfrastructure, "workspace preparation", err)
	}
	if len(workspaceValue.Members) > 1 && prepared.multiRuntime == nil {
		return failCoded(run.FailureInfrastructure, "exact repository workset", "RUNTIME_WORKSET_INCOMPLETE", errors.New("multi-member workspace has no frozen RuntimeWorkset handoff"))
	}
	var conversation agentconversation.Conversation
	conversationAdvance := agentconversation.NoAdvance
	conversationExitTurnID := ""
	conversationCanceled := false
	// Before a Codex session exists no backend turn can be outstanding.
	conversationQuiesced := true
	if runValue.FeatureAreaID != "" && c.Conversations == nil {
		return failCoded(run.FailureInfrastructure, "Feature Area agent context", "AGENT_CONTEXT_UNAVAILABLE", errors.New("Feature Area AgentConversation runtime is not configured"))
	}
	if c.Conversations != nil {
		var conversationErr error
		if prepared.rejoin != nil {
			conversation = prepared.rejoin.Checkpoint.Conversation
			conversation.BackendThreadID = runValue.ThreadID
		} else {
			conversation, conversationErr = c.Conversations.BeginRun(context.WithoutCancel(ctx), runValue.ID)
		}
		if conversationErr != nil {
			return failCoded(run.FailureInfrastructure, "Feature Area agent context", "AGENT_CONTEXT_UNAVAILABLE", conversationErr)
		}
		if prepared.backendSelection == nil {
			return failCoded(run.FailureInfrastructure, "Feature Area backend continuity", "BACKEND_CONTINUITY_UNAVAILABLE", errors.New("Run has no frozen backend selection"))
		}
		if prepared.rejoin == nil && conversation.BackendThreadID != "" {
			bound := conversation.BackendSelection
			if bound == nil || conversation.BackendSelectionSourceRunID == "" || conversation.BackendSelectionSourceThreadID != conversation.BackendThreadID || bound.ConfigID != prepared.backendSelection.ConfigID || bound.ConfigFingerprint != prepared.backendSelection.ConfigFingerprint {
				return failCoded(run.FailureInfrastructure, "Feature Area backend continuity", "BACKEND_CONTINUITY_CONFLICT", errors.New("the frozen Run backend does not match the canonical Thread backend evidence"))
			}
		}
		recorded := false
		state.beforeTerminal = func() error {
			if recorded {
				return nil
			}
			var finalizationErr error
			if conversationCanceled {
				if recorder, ok := c.Conversations.(interface {
					RecordFinalization(context.Context, string, agentconversation.Finalization) error
				}); ok {
					finalizationErr = recorder.RecordFinalization(context.WithoutCancel(ctx), runValue.ID, agentconversation.Finalization{Advance: conversationAdvance, ExitBoundaryTurnID: conversationExitTurnID, Quiesced: conversationQuiesced, Canceled: true})
				} else {
					finalizationErr = errors.New("cancellation finalization is unavailable")
				}
			} else {
				finalizationErr = c.Conversations.FinishRun(context.WithoutCancel(ctx), runValue.ID, conversationAdvance, conversationExitTurnID, conversationQuiesced)
			}
			if err := finalizationErr; err != nil {
				observability.Error(ctx, "runtime", "agent.finalization.conflict", "Agent finalization intent rejected", map[string]any{"runId": runValue.ID, "conversationId": conversation.ID, "errorSummary": err.Error()})
				return err
			}
			recorded = true
			return nil
		}
		// Best-effort observation only for abnormal exits. Every normal terminal
		// write below first records the intent; RunStore consumes it atomically.
		defer func() {
			if errors.Is(resultErr, ErrAppServerDetached) || errors.Is(resultErr, agentruntime.ErrResponseDispositionUnknown) {
				return
			}
			if finishErr := state.beforeTerminal(); finishErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("record Feature Area agent finalization: %w", finishErr))
			}
		}()
	}
	model, reasoningEffort := taskValue.Model, taskValue.ReasoningEffort
	serviceTier := ""
	agentID, executionPolicy, agentConfigFingerprint := "", "", ""
	if runValue.ExecutionProposalID != "" {
		if c.Executions == nil {
			return failCoded(run.FailureInfrastructure, "Execution runtime configuration", "EXECUTION_RUNTIME_CONFIG_UNAVAILABLE", errors.New("execution lookup unavailable"))
		}
		proposal, proposalErr := c.Executions.Get(ctx, runValue.ExecutionProposalID)
		if proposalErr != nil {
			return failCoded(run.FailureInfrastructure, "Execution runtime configuration", "EXECUTION_RUNTIME_CONFIG_UNAVAILABLE", proposalErr)
		}
		if c.RuntimeConfigProvider == nil {
			return failCoded(run.FailureInfrastructure, "Execution runtime configuration", "EXECUTION_RUNTIME_CONFIG_UNAVAILABLE", errors.New("runtime authority unavailable"))
		}
		if configErr := proposal.RuntimeConfig.ValidateFrozen(c.RuntimeConfigProvider()); configErr != nil {
			return failCoded(run.FailureInfrastructure, "Execution runtime configuration", "EXECUTION_RUNTIME_CONFIG_UNAVAILABLE", configErr)
		}
		model, reasoningEffort, serviceTier = proposal.RuntimeConfig.Model, proposal.RuntimeConfig.ReasoningEffort, proposal.RuntimeConfig.ServiceTier
		agentID, executionPolicy, agentConfigFingerprint = proposal.RuntimeConfig.AgentID, proposal.RuntimeConfig.ExecutionPolicy, proposal.RuntimeConfig.AgentConfigFingerprint
		if executionPolicy == agentpolicy.PolicyCooperativeTrustedV1 && (proposal.RuntimeAdapterIdentity != agentpolicy.CooperativeAdapterIdentity || proposal.RuntimeProtocolIdentity != agentpolicy.CooperativeProtocolIdentity) {
			return failCoded(run.FailureInfrastructure, "cooperative runtime", "COOPERATIVE_POLICY_IDENTITY_MISMATCH", errors.New("frozen Execution does not carry the cooperative adapter/protocol identity"))
		}
	}
	baseCommit := taskValue.BaseCommit
	if prepared.multiRuntime != nil {
		for _, member := range prepared.multiRuntime.Workset.Members {
			if member.MemberID == prepared.multiRuntime.Workset.PrimaryMemberID {
				baseCommit = member.OriginCommit
				break
			}
		}
	}
	if productReviewRun {
		if prepared.review == nil {
			return failCoded(run.FailureInfrastructure, "Review context", "REVIEW_CONTEXT_UNAVAILABLE", errors.New("Review runtime snapshot missing"))
		}
		if c.RuntimeConfigProvider == nil {
			return failCoded(run.FailureInfrastructure, "Review runtime configuration", "REVIEW_RUNTIME_CONFIG_UNAVAILABLE", errors.New("runtime authority unavailable"))
		}
		if configErr := prepared.review.RuntimeConfig.ValidateFrozen(c.RuntimeConfigProvider()); configErr != nil {
			return failCoded(run.FailureInfrastructure, "Review runtime configuration", "REVIEW_RUNTIME_CONFIG_UNAVAILABLE", configErr)
		}
		model, reasoningEffort, baseCommit = prepared.review.RuntimeConfig.Model, prepared.review.RuntimeConfig.ReasoningEffort, prepared.review.BaseCommit
		serviceTier = prepared.review.RuntimeConfig.ServiceTier
		agentID, executionPolicy, agentConfigFingerprint = prepared.review.RuntimeConfig.AgentID, prepared.review.RuntimeConfig.ExecutionPolicy, prepared.review.RuntimeConfig.AgentConfigFingerprint
	}
	// Canonical backend identity includes the frozen agent and policy, not only
	// the historical backend-config fingerprint. Do not send a Codex thread to
	// another agent (or reinterpret a cooperative thread as native recovery).
	continuitySelection := codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: agentConfigFingerprint, AgentID: agentID, ExecutionPolicy: executionPolicy, Model: model, ReasoningEffort: reasoningEffort, ServiceTier: serviceTier}
	if continuityErr := c.validateConversationAgentIdentity(ctx, conversation, runValue, continuitySelection); continuityErr != nil {
		if prepared.rejoin != nil {
			return detachRejectedAgentRejoin(runValue, prepared.rejoin, prepared.rejoin.Client, continuityErr)
		}
		return failCoded(run.FailureInfrastructure, "agent identity continuity", "AGENT_CONTINUITY_UNAVAILABLE", continuityErr)
	}
	if workspaceValue.RuntimeQuarantined || (c.Conversations == nil && workspaceValue.Status != workspace.StatusReady) {
		cause := &WorkspaceContinuityUnavailableError{TaskID: runValue.TaskID, ReviewID: runValue.ReviewID, WorkspaceID: workspaceValue.ID, Reason: "the preserved workspace is isolated after unsafe runtime interruption"}
		return failCoded(run.FailureInfrastructure, "workspace continuity", "workspace_continuity_unavailable", cause)
	}
	if prepared.rejoin == nil {
		runValue, err = c.Runs.AttachWorkspace(ctx, runValue.ID, workspaceValue.ID)
	}
	if err != nil {
		return fail(run.FailureInfrastructure, "attach workspace", err)
	}
	prepared.Run = runValue
	var worktreeValue worktree.Worktree
	if prepared.rejoin != nil {
		worktreeValue = prepared.rejoin.Checkpoint.Worktree
	} else if prepared.multiRuntime != nil {
		getter, ok := c.Worktrees.(worktreeGetter)
		if !ok {
			err = errors.New("worktree manager cannot reload primary multi-runtime member")
		} else {
			for _, member := range prepared.multiRuntime.Workset.Members {
				if member.MemberID == prepared.multiRuntime.Workset.PrimaryMemberID {
					worktreeValue, err = getter.Get(ctx, member.WorktreeID)
					break
				}
			}
		}
	} else {
		worktreeValue, workspaceValue, err = c.workspaceWorktree(ctx, workspaceValue, baseCommit, repositoryValue, runValue)
	}
	if err != nil {
		if contextEnded(ctx, err) {
			return state.failContext("worktree preparation", err)
		}
		return fail(run.FailureInfrastructure, "worktree preparation", err)
	}
	if filepath.Clean(worktreeValue.Path) == filepath.Clean(repositoryValue.Path) {
		return fail(run.FailureInfrastructure, "worktree boundary", errors.New("managed worktree equals source repository"))
	}
	if prepared.scalarRuntime != nil {
		member := prepared.scalarRuntime.Workset.Members[0]
		root, identity, rootErr := multiRuntimeRootIdentity(worktreeValue.Path)
		if rootErr != nil || workspaceValue.ID != prepared.scalarRuntime.Workset.WorkspaceID || worktreeValue.ID != member.WorktreeID || root != member.CanonicalRoot || identity != member.RootFileIdentity {
			return fail(run.FailureInfrastructure, "scalar worktree boundary", errors.Join(rootErr, errors.New("workspace differs from frozen scalar observation")))
		}
		worktreeValue.Path = root
	}
	if prepared.purpose == run.PurposeScopeReview {
		if err := reviewaccess.RepositoryBoundary().ValidatePaths(reviewaccess.OperationScope, prepared.reviewScope.Paths); err != nil {
			return failCoded(run.FailureTask, "Scope Review read scope", string(reviewaccess.FailureScopeForbidden), err)
		}
		reviewBoundary := task.ExecutionScope{Kind: task.ExecutionScopeKind(prepared.reviewScope.Kind), Paths: append([]string(nil), prepared.reviewScope.Paths...)}
		if err := validateReviewScopeBoundary(worktreeValue.Path, reviewBoundary); err != nil {
			return failCoded(run.FailureInfrastructure, "Scope Review read scope", "SCOPE_REVIEW_SCOPE_INVALID", err)
		}
	}
	if prepared.rejoin == nil {
		runValue, err = c.Runs.AttachWorktree(ctx, runValue.ID, worktreeValue.ID)
	}
	if err != nil {
		return fail(run.FailureInfrastructure, "attach worktree", err)
	}
	prepared.Run = runValue
	startSnapshot := ""
	if prepared.rejoin != nil {
		startSnapshot = prepared.rejoin.Checkpoint.StartSnapshot
	} else if prepared.multiRuntime != nil {
		for _, member := range prepared.multiRuntime.Workset.Members {
			if member.MemberID == prepared.multiRuntime.Workset.PrimaryMemberID {
				startSnapshot = member.StartSnapshot
				break
			}
		}
	} else if prepared.scalarRuntime != nil {
		startSnapshot = prepared.scalarRuntime.Workset.Members[0].StartSnapshot
	} else {
		startSnapshot, err = c.Evidence.CaptureSnapshot(ctx, worktreeValue.Path)
	}
	if err != nil {
		return fail(run.FailureInfrastructure, "capture run start snapshot", err)
	}
	if prepared.rejoin == nil {
		runValue, err = c.Runs.AttachStartSnapshot(ctx, runValue.ID, startSnapshot)
	}
	if err != nil {
		return fail(run.FailureInfrastructure, "attach run start snapshot", err)
	}
	prepared.Run = runValue

	var session agent.Session
	if err = c.prepareScalarReviewSandbox(ctx, &prepared, worktreeValue, startSnapshot); err != nil {
		return failCoded(run.FailureInfrastructure, "review sandbox", "TASK_PERMISSION_PREPARATION_FAILED", err)
	}
	if err = c.prepareScalarVerificationSandbox(ctx, &prepared, worktreeValue, startSnapshot); err != nil {
		return failCoded(run.FailureInfrastructure, "verification sandbox", "TASK_PERMISSION_PREPARATION_FAILED", err)
	}
	runValue = prepared.Run
	if prepared.backendSelection != nil {
		freshConfig, configErr := c.CodexConfigs.Get(ctx, prepared.backendSelection.ConfigID)
		if configErr != nil || !freshConfig.Enabled || freshConfig.Fingerprint() != prepared.backendSelection.ConfigFingerprint {
			return failCoded(run.FailureInfrastructure, "Feature Area backend continuity", "BACKEND_CONTINUITY_UNAVAILABLE", errors.Join(configErr, errors.New("the frozen backend changed before client creation")))
		}
		configValue = freshConfig
	}
	cooperative := executionPolicy == agentpolicy.PolicyCooperativeTrustedV1
	cooperativeReview := cooperative && prepared.purpose == run.PurposeScopeReview
	var reviewSources map[string]cooperativeReviewSource
	if cooperativeReview {
		reviewSources, err = captureCooperativeReviewSources(ctx, prepared, worktreeValue.Path)
		if err != nil {
			return failCoded(run.FailureInfrastructure, "review source observation", "REVIEW_SOURCE_OBSERVATION_UNKNOWN", err)
		}
	}
	if cooperative && (prepared.rejoin != nil || prepared.qualifiedExecution != nil) {
		return failCoded(run.FailureInfrastructure, "cooperative runtime", "COOPERATIVE_NATIVE_RECOVERY_UNSUPPORTED", errors.New("cooperative execution cannot adopt native recovery or qualification"))
	}
	authorizationCeiling := codex.AuthorizationWorkspaceWrite
	var qualifiedLaunch codexlaunch.VerifiedExecutable
	if prepared.qualifiedExecution != nil {
		verifier, ok := c.QualifiedRuntime.(qualifiedRuntimeLaunchVerifier)
		if !ok {
			return failCoded(run.FailureInfrastructure, "qualified runtime", "RUNTIME_PLATFORM_QUALIFICATION_MISSING", errors.New("qualified native launch verifier unavailable"))
		}
		qualifiedLaunch, err = verifier.VerifiedExecutionLaunch(ctx, *prepared.qualifiedExecution)
		if err != nil {
			return failCoded(run.FailureInfrastructure, "qualified runtime", "RUNTIME_PLATFORM_IDENTITY_MISMATCH", err)
		}
		if qualifiedLaunch == nil {
			return failCoded(run.FailureInfrastructure, "qualified runtime", "RUNTIME_PLATFORM_QUALIFICATION_MISSING", errors.New("qualified native launch identity unavailable"))
		}
	}
	if prepared.purpose == run.PurposeScopeReview || prepared.purpose == run.PurposeVerification {
		authorizationCeiling = codex.AuthorizationReadOnly
	}
	if productReviewRun || runValue.ExecutionProposalID != "" {
		selected := codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: agentConfigFingerprint, AgentID: agentID, ExecutionPolicy: executionPolicy, Model: model, ReasoningEffort: reasoningEffort, CodexFastMode: serviceTier != "", ServiceTier: serviceTier}
		if c.RuntimeConfigProvider == nil {
			return failCoded(run.FailureInfrastructure, "runtime configuration", "RUNTIME_CONFIG_UNAVAILABLE", errors.New("runtime authority unavailable"))
		}
		if configErr := selected.ValidateFrozen(c.RuntimeConfigProvider()); configErr != nil {
			return failCoded(run.FailureInfrastructure, "runtime configuration", "RUNTIME_CONFIG_UNAVAILABLE", configErr)
		}
	}
	if !cooperative && serviceTier != "" {
		if _, ok := c.Codex.(runtimeSelectionAwareCodexFactory); !ok {
			return failCoded(run.FailureInfrastructure, "runtime configuration", "RUNTIME_SELECTION_UNSUPPORTED", errors.New("runtime factory cannot honor frozen service tier"))
		}
	}

	var factory CodexFactory
	var requiredCapabilities []agent.Capability
	if cooperative {
		session, err = c.startCooperativeAgent(ctx, prepared, configValue, codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: agentConfigFingerprint, AgentID: agentID, ExecutionPolicy: executionPolicy, Model: model, ReasoningEffort: reasoningEffort, ServiceTier: serviceTier}, worktreeValue.Path, conversation.BackendThreadID)
	} else {
		// Require declared capabilities before creating a transport. This does not
		// replace immutable admission, concrete method checks, or runtime proofs.
		requiredCapabilities = []agent.Capability{}
		if authorizationCeiling == codex.AuthorizationReadOnly {
			requiredCapabilities = append(requiredCapabilities, agent.CapabilityAuthorizationCeiling)
		}
		if serviceTier != "" {
			requiredCapabilities = append(requiredCapabilities, agent.CapabilityServiceTier)
		}
		if qualifiedLaunch != nil {
			requiredCapabilities = append(requiredCapabilities, agent.CapabilityQualifiedLaunch)
		}
		if prepared.multiRuntime != nil || prepared.scalarRuntime != nil {
			requiredCapabilities = append(requiredCapabilities, agent.CapabilityRuntimeBinding)
		}
		if c.Conversations != nil {
			requiredCapabilities = append(requiredCapabilities, agent.CapabilityHostTools)
		}
		if c.Cancellations != nil {
			requiredCapabilities = append(requiredCapabilities, agent.CapabilityInterrupt)
			if prepared.rejoin == nil {
				requiredCapabilities = append(requiredCapabilities, agent.CapabilityTurnStarted)
			}
		}
		if prepared.rejoin != nil {
			requiredCapabilities = append(requiredCapabilities, agent.CapabilitySameTurnRecovery)
		} else if conversation.BackendThreadID != "" {
			requiredCapabilities = append(requiredCapabilities, agent.CapabilityThreadResume)
		}
		var selectErr error
		factory, selectErr = c.selectAgentFactory(prepared.review, requiredCapabilities)
		if selectErr != nil {
			if prepared.rejoin != nil {
				return detachRejectedAgentRejoin(runValue, prepared.rejoin, prepared.rejoin.Client, selectErr)
			}
			if prepared.review != nil && prepared.review.ExecutorKind == review.ExecutorPiAgent && c.PiAgent == nil {
				return failCoded(run.FailureInfrastructure, "PI Agent runtime", "PI_AGENT_UNAVAILABLE", errors.New("PI Agent factory is not configured"))
			}
			return failCoded(run.FailureInfrastructure, "agent capability selection", "BACKEND_CAPABILITY_INSUFFICIENT", selectErr)
		}

		if qualifiedLaunch != nil {
			factoryImpl, ok := factory.(qualifiedRunCodexFactory)
			if !ok {
				return failCoded(run.FailureInfrastructure, "qualified runtime", "RUNTIME_SELECTION_UNSUPPORTED", errors.New("runtime factory cannot honor the verified native executable"))
			}
			checkpoint := newExecutionCheckpoint(prepared, workspaceValue, worktreeValue, conversation, startSnapshot)
			session, err = factoryImpl.StartForQualifiedRun(ctx, runValue.ID, configValue, model, reasoningEffort, serviceTier, authorizationCeiling, qualifiedLaunch, checkpoint, prepared.rejoin)
		} else if continuity, ok := factory.(boundRunCodexFactory); ok && (continuity.ContinuityEnabled() || prepared.rejoin != nil) {
			checkpoint := newExecutionCheckpoint(prepared, workspaceValue, worktreeValue, conversation, startSnapshot)
			session, err = continuity.StartForBoundRun(ctx, runValue.ID, configValue, model, reasoningEffort, serviceTier, authorizationCeiling, checkpoint, prepared.rejoin)
		} else if taskFactory, ok := factory.(taskRunCodexFactory); ok {
			checkpoint := newExecutionCheckpoint(prepared, workspaceValue, worktreeValue, conversation, startSnapshot)
			session, err = taskFactory.StartForTaskRun(ctx, runValue.ID, configValue, model, reasoningEffort, serviceTier, authorizationCeiling, checkpoint)
		} else if runtimeAware, ok := factory.(runtimeSelectionAwareCodexFactory); ok {
			session, err = runtimeAware.StartForRuntimeSelection(ctx, configValue, model, reasoningEffort, serviceTier, authorizationCeiling)
		} else if operationAware, ok := factory.(operationAwareCodexFactory); ok {
			session, err = operationAware.StartForOperation(ctx, configValue, model, reasoningEffort, authorizationCeiling)
		} else if modelAware, ok := factory.(modelAwareCodexFactory); ok && model != "" && reasoningEffort != "" {
			session, err = modelAware.StartWithSelection(ctx, configValue, model, reasoningEffort)
		} else {
			session, err = factory.Start(ctx, configValue)
		}
	}
	if err != nil {
		if contextEnded(ctx, err) {
			return state.failContext("start Codex", err)
		}
		if productReviewRun {
			return failCoded(run.FailureInfrastructure, "Review runtime", "REVIEW_RUNTIME_FAILED", err)
		}
		return fail(run.FailureInfrastructure, "start Codex", err)
	}
	if session == nil {
		return failCoded(run.FailureInfrastructure, "agent session", "BACKEND_CAPABILITY_INSUFFICIENT", errors.New("agent factory returned no session"))
	}
	if capabilityErr := validateAgentSessionCapabilities(factory, session, requiredCapabilities); !cooperative && capabilityErr != nil {
		if prepared.rejoin != nil {
			return detachRejectedAgentRejoin(runValue, prepared.rejoin, session, capabilityErr)
		}
		return failCoded(run.FailureInfrastructure, "agent session capabilities", "BACKEND_CAPABILITY_INSUFFICIENT", errors.Join(capabilityErr, session.Close()))
	}
	if accounting, ok := session.(agent.UsageRunBinder); ok {
		accounting.SetUsageRunID(runValue.ID)
	}
	if binding, ok := session.(agent.RuntimeBinder); ok && prepared.multiRuntime != nil {
		binding.SetRuntimeBinding(agent.RuntimeBinding(prepared.multiRuntime.Binding))
	}
	if binding, ok := session.(agent.RuntimeBinder); ok && prepared.scalarRuntime != nil {
		binding.SetRuntimeBinding(agent.RuntimeBinding(prepared.scalarRuntime.Binding))
	}
	if c.Conversations != nil {
		conversationQuiesced = false
	}
	if live != nil {
		live.attachClient(session)
	}
	if configurable, ok := session.(agent.ToolConfigurer); ok {
		var specs []codex.DynamicToolSpec
		if c.Conversations != nil {
			specs = FeatureAreaHostToolV1Specs()
		} else if prepared.purpose == run.PurposeScopeReview && (runValue.ReviewID != "") {
			specs = ReviewDynamicToolSpecs()
		}
		if c.Conversations == nil && prepared.purpose == run.PurposeImplementation && runValue.ExecutionProposalID != "" {
			if lister, ok := c.Tasks.(taskGoalLister); ok {
				if goals, goalErr := lister.ListGoals(ctx, taskValue.ID); goalErr == nil && len(goals) > 0 {
					specs = append(specs, GoalDynamicToolSpecs()...)
				}
			}
		}
		tools := make([]agent.ToolSpec, len(specs))
		for i, spec := range specs {
			tools[i] = agent.ToolSpec(spec)
		}
		configurable.SetDynamicTools(tools)
	}
	closed := false
	var sessionCloseErr error
	closeSession := func() error {
		if closed {
			return sessionCloseErr
		}
		closeErr := session.Close()
		sessionCloseErr = closeErr
		closed = true
		if c.Conversations != nil && closeErr == nil {
			conversationQuiesced = true
		}
		return closeErr
	}
	if state.beforeTerminal != nil {
		record := state.beforeTerminal
		state.beforeTerminal = func() error {
			_ = closeSession()
			return record()
		}
	}
	defer func() {
		if !closed {
			_ = closeSession()
		}
		if live != nil {
			live.detachClient(session)
		}
		if terminalRun(returnedRun.Status) {
			if cleanup, ok := session.(interface{ stopIdleAppServer(context.Context) error }); ok {
				cleanupContext, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if cleanupErr := cleanup.stopIdleAppServer(cleanupContext); cleanupErr != nil {
					observability.Error(ctx, "runtime", "appserver.cleanup.unconfirmed", "Terminal App Server cleanup could not be confirmed", map[string]any{"runId": runValue.ID, "errorSummary": cleanupErr.Error()})
				}
				cancelCleanup()
			}
		}
	}()

	runtimeCWD := worktreeValue.Path
	if prepared.multiRuntime != nil && prepared.multiRuntime.Workset.TaskPermissions != nil {
		runtimeCWD = prepared.multiRuntime.CWD
	} else if prepared.scalarRuntime != nil && prepared.scalarRuntime.Workset.TaskPermissions != nil {
		runtimeCWD = prepared.scalarRuntime.CWD
	}
	// Backend continuity is owned only by AgentConversation.
	threadID := ""
	if c.Conversations != nil {
		threadID = conversation.BackendThreadID
	}
	operationLabel := runValue.OperationID
	if operationLabel == "" {
		operationLabel = runValue.TaskID
	}
	newThread := threadID == ""
	threadStarted := time.Now()
	if prepared.rejoin != nil {
		threadID = runValue.ThreadID
	} else if threadID == "" {
		if c.Conversations != nil {
			if err = c.Conversations.MarkThreadStartPending(context.WithoutCancel(ctx), runValue.ID); err != nil {
				return failCoded(run.FailureInfrastructure, "thread dispatch boundary", "AGENT_CONTEXT_UNAVAILABLE", err)
			}
			conversationAdvance = agentconversation.Unknown
		}
		observability.Info(ctx, "codex", "codex.thread.creation_requested", "Codex thread creation requested", map[string]any{"purpose": prepared.purpose})
		if err := c.validateRunExecutionDispatch(ctx, runValue); err != nil {
			return failCoded(run.FailureInfrastructure, "AgentExecution thread ownership", "AGENT_EXECUTION_UNAVAILABLE", err)
		}
		threadID, err = session.StartThread(ctx, runtimeCWD)
		if err == nil {
			if naming, ok := session.(agent.ThreadNamer); ok {
				if nameErr := naming.SetThreadName(ctx, threadID, executorThreadLabel(taskValue.Title, prepared.purpose)); nameErr != nil {
					c.recordThreadNameFailure(operationLabel, threadID, nameErr)
				}
			} else {
				c.recordThreadNameFailure(operationLabel, threadID, errors.New("Codex gateway does not support thread/name/set"))
			}
			if c.Conversations != nil {
				if prepared.backendSelection == nil {
					err = errors.New("frozen backend selection is unavailable")
				} else {
					err = c.Conversations.BindThreadWithRuntimeProvenance(context.WithoutCancel(ctx), runValue.ID, threadID, model, "FROZEN_RUN_BACKEND_AND_MODEL", *prepared.backendSelection)
				}
				if err == nil {
					// The binding transaction already attached this exact Thread to
					// the source Run. Reflect that durable minimum immediately, then
					// replace it with the canonical stored snapshot before proceeding.
					runValue.ThreadID = threadID
					prepared.Run = runValue
					var persisted run.Run
					persisted, err = c.Runs.Get(context.WithoutCancel(ctx), runValue.ID)
					if err == nil && (!persisted.SameOwner(runValue) || persisted.ThreadID != threadID || persisted.Status != run.StatusPreparing) {
						err = errors.New("atomically bound source Run does not match the requested Thread")
					}
					if err == nil {
						runValue = persisted
						prepared.Run = persisted
						conversationAdvance = agentconversation.NoAdvance
					}
				}
			}
		}
	} else {
		observability.Info(ctx, "codex", "codex.thread.reuse_requested", "Codex thread reuse requested", map[string]any{"threadId": threadID, "purpose": prepared.purpose})
		var baseline *run.TokenCounts
		if c.Conversations != nil {
			baseline = c.conversationUsageBaseline(ctx, conversation)
		}
		resumable, ok := session.(agent.ThreadResumer)
		if !ok {
			err = errors.New("Codex gateway does not support thread/resume")
		}
		var resumed string
		if err == nil {
			dispatchRun := runValue
			dispatchRun.ThreadID = threadID
			if err := c.validateRunExecutionDispatch(ctx, dispatchRun); err != nil {
				return failCoded(run.FailureInfrastructure, "AgentExecution resume ownership", "AGENT_EXECUTION_UNAVAILABLE", err)
			}
			resumed, err = resumable.ResumeThread(ctx, threadID, runtimeCWD, baseline)
		}
		if err == nil && resumed != threadID {
			err = fmt.Errorf("Codex resumed replacement thread %q instead of %q", resumed, threadID)
		}
	}
	if err != nil {
		observability.Error(ctx, "codex", "codex.thread.preparation_failed", "Codex thread preparation failed", map[string]any{"threadId": threadID, "errorCode": "THREAD_CONTINUITY_FAILED", "durationMillis": time.Since(threadStarted).Milliseconds(), "errorSummary": err.Error()})

		if productReviewRun {
			_ = closeSession()
			return failCoded(run.FailureInfrastructure, "Review runtime", "REVIEW_RUNTIME_FAILED", err)
		}
		if c.Conversations == nil {
			_, markErr := c.Workspaces.MarkContinuityUnavailable(context.WithoutCancel(ctx), workspaceValue.ID)
			err = errors.Join(err, markErr)
		}
		_ = closeSession()
		cause := &WorkspaceContinuityUnavailableError{TaskID: taskValue.ID, WorkspaceID: workspaceValue.ID, Reason: err.Error()}
		return failCoded(run.FailureInfrastructure, "Codex thread continuity", "workspace_continuity_unavailable", cause)
	}
	threadEvent := "codex.thread.reused"
	if newThread {
		threadEvent = "codex.thread.created"
	}
	observability.Info(ctx, "codex", threadEvent, "Codex thread ready", map[string]any{"threadId": threadID, "durationMillis": time.Since(threadStarted).Milliseconds()})
	if prepared.rejoin == nil && (!newThread || c.Conversations == nil) {
		runValue, err = c.Runs.AttachThread(ctx, runValue.ID, threadID)
		if err != nil {
			if c.Conversations == nil {
				_, _ = c.Workspaces.MarkContinuityUnavailable(context.WithoutCancel(ctx), workspaceValue.ID)
			}
			_ = closeSession()
			return fail(run.FailureInfrastructure, "attach Codex thread", err)
		}
		prepared.Run = runValue
	}
	if prepared.rejoin == nil {
		runValue, err = c.Runs.Transition(ctx, runValue.ID, run.StatusRunning)
	}
	if err != nil {
		_ = closeSession()
		return fail(run.FailureInfrastructure, "run execution state", err)
	}
	prepared.Run = runValue
	if prepared.rejoin == nil && prepared.purpose == run.PurposeImplementation && runValue.ExecutionProposalID != "" && c.Goals != nil {
		if _, goalErr := c.Goals.Initialize(ctx, runValue.ExecutionProposalID); goalErr != nil {
			_ = closeSession()
			return fail(run.FailureInfrastructure, "goal progress initialization", goalErr)
		}
	}

	turnContext, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()
	var approvalDone chan error
	var unregisterApprovals func()
	approvalCleaned := false
	defer func() {
		if approvalDone != nil && !approvalCleaned {
			cancelTurn()
			<-approvalDone
			unregisterApprovals()
		}
	}()
	if interactive, ok := session.(InteractiveCodexSession); ok {
		if c.Approvals == nil {
			_ = closeSession()
			return failCoded(run.FailureInfrastructure, "approval channel", "approval_controller_missing", errors.New("managed interactive Codex session has no approval controller"))
		}
		unregisterApprovals, err = c.Approvals.Register(runValue.ID, interactive, cancelTurn)
		if err != nil {
			_ = closeSession()
			return failCoded(run.FailureInfrastructure, "approval channel", "approval_registration_failed", err)
		}
		approvalDone = make(chan error, 1)
		go c.pumpApprovals(turnContext, runValue.ID, interactive, cancelTurn, approvalDone)
	}
	onTurnStarted := func(turnID string) error {
		turnCtx := observability.WithContext(ctx, observability.Correlation{ReviewID: runValue.ReviewID, RunID: runValue.ID})
		updated, attachErr := c.Runs.AttachTurn(context.WithoutCancel(turnContext), runValue.ID, turnID)
		if attachErr != nil {
			observability.Error(turnCtx, "codex", "codex.turn.identity_binding_failed", "Codex turn could not be bound to durable Run identity", map[string]any{"threadId": threadID, "turnId": turnID, "purpose": prepared.purpose, "errorCode": "CODEX_INTERACTION_IDENTITY_BINDING_FAILED"})
			return attachErr
		}
		runValue = updated
		prepared.Run = updated
		if live != nil {
			live.turnStarted(updated.ID)
		}
		observability.Info(turnCtx, "codex", "codex.turn.started", "Codex turn started with durable interaction identity", map[string]any{"threadId": threadID, "turnId": turnID, "purpose": prepared.purpose})
		return nil
	}
	prompt := ""
	if prepared.purpose != run.PurposeScopeReview {
		prompt = BuildRunPrompt(taskValue, worktreeValue.Path, runValue.ID, repositoryValue.ID, prepared.executionScope, prepared.instruction)
	}
	if prepared.purpose == run.PurposeVerification && prepared.verificationSpec != nil {
		prompt = BuildVerificationRunPrompt(taskValue, worktreeValue.Path, runValue.ID, repositoryValue.ID, prepared.instruction.CodexInstruction, *prepared.verificationSpec)
	}
	if prepared.purpose == run.PurposeScopeReview {
		if runValue.ReviewID == "" {
			prompt = BuildScopeReviewRunPrompt(taskValue, worktreeValue.Path, runValue.ID, repositoryValue.ID, prepared.instruction.CodexInstruction, prepared.reviewScope, runValue.ReviewPlan)
		} else {
			if c.Reviews == nil {
				return failCoded(run.FailureInfrastructure, "Review interaction context", "REVIEW_CONTEXT_UNAVAILABLE", errors.New("durable Review runtime context is unavailable"))
			}
			prompt, err = buildProductReviewPrompt(prepared.instruction.CodexInstruction, runValue.ReviewPlan)
			if err != nil {
				return failCoded(run.FailureInfrastructure, "Review prompt", "REVIEW_CONTEXT_UNAVAILABLE", err)
			}
		}
	}
	naturalResult := prepared.rejoin == nil || runValue.ResultProtocol == run.NaturalResultProtocol
	var outputSchema json.RawMessage
	if naturalResult {
		if prepared.rejoin == nil {
			if recorder, ok := c.Runs.(interface {
				PrepareNaturalResult(context.Context, string) error
			}); ok {
				if err := recorder.PrepareNaturalResult(ctx, runValue.ID); err != nil {
					return failCoded(run.FailureInfrastructure, "execution result contract", "RESULT_CONTRACT_UNAVAILABLE", err)
				}
			} else {
				return failCoded(run.FailureInfrastructure, "execution result contract", "RESULT_CONTRACT_UNAVAILABLE", errors.New("native execution recorder unavailable"))
			}
			runValue.ResultProtocol = run.NaturalResultProtocol
			prepared.Run = runValue
		}
	} else {
		// An already-dispatched historical turn keeps its immutable contract.
		outputSchema = json.RawMessage(ImplementationReportJSONSchema)
		if runValue.ReviewID != "" {
			outputSchema = json.RawMessage(review.ResultJSONSchema)
		} else if prepared.purpose == run.PurposeVerification {
			outputSchema = json.RawMessage(VerificationReportV2JSONSchema)
		}
	}
	if prepared.rejoin == nil {
		promptSentAt := time.Now().UTC()
		if promptRuns, ok := c.Runs.(promptRunService); ok {
			if _, err = promptRuns.RecordPrompt(context.WithoutCancel(ctx), runValue.ID, prompt, promptSentAt); err != nil {
				return failCoded(run.FailureInfrastructure, "prompt persistence", "PROMPT_PERSIST_FAILED", err)
			}
		}
		if productReviewRun {
			if _, err = c.Reviews.RecordPrompt(context.WithoutCancel(ctx), runValue.ReviewID, prompt, promptSentAt); err != nil {
				return failCoded(run.FailureInfrastructure, "Review prompt persistence", "REVIEW_CONTEXT_UNAVAILABLE", err)
			}
		}
		if c.Conversations != nil {
			if err := c.Conversations.MarkTurnPending(context.WithoutCancel(ctx), runValue.ID); err != nil {
				return failCoded(run.FailureInfrastructure, "Feature Area agent context", "AGENT_CONTEXT_UNAVAILABLE", err)
			}
			conversationAdvance = agentconversation.Unknown
		}
	}
	turnRequestedAt := time.Now()
	observability.Info(ctx, "codex", "codex.turn.requested", "Codex turn requested", map[string]any{"threadId": threadID, "purpose": prepared.purpose, "instruction": observability.Fingerprint(prompt)})
	var turnResult agent.TurnResult
	var turnErr error
	if err := c.validateRunExecutionDispatch(ctx, runValue); err != nil {
		return failCoded(run.FailureInfrastructure, "AgentExecution turn ownership", "AGENT_EXECUTION_UNAVAILABLE", err)
	}
	if prepared.rejoin != nil {
		rejoining, ok := session.(rejoiningCodexSession)
		if !ok {
			return runValue, ErrAppServerDetached
		}
		if live != nil {
			live.turnStarted(runValue.ID)
		}
		turnResult, turnErr = rejoining.RejoinTurn(turnContext, runValue.ThreadID, runValue.TurnID, prepared.rejoin.Runtime)
	} else if started, ok := session.(agent.StructuredTurnStarter); ok && len(outputSchema) != 0 {
		turnResult, turnErr = started.StartTurnWithSchemaAndStarted(turnContext, threadID, prompt, outputSchema, onTurnStarted)
	} else if structured, ok := session.(agent.StructuredSession); ok && len(outputSchema) != 0 {
		turnResult, turnErr = structured.StartTurnWithSchema(turnContext, threadID, prompt, outputSchema)
		if turnResult.TurnID != "" {
			turnErr = errors.Join(turnErr, onTurnStarted(turnResult.TurnID))
		}
	} else if len(outputSchema) != 0 {
		return failCoded(run.FailureInfrastructure, "structured output", "BACKEND_CAPABILITY_INSUFFICIENT", errors.New("Coding Agent backend must support output schema"))
	} else if started, ok := session.(agent.TurnStarter); ok {
		turnResult, turnErr = started.StartTurnWithStarted(turnContext, threadID, prompt, onTurnStarted)
	} else {
		turnResult, turnErr = session.StartTurn(turnContext, threadID, prompt)
		if turnResult.TurnID != "" {
			turnErr = errors.Join(turnErr, onTurnStarted(turnResult.TurnID))
		}
	}
	// Capture backend completion before local accounting/evidence failures are
	// joined. Those failures cannot invalidate an already proven context point.
	terminalTurn := turnErr == nil && turnResult.TurnID != ""
	if turnErr != nil {
		terminal := terminalAgentObservation(turnErr)
		terminalTurn = terminal.Matches(threadID, turnResult.TurnID)
		if terminalTurn && terminal.State == agent.TurnInterrupted && c.Cancellations != nil {
			conversationCanceled, err = c.Cancellations.Requested(context.WithoutCancel(ctx), runValue.ID)
			if err != nil {
				return fail(run.FailureInfrastructure, "cancellation observation", err)
			}
		}
	}
	if bound, ok := session.(interface{ independentlyOwned() bool }); ok && bound.independentlyOwned() && !terminalTurn {
		cancellationRequested := false
		if c.Cancellations != nil {
			cancellationRequested, _ = c.Cancellations.Requested(context.WithoutCancel(ctx), runValue.ID)
		}
		if !cancellationRequested {
			c.observeDetach(ctx, runValue, session, turnErr)
			return runValue, errors.Join(ErrAppServerDetached, turnErr)
		}
	}
	if terminalTurn && live != nil {
		live.turnFinished(runValue.ID)
	}
	if canceled, dispositionErr := c.awaitResponseDisposition(ctx, runValue.ID); dispositionErr != nil {
		return runValue, dispositionErr
	} else if canceled && terminalTurn {
		conversationCanceled = true
	}
	if turnErr != nil {
		observability.Error(ctx, "codex", "codex.turn.failed", "Codex turn failed", map[string]any{"threadId": threadID, "turnId": turnResult.TurnID, "durationMillis": time.Since(turnRequestedAt).Milliseconds(), "errorCode": "CODEX_TURN_FAILED", "errorSummary": turnErr.Error()})
	} else {
		observability.Info(ctx, "codex", "codex.turn.completed", "Codex turn completed", map[string]any{"threadId": threadID, "turnId": turnResult.TurnID, "durationMillis": time.Since(turnRequestedAt).Milliseconds(), "agentClaim": observability.Fingerprint(turnResult.Message)})
	}
	tokenUsage = turnResult.TokenUsage
	if prepared.purpose == run.PurposeScopeReview {
		for _, command := range turnResult.Commands {
			logReviewCommand(ctx, command.Command)
			for _, action := range command.Actions {
				logReviewCommand(ctx, action)
			}
		}
	}
	if tokenUsage != nil && tokenUsage.Reliable {
		counts := workspaceTokenCounts(tokenUsage.EndCumulative)
		workspaceValue, err = c.Workspaces.RecordTokenBaseline(context.WithoutCancel(ctx), workspaceValue.ID, &counts)
	} else {
		workspaceValue, err = c.Workspaces.RecordTokenBaseline(context.WithoutCancel(ctx), workspaceValue.ID, nil)
	}
	if err != nil {
		turnErr = errors.Join(turnErr, fmt.Errorf("persist workspace token baseline: %w", err))
	}

	var approvalErr error
	if approvalDone != nil {
		cancelTurn()
		if pumpErr := <-approvalDone; pumpErr != nil {
			approvalErr = errors.Join(approvalErr, pumpErr)
		}
		// Seal and join decision delivery before boundary/terminal finalization.
		// Interruption must also close the transport to unblock resolver waits;
		// an interrupted waiter alone is not evidence that delivery has drained.
		if conversationCanceled {
			_ = closeSession()
		}
		waitErr := c.Approvals.Wait(ctx, runValue.ID)
		if ctx.Err() != nil {
			_ = closeSession()
			waitErr = c.Approvals.Wait(context.WithoutCancel(ctx), runValue.ID)
		}
		if waitErr != nil {
			approvalErr = errors.Join(approvalErr, waitErr)
		}
		approvalCleaned = true
		unregisterApprovals()
	} else {
		cancelTurn()
	}
	closeErr := closeSession()
	if c.Conversations != nil {
		conversationQuiesced = closeErr == nil
		if terminalTurn && closeErr == nil && turnResult.TurnID != "" {
			conversationAdvance = agentconversation.KnownAdvance
			conversationExitTurnID = turnResult.TurnID
		}
	}
	// An interrupted Turn alone does not certify transport/process quiescence.
	if conversationCanceled && (closeErr != nil || !terminalTurn) {
		conversationCanceled = false
		turnErr = errors.Join(turnErr, executioncancel.ErrOutcomeUnknown)
	}

	if state.beforeTerminal != nil {
		if err := state.beforeTerminal(); err != nil {
			return runValue, &InfrastructureError{Stage: "Agent finalization intent", Err: err}
		}
	}
	var multiObservationErr error
	if prepared.multiRuntime != nil {
		multiObservationErr = c.observeMultiRuntime(context.WithoutCancel(ctx), *prepared.multiRuntime, reviewSources)
	}
	endSnapshot, snapshotErr := c.Evidence.CaptureSnapshot(context.WithoutCancel(ctx), worktreeValue.Path)
	verificationMutated := prepared.purpose == run.PurposeVerification && snapshotErr == nil && startSnapshot != endSnapshot
	var reviewObservationErr, reviewStateMutation error
	if cooperativeReview {
		reviewStateMutation, reviewObservationErr = compareCooperativeReviewSources(context.WithoutCancel(ctx), reviewSources)
	}
	reviewMutated := cooperativeReview && ((snapshotErr == nil && startSnapshot != endSnapshot) || reviewStateMutation != nil)
	if snapshotErr == nil {
		runValue, snapshotErr = c.Runs.AttachEndSnapshot(context.WithoutCancel(ctx), runValue.ID, endSnapshot)
		prepared.Run = runValue
	}
	var result run.Result
	if snapshotErr == nil {
		gitEvidence, collectErr := c.Evidence.CollectRun(context.WithoutCancel(ctx), worktreeValue.Path, baseCommit, startSnapshot, endSnapshot)
		if collectErr == nil {
			claimMessage := turnResult.Message
			if prepared.purpose == run.PurposeVerification && !naturalResult {
				claimMessage = ""
			}
			if prepared.purpose == run.PurposeScopeReview && (runValue.ReviewID != "") {
				claimMessage = boundedReviewResult(claimMessage, 64<<10)
			}
			claim := evidence.AgentClaim{Message: claimMessage}
			result = run.Result{AgentClaim: claim, GitEvidence: gitEvidence, Artifacts: evidence.Artifacts(claim, gitEvidence)}
			if c.Results != nil {
				result, collectErr = c.Results.Record(context.WithoutCancel(ctx), runValue.ID, claim, gitEvidence)
			}
		}
		snapshotErr = collectErr
	}
	if (prepared.purpose == run.PurposeVerification || reviewMutated || reviewObservationErr != nil) && snapshotErr == nil {
		resultPurpose := "verification"
		if cooperativeReview {
			resultPurpose = "review"
		}
		if results, ok := c.Runs.(resultRunService); ok {
			updated, attachErr := results.AttachResult(context.WithoutCancel(ctx), runValue.ID, result)
			if attachErr == nil {
				runValue = updated
				prepared.Run = updated
			} else {
				snapshotErr = fmt.Errorf("persist %s Bridge-observed evidence: %w", resultPurpose, attachErr)
			}
		} else {
			snapshotErr = fmt.Errorf("run service does not support pre-terminal %s results", resultPurpose)
		}
	}
	if conversationCanceled {
		return failCoded(run.FailureCanceled, "Execution cancellation", "EXECUTION_CANCELED", errors.Join(ErrRunCanceledByUser, snapshotErr))
	}
	if runValue.RuntimeWorkset != nil && runValue.RuntimeWorkset.TaskPermissions != nil && prepared.purpose != run.PurposeImplementation {
		if copyErr := c.verifyTaskCopies(context.WithoutCancel(ctx), *runValue.RuntimeWorkset); copyErr != nil {
			return failCoded(run.FailureInfrastructure, "verification copy policy", "VERIFICATION_MUTATED_COPY_SOURCE", copyErr)
		}
	}
	if reviewMutated {
		return failCoded(run.FailureInfrastructure, "review mutation policy", "REVIEW_MUTATED_WORKTREE", errors.Join(errors.New("cooperative read-only Review changed the managed source worktree; changes retained for inspection"), reviewStateMutation, reviewObservationErr, snapshotErr))
	}
	if reviewObservationErr != nil {
		return failCoded(run.FailureInfrastructure, "review source observation", "REVIEW_SOURCE_OBSERVATION_UNKNOWN", errors.Join(reviewObservationErr, snapshotErr))
	}
	if verificationMutated {
		return failCoded(run.FailureInfrastructure, "verification mutation policy", "VERIFICATION_MUTATED_WORKTREE", errors.Join(errors.New("Verification Run changed the managed worktree"), snapshotErr))
	}
	if multiObservationErr != nil {
		var observation *MultiRuntimeObservationError
		if errors.As(multiObservationErr, &observation) && observation.State == "SCOPE_CONFLICT" {
			if prepared.purpose == run.PurposeVerification {
				return failCoded(run.FailureInfrastructure, "verification mutation policy", "VERIFICATION_MUTATED_WORKTREE", multiObservationErr)
			}
			return failCoded(run.FailureTask, "exact repository workset", "SCOPE_CONFLICT", multiObservationErr)
		}
		return failCoded(run.FailureInfrastructure, "runtime observation", "RUNTIME_OBSERVATION_UNKNOWN", multiObservationErr)
	}
	if approvalErr != nil {
		var delivery *approval.DeliveryError
		if errors.As(approvalErr, &delivery) {
			return failCoded(run.FailureInfrastructure, "approval", delivery.Code, errors.Join(approvalErr, snapshotErr))
		}
		var persistence *ApprovalPersistenceError
		if errors.As(approvalErr, &persistence) {
			return failCoded(run.FailureInfrastructure, "approval persistence", "approval_persistence_failed", errors.Join(approvalErr, snapshotErr))
		}
		return failCoded(run.FailureInfrastructure, "approval channel", "approval_channel_failed", errors.Join(approvalErr, snapshotErr))
	}
	currentRun, currentErr := c.Runs.Get(context.WithoutCancel(ctx), runValue.ID)
	if currentErr == nil && currentRun.Status == run.StatusCanceled {
		if turnResult.TurnID == "" && c.Conversations == nil && !productReviewRun {
			_, _ = c.Workspaces.MarkContinuityUnavailable(context.WithoutCancel(ctx), workspaceValue.ID)
		}
		return currentRun, context.Canceled
	}
	if currentErr == nil && currentRun.Status == run.StatusInfrastructureFailed {
		message := "approval delivery failed"
		if currentRun.Failure != nil && currentRun.Failure.Message != "" {
			message = currentRun.Failure.Message
		}
		return currentRun, &InfrastructureError{Stage: "approval delivery", Err: errors.New(message)}
	}
	if turnErr != nil {
		interrupted := terminalAgentObservation(turnErr)
		if terminalTurn && interrupted != nil && interrupted.State == agent.TurnInterrupted {
			return failCoded(run.FailureInfrastructure, "Codex turn", run.CodexTurnInterruptedCode, errors.Join(turnErr, snapshotErr, closeErr))
		}
		if contextEnded(ctx, turnErr) {
			if turnResult.TurnID == "" && c.Conversations == nil && !productReviewRun {
				_, _ = c.Workspaces.MarkContinuityUnavailable(context.WithoutCancel(ctx), workspaceValue.ID)
			}
			return state.failContext("Codex turn", errors.Join(turnErr, snapshotErr))
		}
		var taskErr *TaskExecutionError
		if productReviewRun {
			return failCoded(run.FailureInfrastructure, "Review runtime", "REVIEW_RUNTIME_FAILED", errors.Join(turnErr, snapshotErr))
		}

		if errors.As(turnErr, &taskErr) {
			return fail(run.FailureTask, "Codex turn", errors.Join(turnErr, snapshotErr))
		}
		if c.Conversations == nil && !productReviewRun {
			_, _ = c.Workspaces.MarkContinuityUnavailable(context.WithoutCancel(ctx), workspaceValue.ID)
		}
		return fail(run.FailureInfrastructure, "Codex turn", errors.Join(turnErr, snapshotErr))
	}
	if closeErr != nil {
		if c.Conversations == nil && !productReviewRun {
			_, _ = c.Workspaces.MarkContinuityUnavailable(context.WithoutCancel(ctx), workspaceValue.ID)
		}
		if productReviewRun {
			return failCoded(run.FailureInfrastructure, "Review runtime", "REVIEW_RUNTIME_FAILED", errors.Join(closeErr, snapshotErr))
		}

		return fail(run.FailureInfrastructure, "close Codex", errors.Join(closeErr, snapshotErr))
	}
	if snapshotErr != nil {

		return fail(run.FailureInfrastructure, "per-Run evidence", snapshotErr)
	}
	if prepared.purpose == run.PurposeImplementation && !naturalResult {
		report, reportErr := parseImplementationReport(turnResult.Message)
		if reportErr != nil {
			return failCoded(run.FailureInfrastructure, "implementation result", "IMPLEMENTATION_REPORT_INVALID", reportErr)
		}
		if report.ResultDisposition == "SCOPE_CONFLICT" {
			return failCoded(run.FailureTask, "Task scope", "SCOPE_CONFLICT", errors.New(boundedReviewResult(report.Summary, 2048)))
		}
	}
	if prepared.purpose == run.PurposeVerification && !naturalResult {
		if prepared.verificationSpec == nil {
			return failCoded(run.FailureInfrastructure, "verification contract", "VERIFICATION_CONTRACT_INVALID", errors.New("Verification Run has no confirmed VerificationSpec"))
		}
		scope := c.verificationScope(context.WithoutCancel(ctx), runValue, *prepared.verificationSpec, result.GitEvidence)
		report, reportErr := buildVerificationReportWithScope(*prepared.verificationSpec, turnResult.Message, turnResult.Commands, result.GitEvidence, scope)
		if reportErr != nil {
			return failCoded(run.FailureInfrastructure, "verification report", "VERIFICATION_REPORT_INVALID", reportErr)
		}
		updated, attachErr := c.attachVerificationReport(context.WithoutCancel(ctx), runValue.ID, report)
		if attachErr != nil {
			return failCoded(run.FailureInfrastructure, "verification report", "VERIFICATION_REPORT_PERSIST_FAILED", attachErr)
		}
		runValue, prepared.Run = updated, updated
	}
	if prepared.purpose == run.PurposeScopeReview && runValue.ReviewID != "" {
		results, ok := c.Runs.(resultRunService)
		if !ok {
			return failCoded(run.FailureInfrastructure, "Review result", "REVIEW_CONTEXT_UNAVAILABLE", errors.New("run service does not support durable Review results"))
		}
		updated, attachErr := results.AttachResult(context.WithoutCancel(ctx), runValue.ID, result)
		if attachErr != nil {
			return failCoded(run.FailureInfrastructure, "Review result", "REVIEW_CONTEXT_UNAVAILABLE", attachErr)
		}
		runValue = updated
		prepared.Run = updated
	}
	if prepared.purpose == run.PurposeScopeReview && runValue.ReviewID != "" && !naturalResult {
		assessmentData := map[string]any{"agentClaimPresent": strings.TrimSpace(result.AgentClaim.Message) != ""}
		if strings.TrimSpace(result.AgentClaim.Message) == "" {
			assessmentData["errorCode"] = "REVIEW_RESULT_INVALID"
			observability.Info(ctx, "review", "review.completion.assessed", "Review usable result assessed", assessmentData)
			return failCoded(run.FailureTask, "Review result", "REVIEW_RESULT_INVALID", errors.New("Review produced no bounded structurally usable engineering result"))
		}
		observability.Info(ctx, "review", "review.completion.assessed", "Review usable result assessed", assessmentData)
	}
	if naturalResult {
		if recorder, ok := c.Runs.(interface {
			RecordExecutionEvidence(context.Context, string, run.ExecutionEvidence) error
		}); ok {
			captured := run.ExecutionEvidence{Commands: turnResult.Commands}
			if prepared.purpose == run.PurposeVerification && prepared.verificationSpec != nil {
				scope := c.verificationScope(context.WithoutCancel(ctx), runValue, *prepared.verificationSpec, result.GitEvidence)
				for _, check := range prepared.verificationSpec.Checks {
					fact := observeCheck(check, turnResult.Commands, result.GitEvidence)
					if check.RepositoryID != "" || check.WorkingDirectory != "" || len(check.RepositoryIDs) > 0 {
						fact = observeQualifiedCheck(check, turnResult.Commands, scope)
					}
					captured.Checks = append(captured.Checks, fact)
				}
			}
			if err := recorder.RecordExecutionEvidence(context.WithoutCancel(ctx), runValue.ID, captured); err != nil {
				return failCoded(run.FailureInfrastructure, "execution evidence", "EXECUTION_EVIDENCE_PERSIST_FAILED", err)
			}
		} else {
			return failCoded(run.FailureInfrastructure, "execution evidence", "EXECUTION_EVIDENCE_PERSIST_FAILED", errors.New("native execution recorder unavailable"))
		}
	}
	runValue, err = c.Runs.Transition(ctx, runValue.ID, run.StatusCollecting)
	if err != nil {
		if contextEnded(ctx, err) {
			return state.failContext("evidence collection state", err)
		}
		return fail(run.FailureInfrastructure, "evidence collection state", err)
	}
	prepared.Run = runValue
	if state.beforeTerminal != nil {
		if err := state.beforeTerminal(); err != nil {
			return runValue, err
		}
	}
	if usageRuns, ok := c.Runs.(usageAwareRunService); ok {
		runValue, err = usageRuns.SucceedWithUsage(ctx, runValue.ID, result, tokenUsage)
	} else {
		runValue, err = c.Runs.Succeed(ctx, runValue.ID, result)
	}
	if err != nil {
		if contextEnded(ctx, err) {
			return state.failContext("record result", err)
		}
		return fail(run.FailureInfrastructure, "record result", err)
	}
	if prepared.purpose == run.PurposeScopeReview && runValue.ReviewID != "" {
		observability.Info(ctx, "review", "review.succeeded", "Codex Review execution completed; engineering analysis is separate", map[string]any{"resultAvailable": true})
	}
	return runValue, nil
}

func buildProductReviewPrompt(goal string, plan run.ReviewPlan) (string, error) {
	input := review.PromptInput{Instruction: goal, FocusPaths: append([]string(nil), plan.InitialScope.Paths...)}
	for _, selected := range plan.SelectedModules {
		value := review.ModuleSummary{Key: selected.Key}
		if selected.Role == run.ReviewModulePrimary {
			input.Primary = append(input.Primary, value)
		} else if selected.Role == run.ReviewModuleSupporting {
			input.Supporting = append(input.Supporting, value)
		}
	}
	return review.BuildPrompt(input)
}

func boundedReviewResult(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func (c *Coordinator) attachVerificationReport(ctx context.Context, runID string, result run.VerificationReport) (run.Run, error) {
	service, ok := c.Runs.(verificationReportRunService)
	if !ok {
		return run.Run{}, errors.New("run service does not support verification results")
	}
	return service.AttachVerificationReport(ctx, runID, result)
}

func (c *Coordinator) recordThreadNameFailure(taskID, threadID string, cause error) {
	if c.Diagnostics == nil || cause == nil {
		return
	}
	message := cause.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	_ = c.Diagnostics.RecordThreadNameFailure(context.Background(), taskID, threadID, message, time.Now().UTC())
}

func executableTaskStatus(status task.Status) bool {
	return status == task.StatusActive
}

func (c *Coordinator) activeRunForTask(ctx context.Context, taskID, exceptRunID string) string {
	lister, ok := c.Runs.(interface {
		List(context.Context) ([]run.Run, error)
	})
	if !ok {
		return ""
	}
	values, err := lister.List(ctx)
	if err != nil {
		return ""
	}
	for _, value := range values {
		if value.TaskID == taskID && value.ID != exceptRunID && !terminalRun(value.Status) && value.Status != run.StatusCreated {
			return value.ID
		}
	}
	return ""
}

func (c *Coordinator) failReservedContext(ctx context.Context, prepared PreparedRun, stage string, cause error) (PreparedRun, error) {
	kind := run.FailureCanceled
	code := ""
	resultErr := cancellationError(ctx, cause)
	if prepared.Run.WorkflowOwned() {
		kind = run.FailureInfrastructure
		code = "WORKFLOW_CONTEXT_INTERRUPTED"
	}
	if errors.Is(context.Cause(ctx), ErrBridgeShutdownInterrupted) {
		kind = run.FailureInfrastructure
		code = BridgeShutdownInterruptedCode
		resultErr = ErrBridgeShutdownInterrupted
	}
	failed, persistErr := c.persistReservedFailure(ctx, prepared.Run, run.Failure{Kind: kind, Stage: stage, Code: code, Message: resultErr.Error()})
	prepared.Run = failed
	if persistErr != nil {
		return prepared, errors.Join(resultErr, persistErr)
	}
	if kind == run.FailureInfrastructure {
		return prepared, &InfrastructureError{Stage: stage, Err: resultErr}
	}
	return prepared, resultErr
}

func (c *Coordinator) failReservedWithCause(ctx context.Context, prepared PreparedRun, stage, code string, cause error) (PreparedRun, error) {
	failed, persistErr := c.persistReservedFailure(ctx, prepared.Run, run.Failure{Kind: run.FailureInfrastructure, Stage: stage, Code: code, Message: cause.Error()})
	prepared.Run = failed
	if persistErr != nil {
		return prepared, errors.Join(cause, persistErr)
	}
	return prepared, cause
}

func (c *Coordinator) persistReservedFailure(ctx context.Context, value run.Run, failure run.Failure) (run.Run, error) {
	failed, err := c.Runs.Fail(context.WithoutCancel(ctx), value.ID, failure)
	if err == nil {
		return failed, nil
	}
	current, getErr := c.Runs.Get(context.WithoutCancel(ctx), value.ID)
	if getErr == nil && terminalRun(current.Status) {
		return current, nil
	}
	return value, fmt.Errorf("record reserved run failure: %w", err)
}

type executionState struct {
	coordinator    *Coordinator
	prepared       *PreparedRun
	ctx            context.Context
	tokenUsage     **run.TokenUsageObservation
	beforeTerminal func() error
}

func (s *executionState) fail(kind run.FailureKind, stage string, cause error) (run.Run, error) {
	return s.failCoded(kind, stage, "", cause)
}

func (s *executionState) failContext(stage string, cause error) (run.Run, error) {
	if errors.Is(context.Cause(s.ctx), executioncancel.ErrOutcomeUnknown) {
		return s.failCoded(run.FailureInfrastructure, stage, "EXECUTION_CANCEL_OUTCOME_UNKNOWN", executioncancel.ErrOutcomeUnknown)
	}
	if errors.Is(context.Cause(s.ctx), ErrBridgeShutdownInterrupted) {
		return s.failCoded(run.FailureInfrastructure, stage, BridgeShutdownInterruptedCode, ErrBridgeShutdownInterrupted)
	}
	if s.prepared.Run.WorkflowOwned() {
		return s.failCoded(run.FailureInfrastructure, stage, "WORKFLOW_CONTEXT_INTERRUPTED", cancellationError(s.ctx, cause))
	}
	return s.fail(run.FailureCanceled, stage, cancellationError(s.ctx, cause))
}

func (s *executionState) failCoded(kind run.FailureKind, stage, code string, cause error) (run.Run, error) {
	runValue := s.prepared.Run
	if s.beforeTerminal != nil {
		if err := s.beforeTerminal(); err != nil {
			return runValue, errors.Join(cause, fmt.Errorf("record Agent finalization: %w", err))
		}
	}
	var usage *run.TokenUsageObservation
	if s.tokenUsage != nil {
		usage = *s.tokenUsage
	}
	failure := run.Failure{Kind: kind, Stage: stage, Code: code, Message: cause.Error()}
	var failed run.Run
	var transitionErr error
	if usageRuns, ok := s.coordinator.Runs.(usageAwareRunService); ok {
		failed, transitionErr = usageRuns.FailWithUsage(context.WithoutCancel(s.ctx), runValue.ID, failure, usage)
	} else {
		failed, transitionErr = s.coordinator.Runs.Fail(context.WithoutCancel(s.ctx), runValue.ID, failure)
	}
	if transitionErr != nil {
		current, getErr := s.coordinator.Runs.Get(context.WithoutCancel(s.ctx), runValue.ID)
		if getErr == nil && terminalRun(current.Status) {
			s.prepared.Run = current
			if current.Status == run.StatusInfrastructureFailed {
				return current, &InfrastructureError{Stage: stage, Err: cause}
			}
			return current, cause
		}
		return runValue, errors.Join(cause, fmt.Errorf("record run failure: %w", transitionErr))
	}
	s.prepared.Run = failed
	// A Run failure terminalizes the Run/Execution transport only. Development
	// Task acceptance is resolved separately; cancellation and recovery keep
	// their existing dedicated transactions.
	if s.prepared.purpose == run.PurposeScopeReview {
		event := "review.failed"
		if kind == run.FailureInfrastructure || code == "REVIEW_EVIDENCE_INSUFFICIENT" {
			event = "review.blocked"
		}
		observability.Info(s.ctx, "review", event, "Review reached a terminal state", map[string]any{"errorCode": code, "failureKind": kind})
	}
	if kind == run.FailureInfrastructure {
		return failed, &InfrastructureError{Stage: stage, Err: cause}
	}
	return failed, cause
}

func (c *Coordinator) lockTask(taskID string) func() {
	c.mu.Lock()
	if c.taskLocks == nil {
		c.taskLocks = make(map[string]*sync.Mutex)
	}
	lock := c.taskLocks[taskID]
	if lock == nil {
		lock = &sync.Mutex{}
		c.taskLocks[taskID] = lock
	}
	c.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (c *Coordinator) lockUpperReviewPreparation(ctx context.Context, reviewID, runID string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := [2]string{reviewID, runID}
	c.mu.Lock()
	if c.upperReviewPreparations == nil {
		c.upperReviewPreparations = make(map[[2]string]*upperReviewPreparationGate)
	}
	gate := c.upperReviewPreparations[key]
	if gate == nil {
		gate = &upperReviewPreparationGate{available: make(chan struct{}, 1)}
		gate.available <- struct{}{}
		c.upperReviewPreparations[key] = gate
	}
	gate.users++
	c.mu.Unlock()
	release := func(acquired bool) {
		c.mu.Lock()
		if acquired {
			gate.available <- struct{}{}
		}
		gate.users--
		if gate.users == 0 {
			delete(c.upperReviewPreparations, key)
		}
		c.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		release(false)
		return nil, ctx.Err()
	case <-gate.available:
	}
	// Cancellation can race with the predecessor releasing the token.
	if err := ctx.Err(); err != nil {
		release(true)
		return nil, err
	}
	return func() { release(true) }, nil
}

func (c *Coordinator) pumpApprovals(ctx context.Context, runID string, session InteractiveCodexSession, cancel context.CancelFunc, done chan<- error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			done <- fmt.Errorf("approval pump panicked: %v", recovered)
		}
	}()
	for {
		request, err := session.NextApproval(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, codex.ErrClientClosed) {
				done <- nil
				return
			}
			done <- err
			return
		}
		if _, err := c.Approvals.Request(ctx, runID, request); err != nil {
			deliveryContext, deliveryCancel := context.WithTimeout(context.Background(), 5*time.Second)
			deliveryErr := session.ResolveApproval(deliveryContext, request.ID, codex.ApprovalDecision{Type: codex.DecisionDecline})
			deliveryCancel()
			cancel()
			if deliveryErr != nil {
				err = errors.Join(err, fmt.Errorf("safely decline unrecorded approval: %w", deliveryErr))
			}
			done <- &ApprovalPersistenceError{Err: err}
			return
		}
	}
}

func contextEnded(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func cancellationError(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}

func fullCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func terminalRun(status run.Status) bool {
	return status == run.StatusSucceeded || status == run.StatusFailed || status == run.StatusInfrastructureFailed || status == run.StatusCanceled
}

// prepareProductReview carries a direct immutable Review runtime snapshot.
func (c *Coordinator) prepareProductReview(ctx context.Context, prepared PreparedRun) (PreparedRun, error) {
	r := prepared.Run
	if c.Reviews == nil {
		return c.failReservedSetup(ctx, prepared, "Review context", "REVIEW_CONTEXT_UNAVAILABLE", errors.New("Review service unavailable"))
	}
	v, err := c.Reviews.Get(ctx, r.ReviewID)
	if err != nil || v.TaskID == "" || r.TaskID == "" || r.ProjectID != v.ProjectID || r.FeatureAreaID != v.FeatureAreaID || r.OperationKind != run.OperationReview || r.OperationID != v.ID || v.RunID != r.ID {
		return c.failReservedSetup(ctx, prepared, "Review context", "REVIEW_CONTEXT_UNAVAILABLE", errors.Join(err, errors.New("Review ownership mismatch")))
	}
	if strings.TrimSpace(v.Instruction) == "" || r.CodexInstruction != v.Instruction {
		return c.failReservedSetup(ctx, prepared, "Review instruction", "CONTRACT_INCOMPLETE", errors.New("Run instruction differs from exact Review instruction"))
	}
	if len(v.RepositoryTargets) > 0 {
		if v.WorkspaceID == "" || v.PrimaryMemberID == "" {
			return c.failReservedSetup(ctx, prepared, "exact Review workset", "RUNTIME_WORKSET_INCOMPLETE", errors.New("Review lacks an exact workspace or primary member"))
		}
		foundPrimary := false
		for _, target := range v.RepositoryTargets {
			foundPrimary = foundPrimary || target.MemberID == v.PrimaryMemberID
		}
		if !foundPrimary {
			return c.failReservedSetup(ctx, prepared, "exact Review workset", "RUNTIME_WORKSET_INCOMPLETE", errors.New("Review primary member is outside its frozen target set"))
		}
	}
	prepared.review = &v
	if v.BackendSelection == nil || v.BackendSelection.ConfigID == "" || v.BackendSelection.ConfigFingerprint == "" || v.BackendSelection.ConfigID != v.CodexConfigID {
		return c.failReservedSetup(ctx, prepared, "Review backend continuity", "REVIEW_BACKEND_EVIDENCE_UNAVAILABLE", errors.New("Review has no frozen backend selection"))
	}
	selection := *v.BackendSelection
	prepared.backendSelection = &selection
	primaryRepositoryID := v.RepositoryID
	if len(v.RepositoryTargets) > 0 {
		for _, target := range v.RepositoryTargets {
			if target.MemberID == v.PrimaryMemberID {
				primaryRepositoryID = target.RepositoryID
				break
			}
		}
	}
	prepared.repository, err = c.Repositories.Get(ctx, primaryRepositoryID)
	if err != nil || prepared.repository.ProjectID != v.ProjectID {
		return c.failReservedSetup(ctx, prepared, "Review repository", "REVIEW_CONTEXT_UNAVAILABLE", errors.Join(err, errors.New("Review Repository ownership mismatch")))
	}
	prepared.config, err = c.CodexConfigs.Get(ctx, v.CodexConfigID)
	if err != nil || !prepared.config.Enabled || prepared.config.Fingerprint() != v.BackendSelection.ConfigFingerprint || (len(v.RepositoryTargets) == 0 && !fullCommit(v.BaseCommit)) {
		return c.failReservedSetup(ctx, prepared, "Review runtime configuration", "REVIEW_CONTEXT_UNAVAILABLE", errors.Join(err, errors.New("Review immutable runtime configuration unavailable")))
	}
	r, err = c.Runs.Transition(ctx, r.ID, run.StatusPreparing)
	if err != nil {
		return c.failReservedSetup(ctx, prepared, "Review preparation", "REVIEW_RUNTIME_FAILED", err)
	}
	prepared.Run = r
	if len(v.RepositoryTargets) > 0 {
		r, err = c.Runs.AttachWorkspace(ctx, r.ID, v.WorkspaceID)
		if err != nil {
			return c.failReservedSetup(ctx, prepared, "exact Review workset", "RUNTIME_WORKSET_PREPARATION_FAILED", err)
		}
		prepared.Run = r
		input := MultiRuntimePrepareInput{
			Owner:       MultiRuntimeOwner{ProjectID: v.ProjectID, FeatureAreaID: v.FeatureAreaID, ReviewID: v.ID, RunID: r.ID},
			WorkspaceID: v.WorkspaceID, PrimaryMemberID: v.PrimaryMemberID, Targets: v.RepositoryTargets,
			Runtime:  MultiRuntimeIdentity{AgentConfigFingerprint: v.RuntimeConfig.AgentConfigFingerprint, AgentID: v.RuntimeConfig.AgentID, ExecutionPolicy: v.RuntimeConfig.ExecutionPolicy, BackendConfigID: v.BackendSelection.ConfigID, BackendConfigFingerprint: v.BackendSelection.ConfigFingerprint, Model: v.RuntimeConfig.Model, ReasoningEffort: v.RuntimeConfig.ReasoningEffort, ServiceTier: v.RuntimeConfig.ServiceTier, RuntimeAdapterIdentity: executionproposal.RuntimeAdapterCodexAppServerV1, RuntimeProtocolIdentity: executionproposal.RuntimeProtocolCodex0149, CreatedAt: r.CreatedAt},
			ReadOnly: true,
		}
		if v.RuntimeConfig.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 {
			input.Runtime.RuntimeAdapterIdentity = agentpolicy.CooperativeAdapterIdentity
			input.Runtime.RuntimeProtocolIdentity = agentpolicy.CooperativeProtocolIdentity
		}
		if preparation, found, readErr := c.recoveryReviewPreparation(ctx, r.ID); readErr != nil {
			return c.failReservedSetup(ctx, prepared, "Review preparation authority", "RUNTIME_WORKSET_PREPARATION_FAILED", readErr)
		} else if found {
			input = reviewPreparationInput(v, r, preparation)
		}
		multi, multiErr := c.PrepareMultiRuntime(ctx, input)
		if multiErr != nil {
			return c.failReservedSetup(ctx, prepared, "exact Review workset", "RUNTIME_WORKSET_PREPARATION_FAILED", multiErr)
		}
		prepared.multiRuntime = &multi
		prepared.Run, err = c.Runs.Get(ctx, r.ID)
		if err != nil {
			return c.failReservedSetup(ctx, prepared, "exact Review workset", "RUNTIME_WORKSET_PREPARATION_FAILED", err)
		}
	}
	return prepared, nil
}

func (c *Coordinator) frozenTask(ctx context.Context, current task.Task, revisionID string) (task.Task, error) {
	lookup, ok := c.Tasks.(interface {
		GetDefinitionRevision(context.Context, string) (task.DefinitionRevision, error)
	})
	if !ok || revisionID == "" {
		return task.Task{}, errors.New("immutable Task definition unavailable")
	}
	revision, err := lookup.GetDefinitionRevision(ctx, revisionID)
	if err != nil {
		return task.Task{}, err
	}
	if revision.TaskID != current.ID {
		return task.Task{}, errors.New("immutable Task definition owner mismatch")
	}
	return revision.Definition.ApplyTo(current), nil
}

func (c *Coordinator) failReservedSetup(ctx context.Context, prepared PreparedRun, stage, code string, cause error) (PreparedRun, error) {
	failed, persistErr := c.persistReservedFailure(ctx, prepared.Run, run.Failure{Kind: run.FailureInfrastructure, Stage: stage, Code: code, Message: cause.Error()})
	prepared.Run = failed
	if persistErr != nil {
		return prepared, errors.Join(cause, persistErr)
	}
	return prepared, &InfrastructureError{Stage: stage, Err: cause}
}
