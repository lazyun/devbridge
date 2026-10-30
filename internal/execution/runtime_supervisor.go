package execution

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
)

// RunSession is the live, in-memory ownership record for one managed run. It
// owns the execution context, cancellation, Codex client and join signal.
// Durable run state remains authoritative after the session is gone.
type RunSession struct {
	RunID string
	owner runtimeOwner

	ctx          context.Context
	cancel       context.CancelCauseFunc
	done         chan struct{}
	start        chan struct{}
	turnReady    chan struct{}
	turnTerminal chan struct{}

	mu     sync.RWMutex
	client CodexSession
	result run.Run
	err    error
}

// Done closes only after all execution and approval-pump work for this session
// has stopped and the durable terminal result has been attempted.
func (s *RunSession) Done() <-chan struct{} { return s.done }

// Wait joins this live session. Canceling waitCtx only stops the caller's wait;
// it never cancels the owned run.
func (s *RunSession) Wait(waitCtx context.Context) (run.Run, error) {
	select {
	case <-s.done:
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.result, s.err
	case <-waitCtx.Done():
		return run.Run{}, waitCtx.Err()
	}
}

func (s *RunSession) attachClient(client CodexSession) {
	s.mu.Lock()
	s.client = client
	s.mu.Unlock()
}

func (s *RunSession) detachClient(client CodexSession) {
	s.mu.Lock()
	if s.client == client {
		s.client = nil
	}
	s.mu.Unlock()
}

func (s *RunSession) finish(value run.Run, err error) {
	s.mu.Lock()
	s.result, s.err = value, err
	s.mu.Unlock()
}

func (s *RunSession) setRunID(id string) {
	s.mu.Lock()
	s.RunID = id
	s.mu.Unlock()
}

func (s *RunSession) setRuntimeFence(epoch, runID string) {
	s.mu.Lock()
	s.ctx = runtimeepoch.WithFence(s.ctx, epoch, runID)
	s.RunID = runID
	s.turnReady = make(chan struct{})
	s.turnTerminal = make(chan struct{})
	s.mu.Unlock()
}

func (s *RunSession) context() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ctx
}

func (s *RunSession) currentRunID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.RunID
}

// runtimeOwner identifies the local admission slot without representing a
// Review as a Task. FeatureArea claims remain the durable serial authority.
type runtimeOwner struct {
	TaskID   string
	ReviewID string
}

func (o runtimeOwner) activeRunError(runID string) error {
	if o.ReviewID != "" {
		return fmt.Errorf("%w: Review %s already owns Run %s", agentconversation.ErrBusy, o.ReviewID, runID)
	}
	return &TaskAlreadyHasActiveRunError{TaskID: o.TaskID, RunID: runID}
}

type startReservation struct {
	runID  string
	cancel context.CancelCauseFunc
}

// RuntimeSupervisor owns every live managed execution. It deliberately has no
// queue, retry, priority or task-selection behavior.
type RuntimeSupervisor struct {
	onTerminal  func()
	coordinator *Coordinator
	runs        RunService

	rootContext context.Context
	cancelRoot  context.CancelCauseFunc

	mu              sync.RWMutex
	accepting       bool
	sessions        map[string]*RunSession
	operationOwners map[runtimeOwner]string
	starting        map[runtimeOwner]*startReservation
	startWG         sync.WaitGroup
	cancelWG        sync.WaitGroup

	shutdownOnce sync.Once
	shutdownErr  error
}

func NewRuntimeSupervisor(coordinator *Coordinator) (*RuntimeSupervisor, error) {
	if coordinator == nil || coordinator.Runs == nil {
		return nil, errors.New("runtime supervisor coordinator and run service are required")
	}
	root, cancel := context.WithCancelCause(context.Background())
	return &RuntimeSupervisor{
		coordinator:     coordinator,
		runs:            coordinator.Runs,
		rootContext:     root,
		cancelRoot:      cancel,
		accepting:       true,
		sessions:        make(map[string]*RunSession),
		operationOwners: make(map[runtimeOwner]string),
		starting:        make(map[runtimeOwner]*startReservation),
	}, nil
}

