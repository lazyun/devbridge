package probeexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

type CanonicalInspectionClient interface {
	StartThread(context.Context, codex.ThreadOptions) (codex.Thread, error)
	StartTurn(context.Context, codex.TurnOptions) (codex.TurnResult, error)
	RejoinTurn(context.Context, codex.TurnRejoinOptions) (codex.TurnResult, error)
	ObserveLoadedTurn(context.Context, string, string) (codex.TurnObservation, error)
	InterruptTurn(context.Context, string, string) error
	Close() error
}
type canonicalInspectionWorker struct {
	client       CanonicalInspectionClient
	state        CanonicalState
	cancel       context.CancelFunc
	ready        chan struct{}
	done         chan struct{}
	cancellation bool
}
type CanonicalRunner struct {
	Store         CanonicalStore
	Materializer  InspectionMaterializer
	AppServers    InspectionAppServers
	Configs       ConfigLookup
	AgentRuntime  *agentruntime.Service
	Cancellations InspectionCancellationStore
	Config        codex.Config
	DataRoot      string
	AppServerSpec func(codexconfig.Config) appserver.Spec
	Connect       func(context.Context, codex.Config) (CanonicalInspectionClient, error)
	mu            sync.Mutex
	workers       map[string]*canonicalInspectionWorker
	closed        bool
	wg            sync.WaitGroup
}

