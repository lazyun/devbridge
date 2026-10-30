package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/workset"
)

type RecoveryStatusAppServers interface {
	AppServerBinding(context.Context, string) (appserver.Binding, error)
	BindAppServer(context.Context, string, appserver.Binding) error
	AppServerCheckpoint(context.Context, string) (json.RawMessage, error)
}

// RecoveryStatusService owns only the diagnostic child runtime. Source operation
// authority and all progression are revalidated by the canonical durable store.
type RecoveryStatusService struct {
	Store           recoverystatus.Store
	Runs            *run.Service
	Coordinator     *Coordinator
	AppServers      RecoveryStatusAppServers
	Config          codex.Config
	Supervisor      *RuntimeSupervisor
	OwnerFacts      func(context.Context, run.Run, recoverystatus.Report, *recoverystatus.FactualObservation) error
	InspectionOwner InspectionRecoveryOwner
	mu              sync.Mutex
	active          map[string]context.CancelFunc
	closed          bool
	wg              sync.WaitGroup
}

func (s *RecoveryStatusService) Prepare(ctx context.Context, spec recoverystatus.Spec) (recoverystatus.Record, error) {
	wantVersion := recoverystatus.ReportVersion
	if spec.OwnerKind == "PROJECT_INSPECTION" {
		wantVersion = recoverystatus.InspectionReportVersion
	}
	if spec.OutputSchemaVersion != wantVersion {
		return recoverystatus.Record{}, recoverystatus.ErrIneligible
	}
	return s.Store.Prepare(ctx, spec, recoverystatus.ReportSchemaForSource(spec.Source))
}
func (s *RecoveryStatusService) Read(ctx context.Context, id string) (recoverystatus.Record, error) {
	return s.Store.Read(ctx, id)
}
func (s *RecoveryStatusService) Close() {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

type recoveryRuntime struct {
	client        *codex.Client
	source        run.Run
	workset       workset.RuntimeWorkset
	binding       appserver.Binding
	cwd           string
	options       codex.TurnOptions
	childThreadID string
	childCWD      string
}

type recoveryChildStore interface {
	AdmitRecoveryChild(context.Context, string, string, string, recoverystatus.Admission, time.Time) (string, error)
	ReadRecoveryChildPreparation(context.Context, string) (recoverystatus.ChildPreparation, error)
	ClaimRecoveryChildThread(context.Context, recoverystatus.ChildThreadClaim, time.Time) (bool, error)
	ReadRecoveryChildThread(context.Context, string) (recoverystatus.ChildThreadReceipt, bool, error)
	RecordRecoveryChildThread(context.Context, recoverystatus.ChildThreadReceipt, time.Time) error
	ClaimRecoveryChildTurn(context.Context, recoverystatus.ChildTurnClaim, time.Time) (bool, error)
	ReadRecoveryChildTurn(context.Context, string) (recoverystatus.ChildTurnReceipt, bool, error)
	RecordRecoveryChildTurn(context.Context, recoverystatus.ChildTurnReceipt, time.Time) error
}

func recoveryHash(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *RecoveryStatusService) connect(ctx context.Context, r recoverystatus.Record) (*recoveryRuntime, error) {
	source, e := s.Runs.Get(ctx, r.Spec.RunID)
	if e != nil {
		return nil, e
	}
	w, e := s.Runs.RuntimeWorkset(ctx, source.ID)
	if e != nil {
		return nil, e
	}
	if e = w.Validate(); e != nil {
		return nil, e
	}
	if w.ContentHash != r.Spec.RuntimeWorksetHash || source.ThreadID != r.Spec.ThreadID || source.TurnID != r.Spec.TurnID {
		return nil, recoverystatus.ErrConflict
	}
	if source.OperationKind == run.OperationInspection {
		if _, e = s.inspectionSource(ctx, source, r.Spec.Source, w); e != nil {
			return nil, e
		}
	}
	b, e := s.AppServers.AppServerBinding(ctx, source.ID)
	if e != nil {
		return nil, e
	}
	if b.ID != r.Spec.AppServerInstanceID || b.BackendConfigID != w.BackendConfigID || b.BackendFingerprint != w.BackendConfigFingerprint {
		return nil, recoverystatus.ErrConflict
	}
	cfg := s.Config
	cfg.AppServerBinding = &b
	sourceCWD, sourceBinding, bindingErr := multiRuntimeClientBinding(w)
	if bindingErr != nil {
		return nil, bindingErr
	}
	if w.TaskPermissions != nil {
		cfg.TaskRuntimeBinding = &sourceBinding
		cfg.DiagnosticTaskReadOnly = true
	}
	cfg.ApprovalMode = codex.ApprovalModeInteractive
	cfg.AuthorizationCeiling = codex.AuthorizationReadOnly
	client, e := codex.Start(ctx, cfg)
	if e != nil {
		return nil, e
	}
	rr := &recoveryRuntime{client: client, source: source, workset: w, binding: b}
	for _, m := range w.Members {
		if m.MemberID == w.PrimaryMemberID {
			rr.cwd = m.CanonicalRoot
		}
	}
	rr.cwd = sourceCWD
	rr.options = codex.TurnOptions{RunID: r.RecoveryRunID, Prompt: r.Spec.Instruction, OutputSchema: func() json.RawMessage {
		if r.RecoveryTurnID == "" {
			return nil
		}
		if child, e := s.Runs.Get(ctx, r.RecoveryRunID); e == nil && child.ResultProtocol == run.NaturalResultProtocol {
			return nil
		}
		return r.Schema
	}(), AuthorizationCeiling: codex.AuthorizationReadOnly}
	return rr, nil
}
func recoveryPending(source run.Run) bool {
	if source.AgentRuntime == nil {
		return false
	}
	for _, q := range source.AgentRuntime.Requests {
		d := agentruntime.ResponseDisposition(q)
		if d == "DISPATCHING" {
			return true
		}
		if !agentruntime.OpenRequest(q) {
			continue
		}
		if q.Kind == "CODEX_REQUEST" && d == "UNKNOWN" && q.UserDecision != nil {
			continue
		}
		return true
	}
	return false
}
func (s *RecoveryStatusService) admission(ctx context.Context, r recoverystatus.Record, rr *recoveryRuntime, turn string) (recoverystatus.Admission, error) {
	a := recoverystatus.Admission{ObservedAt: time.Now().UTC()}
	if _, e := rr.client.ObserveRecoverySource(ctx, r.Spec.ThreadID, turn, rr.cwd); e != nil {
		return a, e
	}
	source, e := s.Runs.GetForRuntimeAuthority(ctx, r.Spec.RunID)
	if e != nil {
		return a, e
	}
	if recoveryPending(source) {
		return a, errors.New("RECOVERY_PENDING_REQUEST")
	}
	if r.RecoveryRunID != "" {
		child, e := s.Runs.GetForRuntimeAuthority(ctx, r.RecoveryRunID)
		if e != nil {
			return a, e
		}
		if recoveryPending(child) || (child.AgentRuntime != nil && agentruntime.HasUnresolvedResponse(*child.AgentRuntime)) {
			return a, errors.New("RECOVERY_PENDING_REQUEST")
		}
	}
	obs, e := recoverystatus.ObserveWorkset(ctx, rr.workset)
	if e != nil {
		return a, e
	}
	a.Before, e = json.Marshal(obs)
	if e != nil {
		return a, e
	}
	a.SourceTurnTerminal = true
	a.ThreadIdle = true
	a.NoPendingRequests = true
	a.Quiesced = true
	return a, nil
}

// Start is the only entrance that may dispatch. Concurrent/exact replay returns
// the existing record. An uncertain dispatch remains UNKNOWN and is never sent
// again, including after process restart.
func (s *RecoveryStatusService) Start(ctx context.Context, id string) (recoverystatus.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.Store.Read(ctx, id)
	if e != nil {
		return r, e
	}
	if s.closed {
		return r, errors.New("recovery service closed")
	}
	if s.active[id] != nil || r.State == "UNKNOWN" || r.State == "RUNNING" || r.State == "REPORT_READY" || r.State == "ASSESSED" || r.State == "RESOLVED" || r.State == "FAILED" {
		return r, nil
	}
	rr, e := s.connect(ctx, r)
	if e != nil {
		return r, e
	}
	a, e := s.admission(ctx, r, rr, r.Spec.TurnID)
	if e != nil {
		rr.client.Close()
		return r, e
	}
	if r.Admission.SourceTurnTerminal {
		if !bytes.Equal(a.Before, r.Admission.Before) || !r.Admission.ThreadIdle || !r.Admission.NoPendingRequests || !r.Admission.Quiesced {
			rr.client.Close()
			return r, recoverystatus.ErrConflict
		}
		a = r.Admission
	}
	childStore, ok := s.Store.(recoveryChildStore)
	if !ok || s.Coordinator == nil {
		rr.client.Close()
		return r, errors.New("canonical Recovery child preparation unavailable")
	}
	requestID := "recovery_child_admission_" + id
	payload := recoveryHash(r.Spec.PayloadHash, r.Spec.RuntimeWorksetHash, r.Spec.ThreadID, r.Spec.TurnID, string(a.Before), r.Spec.Instruction)
	planID, e := childStore.AdmitRecoveryChild(ctx, id, requestID, payload, a, time.Now().UTC())
	if e != nil {
		rr.client.Close()
		return r, e
	}
	plan, e := childStore.ReadRecoveryChildPreparation(ctx, id)
	if e != nil || plan.PlanID != planID {
		rr.client.Close()
		return r, errors.Join(e, recoverystatus.ErrConflict)
	}
	child, e := s.Runs.CreatePreparationRun(ctx, planID, id+":run", requestID, payload, time.Now().UTC())
	if e != nil {
		rr.client.Close()
		return r, e
	}
	r, e = s.Store.Read(ctx, id)
	if e != nil || r.RecoveryRunID != child.ID {
		rr.client.Close()
		return r, errors.Join(e, recoverystatus.ErrConflict)
	}
	ctx = runtimeepoch.WithFence(ctx, r.RuntimeEpoch, child.ID)
	prepared, e := s.Coordinator.PrepareRecoveryChildRuntime(ctx, id, plan, child, rr.workset)
	if e != nil {
		rr.client.Close()
		if settled, readErr := s.Store.Read(ctx, id); readErr == nil {
			r = settled
		}
		return r, e
	}
	if child.Status == run.StatusCreated {
		child, e = s.Runs.Transition(ctx, child.ID, run.StatusPreparing)
		if e != nil {
			rr.client.Close()
			return r, e
		}
	}
	if child.Status != run.StatusPreparing {
		rr.client.Close()
		return r, recoverystatus.ErrConflict
	}
	if e = s.AppServers.BindAppServer(ctx, child.ID, rr.binding); e != nil {
		rr.client.Close()
		return r, e
	}
	if e = s.detachSource(ctx, r); e != nil {
		rr.client.Close()
		return r, e
	}
	rr.options.RunID = r.RecoveryRunID
	rr.options.RuntimeBinding = prepared.Binding
	rr.options.Model = prepared.Workset.Model
	rr.options.ReasoningEffort = prepared.Workset.ReasoningEffort
	rr.childCWD = prepared.CWD
	var tier *string
	if prepared.Workset.ServiceTier != "" {
		tier = &prepared.Workset.ServiceTier
	}
	rr.options.ServiceTier = tier
	threadRequest := "recovery_child_thread_" + id
	threadPayload := recoveryHash(planID, child.ID, prepared.Workset.ContentHash, prepared.Workset.WorkspaceID, prepared.CWD, rr.binding.ID,
		r.Spec.RunID, r.Spec.TurnID, r.Spec.ThreadID, r.Spec.OperationKind, r.Spec.OperationID, r.Spec.OwnerKind, r.Spec.OwnerID,
		r.Spec.RuntimeWorksetHash, r.SourceResponseDisposition, r.ReportHash, r.InstructionHash, r.SchemaHash,
		"source-terminal", "thread-idle", "no-pending-requests", "quiesced")
	turnRequest := "recovery_child_turn_" + id
	worker, cancel := context.WithCancel(context.WithoutCancel(ctx))
	worker = runtimeepoch.WithFence(worker, r.RuntimeEpoch, r.RecoveryRunID)
	if s.active == nil {
		s.active = map[string]context.CancelFunc{}
	}
	s.active[id] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer rr.client.Close()
		defer func() { s.mu.Lock(); delete(s.active, id); s.mu.Unlock() }()
		var existingThread string
		if receipt, found, readErr := childStore.ReadRecoveryChildThread(worker, threadRequest); readErr != nil {
			s.recordFailure(worker, r, readErr)
			return
		} else if found {
			if receipt.Outcome != "CREATED" || receipt.PayloadSHA256 != threadPayload || receipt.AppServerInstanceID != rr.binding.ID || receipt.BindingHash != recoveryHash(prepared.Workset.ContentHash, rr.binding.ID) {
				s.recordFailure(worker, r, recoverystatus.ErrConflict)
				return
			}
			existingThread = receipt.ChildThreadID
			if existingThread == "" || existingThread == r.Spec.ThreadID {
				s.recordFailure(worker, r, recoverystatus.ErrConflict)
				return
			}
			if _, attachErr := s.Runs.AttachThread(worker, child.ID, existingThread); attachErr != nil {
				s.recordFailure(worker, r, attachErr)
				return
			}
		}
		rr.childThreadID = existingThread
		turnPayload := recoveryHash(threadPayload, turnRequest, r.InstructionHash, r.SchemaHash, prepared.Workset.ContentHash, prepared.CWD)
		result, err := rr.client.StartRecoveryStatusChildTurn(worker, codex.RecoveryStatusChildTurnOptions{
			SourceThreadID: r.Spec.ThreadID, SourceTurnID: r.Spec.TurnID, SourceCWD: rr.cwd,
			ChildThread:           codex.ThreadOptions{CWD: prepared.CWD, RuntimeBinding: prepared.Binding, Model: prepared.Workset.Model, ReasoningEffort: prepared.Workset.ReasoningEffort, ServiceTier: prepared.Workset.ServiceTier},
			ExistingChildThreadID: existingThread,
			ChildTurn: func() codex.TurnOptions {
				o := rr.options
				o.OnStarted = func(thread, turn string) error {
					if e := childStore.RecordRecoveryChildTurn(worker, recoverystatus.ChildTurnReceipt{RequestID: turnRequest, PayloadSHA256: turnPayload, Outcome: "DISPATCHED", ChildThreadID: thread, ChildTurnID: turn, DispatchAttemptID: turnRequest}, time.Now().UTC()); e != nil {
						return e
					}
					_, e := s.Store.BindTurn(worker, id, r.RuntimeEpoch, turn)
					return e
				}
				return o
			}(),
			BeforeThreadCreate: func() error {
				replay, claimErr := childStore.ClaimRecoveryChildThread(worker, recoverystatus.ChildThreadClaim{RecoveryID: id, ChildRunID: child.ID, WorksetHash: prepared.Workset.ContentHash, WorkspaceID: prepared.Workset.WorkspaceID, RuntimeEpoch: r.RuntimeEpoch, RequestID: threadRequest, PayloadSHA256: threadPayload}, time.Now().UTC())
				if claimErr != nil {
					return claimErr
				}
				if replay {
					return errors.New("RECOVERY_CHILD_THREAD_OUTCOME_UNKNOWN")
				}
				return nil
			},
			OnThreadCreated: func(thread string) error {
				if e := childStore.RecordRecoveryChildThread(worker, recoverystatus.ChildThreadReceipt{RequestID: threadRequest, PayloadSHA256: threadPayload, Outcome: "CREATED", ChildThreadID: thread, AppServerInstanceID: rr.binding.ID, BindingHash: recoveryHash(prepared.Workset.ContentHash, rr.binding.ID)}, time.Now().UTC()); e != nil {
					return e
				}
				rr.childThreadID = thread
				_, e := s.Runs.AttachThread(worker, child.ID, thread)
				return e
			},
			BeforeTurnDispatch: func(thread string) error {
				rr.childThreadID = thread
				fresh, checkErr := s.admission(worker, r, rr, r.Spec.TurnID)
				if checkErr != nil {
					return checkErr
				}
				if !bytes.Equal(fresh.Before, r.Admission.Before) {
					return errors.New("RECOVERY_PRE_DISPATCH_DRIFT")
				}
				replay, claimErr := childStore.ClaimRecoveryChildTurn(worker, recoverystatus.ChildTurnClaim{RecoveryID: id, ThreadRequestID: threadRequest, ChildThreadID: thread, RequestID: turnRequest, PayloadSHA256: turnPayload}, time.Now().UTC())
				if claimErr != nil {
					return claimErr
				}
				if replay {
					return errors.New("RECOVERY_CHILD_TURN_OUTCOME_UNKNOWN")
				}
				_, checkErr = s.Store.BeforeStart(worker, id, r.RuntimeEpoch)
				return checkErr
			},
		})
		if err != nil {
			var terminal *codex.TurnError
			if errors.As(err, &terminal) {
				s.recordFailure(worker, r, err)
				return
			}
			code := "RECOVERY_CHILD_THREAD_OUTCOME_UNKNOWN"
			if _, found, readErr := childStore.ReadRecoveryChildThread(worker, threadRequest); readErr == nil && found {
				code = "RECOVERY_CHILD_TURN_OUTCOME_UNKNOWN"
			}
			if unknown, ok := s.Store.(interface {
				MarkUnknown(context.Context, string, string, string) (recoverystatus.Record, error)
			}); ok {
				_, _ = unknown.MarkUnknown(worker, r.ID, r.RuntimeEpoch, code)
			}
			return
		}
		r.RecoveryTurnID = result.TurnID
		if err = s.finish(worker, r, rr, []byte(result.FinalMessage), result.Commands); err != nil {
			s.recordFinishFailure(worker, r, err)
		}
	}()
	return r, nil
}
func (s *RuntimeSupervisor) detachRecoverySource(ctx context.Context, id string) error {
	s.mu.RLock()
	session := s.sessions[id]
	s.mu.RUnlock()
	if session == nil {
		return nil
	}
	current, e := runtimeAuthorityRun(ctx, s.runs, id)
	if e != nil {
		return e
	}
	if current.AgentRuntime == nil || !agentruntime.HasUnresolvedResponse(*current.AgentRuntime) {
		return errors.New("RECOVERY_SOURCE_EXECUTOR_STILL_ACTIVE")
	}
	session.cancel(ErrAppServerDetached)
	select {
	case <-session.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *RecoveryStatusService) Resolve(ctx context.Context, in recoverystatus.ResolveInput) (recoverystatus.Record, error) {
	r, e := s.Store.Read(ctx, in.ID)
	if e != nil {
		return r, e
	}
	if r.State == "RESOLVED" {
		return s.resolveAndRelease(ctx, in)
	}
	rr, e := s.connect(ctx, r)
	if e != nil {
		return r, e
	}
	defer rr.client.Close()
	in.Admission, e = s.admission(ctx, r, rr, r.Spec.TurnID)
	if e != nil {
		return r, e
	}
	return s.resolveAndRelease(ctx, in)
}

func (s *RecoveryStatusService) resolveAndRelease(ctx context.Context, in recoverystatus.ResolveInput) (recoverystatus.Record, error) {
	r, err := s.Store.Resolve(ctx, in)
	if err != nil || r.State != "RESOLVED" || s.Supervisor == nil {
		return r, err
	}
	// Detaching an unresolved source intentionally retains its process-local
	// reservation. Only the durable canonical resolution can release it.
	s.Supervisor.releaseRecoveredSource(context.WithoutCancel(ctx), r.Spec.RunID)
	return r, nil
}

func (s *RuntimeSupervisor) releaseRecoveredSource(ctx context.Context, id string) {
	current, err := s.runs.Get(ctx, id)
	if err != nil || !terminalRun(current.Status) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[id] != nil {
		return
	}
	for owner, runID := range s.operationOwners {
		if runID == id {
			delete(s.operationOwners, owner)
		}
	}
}

func (s *RecoveryStatusService) finish(ctx context.Context, r recoverystatus.Record, rr *recoveryRuntime, raw []byte, nativeCommands ...[]codex.CommandExecution) error {
	if rr.childThreadID == "" || rr.childCWD == "" {
		return recoverystatus.ErrConflict
	}
	if _, e := rr.client.ObserveRecoverySource(ctx, rr.childThreadID, r.RecoveryTurnID, rr.childCWD); e != nil {
		return e
	}
	child, e := s.Runs.GetForRuntimeAuthority(ctx, r.RecoveryRunID)
	if e != nil {
		return e
	}
	if recoveryPending(child) || (child.AgentRuntime != nil && agentruntime.HasUnresolvedResponse(*child.AgentRuntime)) {
		return errors.New("RECOVERY_PENDING_REQUEST")
	}
	// Even unavailable/replaced roots are immutable factual evidence. Preserve
	// every observation so assessment can reject the report without cleanup.
	observations, _ := recoverystatus.ObserveWorkset(ctx, rr.workset)
	after, e := json.Marshal(observations)
	if e != nil {
		return e
	}
	if child.ResultProtocol == run.NaturalResultProtocol {
		store, ok := s.Store.(interface {
			SaveNaturalExecution(context.Context, string, string, []byte, []byte, []byte) (recoverystatus.Record, error)
		})
		if !ok {
			return errors.New("natural recovery result store unavailable")
		}
		captured := run.ExecutionEvidence{Commands: []run.ObservedCommand{}, RecoveryAfter: after}
		if len(nativeCommands) > 0 {
			for _, v := range nativeCommands[0] {
				captured.Commands = append(captured.Commands, run.ObservedCommand{ItemID: v.ItemID, Command: v.Command, CWD: v.CWD, Status: v.Status, Actions: v.Actions, ExitCode: v.ExitCode, Output: v.Output, OutputTruncated: v.OutputTruncated, DurationMillis: v.DurationMS, StartedAt: v.StartedAt, FinishedAt: v.FinishedAt})
			}
		}
		encoded, encodeErr := json.Marshal(captured)
		if encodeErr != nil {
			return encodeErr
		}
		_, e = store.SaveNaturalExecution(ctx, r.ID, r.RuntimeEpoch, raw, after, encoded)
		if e != nil {
			return &recoveryPersistenceError{e}
		}
		return nil
	}
	if _, parseErr := recoverystatus.ParseReport(raw, r.Spec.Source); parseErr != nil {
		// A malformed result cannot erase independently observed probe mutations.
		// Persist observations only after authoritative terminal/quiescent read.
		if store, ok := s.Store.(interface {
			SaveFailedObservation(context.Context, string, string, string, []byte) (recoverystatus.Record, error)
		}); ok {
			if _, persistErr := store.SaveFailedObservation(ctx, r.ID, r.RuntimeEpoch, parseErr.Error(), after); persistErr != nil {
				return &recoveryPersistenceError{persistErr}
			}
		}
		return parseErr
	}
	r, e = s.Store.SaveReport(ctx, r.ID, r.RuntimeEpoch, raw, after)
	if e != nil {
		return &recoveryPersistenceError{e}
	}
	return s.assess(ctx, r, rr)
}
func (s *RecoveryStatusService) assess(ctx context.Context, r recoverystatus.Record, rr *recoveryRuntime) error {
	report, e := recoverystatus.ParseReport(r.Report, r.Spec.Source)
	if e != nil {
		return e
	}
	facts := recoverystatus.FactualObservation{Source: r.Spec.Source, RecoveryRunID: r.RecoveryRunID, ReportID: r.ID + "-report", ReportHash: r.ReportHash, Workset: rr.workset, SourceTurnTerminal: true, BackendQuiescent: true, ScopeValid: true, SourceResponseDisposition: r.SourceResponseDisposition, ObservedAt: r.UpdatedAt}
	if r.Spec.OwnerKind == "PROJECT_INSPECTION" {
		facts.RecoveryTurnID = r.RecoveryTurnID
	}
	if e = json.Unmarshal(r.Admission.Before, &facts.Before); e != nil {
		return e
	}
	if e = json.Unmarshal(r.After, &facts.After); e != nil {
		return e
	}
	if s.OwnerFacts != nil || rr.source.OperationKind == run.OperationInspection {
		// Preserve captured command evidence and supplement it with exact source
		// Turn items when the backend retains them. Missing command history never
		// becomes proof; this read does not mutate the source or settle its answer.
		observed, readErr := rr.client.ObserveRecoverySource(ctx, r.Spec.ThreadID, r.Spec.TurnID, rr.cwd)
		if readErr != nil {
			return readErr
		}
		ownerSource := rr.source
		var snapshot agentruntime.Snapshot
		if ownerSource.AgentRuntime != nil {
			snapshot = *ownerSource.AgentRuntime
		}
		if ownerSource.Purpose == run.PurposeVerification {
			snapshot.Items, e = mergeRecoverySourceItems(snapshot.Items, observed.Turn.Items)
			if e != nil {
				return e
			}
		} else {
			// Other owner contracts do not derive verification check success from
			// this command history. Retain their existing bounded supplementation.
			snapshot.Items = append([]agentruntime.Item(nil), snapshot.Items...)
			for _, rawItem := range observed.Turn.Items {
				if item, include, err := recoverySourceItem(rawItem); err == nil && include {
					snapshot.Items = append(snapshot.Items, item)
				}
			}
		}
		ownerSource.AgentRuntime = &snapshot
		if ownerSource.OperationKind == run.OperationInspection {
			e = s.validateInspectionOwnerFacts(ctx, ownerSource, report, &facts)
		} else {
			e = s.OwnerFacts(ctx, ownerSource, report, &facts)
		}
		if e != nil {
			return e
		}
	}
	assessment, e := recoverystatus.Assess(report, facts)
	if e != nil {
		return e
	}
	encoded, e := json.Marshal(assessment)
	if e != nil {
		return e
	}
	factBytes, e := json.Marshal(facts)
	if e != nil {
		return e
	}
	_, e = s.Store.SaveAssessment(ctx, r.ID, r.RuntimeEpoch, encoded, factBytes)
	if e != nil {
		return &recoveryPersistenceError{e}
	}
	return nil
}

func (s *RecoveryStatusService) LatestForSource(ctx context.Context, id string) (recoverystatus.Record, error) {
	return s.Store.LatestForSource(ctx, id)
}

func (s *RecoveryStatusService) recordFailure(ctx context.Context, r recoverystatus.Record, err error) {
	if ctx.Err() != nil {
		return
	}
	if store, ok := s.Store.(interface {
		Fail(context.Context, string, string, string) (recoverystatus.Record, error)
	}); ok {
		_, _ = store.Fail(ctx, r.ID, r.RuntimeEpoch, err.Error())
	}
}

// Recover resumes only an already persisted child Turn or local persistence
// steps. RUN_CREATED and uncertain turn/start never cause automatic dispatch.
func (s *RecoveryStatusService) Recover(ctx context.Context, id string) (recoverystatus.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.Store.Read(ctx, id)
	if e != nil {
		return r, e
	}
	if s.closed {
		return r, errors.New("recovery service closed")
	}
	if s.active[id] != nil || r.State == "RESOLVED" || r.State == "FAILED" {
		return r, nil
	}
	r, e = s.Store.Adopt(ctx, id)
	if e != nil {
		return r, e
	}
	// A known turn response may have committed just before the owner binding.
	// Recover that local write from its immutable receipt; an unresolved claim
	// still remains UNKNOWN and can never authorize another turn/start.
	if r.RecoveryTurnID == "" && r.RecoveryRunID != "" && r.State == "UNKNOWN" {
		if childStore, ok := s.Store.(recoveryChildStore); ok {
			requestID := "recovery_child_turn_" + id
			receipt, found, readErr := childStore.ReadRecoveryChildTurn(ctx, requestID)
			if readErr != nil {
				return r, readErr
			}
			if found && receipt.Outcome == "DISPATCHED" {
				if receipt.RequestID != requestID || receipt.DispatchAttemptID != requestID || receipt.ChildTurnID == "" || receipt.ChildThreadID == "" || receipt.ChildThreadID == r.Spec.ThreadID {
					return r, recoverystatus.ErrConflict
				}
				r, e = s.Store.BindTurn(ctx, id, r.RuntimeEpoch, receipt.ChildTurnID)
				if e != nil {
					return r, e
				}
			}
		}
	}
	if r.RecoveryTurnID == "" || r.State == "ASSESSED" {
		return r, nil
	}
	rr, e := s.connect(ctx, r)
	if e != nil {
		return r, e
	}
	child, e := s.Runs.Get(ctx, r.RecoveryRunID)
	if e != nil || child.ThreadID == "" || child.ThreadID == r.Spec.ThreadID || child.RuntimeWorkset == nil {
		rr.client.Close()
		return r, errors.Join(e, recoverystatus.ErrConflict)
	}
	if child.ResultProtocol == run.NaturalResultProtocol && child.Status == run.StatusSucceeded && len(r.Report) == 0 {
		rr.client.Close()
		return r, nil
	}
	if rr.childCWD, rr.options.RuntimeBinding, e = multiRuntimeClientBinding(*child.RuntimeWorkset); e != nil {
		rr.client.Close()
		return r, e
	}
	rr.childThreadID = child.ThreadID
	rr.options.ThreadID = child.ThreadID
	rr.options.Model = child.RuntimeWorkset.Model
	rr.options.ReasoningEffort = child.RuntimeWorkset.ReasoningEffort
	worker, cancel := context.WithCancel(context.WithoutCancel(ctx))
	worker = runtimeepoch.WithFence(worker, r.RuntimeEpoch, r.RecoveryRunID)
	if s.active == nil {
		s.active = map[string]context.CancelFunc{}
	}
	s.active[id] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer rr.client.Close()
		defer func() { s.mu.Lock(); delete(s.active, id); s.mu.Unlock() }()
		if len(r.Report) > 0 {
			if e := s.assess(worker, r, rr); e != nil {
				s.recordFinishFailure(worker, r, e)
			}
			return
		}
		if s.Config.AgentRuntime == nil {
			s.recordFailure(worker, r, errors.New("recovery runtime request service unavailable"))
			return
		}
		snapshot, readErr := s.Config.AgentRuntime.Read(worker, r.RecoveryRunID)
		var session *agentruntime.Session
		var err error
		if readErr == nil && snapshot.ConnectionID != "" {
			session, err = s.Config.AgentRuntime.RestoreTurn(worker, r.RecoveryRunID, rr.childThreadID, r.RecoveryTurnID, rr.binding, true)
		} else {
			session = s.Config.AgentRuntime.NewTurn(worker, r.RecoveryRunID, rr.childThreadID, true)
			err = session.Bind(r.RecoveryTurnID)
		}
		if err != nil {
			if session != nil {
				session.Close()
			}
			s.recordFailure(worker, r, err)
			return
		}
		result, err := rr.client.RejoinTurn(worker, codex.TurnRejoinOptions{TurnOptions: rr.options, TurnID: r.RecoveryTurnID, RuntimeSession: session})
		if err != nil {
			s.recordTransportFailure(worker, r, err)
			return
		}
		if err = s.finish(worker, r, rr, []byte(result.FinalMessage), result.Commands); err != nil {
			s.recordFinishFailure(worker, r, err)
		}
	}()
	return r, nil
}
func (s *RecoveryStatusService) RecoverAll(ctx context.Context) error {
	records, e := s.Store.ListRecoverable(ctx)
	if e != nil {
		return e
	}
	for _, r := range records {
		if _, e = s.Recover(ctx, r.ID); e != nil {
			// An unavailable original instance is a record-local failed recovery, never
			// permission to replace its Thread or prevent unrelated projects opening.
			current, readErr := s.Store.Read(ctx, r.ID)
			if readErr != nil {
				return readErr
			}
			s.recordFailure(ctx, current, e)
		}
	}
	return nil
}

