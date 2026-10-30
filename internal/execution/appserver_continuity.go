package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
	"time"
)

// ErrAppServerDetached preserves an independently owned active operation when
// this Core relinquishes only its local transport and executor.
var ErrAppServerDetached = errors.New("Core detached from independently owned App Server")

// AppServerObservationError carries only observed protocol facts. In particular,
// an idle Thread cannot prove that an UNKNOWN semantic answer was consumed.
type AppServerObservationError struct {
	Observation agentruntime.RuntimeObservation
	Err         error
}

func (e *AppServerObservationError) Error() string { return e.Err.Error() }
func (e *AppServerObservationError) Unwrap() error { return e.Err }

type AppServerContinuityStore interface {
	BindAppServer(context.Context, string, appserver.Binding) error
	SaveAppServerCheckpoint(context.Context, string, json.RawMessage) error
	AppServerCheckpoint(context.Context, string) (json.RawMessage, error)
}

type executionCheckpoint struct {
	Version          int
	RunID            string
	MultiRuntime     *PreparedMultiRuntime
	Task             task.Task
	Review           *review.Review
	Repository       repository.Repository
	Config           codexconfig.Config
	BackendSelection *codexconfig.BackendSelection
	Instruction      Instruction
	ExecutionScope   task.ExecutionScope
	Purpose          run.Purpose
	VerificationSpec *task.VerificationSpec
	ReviewScope      run.ReviewScope
	Workspace        workspace.Workspace
	Worktree         worktree.Worktree
	Conversation     agentconversation.Conversation
	StartSnapshot    string
}

func newExecutionCheckpoint(p PreparedRun, w workspace.Workspace, t worktree.Worktree, c agentconversation.Conversation, start string) executionCheckpoint {
	if p.multiRuntime == nil && p.scalarRuntime != nil && p.scalarRuntime.Workset.TaskPermissions != nil {
		p.multiRuntime = p.scalarRuntime
	}
	return executionCheckpoint{Version: 1, RunID: p.Run.ID, MultiRuntime: p.multiRuntime, Task: p.task, Review: p.review, Repository: p.repository, Config: p.config, BackendSelection: p.backendSelection, Instruction: p.instruction, ExecutionScope: p.executionScope, Purpose: p.purpose, VerificationSpec: p.verificationSpec, ReviewScope: p.reviewScope, Workspace: w, Worktree: t, Conversation: c, StartSnapshot: start}
}

type rejoinExecution struct {
	Checkpoint executionCheckpoint
	Binding    appserver.Binding
	Runtime    *agentruntime.Session
	Client     CodexSession
}
type boundRunCodexFactory interface {
	ContinuityEnabled() bool
	StartForBoundRun(context.Context, string, codexconfig.Config, string, string, string, codex.AuthorizationCeiling, executionCheckpoint, *rejoinExecution) (CodexSession, error)
}
type rejoiningCodexSession interface {
	RejoinTurn(context.Context, string, string, *agentruntime.Session) (CodexTurnResult, error)
}