func (r *CanonicalRunner) StartConfirmed(ctx context.Context, sessionID, probeID, requestID, payloadHash string) (contextprobe.Probe, error) {
	if r.Store == nil || requestID == "" || payloadHash == "" {
		return contextprobe.Probe{}, contextprobe.ErrInvalidArgument
	}
	spec, err := r.Store.ConfirmedInspection(ctx, sessionID, probeID)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	if spec.RequestID != requestID || spec.PayloadHash != payloadHash {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	return r.Start(ctx, sessionID, probeID)
}

func (r *CanonicalRunner) Start(ctx context.Context, sessionID, probeID string) (contextprobe.Probe, error) {
	if r.Store == nil || r.Materializer == nil || r.AppServers == nil || r.Configs == nil || r.AgentRuntime == nil || r.DataRoot == "" {
		return contextprobe.Probe{}, errors.New("canonical Inspection runtime unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return contextprobe.Probe{}, errors.New("Inspection runtime is closed")
	}
	spec, err := r.Store.ConfirmedInspection(ctx, sessionID, probeID)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	if existing := r.workers[probeID]; existing != nil {
		return spec.Probe, nil
	}
	if spec.Probe.RunID != "" {
		state, err := r.Store.InspectionState(ctx, probeID)
		if err != nil || state.State != "PREPARED" {
			return spec.Probe, err
		}
	}
	config, err := r.Configs.Get(ctx, spec.BackendSelection.ConfigID)
	if err != nil {
		return spec.Probe, err
	}
	if !config.Enabled || config.Fingerprint() != spec.BackendSelection.ConfigFingerprint {
		return spec.Probe, contextprobe.ErrConflict
	}
	reserved, err := r.Store.ReserveInspection(ctx, spec)
	if err != nil {
		return spec.Probe, err
	}
	spec = reserved.Spec
	w, before, err := r.Materializer.MaterializeInspection(ctx, spec)
	if err != nil {
		return spec.Probe, err
	}
	state, err := r.Store.BeginInspection(ctx, spec, w, before)
	if err != nil {
		return spec.Probe, err
	}
	r.spawn(ctx, state, config, false)
	return state.Spec.Probe, nil
}
func (r *CanonicalRunner) spawn(ctx context.Context, state CanonicalState, config codexconfig.Config, rejoin bool) {
	workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	workerCtx = runtimeepoch.WithFence(workerCtx, state.Run.RuntimeEpoch, state.Run.ID)
	owned := &canonicalInspectionWorker{state: state, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	if r.workers == nil {
		r.workers = map[string]*canonicalInspectionWorker{}
	}
	r.workers[state.Spec.Probe.ID] = owned
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		defer func() { r.mu.Lock(); delete(r.workers, state.Spec.Probe.ID); close(owned.done); r.mu.Unlock() }()
		r.executeCanonical(workerCtx, owned, config, rejoin)
	}()
}
func (r *CanonicalRunner) connect(ctx context.Context, state CanonicalState, selected codexconfig.Config, rejoin bool) (CanonicalInspectionClient, appserver.Binding, error) {
	var binding appserver.Binding
	var err error
	if rejoin || state.ContinuationRecoveryStatusID != "" {
		raw, readErr := r.AppServers.AppServerCheckpoint(ctx, state.Run.ID)
		var frozen CanonicalState
		if readErr != nil {
			return nil, binding, readErr
		}
		if json.Unmarshal(raw, &frozen) != nil || frozen.Run.ID != state.Run.ID || frozen.Spec.PayloadHash != state.Spec.PayloadHash || frozen.Workset.ContentHash != state.Workset.ContentHash || string(frozen.Schema) != string(state.Schema) {
			return nil, binding, errors.New("Inspection checkpoint identity mismatch")
		}
		binding, err = r.AppServers.AppServerBinding(ctx, state.Run.ID)
		if err == nil {
			err = appserver.VerifyOwner(binding, r.DataRoot)
		}
		if err == nil {
			err = appserver.Verify(ctx, binding)
		}
		if err != nil {
			return nil, binding, err
		}
	} else {
		spec := appserver.Spec{DataRoot: r.DataRoot, ExecutablePath: selected.CodexPath, BackendConfigID: selected.ID, BackendFingerprint: selected.Fingerprint()}
		if r.AppServerSpec != nil {
			spec = r.AppServerSpec(selected)
			spec.DataRoot = r.DataRoot
			spec.BackendConfigID = selected.ID
			spec.BackendFingerprint = selected.Fingerprint()
		}
		if spec.ExecutablePath == "" {
			spec.ExecutablePath = r.Config.CodexPath
		}
		spec.ExecutablePath, err = appserver.ResolveExecutable(spec.ExecutablePath)
		if err != nil {
			return nil, binding, err
		}
		binding, err = appserver.Launch(ctx, spec)
		if err != nil {
			return nil, binding, err
		}
		if err = r.AppServers.BindAppServer(ctx, state.Run.ID, binding); err != nil {
			_ = appserver.Stop(context.WithoutCancel(ctx), binding)
			return nil, binding, err
		}
		checkpoint, _ := json.Marshal(state)
		if err = r.AppServers.SaveAppServerCheckpoint(ctx, state.Run.ID, checkpoint); err != nil {
			_ = appserver.Stop(context.WithoutCancel(ctx), binding)
			return nil, binding, err
		}
	}
	if binding.BackendConfigID != selected.ID || binding.BackendFingerprint != selected.Fingerprint() {
		return nil, binding, errors.New("Inspection App Server backend differs from confirmed receipt")
	}
	config := r.Config
	config.AppServerBinding = &binding
	config.AgentRuntime = r.AgentRuntime
	config.ApprovalMode = codex.ApprovalModeInteractive
	config.AuthorizationCeiling = codex.AuthorizationReadOnly
	connect := r.Connect
	if connect == nil {
		connect = func(ctx context.Context, c codex.Config) (CanonicalInspectionClient, error) {
			return codex.Start(ctx, c)
		}
	}
	client, err := connect(ctx, config)
	return client, binding, err
}
func inspectionOptions(s CanonicalState) (string, codex.RuntimeBinding, codex.TurnOptions) {
	var cwd string
	binding := codex.RuntimeBinding{RuntimeWorkspaceRoots: []string{}, WritableRoots: []string{}}
	for _, m := range s.Workset.Members {
		binding.RuntimeWorkspaceRoots = append(binding.RuntimeWorkspaceRoots, m.CanonicalRoot)
		if m.MemberID == s.Workset.PrimaryMemberID {
			cwd = m.CanonicalRoot
		}
	}
	var tier *string
	if s.Spec.ServiceTier != "" {
		tier = &s.Spec.ServiceTier
	}
	return cwd, binding, codex.TurnOptions{RunID: s.Run.ID, ThreadID: s.Run.ThreadID, Prompt: s.Spec.Probe.CodexInstruction, OutputSchema: func() json.RawMessage {
		if s.Run.ResultProtocol == run.NaturalResultProtocol || s.Run.TurnID == "" {
			return nil
		}
		return json.RawMessage(s.Schema)
	}(), Model: s.Spec.Model, ReasoningEffort: s.Spec.ReasoningEffort, ServiceTier: tier, AuthorizationCeiling: codex.AuthorizationReadOnly, RuntimeBinding: binding}
}
func (r *CanonicalRunner) unknown(ctx context.Context, s CanonicalState, err error) {
	if err != nil {
		if _, cancelErr := r.Store.CancelPreparedInspection(context.WithoutCancel(ctx), s.Run.ID, s.Run.RuntimeEpoch); cancelErr == nil {
			return
		}
		_, _ = r.Store.MarkInspectionUnknown(context.WithoutCancel(ctx), s.Run.ID, s.Run.RuntimeEpoch, boundedFailure(err))
	}
}
func (r *CanonicalRunner) executeCanonical(ctx context.Context, owned *canonicalInspectionWorker, selected codexconfig.Config, rejoin bool) {
	state := owned.state
	client, binding, err := r.connect(ctx, state, selected, rejoin)
	if err != nil {
		r.unknown(ctx, state, err)
		return
	}
	defer client.Close()
	r.mu.Lock()
	owned.client = client
	r.mu.Unlock()
	cwd, filesystem, options := inspectionOptions(state)
	if cwd == "" {
		r.unknown(ctx, state, errors.New("Inspection primary managed root missing"))
		return
	}
	var result codex.TurnResult
	if rejoin {
		if state.Run.ThreadID == "" || state.Run.TurnID == "" {
			r.unknown(ctx, state, errors.New("Inspection start outcome uncertain; no automatic redispatch"))
			return
		}
		var session *agentruntime.Session
		snapshot, readErr := r.AgentRuntime.Read(ctx, state.Run.ID)
		if readErr == nil && snapshot.ConnectionID != "" {
			session, err = r.AgentRuntime.RestoreTurn(ctx, state.Run.ID, state.Run.ThreadID, state.Run.TurnID, binding, true)
		} else {
			session = r.AgentRuntime.NewTurn(ctx, state.Run.ID, state.Run.ThreadID, true)
			err = session.Bind(state.Run.TurnID)
		}
		if err != nil {
			r.unknown(ctx, state, err)
			return
		}
		r.mu.Lock()
		close(owned.ready)
		r.mu.Unlock()
		result, err = client.RejoinTurn(ctx, codex.TurnRejoinOptions{TurnOptions: options, TurnID: state.Run.TurnID, RuntimeSession: session})
	} else {
		if state.ContinuationRecoveryStatusID == "" || state.ContinuationFreshThread {
			state, err = r.Store.InspectionBeforeThread(ctx, state.Run.ID, state.Run.RuntimeEpoch)
			if err != nil {
				r.unknown(ctx, state, err)
				return
			}
			thread, threadErr := client.StartThread(ctx, codex.ThreadOptions{CWD: cwd, RuntimeBinding: filesystem, Model: state.Spec.Model, ReasoningEffort: state.Spec.ReasoningEffort, ServiceTier: state.Spec.ServiceTier})
			if threadErr != nil {
				r.unknown(ctx, state, threadErr)
				return
			}
			state, err = r.Store.InspectionBindThread(ctx, state.Run.ID, state.Run.RuntimeEpoch, thread.ID)
			if err != nil {
				r.unknown(ctx, state, err)
				return
			}
		}
		observed, observeErr := recoverystatus.ObserveWorkset(ctx, state.Workset)
		if observeErr != nil || InspectionObservationStatus(state.Before, observed) != "UNCHANGED" {
			r.unknown(ctx, state, errors.New("Inspection workset changed before turn dispatch"))
			return
		}
		state, err = r.Store.InspectionBeforeTurn(ctx, state.Run.ID, state.Run.RuntimeEpoch)
		if err != nil {
			r.unknown(ctx, state, err)
			return
		}
		options.ThreadID = state.Run.ThreadID
		options.OnStarted = func(thread, turn string) error {
			if thread != state.Run.ThreadID {
				return contextprobe.ErrConflict
			}
			var bindErr error
			state, bindErr = r.Store.InspectionBindTurn(context.WithoutCancel(ctx), state.Run.ID, state.Run.RuntimeEpoch, turn)
			if bindErr == nil {
				r.mu.Lock()
				owned.state = state
				close(owned.ready)
				r.mu.Unlock()
			}
			return bindErr
		}
		if state.ContinuationRecoveryStatusID != "" && !state.ContinuationFreshThread {
			continuation, ok := client.(interface {
				StartRecoveryStatusTurn(context.Context, codex.RecoveryStatusTurnOptions) (codex.TurnResult, error)
			})
			if !ok {
				r.unknown(ctx, state, errors.New("Inspection exact-thread continuation unavailable"))
				return
			}
			result, err = continuation.StartRecoveryStatusTurn(ctx, codex.RecoveryStatusTurnOptions{TurnOptions: options, SourceTurnID: state.ContinuationSourceTurnID, ExpectedCWD: cwd, BeforeDispatch: func() error {
				current, e := recoverystatus.ObserveWorkset(ctx, state.Workset)
				if e != nil {
					return e
				}
				if InspectionObservationStatus(state.Before, current) != "UNCHANGED" {
					return contextprobe.ErrConflict
				}
				return nil
			}})
		} else {
			result, err = client.StartTurn(ctx, options)
		}
	}
	if ctx.Err() != nil {
		return
	} // Detach only. Shutdown does not infer finalization.
	if result.TurnID != "" && state.Run.TurnID != "" && result.TurnID != state.Run.TurnID {
		r.unknown(ctx, state, errors.New("Inspection terminal Turn identity changed"))
		return
	}
	var terminalError *codex.TurnError
	if err != nil && !errors.As(err, &terminalError) {
		r.unknown(ctx, state, err)
		return
	}
	for {
		snapshot, readErr := r.AgentRuntime.Read(ctx, state.Run.ID)
		if readErr != nil {
			r.unknown(ctx, state, readErr)
			return
		}
		r.mu.Lock()
		canceled := owned.cancellation
		r.mu.Unlock()
		pending := agentruntime.HasUnresolvedResponse(snapshot)
		for _, q := range snapshot.Requests {
			pending = pending || agentruntime.OpenRequest(q)
		}
		if !pending || canceled {
			break
		}
		_, _ = r.Store.MarkInspectionUnknown(ctx, state.Run.ID, state.Run.RuntimeEpoch, "Inspection runtime response remains unresolved")
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	observation, observeErr := client.ObserveLoadedTurn(ctx, state.Run.ThreadID, state.Run.TurnID)
	if observeErr != nil || observation.ThreadStatus != "idle" || !inspectionTerminalStatus(string(observation.Turn.Status)) {
		r.unknown(ctx, state, errors.New("Inspection terminal backend quiescence unproven"))
		return
	}
	if result.FinalMessage != "" {
		var saved CanonicalState
		saved, observeErr = r.Store.InspectionSaveReport(ctx, state.Run.ID, state.Run.RuntimeEpoch, []byte(result.FinalMessage))
		if observeErr != nil {
			r.unknown(ctx, state, observeErr)
			return
		}
		state = saved
	}
	after, _ := recoverystatus.ObserveWorkset(ctx, state.Workset)
	final := InspectionFinalization{RunID: state.Run.ID, InspectionID: state.Spec.Probe.ID, RuntimeEpoch: state.Run.RuntimeEpoch, ThreadID: state.Run.ThreadID, TurnID: state.Run.TurnID, WorksetHash: state.Workset.ContentHash, Report: state.Report, ReportHash: inspectionHash(state.Report), Before: state.Before, After: after, Terminal: true, TerminalStatus: string(observation.Turn.Status), Quiesced: true, ObservedAt: time.Now().UTC()}
	r.mu.Lock()
	canceled := owned.cancellation
	r.mu.Unlock()
	if canceled {
		final.Status = "CANCELED"
		final.Reason = "Explicit Inspection cancellation"
	} else if InspectionObservationStatus(state.Before, after) != "UNCHANGED" {
		final.Status = "SCOPE_CONFLICT"
		final.Reason = "Managed Inspection worktree changed"
	} else if err != nil {
		final.Status = "FAILED"
		final.Reason = boundedFailure(err)
	} else if state.Run.ResultProtocol == run.NaturalResultProtocol {
		final.Status = "SUCCEEDED"
	} else if report, parseErr := ParseInspectionReport(state.Report, state.Workset); parseErr != nil {
		final.Status = "FAILED"
		final.Reason = parseErr.Error()
	} else {
		final.Status = report.Status
	}
	for _, command := range result.Commands {
		final.Commands = append(final.Commands, run.ObservedCommand{ItemID: command.ItemID, Command: command.Command, CWD: command.CWD, Status: command.Status, Actions: command.Actions, ExitCode: command.ExitCode, Output: command.Output, OutputTruncated: command.OutputTruncated, DurationMillis: command.DurationMS, StartedAt: command.StartedAt, FinishedAt: command.FinishedAt})
	}
	if _, err = r.Store.FinalizeInspection(ctx, final); err != nil {
		r.unknown(ctx, state, err)
	}
}
func inspectionTerminalStatus(s string) bool {
	return s == "completed" || s == "failed" || s == "interrupted"
}
func (r *CanonicalRunner) Recover(ctx context.Context, probeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("Inspection runtime closed")
	}
	if r.workers[probeID] != nil {
		return nil
	}
	state, err := r.Store.AdoptInspection(ctx, probeID)
	if err != nil {
		return err
	}
	if state.Finalization != nil {
		return nil
	}
	if state.State == "PREPARED" {
		return nil
	} // The explicit Start replay reuses this reservation and materializes its frozen resources.
	if state.Run.ThreadID == "" || state.Run.TurnID == "" {
		r.unknown(ctx, state, errors.New("Inspection start uncertainty requires explicit recovery"))
		return nil
	}
	config, err := r.Configs.Get(ctx, state.Spec.BackendSelection.ConfigID)
	if err != nil {
		return err
	}
	if config.Fingerprint() != state.Spec.BackendSelection.ConfigFingerprint {
		return contextprobe.ErrConflict
	}
	r.spawn(ctx, state, config, true)
	return nil
}
func (r *CanonicalRunner) RecoverAll(ctx context.Context) error {
	ids, err := r.Store.ListCanonicalInspections(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if e := r.Recover(ctx, id); e != nil {
			failures = append(failures, fmt.Errorf("Inspection %s: %w", id, e))
		}
	}
	return errors.Join(failures...)
}

// InspectionState exposes only the canonical owner snapshot required by the
// generic Recovery Status adapter. The adapter still validates every durable
// identity before it can dispatch or resolve a child Turn.
func (r *CanonicalRunner) InspectionState(ctx context.Context, operationID string) (CanonicalState, error) {
	return r.Store.InspectionState(ctx, operationID)
}

// DetachRecoverySource transfers the exact source Run from the Inspection
// runner to Recovery Status. It never stops an unrelated Inspection owner and
// is idempotent when the terminal source worker is already gone.
func (r *CanonicalRunner) DetachRecoverySource(ctx context.Context, sourceRunID string) error {
	if sourceRunID == "" {
		return contextprobe.ErrInvalidArgument
	}
	r.mu.Lock()
	var done <-chan struct{}
	for _, owned := range r.workers {
		if owned.state.Run.ID == sourceRunID {
			owned.cancel()
			done = owned.done
			break
		}
	}
	r.mu.Unlock()
	if done == nil {
		state, err := r.Store.InspectionState(ctx, sourceRunID)
		if err != nil {
			return err
		}
		if state.Run.ID != sourceRunID {
			return contextprobe.ErrConflict
		}
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *CanonicalRunner) Shutdown() {
	r.mu.Lock()
	r.closed = true
	for _, w := range r.workers {
		w.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *CanonicalRunner) CancelExecution(ctx context.Context, in executioncancel.Input) (executioncancel.Receipt, error) {
	in = in.Normalized()
	if r.Cancellations == nil || in.OwnerType != "INSPECTION" {
		return executioncancel.Receipt{}, executioncancel.ErrInvalidArgument
	}
	r.mu.Lock()
	owned := r.workers[in.OwnerID]
	live := owned != nil && owned.state.Run.ID == in.RunID
	prepared := false
	if !live {
		state, e := r.Store.InspectionState(ctx, in.OwnerID)
		prepared = e == nil && state.State == "PREPARED" && state.Run.ID == in.RunID
		live = prepared
	}
	receipt, dispatch, err := r.Cancellations.Begin(ctx, in, live)
	if err != nil || !dispatch {
		r.mu.Unlock()
		return receipt, err
	}
	if prepared {
		r.mu.Unlock()
		state, e := r.Store.InspectionState(ctx, in.OwnerID)
		if e != nil {
			return receipt, e
		}
		_, e = r.Store.CancelPreparedInspection(ctx, in.RunID, state.Run.RuntimeEpoch)
		if e != nil {
			return receipt, e
		}
		receipt, _, e = r.Cancellations.Begin(ctx, in, false)
		return receipt, e
	}
	owned.cancellation = true
	state, readErr := r.Store.InspectionState(ctx, in.OwnerID)
	if readErr == nil && state.Run.TurnID == "" {
		if _, finishErr := r.Store.CancelPreparedInspection(ctx, in.RunID, state.Run.RuntimeEpoch); finishErr == nil {
			owned.cancel()
			r.mu.Unlock()
			receipt, _, err = r.Cancellations.Begin(ctx, in, false)
			return receipt, err
		}
	}
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		select {
		case <-owned.ready:
		case <-owned.done:
			return
		case <-time.After(30 * time.Second):
			_ = r.Cancellations.RecordInterrupt(context.Background(), in.ProjectID, in.RequestID, "OUTCOME_UNKNOWN")
			return
		}
		target, send, err := r.Cancellations.ClaimInterrupt(context.Background(), in.ProjectID, in.RequestID)
		if err != nil || !send {
			return
		}
		signal, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.mu.Lock()
		client := owned.client
		r.mu.Unlock()
		outcome := "ACKNOWLEDGED"
		if client == nil || client.InterruptTurn(signal, target.ThreadID, target.TurnID) != nil {
			outcome = "OUTCOME_UNKNOWN"
		}
		_ = r.Cancellations.RecordInterrupt(context.Background(), in.ProjectID, in.RequestID, outcome)
	}()
	return receipt, nil
}