func (s *RecoveryStatusService) recordTransportFailure(ctx context.Context, r recoverystatus.Record, err error) {
	if ctx.Err() != nil {
		return
	}
	var terminal *codex.TurnError
	if errors.As(err, &terminal) {
		s.recordFailure(ctx, r, err)
		return
	}
	if store, ok := s.Store.(interface {
		MarkUnknown(context.Context, string, string, string) (recoverystatus.Record, error)
	}); ok {
		_, _ = store.MarkUnknown(ctx, r.ID, r.RuntimeEpoch, err.Error())
	}
}

type recoveryPersistenceError struct{ err error }

func (e *recoveryPersistenceError) Error() string { return e.err.Error() }
func (e *recoveryPersistenceError) Unwrap() error { return e.err }
func (s *RecoveryStatusService) recordFinishFailure(ctx context.Context, r recoverystatus.Record, err error) {
	var persistence *recoveryPersistenceError
	if errors.As(err, &persistence) {
		s.recordTransportFailure(ctx, r, err)
		return
	}
	s.recordFailure(ctx, r, err)
}

// CancelRecovery participates in the ordinary canonical cancellation facade.
// It never signals the old source Turn while a recovery child owns the Thread.
func (s *RecoveryStatusService) CancelRecovery(ctx context.Context, in executioncancel.Input) (executioncancel.Receipt, bool, error) {
	var empty executioncancel.Receipt
	r, e := s.Store.LatestForSource(ctx, in.RunID)
	if errors.Is(e, recoverystatus.ErrNotFound) {
		if lookup, ok := s.Store.(interface {
			GetByRecoveryRun(context.Context, string) (recoverystatus.Record, error)
		}); ok {
			r, e = lookup.GetByRecoveryRun(ctx, in.RunID)
		}
	}
	if errors.Is(e, recoverystatus.ErrNotFound) {
		return empty, false, nil
	}
	if e != nil {
		return empty, true, e
	}
	if r.RecoveryRunID == "" {
		return empty, false, nil
	}
	in = in.Normalized()
	if in.ProjectID != r.Spec.ProjectID || in.OwnerType != r.Spec.OperationKind || in.OwnerID != r.Spec.OperationID {
		return empty, true, executioncancel.ErrRunExecutionMismatch
	}
	store, ok := s.Store.(interface {
		BeginCancel(context.Context, string, executioncancel.Input) (recoverystatus.Record, executioncancel.Receipt, bool, error)
		RecordCancelInterrupt(context.Context, string, executioncancel.Input, string) (executioncancel.Receipt, error)
		FinishCancel(context.Context, string, executioncancel.Input, recoverystatus.Admission) (recoverystatus.Record, executioncancel.Receipt, error)
	})
	if !ok {
		return empty, true, executioncancel.ErrRecoveryRequired
	}
	r, receipt, dispatch, e := store.BeginCancel(ctx, r.ID, in)
	if e != nil {
		return receipt, true, e
	}
	if receipt.State == "CANCELED" || receipt.State == "ALREADY_TERMINAL" {
		if s.Supervisor != nil {
			s.Supervisor.releaseRecoveredSource(context.WithoutCancel(ctx), r.Spec.RunID)
		}
		return receipt, true, nil
	}
	if r.RecoveryTurnID == "" {
		return receipt, true, executioncancel.ErrOutcomeUnknown
	}
	rr, e := s.connect(ctx, r)
	if e != nil {
		return receipt, true, e
	}
	defer rr.client.Close()
	child, e := s.Runs.Get(ctx, r.RecoveryRunID)
	if e != nil || child.ThreadID == "" || child.ThreadID == r.Spec.ThreadID || child.RuntimeWorkset == nil {
		return receipt, true, errors.Join(e, recoverystatus.ErrConflict)
	}
	childCWD, _, e := multiRuntimeClientBinding(*child.RuntimeWorkset)
	if e != nil {
		return receipt, true, e
	}
	observed, e := rr.client.ObserveLoadedTurn(ctx, child.ThreadID, r.RecoveryTurnID)
	if e != nil {
		return receipt, true, e
	}
	if observed.CWD != childCWD {
		return receipt, true, recoverystatus.ErrConflict
	}
	if observed.Turn.Status == "inProgress" {
		if !dispatch {
			return receipt, true, executioncancel.ErrOutcomeUnknown
		}
		recorded, interruptErr := interruptRecoveryChild(ctx, rr.client, store, r.ID, in, child.ThreadID, r.RecoveryTurnID)
		if recorded.RequestID != "" {
			receipt = recorded
		}
		if interruptErr != nil {
			return receipt, true, interruptErr
		}
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if _, e = rr.client.ObserveRecoverySource(wait, child.ThreadID, r.RecoveryTurnID, childCWD); e == nil {
			break
		}
		select {
		case <-wait.Done():
			return receipt, true, executioncancel.ErrOutcomeUnknown
		case <-time.After(25 * time.Millisecond):
		}
	}
	// Join only the diagnostic local executor after the exact backend Turn is
	// terminal. This is not transport detachment as a substitute for quiescence.
	s.mu.Lock()
	stop := s.active[r.ID]
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	for {
		s.mu.Lock()
		active := s.active[r.ID] != nil
		s.mu.Unlock()
		if !active {
			break
		}
		select {
		case <-wait.Done():
			return receipt, true, executioncancel.ErrOutcomeUnknown
		case <-time.After(10 * time.Millisecond):
		}
	}
	observations, _ := recoverystatus.ObserveWorkset(wait, rr.workset)
	raw, e := json.Marshal(observations)
	if e != nil {
		return receipt, true, e
	}
	a := recoverystatus.Admission{SourceTurnTerminal: true, ThreadIdle: true, NoPendingRequests: true, Quiesced: true, Before: raw, ObservedAt: time.Now().UTC()}
	_, receipt, e = store.FinishCancel(ctx, r.ID, in, a)
	if e == nil && s.Supervisor != nil {
		s.Supervisor.releaseRecoveredSource(context.WithoutCancel(ctx), r.Spec.RunID)
	}
	return receipt, true, e
}