// StartRun returns once durable preparation is complete, unique task ownership
// is registered and the supervisor goroutine acknowledges its owned start. The
// caller's context is used only for this pre-handoff phase.
func (s *RuntimeSupervisor) StartRun(ctx context.Context, taskID string) (run.Run, error) {
	return s.start(ctx, runtimeOwner{TaskID: taskID}, func(handoffContext context.Context) (PreparedRun, error) {
		return s.coordinator.PrepareRun(handoffContext, taskID)
	})
}

// StartAuthorizedRun hands one atomically reserved Run to the supervisor. It
// never asks the coordinator to create a second Run.
func (s *RuntimeSupervisor) StartAuthorizedRun(ctx context.Context, taskID, instructionGoal string, instructionConstraints []string, reserve func(context.Context) (run.Run, error)) (run.Run, error) {
	if strings.TrimSpace(instructionGoal) == "" {
		return run.Run{}, task.ErrInstructionRequired
	}
	if reserve == nil {
		return run.Run{}, errors.New("authorized run reservation is required")
	}
	// Reserving an Execution Run is durable. Reject an already-owned Task
	// before invoking the reservation callback so a duplicate user action
	// cannot leave an unused authoritative Run behind. startWithExecutor keeps
	// the same check while claiming the slot to close the concurrent race.
	if err := s.preflightOwner(runtimeOwner{TaskID: taskID}); err != nil {
		return run.Run{}, err
	}
	reserved, reserveErr := reserve(ctx)
	if reserveErr != nil {
		return reserved, reserveErr
	}
	return s.startReservedWithExecutor(ctx, runtimeOwner{TaskID: taskID}, reserved, func(handoffContext context.Context) (PreparedRun, error) {
		if reserved.TaskID != taskID {
			cause := fmt.Errorf("reserved run task %s does not match authorized task %s", reserved.TaskID, taskID)
			prepared := PreparedRun{Run: reserved}
			return s.coordinator.failReservedSetup(handoffContext, prepared, "authorization handoff", "reserved_task_mismatch", cause)
		}
		return s.coordinator.PrepareReservedRun(handoffContext, reserved.ID, Instruction{CodexInstruction: instructionGoal, Constraints: append([]string(nil), instructionConstraints...)})
	}, nil)
}

