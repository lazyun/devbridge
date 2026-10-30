package executionproposal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 100
	MaxOffset        = 1_000_000
	MaxIDBytes       = 128
)

var (
	ErrNotFound                      = errors.New("execution proposal not found")
	ErrConflict                      = errors.New("execution proposal conflict")
	ErrActiveExecution               = fmt.Errorf("task already has an active execution: %w", ErrConflict)
	ErrInvalidArgument               = errors.New("invalid execution proposal argument")
	ErrNoStartRecorded               = fmt.Errorf("execution start effect is NO_START: %w", ErrConflict)
	ErrStartAuthorizationUnavailable = fmt.Errorf("execution start authorization evidence is unavailable: %w", ErrConflict)
	ErrStartEffectConflict           = fmt.Errorf("execution start request conflicts with its recorded effect: %w", ErrConflict)
	validIdentifier                  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

type TransitionError struct {
	From Status
	To   Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("execution proposal cannot transition from %s to %s", e.From, e.To)
}

type Store interface {
	Create(context.Context, Proposal) error
	Get(context.Context, string) (Proposal, error)
	ListBySession(context.Context, string) ([]Proposal, error)
	ListByTask(context.Context, string) ([]Proposal, error)
	Confirm(context.Context, string, time.Time) (Proposal, error)
	Reject(context.Context, string, time.Time) (Proposal, error)
	ConsumeAndCreateRun(context.Context, string, string, time.Time) (Proposal, run.Run, error)
	ReserveVerificationRun(context.Context, string, string, time.Time) (Proposal, run.Run, error)
	Complete(context.Context, string, Outcome, string, string, time.Time) (Proposal, error)
}

func (s *Service) ListByTask(ctx context.Context, taskID string) ([]Proposal, error) {
	store, ok := s.store.(interface {
		ListByTask(context.Context, string) ([]Proposal, error)
	})
	if !ok {
		return nil, errors.New("Task Execution history is unavailable")
	}
	return store.ListByTask(ctx, taskID)
}

type SessionLookup interface {
	Get(context.Context, string) (aisession.Session, error)
}

type TaskPlanLookup interface {
	Get(context.Context, string, string) (taskplan.Plan, error)
}

type TaskLookup interface {
	Get(context.Context, string) (task.Task, error)
}

type Starter interface {
	StartAuthorizedRun(context.Context, string, string, []string, func(context.Context) (run.Run, error)) (run.Run, error)
}

type PlanStarter interface {
	StartAuthorizedPlan(context.Context, string, task.WorkSpec, task.VerificationSpec, func(context.Context) (run.Run, error), func(context.Context) (run.Run, error), func(context.Context, Outcome, string, string) error) (run.Run, error)
}

type VerificationStarter interface {
	StartAuthorizedVerification(context.Context, string, task.VerificationSpec, func(context.Context) (run.Run, error), func(context.Context, Outcome, string, string) error) (run.Run, error)
}

type CreateInput struct {
	AISessionID              string
	TaskPlanID               string
	TaskDefinitionRevisionID string
}

type ListOptions struct {
	Limit  int
	Offset int
}

// StartReceiptIdentity retains only IDs confirmed by the original durable
// start receipt. It does not assert that complete child records were read, a
// runtime handoff succeeded, or any platform qualification was issued.
type StartReceiptIdentity struct {
	ProjectID                string `json:"projectId"`
	TaskID                   string `json:"taskId"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId"`
	OriginalExecutionID      string `json:"originalExecutionId"`
	OriginalRunID            string `json:"originalRunId,omitempty"`
}

type StartResult struct {
	Receipt  *StartReceiptIdentity `json:"receipt,omitempty"`
	Proposal Proposal
	Run      run.Run
	Handoff  Handoff `json:"handoff"`
}

type TaskConfirmationResult struct {
	TaskPlan taskplan.Plan
	Task     task.Task
	Proposal Proposal
}

type taskConfirmationStore interface {
	ConfirmTaskAndCreateExecution(context.Context, Proposal, time.Time) (taskplan.Plan, task.Task, Proposal, error)
}

type Service struct {
	store         Store
	sessions      SessionLookup
	taskPlans     TaskPlanLookup
	tasks         TaskLookup
	starter       Starter
	newProposalID func() (string, error)
	newRunID      func() (string, error)
	now           func() time.Time
}

func NewService(store Store, sessions SessionLookup, taskPlans TaskPlanLookup, tasks TaskLookup, starter Starter, newProposalID, newRunID func() (string, error), now func() time.Time) (*Service, error) {
	if store == nil || sessions == nil || taskPlans == nil || tasks == nil || starter == nil {
		return nil, errors.New("execution proposal dependencies are incomplete")
	}
	if newProposalID == nil {
		newProposalID = func() (string, error) { return randomID("execution_proposal_") }
	}
	if newRunID == nil {
		newRunID = func() (string, error) { return randomID("run_") }
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, sessions: sessions, taskPlans: taskPlans, tasks: tasks, starter: starter, newProposalID: newProposalID, newRunID: newRunID, now: now}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Proposal, error) {
	if !validID(input.AISessionID) || !validID(input.TaskPlanID) {
		return Proposal{}, ErrInvalidArgument
	}
	session, source, taskValue, err := s.resolveSource(ctx, input.AISessionID, input.TaskPlanID)
	if err != nil {
		return Proposal{}, err
	}
	if task.ValidateWorkSpec(source.WorkSpec) != nil {
		return Proposal{}, ErrConflict
	}
	id, err := s.newProposalID()
	if err != nil {
		return Proposal{}, fmt.Errorf("allocate execution proposal id: %w", err)
	}
	now := s.now().UTC()
	value := Proposal{ID: id, AISessionID: session.ID, ProjectID: session.ProjectID, TaskID: taskValue.ID, TaskPlanID: source.ID, TaskDefinitionRevisionID: input.TaskDefinitionRevisionID, WorkSpec: task.CloneWorkSpec(source.WorkSpec), VerificationSpec: task.CloneVerificationSpec(source.VerificationSpec), Status: StatusPendingConfirmation, Outcome: OutcomePending, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Create(ctx, value); err != nil {
		return Proposal{}, err
	}
	stored, err := s.store.Get(ctx, value.ID)
	if err != nil {
		return Proposal{}, err
	}
	logExecutionCreated(ctx, stored, false)
	return clone(stored), nil
}

// ConfirmTask converges one external Task confirmation into the applied
// internal plan and exactly one pending Execution authorization in one local
// transaction. It is safe to repeat after any successful commit.
func (s *Service) ConfirmTask(ctx context.Context, aiSessionID, taskPlanID string) (TaskConfirmationResult, error) {
	if !validID(aiSessionID) || !validID(taskPlanID) {
		return TaskConfirmationResult{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return TaskConfirmationResult{}, mapNotFound(err)
	}
	planValue, err := s.taskPlans.Get(ctx, aiSessionID, taskPlanID)
	if err != nil {
		return TaskConfirmationResult{}, mapNotFound(err)
	}
	taskValue, err := s.tasks.Get(ctx, planValue.TaskID)
	if err != nil {
		return TaskConfirmationResult{}, mapNotFound(err)
	}
	if planValue.ProjectID != session.ProjectID || taskValue.ProjectID != session.ProjectID || task.ValidateWorkSpec(planValue.WorkSpec) != nil || task.ValidateVerificationSpec(planValue.VerificationSpec) != nil {
		return TaskConfirmationResult{}, ErrConflict
	}
	store, ok := s.store.(taskConfirmationStore)
	if !ok {
		return TaskConfirmationResult{}, errors.New("atomic Task confirmation is unavailable")
	}
	// A retry after the durable transaction committed must not depend on a new
	// ID allocation. Return the existing lineage before allocating a candidate
	// used only by the first click.
	if _, resolving := decisionresolution.FromContext(ctx); !resolving {
		existing, err := s.store.ListBySession(ctx, session.ID)
		if err != nil {
			return TaskConfirmationResult{}, err
		}
		for _, proposal := range existing {
			if proposal.TaskPlanID == planValue.ID {
				if planValue.Status != taskplan.StatusApplied || taskValue.ConfirmedTaskPlanID != planValue.ID {
					return TaskConfirmationResult{}, ErrConflict
				}
				logTaskConfirmed(ctx, session.ID, planValue, taskValue, proposal, true)
				return TaskConfirmationResult{TaskPlan: taskplan.Clone(planValue), Task: cloneTask(taskValue), Proposal: clone(proposal)}, nil
			}
		}
	}
	id, err := s.newProposalID()
	if err != nil {
		return TaskConfirmationResult{}, fmt.Errorf("allocate execution proposal id: %w", err)
	}
	candidate := Proposal{ID: id, AISessionID: session.ID, ProjectID: session.ProjectID, TaskID: taskValue.ID, TaskPlanID: planValue.ID}
	confirmedPlan, confirmedTask, proposal, err := store.ConfirmTaskAndCreateExecution(ctx, candidate, s.now().UTC())
	confirmedTask.Constraints = append([]string(nil), confirmedTask.Constraints...)
	confirmedTask.WorkSpec = task.CloneWorkSpec(confirmedTask.WorkSpec)
	confirmedTask.VerificationSpec = task.CloneVerificationSpec(confirmedTask.VerificationSpec)
	if err == nil {
		logTaskConfirmed(ctx, session.ID, confirmedPlan, confirmedTask, proposal, false)
		logExecutionCreated(ctx, proposal, false)
	}
	return TaskConfirmationResult{TaskPlan: taskplan.Clone(confirmedPlan), Task: confirmedTask, Proposal: clone(proposal)}, err
}

func (s *Service) Get(ctx context.Context, aiSessionID, proposalID string) (Proposal, error) {
	if !validID(aiSessionID) || !validID(proposalID) {
		return Proposal{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return Proposal{}, mapNotFound(err)
	}
	value, err := s.store.Get(ctx, proposalID)
	if err != nil {
		return Proposal{}, mapNotFound(err)
	}
	// AISessionID is immutable creation provenance; visibility follows the
	// Project boundary so any authorized session can continue the workflow.
	if value.ProjectID != session.ProjectID {
		return Proposal{}, ErrNotFound
	}
	return clone(value), nil
}

// GetForProject reads immutable Execution history for trusted local operator
// surfaces after they have authorized the owning Project.
func (s *Service) GetForProject(ctx context.Context, projectID, executionID string) (Proposal, error) {
	if !validID(projectID) || !validID(executionID) {
		return Proposal{}, ErrInvalidArgument
	}
	value, err := s.store.Get(ctx, executionID)
	if err != nil {
		return Proposal{}, mapNotFound(err)
	}
	if value.ProjectID != projectID {
		return Proposal{}, ErrNotFound
	}
	return clone(value), nil
}

// HistoryForTask returns exact persisted Execution owners for a trusted Task
// projection. It does not infer an AI session from creation provenance.
func (s *Service) HistoryForTask(ctx context.Context, taskID string) ([]Proposal, error) {
	if !validID(taskID) {
		return nil, ErrInvalidArgument
	}
	owner, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	values, err := s.store.ListByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	result := make([]Proposal, 0, len(values))
	for _, value := range values {
		if value.TaskID == owner.ID && value.ProjectID == owner.ProjectID {
			result = append(result, clone(value))
		}
	}
	return result, nil
}

func (s *Service) List(ctx context.Context, aiSessionID string, options ListOptions) ([]Proposal, error) {
	if !validID(aiSessionID) {
		return nil, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	values, err := s.store.ListBySession(ctx, aiSessionID)
	if err != nil {
		return nil, err
	}
	filtered := make([]Proposal, 0, len(values))
	for _, value := range values {
		if value.AISessionID == session.ID && value.ProjectID == session.ProjectID {
			filtered = append(filtered, clone(value))
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})
	return page(filtered, offset, limit), nil
}

func (s *Service) LatestForTask(ctx context.Context, aiSessionID, taskID string) (Proposal, error) {
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return Proposal{}, mapNotFound(err)
	}
	if store, ok := s.store.(interface {
		LatestByTask(context.Context, string) (Proposal, error)
	}); ok {
		// Executions are durable Project-owned state: any session bound to
		// the Project reads the same Execution lineage.
		taskValue, taskErr := s.tasks.Get(ctx, taskID)
		if taskErr != nil {
			return Proposal{}, mapNotFound(taskErr)
		}
		if taskValue.ProjectID != session.ProjectID {
			return Proposal{}, ErrNotFound
		}
		value, getErr := store.LatestByTask(ctx, taskID)
		return clone(value), getErr
	}
	values, err := s.store.ListBySession(ctx, session.ID)
	if err != nil {
		return Proposal{}, err
	}
	var latest Proposal
	for _, value := range values {
		if value.TaskID != taskID || (latest.ID != "" && (value.CreatedAt.Before(latest.CreatedAt) || value.CreatedAt.Equal(latest.CreatedAt) && value.ID >= latest.ID)) {
			continue
		}
		latest = value
	}
	if latest.ID == "" {
		return Proposal{}, ErrNotFound
	}
	return clone(latest), nil
}

func (s *Service) Confirm(ctx context.Context, aiSessionID, proposalID string) (Proposal, error) {
	current, err := s.Get(ctx, aiSessionID, proposalID)
	if err != nil {
		return Proposal{}, err
	}
	if current.TaskDefinitionRevisionID == "" || current.ProvenanceKind == "LEGACY_UNKNOWN" {
		return Proposal{}, ErrConflict
	}
	value, err := s.store.Confirm(ctx, proposalID, s.now().UTC())
	if err == nil {
		logExecutionAuthorized(ctx, value, false)
	}
	return value, err
}

func (s *Service) Reject(ctx context.Context, aiSessionID, proposalID string) (Proposal, error) {
	if _, err := s.Get(ctx, aiSessionID, proposalID); err != nil {
		return Proposal{}, err
	}
	value, err := s.store.Reject(ctx, proposalID, s.now().UTC())
	if err == nil {
		logExecutionTerminal(ctx, value, "execution.canceled", "EXECUTION_AUTHORIZATION_REJECTED")
	}
	return value, err
}

func (s *Service) Start(ctx context.Context, aiSessionID, proposalID string) (StartResult, error) {
	value, err := s.Get(ctx, aiSessionID, proposalID)
	if err != nil {
		return StartResult{}, err
	}
	if value.Status != StatusConfirmed && value.Status != StatusAdmitted && value.Status != StatusConsumed {
		return StartResult{Proposal: value}, &TransitionError{From: value.Status, To: StatusConsumed}
	}
	store, ok := s.store.(reservedExecutionStore)
	if !ok {
		return StartResult{Proposal: value}, errors.New("durable execution reservation unavailable")
	}
	consumed, reserved, err := store.ReserveExecution(ctx, value.ID, false, s.now().UTC())
	if err != nil {
		return StartResult{Proposal: consumed, Run: reserved}, err
	}
	consumed, reserved, err = s.prepareAdmittedExecution(ctx, consumed, reserved)
	if err != nil {
		return StartResult{Proposal: consumed, Run: reserved}, err
	}
	return s.StartAuthorizedTurn(ctx, aiSessionID, consumed, reserved)
}

func cloneTask(value task.Task) task.Task {
	value.Constraints = append([]string(nil), value.Constraints...)
	value.WorkSpec = task.CloneWorkSpec(value.WorkSpec)
	value.VerificationSpec = task.CloneVerificationSpec(value.VerificationSpec)
	return value
}

// AuthorizeAndStart replays the recorded start effect through the same
// admission and Core preparation boundary as Task-owned starts.
func (s *Service) AuthorizeAndStart(ctx context.Context, aiSessionID, proposalID string) (StartResult, error) {
	value, err := s.Get(ctx, aiSessionID, proposalID)
	if err != nil {
		return StartResult{}, err
	}
	_, source, taskValue, err := s.resolveSource(ctx, aiSessionID, value.TaskPlanID)
	if err == nil {
		for _, contractErr := range []error{task.ValidateWorkSpec(source.WorkSpec), task.ValidateVerificationSpec(source.VerificationSpec)} {
			if errors.Is(contractErr, task.ErrInstructionRequired) {
				return StartResult{Proposal: value}, contractErr
			}
		}
	}
	if err != nil || taskValue.ID != value.TaskID || task.ValidateWorkSpec(source.WorkSpec) != nil || task.ValidateVerificationSpec(source.VerificationSpec) != nil {
		if err == nil {
			err = ErrConflict
		}
		return StartResult{Proposal: value}, err
	}
	store, ok := s.store.(reservedExecutionStore)
	if !ok {
		return StartResult{Proposal: value}, errors.New("durable Execution preparation is unavailable")
	}
	consumed, reserved, err := store.ReserveExecution(ctx, value.ID, false, s.now().UTC())
	if err != nil {
		return StartResult{Proposal: clone(consumed), Run: reserved}, err
	}
	consumed, reserved, err = s.prepareAdmittedExecution(ctx, consumed, reserved)
	if err != nil {
		return StartResult{Proposal: clone(consumed), Run: reserved}, err
	}
	return s.StartAuthorizedTurn(ctx, aiSessionID, consumed, reserved)
}

func (s *Service) prepareAdmittedExecution(ctx context.Context, value Proposal, reserved run.Run) (Proposal, run.Run, error) {
	if value.Status != StatusAdmitted {
		return value, reserved, nil
	}
	if reserved.ID != "" || value.RunID != "" {
		return value, reserved, ErrConflict
	}
	preparation, ok := s.store.(interface {
		PrepareAdmittedExecution(context.Context, string, time.Time) (Proposal, run.Run, error)
	})
	if !ok {
		return value, reserved, errors.New("Core Execution preparation unavailable")
	}
	return preparation.PrepareAdmittedExecution(ctx, value.ID, s.now().UTC())
}

// PrepareAdmittedExecution is the post-admission Core step used by Task start
// orchestration. Session scope is rechecked before consuming the exact plan.
func (s *Service) PrepareAdmittedExecution(ctx context.Context, aiSessionID string, proposal Proposal) (Proposal, run.Run, error) {
	value, err := s.Get(ctx, aiSessionID, proposal.ID)
	if err != nil {
		return Proposal{}, run.Run{}, err
	}
	if value.ID != proposal.ID || value.ProjectID != proposal.ProjectID || value.TaskID != proposal.TaskID {
		return value, run.Run{}, ErrConflict
	}
	if value.Status == StatusConsumed {
		reservations, ok := s.store.(reservedExecutionStore)
		if !ok {
			return value, run.Run{}, errors.New("durable Execution reservation unavailable")
		}
		return reservations.ReserveExecution(ctx, value.ID, false, s.now().UTC())
	}
	return s.prepareAdmittedExecution(ctx, value, run.Run{})
}

// ResumeReserved hands off only a durably authorized Run. Retries never allocate
// another business Run; the runtime supervisor fences duplicate handoffs.
func (s *Service) ResumeReserved(ctx context.Context, aiSessionID string, consumed Proposal, reserved run.Run) (StartResult, error) {
	value, err := s.Get(ctx, aiSessionID, consumed.ID)
	if err != nil {
		return StartResult{}, err
	}
	return s.startAuthorizedTurn(ctx, value, reserved)
}

// ResumeReservedForProject is the trusted local-operator counterpart to
// ResumeReserved. It reads the immutable proposal through its Project boundary
// and then enters the same single dispatcher; no caller is represented as the
// proposal's historical AI Session.
func (s *Service) ResumeReservedForProject(ctx context.Context, projectID string, consumed Proposal, reserved run.Run) (StartResult, error) {
	value, err := s.GetForProject(ctx, projectID, consumed.ID)
	if err != nil {
		return StartResult{}, err
	}
	return s.startAuthorizedTurn(ctx, value, reserved)
}

// StartAuthorizedTurn enters the Core preparation pipeline for a durably
// admitted Execution. The Coordinator records fresh workspace observations and
// binds the exact RuntimeWorkset/StartAuthority before any backend Turn. A
// historical scalar Run can use the original preparation path only when its
// proposal has no v77 approved Workset identity.
func (s *Service) StartAuthorizedTurn(ctx context.Context, aiSessionID string, consumed Proposal, reserved run.Run) (StartResult, error) {
	value, err := s.Get(ctx, aiSessionID, consumed.ID)
	if err != nil {
		return StartResult{}, err
	}
	return s.startAuthorizedTurn(ctx, value, reserved)
}

func (s *Service) StartAuthorizedTurnForProject(ctx context.Context, projectID string, consumed Proposal, reserved run.Run) (StartResult, error) {
	value, err := s.GetForProject(ctx, projectID, consumed.ID)
	if err != nil {
		return StartResult{}, err
	}
	return s.startAuthorizedTurn(ctx, value, reserved)
}

func (s *Service) startAuthorizedTurn(ctx context.Context, value Proposal, reserved run.Run) (StartResult, error) {
	if reserved.ID == "" || reserved.ExecutionProposalID != value.ID || value.Status != StatusConsumed {
		return StartResult{Proposal: value, Run: reserved}, ErrConflict
	}
	if reserved.Purpose == run.PurposeImplementation && value.ApprovedWorksetHash != "" {
		reader, ok := s.store.(interface {
			CanonicalExecutionPreparation(context.Context, string, string) (bool, error)
		})
		if !ok {
			return StartResult{Proposal: value, Run: reserved}, errors.New("canonical Execution preparation unavailable")
		}
		canonical, err := reader.CanonicalExecutionPreparation(ctx, value.ID, reserved.ID)
		if err != nil || !canonical {
			return StartResult{Proposal: value, Run: reserved}, errors.Join(err, ErrConflict)
		}
	}
	return s.resumeReserved(ctx, value, reserved)
}

// resumeReserved is the single dispatcher shared by explicit callers and the
// trusted continuation worker. Durable CAS admits at most one preparation.
func (s *Service) resumeReserved(ctx context.Context, consumed Proposal, reserved run.Run) (StartResult, error) {
	if reserved.ID == "" || consumed.ProjectID != reserved.ProjectID || consumed.TaskID != reserved.TaskID || reserved.ExecutionProposalID != consumed.ID || (reserved.ID != consumed.ImplementationRunID && reserved.ID != consumed.VerificationRunID) {
		return StartResult{}, ErrConflict
	}
	store, ok := s.store.(handoffStore)
	if !ok {
		return StartResult{Proposal: consumed, Run: reserved}, errors.New("durable runtime handoff unavailable")
	}
	receipt, err := store.ReadHandoff(ctx, consumed.ProjectID, reserved.ID)
	result := StartResult{Proposal: clone(consumed), Run: reserved, Handoff: receipt}
	if err != nil {
		return result, err
	}
	logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: consumed.ProjectID, TaskID: consumed.TaskID, ExecutionID: consumed.ID, RunID: reserved.ID})
	reserve := func(context.Context) (run.Run, error) { return reserved, nil }
	reserveVerification := func(reserveCtx context.Context) (run.Run, error) {
		verificationRunID, allocateErr := s.newRunID()
		if allocateErr != nil {
			return run.Run{}, fmt.Errorf("allocate verification run id: %w", allocateErr)
		}
		verificationProposal, verificationRun, reserveErr := s.store.ReserveVerificationRun(reserveCtx, consumed.ID, verificationRunID, s.now().UTC())
		if reserveErr == nil {
			logReservedRunCreated(reserveCtx, verificationProposal, verificationRun)
		}
		return verificationRun, reserveErr
	}
	complete := func(completeCtx context.Context, outcome Outcome, code, message string) error {
		completed, completeErr := s.store.Complete(completeCtx, consumed.ID, outcome, code, message, s.now().UTC())
		if completeErr == nil {
			logExecutionOutcome(completeCtx, completed)
		}
		return completeErr
	}
	// A recovery continuation is a Run instruction overlay, never a mutation of
	// the approved engineering definition. Its immutable receipt is revalidated
	// by both reservation and Coordinator before any model effect.
	runtimeWork := task.CloneWorkSpec(consumed.WorkSpec)
	if reader, ok := s.store.(interface {
		RecoveryContinuationInstruction(context.Context, string) (string, error)
	}); ok && reserved.Purpose == run.PurposeImplementation {
		override, e := reader.RecoveryContinuationInstruction(ctx, consumed.ID)
		if e != nil {
			return result, e
		}
		if override != "" {
			runtimeWork.CodexInstruction = override
		}
	}
	var started run.Run
	observability.Info(logCtx, "execution", "execution.runtime_handoff", "authorized Execution handed to runtime supervisor", map[string]any{"handoffState": HandoffPreparing})
	if reserved.Purpose == run.PurposeVerification {
		verificationStarter, ok := s.starter.(VerificationStarter)
		if !ok {
			return StartResult{Proposal: consumed, Run: reserved}, errors.New("verification starter unavailable")
		}
		started, err = verificationStarter.StartAuthorizedVerification(ctx, consumed.TaskID, consumed.VerificationSpec, reserve, complete)
	} else if plans, planOK := s.starter.(PlanStarter); planOK {
		started, err = plans.StartAuthorizedPlan(ctx, consumed.TaskID, runtimeWork, consumed.VerificationSpec, reserve, reserveVerification, complete)
	} else if consumed.VerificationSpec.Mode == task.VerificationModeRun {
		err = errors.New("execution starter does not support Verification Runs")
	} else {
		started, err = s.starter.StartAuthorizedRun(ctx, consumed.TaskID, runtimeWork.CodexInstruction, nil, reserve)
	}
	if started.ID != "" {
		result.Run = started
	}
	receipt, persistErr := store.ReadHandoff(context.WithoutCancel(ctx), consumed.ProjectID, reserved.ID)
	if persistErr != nil {
		result.Handoff.State = HandoffUnknown
		result.Handoff.Code = "HANDOFF_RECEIPT_READ_FAILED"
		return result, errors.Join(err, persistErr)
	}
	var reported *HandoffError
	if errors.As(err, &reported) && reported.Handoff.State == HandoffUnknown {
		receipt = reported.Handoff
	}
	result.Handoff = receipt
	if !receipt.Accepted() {
		err = errors.Join(err, &HandoffError{Handoff: receipt})
	}

	return result, err
}

func (s *Service) resolveSource(ctx context.Context, aiSessionID, sourceID string) (aisession.Session, taskplan.Plan, task.Task, error) {
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return aisession.Session{}, taskplan.Plan{}, task.Task{}, mapNotFound(err)
	}
	source, err := s.taskPlans.Get(ctx, aiSessionID, sourceID)
	if err != nil {
		return aisession.Session{}, taskplan.Plan{}, task.Task{}, mapNotFound(err)
	}
	if source.Status != taskplan.StatusApplied || !validID(source.TaskID) {
		return aisession.Session{}, taskplan.Plan{}, task.Task{}, ErrConflict
	}
	if source.ProjectID != session.ProjectID {
		return aisession.Session{}, taskplan.Plan{}, task.Task{}, ErrNotFound
	}
	taskValue, err := s.tasks.Get(ctx, source.TaskID)
	if err != nil {
		return aisession.Session{}, taskplan.Plan{}, task.Task{}, mapNotFound(err)
	}
	if taskValue.ProjectID != session.ProjectID || taskValue.ConfirmedTaskPlanID != source.ID || taskValue.Status == task.StatusPlanning {
		return aisession.Session{}, taskplan.Plan{}, task.Task{}, ErrConflict
	}
	return session, source, taskValue, nil
}

func validID(value string) bool {
	return value != "" && len(value) <= MaxIDBytes && validIdentifier.MatchString(value)
}

func boundedList(options ListOptions) (int, int, error) {
	limit := options.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit || options.Offset < 0 || options.Offset > MaxOffset {
		return 0, 0, ErrInvalidArgument
	}
	return limit, options.Offset, nil
}

func page[T any](values []T, offset, limit int) []T {
	if offset >= len(values) {
		return []T{}
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	return values[offset:end]
}

func mapNotFound(err error) error {
	if errors.Is(err, aisession.ErrNotFound) || errors.Is(err, taskplan.ErrNotFound) || errors.Is(err, task.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func randomID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