type recoveryInterruptRecorder interface {
	RecordCancelInterrupt(context.Context, string, executioncancel.Input, string) (executioncancel.Receipt, error)
}

func interruptRecoveryChild(ctx context.Context, client interface {
	InterruptTurn(context.Context, string, string) error
}, store recoveryInterruptRecorder, recoveryID string, in executioncancel.Input, thread, turn string) (executioncancel.Receipt, error) {
	rpcErr := client.InterruptTurn(ctx, thread, turn)
	outcome := "ACKNOWLEDGED"
	if rpcErr != nil {
		outcome = "OUTCOME_UNKNOWN"
	}
	// The caller may disconnect after transmission. Persist the outcome before
	// returning; this never retries the interrupt or substitutes for termination.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	receipt, recordErr := store.RecordCancelInterrupt(writeCtx, recoveryID, in, outcome)
	return receipt, errors.Join(rpcErr, recordErr)
}

// ResumeAnalysis continues local assessment after an explicit GPT submission.
// It waits for the existing diagnostic worker to detach; no new Turn is sent.
func (s *RecoveryStatusService) ResumeAnalysis(ctx context.Context, id string) {
	ctx = context.WithoutCancel(ctx)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		for {
			s.mu.Lock()
			closed, active := s.closed, s.active[id] != nil
			s.mu.Unlock()
			if closed {
				return
			}
			if !active {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		_, _ = s.Recover(context.WithoutCancel(ctx), id)
	}()
}