func (s *RuntimeSupervisor) preflightOwner(owner runtimeOwner) error {
	if s == nil {
		return errors.New("runtime supervisor is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepting {
		return ErrSupervisorShuttingDown
	}
	if runID, exists := s.operationOwners[owner]; exists {
		return owner.activeRunError(runID)
	}
	if pending, exists := s.starting[owner]; exists {
		return owner.activeRunError(pending.runID)
	}
	return nil
}

// StartReview hands a direct Review-owned Run to the runtime. The durable
// FeatureArea claim serializes it with Development operations.
func (s *RuntimeSupervisor) StartReview(ctx context.Context, reviewID string, reserve func(context.Context) (run.Run, error)) (run.Run, error) {
	if reviewID == "" || reserve == nil {
		return run.Run{}, errors.New("Review reservation required")
	}
	reserved, err := reserve(ctx)
	if err != nil {
		return reserved, err
	}
	return s.StartReservedReview(ctx, reviewID, reserved)
}

// StartReservedReview hands an already admitted Review-owned Run to the
// runtime. The Review, Run, Feature Area claim, request receipt, and initial
// grant were committed atomically before this in-memory handoff begins.
func (s *RuntimeSupervisor) StartReservedReview(ctx context.Context, reviewID string, reserved run.Run) (run.Run, error) {
	if reviewID == "" || reserved.ID == "" || reserved.ReviewID != reviewID || reserved.TaskID == "" || reserved.OperationKind != run.OperationReview || reserved.OperationID != reviewID || reserved.Purpose != run.PurposeScopeReview {
		return reserved, errors.New("admitted Review reservation is invalid")
	}
	return s.startReservedWithExecutor(ctx, runtimeOwner{ReviewID: reviewID}, reserved, func(handoff context.Context) (PreparedRun, error) {
		return s.coordinator.PrepareScopeReview(handoff, reserved.ID)
	}, nil)
}

func (s *RuntimeSupervisor) StartAuthorizedPlan(ctx context.Context, taskID string, work task.WorkSpec, verification task.VerificationSpec, reserveImplementation func(context.Context) (run.Run, error), reserveVerification func(context.Context) (run.Run, error), complete func(context.Context, executionproposal.Outcome, string, string) error) (run.Run, error) {
	if reserveImplementation == nil || reserveVerification == nil || complete == nil {
		return run.Run{}, errors.New("authorized execution plan callbacks are required")
	}
	validateWork := task.ValidateWorkSpec
	if work.ExecutionScope.Kind == task.ExecutionScopeTargets {
		validateWork = task.ValidateTargetWorkSpec
	}
	if err := validateWork(work); err != nil {
		return run.Run{}, fmt.Errorf("invalid authorized WorkSpec: %w", err)
	}
	if err := task.ValidateVerificationSpec(verification); err != nil {
		return run.Run{}, fmt.Errorf("invalid authorized VerificationSpec: %w", err)
	}
	reserved, reserveErr := reserveImplementation(ctx)
	if reserveErr != nil {
		return reserved, reserveErr
	}
	return s.startReservedWithExecutor(ctx, runtimeOwner{TaskID: taskID}, reserved, func(handoffContext context.Context) (PreparedRun, error) {
		if reserved.TaskID != taskID || reserved.Purpose != run.PurposeImplementation {
			cause := fmt.Errorf("reserved run is not the authorized Task implementation")
			prepared := PreparedRun{Run: reserved}
			return s.coordinator.failReservedSetup(handoffContext, prepared, "authorization handoff", "reserved_implementation_mismatch", cause)
		}
		return s.coordinator.PrepareReservedExecution(handoffContext, reserved.ID, Instruction{CodexInstruction: work.CodexInstruction, ExecutionScope: task.CloneWorkSpec(work).ExecutionScope}, nil)
	}, func(session *RunSession, implementation PreparedRun) (run.Run, error) {
		implemented, implementationErr := s.coordinator.ExecuteRun(session.context(), implementation, session)
		if errors.Is(implementationErr, ErrAgentExecutionUnproven) || (errors.Is(implementationErr, ErrAppServerDetached) || errors.Is(implementationErr, agentruntime.ErrResponseDispositionUnknown)) {
			return implemented, implementationErr
		}
		implemented = s.finishCancellation(session.context(), implemented)
		if implementationErr != nil || implemented.Status != run.StatusSucceeded {
			outcome, code, message := executionOutcome(implemented, implementationErr)
			completeErr := complete(context.WithoutCancel(session.context()), outcome, code, message)
			return implemented, errors.Join(implementationErr, completeErr)
		}
		if verification.Mode == task.VerificationModeNone {
			completeErr := complete(context.WithoutCancel(session.context()), executionproposal.OutcomeSucceeded, "", "")
			return implemented, completeErr
		}
		reserved, reserveErr := reserveVerification(session.context())
		if reserveErr != nil {
			completeErr := complete(context.WithoutCancel(session.context()), executionproposal.OutcomeFailed, "VERIFICATION_RESERVATION_FAILED", reserveErr.Error())
			return implemented, errors.Join(reserveErr, completeErr)
		}
		receipt, admitted, handoffErr := s.coordinator.Handoffs.ClaimHandoff(runtimeepoch.WithFence(session.context(), reserved.RuntimeEpoch, reserved.ID), reserved.ProjectID, reserved.ID, time.Now().UTC())
		if handoffErr != nil {
			return implemented, errors.Join(ErrAgentExecutionUnproven, handoffErr)
		}
		if !admitted {
			return implemented, errors.Join(ErrAgentExecutionUnproven, &executionproposal.HandoffError{Handoff: receipt})
		}
		// Reserving verification transfers the durable Feature Area claim. Every
		// subsequent preparation, failure completion, and backend callback must
		// carry the verification Run's immutable fence.
		s.rekeySession(session, implementation.Run.ID, reserved.ID, reserved.RuntimeEpoch)
		prepared, prepareErr := s.coordinator.PrepareReservedExecution(session.context(), reserved.ID, Instruction{CodexInstruction: verification.CodexInstruction}, &verification)
		if prepareErr != nil {
			_, receiptErr := s.coordinator.Handoffs.RecordHandoff(context.WithoutCancel(session.context()), reserved.ProjectID, reserved.ID, executionproposal.HandoffRejected, "VERIFICATION_PREPARATION_FAILED", time.Now().UTC())
			prepareErr = errors.Join(prepareErr, receiptErr)
			outcome, code, message := executionOutcome(prepared.Run, prepareErr)
			completeErr := complete(context.WithoutCancel(session.context()), outcome, code, message)
			return prepared.Run, errors.Join(prepareErr, completeErr)
		}
		if _, receiptErr := s.coordinator.Handoffs.RecordHandoff(context.WithoutCancel(session.context()), reserved.ProjectID, reserved.ID, executionproposal.HandoffAccepted, "SUPERVISOR_ACCEPTED", time.Now().UTC()); receiptErr != nil {
			return prepared.Run, receiptErr
		}
		verified, verificationErr := s.coordinator.ExecuteRun(session.context(), prepared, session)
		if errors.Is(verificationErr, ErrAgentExecutionUnproven) || (errors.Is(verificationErr, ErrAppServerDetached) || errors.Is(verificationErr, agentruntime.ErrResponseDispositionUnknown)) {
			return verified, verificationErr
		}
		verified = s.finishCancellation(session.context(), verified)
		outcome, code, message := executionOutcome(verified, verificationErr)
		if verificationErr == nil && verified.Status == run.StatusSucceeded {
			outcome, code, message = executionproposal.OutcomeSucceeded, "", ""
		}
		completeErr := complete(context.WithoutCancel(session.context()), outcome, code, message)
		return verified, errors.Join(verificationErr, completeErr)
	})
}

// StartAuthorizedVerification executes the first and only Run of a
// REQUEST_REVERIFICATION successor. The durable receipt has already admitted
// the successor; this method never creates or pretends an Implementation Run.
func (s *RuntimeSupervisor) StartAuthorizedVerification(ctx context.Context, taskID string, verification task.VerificationSpec, reserve func(context.Context) (run.Run, error), complete func(context.Context, executionproposal.Outcome, string, string) error) (run.Run, error) {
	if reserve == nil || complete == nil {
		return run.Run{}, errors.New("authorized verification callbacks are required")
	}
	if err := task.ValidateVerificationSpec(verification); err != nil || verification.Mode != task.VerificationModeRun {
		if err == nil {
			err = errors.New("verification-only successor requires RUN mode")
		}
		return run.Run{}, err
	}
	reserved, reserveErr := reserve(ctx)
	if reserveErr != nil {
		return reserved, reserveErr
	}
	return s.startReservedWithExecutor(ctx, runtimeOwner{TaskID: taskID}, reserved, func(handoffContext context.Context) (PreparedRun, error) {
		if reserved.TaskID != taskID || reserved.Purpose != run.PurposeVerification {
			return s.coordinator.failReservedSetup(handoffContext, PreparedRun{Run: reserved}, "authorization handoff", "reserved_verification_mismatch", errors.New("reserved Run is not the authorized Task verification"))
		}
		return s.coordinator.PrepareReservedExecution(handoffContext, reserved.ID, Instruction{CodexInstruction: verification.CodexInstruction}, &verification)
	}, func(session *RunSession, prepared PreparedRun) (run.Run, error) {
		verified, verificationErr := s.coordinator.ExecuteRun(session.context(), prepared, session)
		if errors.Is(verificationErr, ErrAgentExecutionUnproven) || (errors.Is(verificationErr, ErrAppServerDetached) || errors.Is(verificationErr, agentruntime.ErrResponseDispositionUnknown)) {
			return verified, verificationErr
		}
		verified = s.finishCancellation(session.context(), verified)
		outcome, code, message := executionOutcome(verified, verificationErr)
		if verificationErr == nil && verified.Status == run.StatusSucceeded {
			outcome, code, message = executionproposal.OutcomeSucceeded, "", ""
		}
		completeErr := complete(context.WithoutCancel(session.context()), outcome, code, message)
		return verified, errors.Join(verificationErr, completeErr)
	})
}

// startReservedWithExecutor is the only physical handoff of a durable Run.
// SQLite admission happens outside the Supervisor mutex. A losing caller may
// observe the receipt, but cannot prepare, dispatch or terminalize the winner.
func (s *RuntimeSupervisor) startReservedWithExecutor(ctx context.Context, owner runtimeOwner, reserved run.Run, prepare func(context.Context) (PreparedRun, error), executor supervisedExecutor) (run.Run, error) {
	if s == nil || s.coordinator == nil || s.coordinator.Handoffs == nil {
		return reserved, errors.New("runtime handoff store unavailable")
	}
	h, admitted, err := s.coordinator.Handoffs.ClaimHandoff(ctx, reserved.ProjectID, reserved.ID, time.Now().UTC())
	if err != nil {
		return reserved, err
	}
	if !admitted {
		current, getErr := s.runs.Get(ctx, reserved.ID)
		if getErr != nil {
			return reserved, getErr
		}
		if h.Accepted() {
			return current, nil
		}
		return current, &executionproposal.HandoffError{Handoff: h}
	}
	// Publish the exact identity before acquiring a local owner slot.
	started, err := s.startWithExecutor(runtimeepoch.WithFence(ctx, reserved.RuntimeEpoch, reserved.ID), owner, func(handoff context.Context) (PreparedRun, error) { return prepare(handoff) }, executor)
	state, code := executionproposal.HandoffAccepted, "SUPERVISOR_ACCEPTED"
	if err != nil || started.ID != reserved.ID {
		state, code = executionproposal.HandoffRejected, runtimeErrorCode(started, err)
		if err == nil {
			err = errors.New("runtime handoff result identity mismatch")
		}
		if started.ID != "" && started.ID != reserved.ID {
			state, code = executionproposal.HandoffUnknown, "RUNTIME_HANDOFF_IDENTITY_MISMATCH"
		}
		if started.ThreadID != "" || started.TurnID != "" || errors.Is(err, ErrAgentExecutionUnproven) {
			state, code = executionproposal.HandoffUnknown, "RUNTIME_HANDOFF_BACKEND_UNPROVEN"
		}
	}
	// This caller owns the delivery claim and these explicit pre-admission
	// failures prove no backend operation entered. Use canonical finalization.
	if started.ID == "" && (errors.Is(err, ErrSupervisorShuttingDown) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		failed, failErr := s.coordinator.failReservedSetup(context.WithoutCancel(ctx), PreparedRun{Run: reserved}, "runtime handoff", code, err)
		started = failed.Run
		err = errors.Join(err, failErr)
	}
	_, recordErr := s.coordinator.Handoffs.RecordHandoff(context.WithoutCancel(ctx), reserved.ProjectID, reserved.ID, state, code, time.Now().UTC())
	if recordErr != nil {
		h.State, h.Code = executionproposal.HandoffUnknown, "HANDOFF_RECEIPT_WRITE_FAILED"
		err = errors.Join(err, &executionproposal.HandoffError{Handoff: h})
	}
	if started.ID == "" {
		started = reserved
	}
	return started, errors.Join(err, recordErr)
}

func (s *RuntimeSupervisor) start(ctx context.Context, owner runtimeOwner, prepare func(context.Context) (PreparedRun, error)) (run.Run, error) {
	return s.startWithExecutor(ctx, owner, prepare, nil)
}

type supervisedExecutor func(*RunSession, PreparedRun) (run.Run, error)

func (s *RuntimeSupervisor) startWithExecutor(ctx context.Context, owner runtimeOwner, prepare func(context.Context) (PreparedRun, error), executor supervisedExecutor) (run.Run, error) {
	if s == nil || s.coordinator == nil {
		return run.Run{}, errors.New("runtime supervisor is not initialized")
	}
	if ctx == nil {
		return run.Run{}, errors.New("start run context is required")
	}
	if err := ctx.Err(); err != nil {
		return run.Run{}, err
	}
	handoffContext, cancelHandoff := context.WithCancelCause(ctx)
	handoffContext = observability.WithContext(handoffContext, observability.Correlation{TaskID: owner.TaskID, ReviewID: owner.ReviewID})
	observability.Info(handoffContext, "codex", "codex.runtime_handoff.requested", "runtime handoff requested", nil)
	reservation := &startReservation{cancel: cancelHandoff}
	if fence, ok := runtimeepoch.FromContext(ctx); ok {
		reservation.runID = fence.RunID
	}

	for {
		s.mu.Lock()
		if !s.accepting {
			s.mu.Unlock()
			cancelHandoff(nil)
			return run.Run{}, ErrSupervisorShuttingDown
		}
		if runOwner, exists := s.operationOwners[owner]; exists {
			ownerSession := s.sessions[runOwner]
			s.mu.Unlock()
			// Durable terminal state can become visible just before the managed
			// goroutine finishes its synchronous callbacks and releases ownership.
			// Converge on that in-flight cleanup instead of exposing a timing-only
			// active-Run rejection to a legitimate continuation.
			if ownerSession != nil {
				current, getErr := s.runs.Get(ctx, runOwner)
				if getErr == nil && terminalRun(current.Status) {
					select {
					case <-ownerSession.done:
						continue
					case <-ctx.Done():
						cancelHandoff(nil)
						return run.Run{}, ctx.Err()
					}
				}
			}
			cancelHandoff(nil)
			return run.Run{}, owner.activeRunError(runOwner)
		}
		if pending, exists := s.starting[owner]; exists {
			runID := pending.runID
			s.mu.Unlock()
			cancelHandoff(nil)
			return run.Run{}, owner.activeRunError(runID)
		}
		s.starting[owner] = reservation
		s.startWG.Add(1)
		s.mu.Unlock()
		break
	}

	prepared, err := prepare(handoffContext)
	if prepared.Run.ID != "" {
		s.mu.Lock()
		reservation.runID = prepared.Run.ID
		s.mu.Unlock()
	}
	if err != nil {
		failedContext := observability.WithContext(handoffContext, observability.Correlation{RunID: prepared.Run.ID})
		observability.Error(failedContext, "codex", "codex.runtime_handoff.failed", "runtime handoff failed", map[string]any{"errorCode": runtimeErrorCode(prepared.Run, err), "errorSummary": err.Error()})
		s.finishStarting(owner, reservation)
		cancelHandoff(nil)
		return prepared.Run, err
	}

	sessionContext, cancelSession := context.WithCancelCause(s.rootContext)
	sessionContext = observability.WithLogger(sessionContext, observability.FromContext(ctx))
	sessionContext = observability.WithContext(sessionContext, observability.FieldsFromContext(ctx))
	sessionContext = observability.WithContext(sessionContext, observability.Correlation{ProjectID: prepared.Run.ProjectID, FeatureAreaID: prepared.Run.FeatureAreaID, TaskID: prepared.Run.TaskID, ReviewID: prepared.Run.ReviewID, ExecutionID: prepared.Run.AgentExecutionID, RunID: prepared.Run.ID})
	sessionContext = runtimeepoch.WithFence(sessionContext, prepared.Run.RuntimeEpoch, prepared.Run.ID)
	session := &RunSession{
		RunID:        prepared.Run.ID,
		owner:        owner,
		ctx:          sessionContext,
		cancel:       cancelSession,
		done:         make(chan struct{}),
		start:        make(chan struct{}),
		turnReady:    make(chan struct{}),
		turnTerminal: make(chan struct{}),
	}

	s.mu.Lock()
	if !s.accepting {
		delete(s.starting, owner)
		s.mu.Unlock()
		cancelHandoff(nil)
		cancelSession(ErrBridgeShutdownInterrupted)
		failed, executeErr := s.coordinator.failReservedSetup(context.WithoutCancel(handoffContext), prepared, "runtime handoff", "SUPERVISOR_SHUTTING_DOWN", ErrSupervisorShuttingDown)
		value := failed.Run
		s.startWG.Done()
		observability.Error(sessionContext, "codex", "codex.runtime_handoff.failed", "runtime handoff rejected during shutdown", map[string]any{"errorCode": "SUPERVISOR_SHUTTING_DOWN"})
		return value, errors.Join(ErrSupervisorShuttingDown, executeErr)
	}
	delete(s.starting, owner)
	s.sessions[session.RunID] = session
	s.operationOwners[owner] = session.RunID
	s.startWG.Done()
	s.mu.Unlock()
	cancelHandoff(nil)

	go s.execute(session, prepared, executor)
	<-session.start
	observability.Info(sessionContext, "codex", "codex.runtime_handoff.accepted", "runtime handoff accepted by supervisor", map[string]any{"purpose": prepared.Run.Purpose})
	return prepared.Run, nil
}

func runtimeErrorCode(value run.Run, err error) string {
	if value.Failure != nil && value.Failure.Code != "" {
		return value.Failure.Code
	}
	if errors.Is(err, ErrSupervisorShuttingDown) {
		return "SUPERVISOR_SHUTTING_DOWN"
	}
	return "RUNTIME_HANDOFF_FAILED"
}

func (s *RuntimeSupervisor) finishStarting(owner runtimeOwner, reservation *startReservation) {
	s.mu.Lock()
	if s.starting[owner] == reservation {
		delete(s.starting, owner)
	}
	s.startWG.Done()
	s.mu.Unlock()
}

func (s *RuntimeSupervisor) execute(session *RunSession, prepared PreparedRun, executor supervisedExecutor) {
	close(session.start)
	var result run.Run
	var executeErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			cause := fmt.Errorf("runtime session panicked: %v", recovered)
			observability.Error(session.context(), "runtime", "runtime.panic", "managed runtime session panicked", map[string]any{"errorCode": "RUNTIME_SESSION_PANIC", "errorSummary": cause.Error(), "stack": observability.Fingerprint(string(debug.Stack()))})
			currentRunID := session.currentRunID()
			current, getErr := s.runs.Get(context.WithoutCancel(session.context()), currentRunID)
			if getErr != nil {
				result, executeErr = current, errors.Join(cause, fmt.Errorf("load current runtime session Run %s: %w", currentRunID, getErr))
			} else if terminalRun(current.Status) {
				result, executeErr = current, cause
			} else {
				currentPrepared := PreparedRun{Run: current, purpose: current.Purpose}
				if current.ReviewID != "" {
					currentPrepared.review = prepared.review
				} else if s.coordinator.Tasks == nil {
					cause = errors.Join(cause, errors.New("load current runtime session Task: task service is unavailable"))
				} else if taskValue, taskErr := s.coordinator.Tasks.Get(context.WithoutCancel(session.context()), current.TaskID); taskErr == nil {
					currentPrepared.task = taskValue
				} else {
					cause = errors.Join(cause, fmt.Errorf("load current runtime session Task %s: %w", current.TaskID, taskErr))
				}
				state := executionState{coordinator: s.coordinator, prepared: &currentPrepared, ctx: session.context()}
				result, executeErr = state.failCoded(run.FailureInfrastructure, "runtime session", "runtime_session_panic", cause)
			}
		}
		terminal := terminalRun(result.Status)
		if !terminal {
			current, err := s.runs.Get(context.WithoutCancel(session.context()), session.currentRunID())
			if err == nil {
				result = current
				terminal = terminalRun(current.Status)
			}
		}
		if executeErr != nil && !errors.Is(executeErr, ErrAgentExecutionUnproven) && !errors.Is(executeErr, ErrAppServerDetached) && !errors.Is(executeErr, agentruntime.ErrResponseDispositionUnknown) {
			if recovery, ok := s.coordinator.Conversations.(interface {
				RequireRecovery(context.Context, string, string) error
			}); ok {
				if err := recovery.RequireRecovery(context.WithoutCancel(session.context()), session.currentRunID(), executeErr.Error()); err != nil {
					observability.Error(session.context(), "runtime", "agent.recovery.required_record_failed", "Runtime interruption recovery marker could not be recorded", map[string]any{"runId": session.currentRunID(), "errorCode": "RECOVERY_MARKER_PERSIST_FAILED", "errorSummary": err.Error()})
				}
			}
		}
		if !errors.Is(executeErr, ErrAgentExecutionUnproven) && !errors.Is(executeErr, ErrAppServerDetached) && !errors.Is(executeErr, agentruntime.ErrResponseDispositionUnknown) {
			result = s.finishCancellation(session.context(), result)
		}
		session.finish(result, executeErr)
		s.mu.Lock()
		activeRunID := session.currentRunID()
		if s.sessions[activeRunID] == session {
			delete(s.sessions, activeRunID)
		}
		// If terminal persistence itself failed, keep the operation reservation. A
		// second run must not start merely because its first goroutine exited.
		// Restart recovery is responsible for reconciling that durable state.
		if terminal && s.operationOwners[session.owner] == activeRunID {
			delete(s.operationOwners, session.owner)
		}
		wake := s.onTerminal
		s.mu.Unlock()
		close(session.done)
		if wake != nil {
			wake()
		}
	}()
	if executor == nil {
		result, executeErr = s.coordinator.ExecuteRun(session.context(), prepared, session)
	} else {
		result, executeErr = executor(session, prepared)
	}
}