func (f DefaultCodexFactory) ContinuityEnabled() bool {
	return f.ContinuityDirectory != "" && f.AppServers != nil
}
func (f DefaultCodexFactory) StartForBoundRun(ctx context.Context, runID string, selected codexconfig.Config, model, reasoning, tier string, ceiling codex.AuthorizationCeiling, checkpoint executionCheckpoint, rejoin *rejoinExecution) (CodexSession, error) {
	if err := validateCheckpointTaskBinding(checkpoint); err != nil {
		return nil, err
	}
	f = f.withTaskCheckpoint(checkpoint)
	if f.AppServers == nil || (rejoin == nil && !f.ContinuityEnabled()) {
		return nil, errors.New("independent App Server continuity is not configured")
	}
	config := codexLaunchConfig(f.Config, selected)
	verifiedPath := ""
	if config.VerifiedExecutable != nil {
		var err error
		verifiedPath, err = config.VerifiedExecutable.Executable(ctx)
		if err != nil {
			return nil, err
		}
	}
	var binding appserver.Binding
	var err error
	reused := false
	if rejoin != nil {
		if rejoin.Client != nil && verifiedPath == "" {
			return rejoin.Client, nil
		}
		binding = rejoin.Binding
		if binding.BackendConfigID != selected.ID || binding.BackendFingerprint != selected.Fingerprint() {
			return nil, errors.New("App Server backend identity differs from frozen Run")
		}
		if verifiedPath != "" && binding.ExecutablePath != verifiedPath {
			return nil, errors.New("App Server native executable differs from qualified binding")
		}
		if err = appserver.Verify(ctx, binding); err != nil {
			return nil, err
		}
		if rejoin.Client != nil {
			return rejoin.Client, nil
		}
	} else {
		if continuations, ok := f.AppServers.(interface {
			RecoveryContinuationBinding(context.Context, string) (appserver.Binding, error)
		}); ok {
			binding, err = continuations.RecoveryContinuationBinding(ctx, runID)
			if err == nil {
				if binding.BackendConfigID != selected.ID || binding.BackendFingerprint != selected.Fingerprint() {
					return nil, errors.New("recovery continuation backend identity changed")
				}
				if verifiedPath != "" && binding.ExecutablePath != verifiedPath {
					return nil, errors.New("recovery continuation native executable differs from qualified binding")
				}
				if err = appserver.Verify(ctx, binding); err != nil {
					return nil, err
				}
				reused = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		if !reused {
			spec := appserver.Spec{DataRoot: f.ContinuityDirectory, ExecutablePath: config.CodexPath, BackendConfigID: selected.ID, BackendFingerprint: selected.Fingerprint()}
			if f.AppServerSpec != nil {
				spec = f.AppServerSpec(selected)
				spec.DataRoot = f.ContinuityDirectory
				spec.BackendConfigID = selected.ID
				spec.BackendFingerprint = selected.Fingerprint()
			}
			if config.VerifiedExecutable != nil {
				spec.ExecutablePath, err = config.VerifiedExecutable.Executable(ctx)
			} else {
				spec.ExecutablePath, err = appserver.ResolveExecutable(spec.ExecutablePath)
			}
			if err != nil {
				return nil, err
			}
			if task := config.TaskRuntimeBinding; task != nil {
				if task.TaskPermissions == nil {
					return nil, errors.New("TASK_PERMISSION_PROFILE_MISSING")
				}
				args, profileErr := task.TaskPermissions.ProfileArgs(task.RuntimeWorkspaceRoots, task.WritableRoots)
				if profileErr != nil {
					return nil, profileErr
				}
				spec.Args = append(spec.Args, args...)
				spec.TaskPermissionHash = task.TaskPermissions.ContentHash
			}
			binding, err = appserver.Launch(ctx, spec)
			if err != nil {
				return nil, err
			}
		}
		// Durable binding and the frozen executor checkpoint precede all thread and
		// turn effects. A failed boundary can stop only the just-created instance.
		if err = f.AppServers.BindAppServer(ctx, runID, binding); err != nil {
			if !reused {
				_ = appserver.Stop(context.WithoutCancel(ctx), binding)
			}
			return nil, err
		}
		raw, marshalErr := json.Marshal(checkpoint)
		if marshalErr != nil {
			if !reused {
				_ = appserver.Stop(context.WithoutCancel(ctx), binding)
			}
			return nil, marshalErr
		}
		if err = f.AppServers.SaveAppServerCheckpoint(ctx, runID, raw); err != nil {
			if !reused {
				_ = appserver.Stop(context.WithoutCancel(ctx), binding)
			}
			return nil, err
		}
	}
	config.AppServerBinding = &binding
	if b := config.TaskRuntimeBinding; b != nil && binding.TaskPermissionHash != b.TaskPermissions.ContentHash {
		return nil, errors.New("TASK_PERMISSION_PROCESS_BINDING_MISMATCH")
	}
	config.ApprovalMode = codex.ApprovalModeInteractive
	config.AuthorizationCeiling = ceiling
	config.DynamicToolHandler = f.DynamicToolHandler
	client, err := codex.Start(ctx, config)
	if err != nil {
		if rejoin == nil {
			if !reused {
				_ = appserver.Stop(context.WithoutCancel(ctx), binding)
			}
		}
		return nil, err
	}
	return &codexSession{client: client, model: model, reasoningEffort: reasoning, serviceTier: tier, authorizationCeiling: ceiling, independent: true, appServerInstance: binding, idleCleanup: f.cleanupIdleAppServer(binding, config)}, nil
}
func (s codexSession) independentlyOwned() bool             { return s.independent }
func (s codexSession) continuityBinding() appserver.Binding { return s.appServerInstance }
func (s codexSession) RejoinTurn(ctx context.Context, threadID, turnID string, runtime *agentruntime.Session) (CodexTurnResult, error) {
	var tier *string
	if s.serviceTier != "" {
		tier = &s.serviceTier
	}
	result, err := s.client.RejoinTurn(ctx, codex.TurnRejoinOptions{TurnOptions: codex.TurnOptions{RunID: s.usageRunID, ThreadID: threadID, Model: s.model, ReasoningEffort: s.reasoningEffort, ServiceTier: tier, AuthorizationCeiling: s.authorizationCeiling, RuntimeBinding: s.runtimeBinding}, TurnID: turnID, RuntimeSession: runtime, DynamicTools: s.dynamicTools})
	value := CodexTurnResult{TurnID: result.TurnID, Message: result.FinalMessage, Commands: make([]ObservedCommand, 0, len(result.Commands))}
	for _, command := range result.Commands {
		value.Commands = append(value.Commands, ObservedCommand{ItemID: command.ItemID, Command: command.Command, CWD: command.CWD, Status: command.Status, ExitCode: command.ExitCode, Actions: append([]string(nil), command.Actions...), Output: command.Output, OutputTruncated: command.OutputTruncated, DurationMillis: command.DurationMS, StartedAt: command.StartedAt, FinishedAt: command.FinishedAt})
	}
	if err != nil {
		var terminal *codex.TurnError
		if errors.As(err, &terminal) {
			err = &TaskExecutionError{Err: err}
		}
	}
	return value, err
}

// RecoverAppServerRun attaches the canonical executor to an already verified,
// adopted loaded Turn. It neither reserves a Run nor emits a prompt/turn start.
func (s *RuntimeSupervisor) RecoverAppServerRun(ctx context.Context, runID string, binding appserver.Binding) (run.Run, error) {
	return s.recoverAppServerRun(ctx, runID, binding, nil)
}
func (s *RuntimeSupervisor) recoverAppServerRun(ctx context.Context, runID string, binding appserver.Binding, executor supervisedExecutor) (run.Run, error) {
	if s == nil || s.coordinator.AppServers == nil {
		return run.Run{}, errors.New("App Server recovery dependencies unavailable")
	}
	raw, err := s.coordinator.AppServers.AppServerCheckpoint(ctx, runID)
	if err != nil {
		return run.Run{}, err
	}
	var cp executionCheckpoint
	if err = json.Unmarshal(raw, &cp); err != nil {
		return run.Run{}, err
	}
	current, err := runtimeAuthorityRun(ctx, s.runs, runID)
	if err != nil {
		return current, err
	}
	if cp.Version != 1 || cp.RunID != current.ID || cp.Purpose != current.Purpose || current.Status != run.StatusRunning || current.ThreadID == "" || current.TurnID == "" {
		return current, fmt.Errorf("invalid same-Turn executor checkpoint")
	}

	qualifiedExecution, err := s.coordinator.qualifiedRecoveryExecution(ctx, current, cp, binding)
	if err != nil {
		return current, err
	}

	factory, ok := s.coordinator.Codex.(DefaultCodexFactory)
	if !ok || factory.Config.AgentRuntime == nil {
		return current, errors.New("continuity factory unavailable")
	}
	selected, err := s.coordinator.CodexConfigs.Get(ctx, cp.Config.ID)
	if err != nil || !selected.Enabled || cp.BackendSelection == nil || selected.Fingerprint() != cp.BackendSelection.ConfigFingerprint || binding.BackendConfigID != selected.ID || binding.BackendFingerprint != selected.Fingerprint() {
		return current, errors.Join(err, errors.New("frozen App Server backend changed"))
	}
	if err = appserver.Verify(ctx, binding); err != nil {
		return current, err
	}
	config := codexLaunchConfig(factory.Config, selected)
	if cp.MultiRuntime != nil && cp.MultiRuntime.Workset.TaskPermissions != nil {
		if current.RuntimeWorkset == nil || current.RuntimeWorkset.TaskPermissions == nil || current.RuntimeWorkset.TaskPermissions.ContentHash != cp.MultiRuntime.Workset.TaskPermissions.ContentHash || binding.TaskPermissionHash != cp.MultiRuntime.Workset.TaskPermissions.ContentHash {
			return current, errors.New("TASK_PERMISSION_RECOVERY_BINDING_MISMATCH")
		}
		if err = validateTaskEnvironment(cp.MultiRuntime.Workset); err != nil {
			return current, err
		}
		b := cp.MultiRuntime.Binding
		config.TaskRuntimeBinding = &b
	}
	config.AppServerBinding = &binding
	config.ApprovalMode = codex.ApprovalModeInteractive
	config.DynamicToolHandler = factory.DynamicToolHandler
	ceiling := codex.AuthorizationWorkspaceWrite
	if current.Purpose == run.PurposeVerification || current.Purpose == run.PurposeScopeReview {
		ceiling = codex.AuthorizationReadOnly
	}
	config.AuthorizationCeiling = ceiling
	client, err := codex.Start(ctx, config)
	if err != nil {
		return current, err
	}
	observed, err := client.ObserveLoadedTurn(ctx, current.ThreadID, current.TurnID)
	unresolvedTerminal := current.AgentRuntime != nil && agentruntime.HasUnresolvedResponse(*current.AgentRuntime) && observed.Turn.Status != "inProgress" && observed.Turn.Status != ""
	if err != nil || !observed.Loaded || (!unresolvedTerminal && (observed.Turn.Status != "inProgress" || observed.ThreadStatus != "active")) || observed.CWD != checkpointRuntimeCWD(cp) {
		_ = client.Close()
		status := observed.ThreadStatus
		if !observed.Loaded {
			status = "notLoaded"
		}
		return current, &AppServerObservationError{Observation: agentruntime.RuntimeObservation{AppServerInstanceID: binding.ID, InstanceEpoch: binding.Epoch, InstanceStatus: "ALIVE", ConnectionState: "UNKNOWN", ThreadID: current.ThreadID, TurnID: current.TurnID, ThreadStatus: status, RequestReattachment: "UNPROVEN", ObservedAt: time.Now().UTC(), SourceMethod: "thread/loaded/list+thread/read", Summary: "原请求状态无法确认，已记录的用户决定保持不变"}, Err: errors.Join(err, errors.New("original loaded active Turn is unproven"))}
	}
	if observations, ok := s.coordinator.AppServers.(interface {
		RecordAppServerObservation(context.Context, string, json.RawMessage) error
	}); ok {
		raw, _ := json.Marshal(agentruntime.RuntimeObservation{AppServerInstanceID: binding.ID, InstanceEpoch: binding.Epoch, InstanceStatus: "ALIVE", ConnectionState: "CONNECTED", ThreadID: current.ThreadID, TurnID: current.TurnID, ThreadStatus: observed.ThreadStatus, RequestReattachment: "AWAITING_EXACT_REQUEST_REPLAY", ObservedAt: time.Now().UTC(), SourceMethod: "process-identity+thread/loaded/list+thread/read", Summary: "已连接原 Codex 会话，正在确认原请求"})
		if err = observations.RecordAppServerObservation(ctx, runID, raw); err != nil {
			_ = client.Close()
			return current, err
		}
	}
	runtime, err := factory.Config.AgentRuntime.RestoreTurn(ctx, runID, current.ThreadID, current.TurnID, binding, ceiling == codex.AuthorizationReadOnly)
	if err != nil {
		_ = client.Close()
		return current, err
	}
	if unresolvedTerminal {
		// The exact original thread/read proves a terminal Turn even if its
		// completion event was sent while this Core was detached. Persist only
		// that protocol observation; the UNKNOWN answer remains unresolved.
		params, marshalErr := json.Marshal(map[string]any{"threadId": current.ThreadID, "turn": observed.Turn})
		if marshalErr != nil {
			_ = client.Close()
			runtime.Close()
			return current, marshalErr
		}
		if err = runtime.Observe(agentruntime.Event{Method: "turn/completed", Params: params}); err == nil {
			err = runtime.Flush(ctx)
		}
		if err != nil {
			_ = client.Close()
			runtime.Close()
			return current, err
		}
	}
	current, err = runtimeAuthorityRun(ctx, s.runs, runID)
	if err != nil {
		_ = client.Close()
		runtime.Close()
		return current, err
	}
	attached := &codexSession{client: client, authorizationCeiling: ceiling, independent: true, appServerInstance: binding, idleCleanup: factory.cleanupIdleAppServer(binding, config)}
	p := PreparedRun{Run: current, qualifiedExecution: qualifiedExecution, multiRuntime: cp.MultiRuntime, task: cp.Task, review: cp.Review, repository: cp.Repository, config: cp.Config, backendSelection: cp.BackendSelection, instruction: cp.Instruction, executionScope: cp.ExecutionScope, purpose: cp.Purpose, verificationSpec: cp.VerificationSpec, reviewScope: cp.ReviewScope, rejoin: &rejoinExecution{Checkpoint: cp, Binding: binding, Runtime: runtime, Client: attached}}
	owner := runtimeOwner{TaskID: current.TaskID}
	if current.ReviewID != "" {
		owner = runtimeOwner{ReviewID: current.ReviewID}
	}
	value, err := s.startWithExecutor(ctx, owner, func(context.Context) (PreparedRun, error) { return p, nil }, executor)
	if err != nil {
		_ = client.Close()
		runtime.Close()
	}
	return value, err
}

// RecoverAppServerPlan restores callbacks owned by the existing Execution
// authorization. Only the original accepted plan may reserve verification.
func (s *RuntimeSupervisor) RecoverAppServerPlan(ctx context.Context, runID string, binding appserver.Binding, verification task.VerificationSpec, reserveVerification func(context.Context) (run.Run, error), complete func(context.Context, executionproposal.Outcome, string, string) error) (run.Run, error) {
	if reserveVerification == nil || complete == nil {
		return run.Run{}, errors.New("recovered Execution callbacks required")
	}
	return s.recoverAppServerRun(ctx, runID, binding, func(session *RunSession, prepared PreparedRun) (run.Run, error) {
		value, runErr := s.coordinator.ExecuteRun(session.context(), prepared, session)
		if errors.Is(runErr, ErrAppServerDetached) || (errors.Is(runErr, ErrAgentExecutionUnproven) || errors.Is(runErr, agentruntime.ErrResponseDispositionUnknown)) {
			return value, runErr
		}
		value = s.finishCancellation(session.context(), value)
		if runErr == nil && value.Status == run.StatusSucceeded && prepared.purpose == run.PurposeImplementation && verification.Mode == task.VerificationModeRun {
			reserved, err := reserveVerification(session.context())
			if err != nil {
				return value, errors.Join(err, complete(context.WithoutCancel(session.context()), executionproposal.OutcomeFailed, "VERIFICATION_RESERVATION_FAILED", err.Error()))
			}
			receipt, admitted, err := s.coordinator.Handoffs.ClaimHandoff(runtimeepoch.WithFence(session.context(), reserved.RuntimeEpoch, reserved.ID), reserved.ProjectID, reserved.ID, time.Now().UTC())
			if err != nil {
				return value, errors.Join(ErrAgentExecutionUnproven, err)
			}
			if !admitted {
				return value, errors.Join(ErrAgentExecutionUnproven, &executionproposal.HandoffError{Handoff: receipt})
			}
			s.rekeySession(session, prepared.Run.ID, reserved.ID, reserved.RuntimeEpoch)
			next, err := s.coordinator.PrepareReservedExecution(session.context(), reserved.ID, Instruction{CodexInstruction: verification.CodexInstruction}, &verification)
			if err != nil {
				return next.Run, errors.Join(err, complete(context.WithoutCancel(session.context()), executionproposal.OutcomeFailed, "VERIFICATION_PREPARATION_FAILED", err.Error()))
			}
			if _, err = s.coordinator.Handoffs.RecordHandoff(context.WithoutCancel(session.context()), reserved.ProjectID, reserved.ID, executionproposal.HandoffAccepted, "SUPERVISOR_ACCEPTED", time.Now().UTC()); err != nil {
				return next.Run, err
			}
			value, runErr = s.coordinator.ExecuteRun(session.context(), next, session)
			if errors.Is(runErr, ErrAppServerDetached) || (errors.Is(runErr, ErrAgentExecutionUnproven) || errors.Is(runErr, agentruntime.ErrResponseDispositionUnknown)) {
				return value, runErr
			}
			value = s.finishCancellation(session.context(), value)
		}
		outcome, code, message := executionOutcome(value, runErr)
		if runErr == nil && value.Status == run.StatusSucceeded {
			outcome, code, message = executionproposal.OutcomeSucceeded, "", ""
		}
		return value, errors.Join(runErr, complete(context.WithoutCancel(session.context()), outcome, code, message))
	})
}

func (c *Coordinator) observeDetach(ctx context.Context, value run.Run, session CodexSession, cause error) {
	store, ok := c.AppServers.(interface {
		RecordAppServerObservation(context.Context, string, json.RawMessage) error
	})
	if !ok {
		return
	}
	bound, ok := session.(interface{ continuityBinding() appserver.Binding })
	if !ok {
		return
	}
	binding := bound.continuityBinding()
	observation := agentruntime.RuntimeObservation{AppServerInstanceID: binding.ID, InstanceEpoch: binding.Epoch, InstanceStatus: "UNVERIFIED", ConnectionState: "DISCONNECTED", ThreadID: value.ThreadID, TurnID: value.TurnID, RequestReattachment: "UNPROVEN", ObservedAt: time.Now().UTC(), SourceMethod: "CORE_TRANSPORT_DETACH", Summary: "Core transport detached; original operation and user decision are retained."}
	if cause != nil {
		observation.FailureReason = cause.Error()
	}
	raw, _ := json.Marshal(observation)
	_ = store.RecordAppServerObservation(context.WithoutCancel(ctx), value.ID, raw)
}

func (s codexSession) stopIdleAppServer(ctx context.Context) error {
	if s.idleCleanup == nil {
		return nil
	}
	return s.idleCleanup(ctx)
}
func (f DefaultCodexFactory) cleanupIdleAppServer(binding appserver.Binding, config codex.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		needed := func() (bool, error) {
			if store, ok := f.AppServers.(interface {
				RecoveryAppServerNeedsContinuity(context.Context, string) (bool, error)
			}); ok {
				return store.RecoveryAppServerNeedsContinuity(ctx, binding.ID)
			}
			return false, nil
		}
		if keep, err := needed(); err != nil || keep {
			return err
		}
		live, ok := f.AppServers.(interface {
			AppServerHasActiveRuns(context.Context, string) (bool, error)
		})
		if !ok {
			return errors.New("App Server active owner proof unavailable")
		}
		active, err := live.AppServerHasActiveRuns(ctx, binding.ID)
		if err != nil || active {
			return err
		}
		if err = appserver.Verify(ctx, binding); err != nil {
			return err
		}
		observationConfig := config
		observationConfig.AgentRuntime = nil
		observationConfig.DynamicToolHandler = nil
		observationConfig.UsageStore = nil
		observer, err := codex.Start(ctx, observationConfig)
		if err != nil {
			return err
		}
		quiescent, err := observer.AppServerQuiescent(ctx)
		closeErr := observer.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		if !quiescent {
			return nil
		}
		// Recheck durable ownership after observation. No business state is changed
		// by cleanup, and any unproven owner prevents process termination.
		active, err = live.AppServerHasActiveRuns(ctx, binding.ID)
		if err != nil || active {
			return err
		}
		if keep, err := needed(); err != nil || keep {
			return err
		}
		return appserver.Stop(ctx, binding)
	}
}

func checkpointRuntimeCWD(cp executionCheckpoint) string {
	if cp.MultiRuntime != nil && cp.MultiRuntime.Workset.TaskPermissions != nil {
		return cp.MultiRuntime.CWD
	}
	return cp.Worktree.Path
}