func (s *RuntimeSupervisor) rekeySession(session *RunSession, oldRunID, newRunID, epoch string) {
	s.mu.Lock()
	if s.sessions[oldRunID] == session {
		delete(s.sessions, oldRunID)
	}
	s.sessions[newRunID] = session
	s.operationOwners[session.owner] = newRunID
	session.setRuntimeFence(epoch, newRunID)
	s.mu.Unlock()
}

func executionOutcome(value run.Run, cause error) (executionproposal.Outcome, string, string) {
	outcome := executionproposal.OutcomeFailed
	if value.Status == run.StatusCanceled {
		outcome = executionproposal.OutcomeCanceled
	}
	code := "EXECUTION_FAILED"
	message := "managed execution did not succeed"
	if value.Failure != nil {
		if value.Failure.Code != "" {
			code = value.Failure.Code
		}
		if value.Failure.Message != "" {
			message = value.Failure.Message
		}
	} else if cause != nil {
		message = cause.Error()
	}
	return outcome, code, message
}

func (s *RuntimeSupervisor) GetSession(runID string) (*RunSession, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[runID]
	return session, ok
}

// CancelRun cancels and joins only the selected live run. The session remains
// registered until its terminal persistence and goroutine cleanup finish.
func (s *RuntimeSupervisor) CancelRun(ctx context.Context, runID string) (run.Run, error) {
	if s == nil || s.runs == nil {
		return run.Run{}, errors.New("runtime supervisor is not initialized")
	}
	if ctx == nil {
		return run.Run{}, errors.New("cancel run context is required")
	}
	if err := ctx.Err(); err != nil {
		return run.Run{}, err
	}
	target, err := s.runs.Get(ctx, runID)
	if err != nil {
		return run.Run{}, err
	}
	if target.WorkflowOwned() {
		return target, errors.New("workflow-owned Runs require their authoritative cancellation operation; use execution_cancel for an Execution")
	}
	s.mu.RLock()
	session := s.sessions[runID]
	s.mu.RUnlock()
	if session == nil {
		value, err := s.runs.Get(ctx, runID)
		if err != nil {
			return run.Run{}, err
		}
		if terminalRun(value.Status) {
			return value, nil
		}
		return value, ErrRunSessionNotLive
	}
	session.cancel(ErrRunCanceledByUser)
	select {
	case <-session.done:
		return s.runs.Get(context.WithoutCancel(ctx), runID)
	case <-ctx.Done():
		err := ctx.Err()
		value, getErr := s.runs.Get(context.WithoutCancel(ctx), runID)
		if getErr != nil {
			return run.Run{}, errors.Join(err, getErr)
		}
		return value, err
	}
}

// Shutdown stops handoffs, interrupts in-progress handoffs, cancels every live
// session as infrastructure interruption, and joins all owned goroutines.
func (s *RuntimeSupervisor) Shutdown() error {
	if s == nil {
		return nil
	}
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.accepting = false
		starts := make([]*startReservation, 0, len(s.starting))
		for _, reservation := range s.starting {
			starts = append(starts, reservation)
		}
		sessions := make([]*RunSession, 0, len(s.sessions))
		for _, session := range s.sessions {
			sessions = append(sessions, session)
		}
		s.mu.Unlock()

		for _, reservation := range starts {
			reservation.cancel(ErrBridgeShutdownInterrupted)
		}
		for _, session := range sessions {
			session.cancel(ErrBridgeShutdownInterrupted)
		}
		s.cancelRoot(ErrBridgeShutdownInterrupted)
		s.startWG.Wait()
		for _, session := range sessions {
			<-session.done
		}
		s.cancelWG.Wait()

		s.mu.RLock()
		remaining := len(s.sessions)
		s.mu.RUnlock()
		if remaining != 0 {
			s.shutdownErr = fmt.Errorf("runtime supervisor retained %d sessions after shutdown", remaining)
		}
	})
	return s.shutdownErr
}

// Run return and durable intent are the authority for orphan convergence. The
// physical backend may remain unknown; no process-death fact is inferred.
func (s *RuntimeSupervisor) finishCancellation(ctx context.Context, value run.Run) run.Run {
	if finisher, ok := s.coordinator.Cancellations.(interface {
		FinishInterrupted(context.Context, string) error
	}); ok && value.ID != "" {
		if err := finisher.FinishInterrupted(context.WithoutCancel(ctx), value.ID); err == nil {
			if current, err := s.runs.Get(context.WithoutCancel(ctx), value.ID); err == nil && current.Status != value.Status {
				return current
			}
		}
	}
	return value
}

// SetTerminalWake observes completed owned work; the receiver owns durable
// reconciliation and this callback never runs under the supervisor mutex.
func (s *RuntimeSupervisor) SetTerminalWake(wake func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTerminal = wake
}
