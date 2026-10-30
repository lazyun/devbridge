package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/workset"
)

type InspectionRuntimeStore struct{ db *DB }

func NewInspectionRuntimeStore(db *DB) *InspectionRuntimeStore {
	return &InspectionRuntimeStore{db: db}
}

var _ probeexecution.CanonicalStore = (*InspectionRuntimeStore)(nil)

func (s *InspectionRuntimeStore) ConfirmedInspection(ctx context.Context, session, id string) (probeexecution.CanonicalSpec, error) {
	p, err := NewContextProbeStore(s.db).Get(ctx, id)
	if err != nil {
		return probeexecution.CanonicalSpec{}, err
	}
	if p.AISessionID != session {
		return probeexecution.CanonicalSpec{}, contextprobe.ErrNotFound
	}
	if p.Canonical == nil || p.Canonical.ContractVersion != contextprobe.CanonicalInspectionContractVersion {
		return probeexecution.CanonicalSpec{}, errors.New("LEGACY_INSPECTION_NO_CANONICAL_RUNTIME")
	}
	plan := p.Canonical
	if plan.InspectionID != p.ID || plan.TaskID != p.TaskID || plan.ProjectID != p.ProjectID || plan.AISessionID != p.AISessionID || plan.CodexInstruction != p.CodexInstruction || plan.PayloadSHA256 != p.CanonicalPayloadSHA256 {
		return probeexecution.CanonicalSpec{}, contextprobe.ErrConflict
	}
	raw, _ := json.Marshal(plan)
	spec := probeexecution.CanonicalSpec{Probe: p, Receipt: raw, RequestID: plan.RequestID, PayloadHash: plan.PayloadSHA256, WorkspaceID: p.WorkspaceID, PrimaryMemberID: plan.PrimaryMemberID, BackendSelection: plan.Backend, Model: plan.Model, ReasoningEffort: plan.ReasoningEffort, ServiceTier: plan.ServiceTier}
	for _, t := range plan.Targets {
		spec.Targets = append(spec.Targets, workset.Target{RepositoryID: t.RepositoryID, MemberID: t.MemberID, MemberVersion: t.MemberVersion, OriginCommit: t.OriginCommit, OriginRef: t.OriginRef, Read: true, ReadScope: workset.ReadScope{Kind: string(t.Scope.Kind), Paths: append([]string(nil), t.Scope.Paths...)}, Write: workset.WriteScope{Kind: "NONE"}})
	}
	return spec, nil
}
func inspectionRuntimeRead(ctx context.Context, q definitionQuerier, id string) (probeexecution.CanonicalState, error) {
	var raw []byte
	var epoch string
	var state probeexecution.CanonicalState
	err := q.QueryRowContext(ctx, `SELECT r.record_json,r.runtime_epoch FROM inspection_runtime_records r LEFT JOIN context_probes p ON p.id=r.inspection_id WHERE r.run_id=? OR r.inspection_id=? ORDER BY CASE WHEN r.run_id=p.recovery_successor_run_id THEN 0 ELSE 1 END, r.created_at DESC LIMIT 1`, id, id).Scan(&raw, &epoch)
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(raw, &state)
	state.Run.RuntimeEpoch = epoch
	state.Spec.Probe.RuntimeEpoch = epoch
	if err == nil {
		var natural bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_execution_records WHERE run_id=?)`, state.Run.ID).Scan(&natural)
		if natural {
			state.Run.ResultProtocol = run.NaturalResultProtocol
		}
	}
	return state, err
}
func saveInspectionRuntime(ctx context.Context, tx *sql.Tx, s probeexecution.CanonicalState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE inspection_runtime_records SET runtime_epoch=?,state=?,record_json=?,updated_at=? WHERE run_id=?`, s.Run.RuntimeEpoch, s.State, string(raw), formatTime(s.UpdatedAt), s.Run.ID)
	return err
}
func (s *InspectionRuntimeStore) InspectionState(ctx context.Context, id string) (probeexecution.CanonicalState, error) {
	return inspectionRuntimeRead(ctx, s.db.sql, id)
}

func (s *InspectionRuntimeStore) ReserveInspection(ctx context.Context, spec probeexecution.CanonicalSpec) (probeexecution.CanonicalState, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return probeexecution.CanonicalState{}, err
	}
	defer tx.Rollback()
	state, err := inspectionRuntimeRead(ctx, tx, spec.Probe.ID)
	if err == nil {
		if state.Spec.PayloadHash != spec.PayloadHash {
			return state, contextprobe.ErrConflict
		}
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	p, err := scanContextProbe(tx.QueryRowContext(ctx, selectContextProbe+` WHERE id=?`, spec.Probe.ID))
	if err != nil {
		return state, err
	}
	if p.Canonical == nil || p.Status != contextprobe.StatusCreated || p.RunID != "" || p.CanonicalPayloadSHA256 != spec.PayloadHash || p.CodexInstruction != spec.Probe.CodexInstruction {
		return state, contextprobe.ErrConflict
	}
	confirmedReceipt, marshalErr := json.Marshal(p.Canonical)
	if marshalErr != nil || !bytes.Equal(confirmedReceipt, spec.Receipt) || spec.RequestID != p.Canonical.RequestID || !reflect.DeepEqual(spec.BackendSelection, p.Canonical.Backend) || spec.Model != p.Canonical.Model || spec.ReasoningEffort != p.Canonical.ReasoningEffort || spec.ServiceTier != p.Canonical.ServiceTier || spec.PrimaryMemberID != p.Canonical.PrimaryMemberID || len(spec.Targets) != len(p.Canonical.Targets) {
		return state, contextprobe.ErrConflict
	}
	for i, t := range p.Canonical.Targets {
		actual := spec.Targets[i]
		if actual.RepositoryID != t.RepositoryID || actual.MemberID != t.MemberID || actual.MemberVersion != t.MemberVersion || actual.OriginCommit != t.OriginCommit || actual.OriginRef != t.OriginRef || !actual.Read || actual.Write.Kind != "NONE" || len(actual.Write.Paths) != 0 || actual.ReadScope.Kind != string(t.Scope.Kind) || !slices.Equal(actual.ReadScope.Paths, t.Scope.Paths) {
			return state, contextprobe.ErrConflict
		}
		if err := ensureRepositoryActiveTx(ctx, tx, p.ProjectID, actual.RepositoryID); err != nil {
			return state, err
		}
	}
	now := time.Now().UTC()
	id := "inspection_" + p.ID
	if _, err = tx.ExecContext(ctx, `UPDATE context_probes SET status='RUNNING',runtime_epoch=?,started_at=?,updated_at=? WHERE id=? AND run_id IS NULL AND status='CREATED'`, s.db.epoch, formatTime(now), formatTime(now), p.ID); err != nil {
		return state, err
	}
	var attempt int
	if err = tx.QueryRowContext(ctx, `UPDATE tasks SET next_attempt=MAX(next_attempt,(SELECT COALESCE(MAX(attempt),0) FROM runs WHERE task_id=?))+1 WHERE id=? RETURNING next_attempt`, p.TaskID, p.TaskID).Scan(&attempt); err != nil {
		return state, err
	}
	v := run.Run{ID: id, ProjectID: p.ProjectID, TaskID: p.TaskID, OperationKind: run.OperationInspection, OperationID: p.ID, ContextProbeID: p.ID, Purpose: run.PurposeInspection, Status: run.StatusPreparing, RuntimeEpoch: s.db.epoch, CreatedAt: now, UpdatedAt: now, Attempt: attempt}
	if err = insertRunAgentExecutionTx(ctx, tx, &v); err != nil {
		return state, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(id,task_id,project_id,attempt,status,purpose,operation_kind,operation_id,context_probe_id,runtime_epoch,created_at,updated_at,agent_execution_id) VALUES(?,?,?,?,'PREPARING','INSPECTION','INSPECTION',?,?,?,?,?,?)`, id, p.TaskID, p.ProjectID, attempt, p.ID, p.ID, s.db.epoch, formatTime(now), formatTime(now), v.AgentExecutionID)
	if err != nil {
		return state, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_token_usage(run_id,status,updated_at) VALUES(?,'PENDING',?)`, id, formatTime(now))
	if err != nil {
		return state, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE context_probes SET run_id=?,runtime_epoch=?,started_at=?,updated_at=? WHERE id=? AND run_id IS NULL AND status='RUNNING'`, id, s.db.epoch, formatTime(now), formatTime(now), p.ID)
	if err != nil {
		return state, err
	}
	if err = inspectionClaimTx(ctx, tx, p.ID, p.TaskID, id, p.ProjectID, s.db.epoch, formatTime(now)); err != nil {
		return state, err
	}
	conversation, err := newInspectionConversationTx(ctx, tx, p.ProjectID, p.ID, "", now)
	if err != nil {
		return state, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_operation_boundaries(run_id,agent_conversation_id,entry_boundary_turn_id,advance_state) VALUES(?,?,?,'NO_ADVANCE')`, id, conversation.ID, nullString(conversation.CurrentBoundaryTurnID))
	if err != nil {
		return state, err
	}
	p.RunID, p.RuntimeEpoch, p.AgentExecutionID, p.Status = id, s.db.epoch, v.AgentExecutionID, contextprobe.StatusRunning
	p.StartedAt = &now
	p.UpdatedAt = now
	spec.Probe = p
	state = probeexecution.CanonicalState{Spec: spec, Run: v, Conversation: conversation, State: "PREPARED", UpdatedAt: now}
	raw, _ := json.Marshal(state)
	_, err = tx.ExecContext(ctx, `INSERT INTO inspection_runtime_records(run_id,inspection_id,runtime_epoch,state,record_json,created_at,updated_at) VALUES(?,?,?,'PREPARED',?,?,?)`, id, p.ID, s.db.epoch, string(raw), formatTime(now), formatTime(now))
	if err != nil {
		return state, err
	}
	return state, tx.Commit()
}
func (s *InspectionRuntimeStore) mutate(ctx context.Context, id, epoch string, fn func(*sql.Tx, *probeexecution.CanonicalState) error) (probeexecution.CanonicalState, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return probeexecution.CanonicalState{}, err
	}
	defer tx.Rollback()
	state, err := inspectionRuntimeRead(ctx, tx, id)
	if err != nil {
		return state, err
	}
	if epoch == "" || state.Run.RuntimeEpoch != epoch || epoch != s.db.epoch {
		return state, runtimeepoch.ErrFenced
	}
	// Detaching the source runner is not an authority fence.  Once an exact
	// recovery child exists, no ordinary source callback may save a report or
	// finalize the old canonical run, even when it still carries the old claim
	// and epoch.  Recovery resolution invokes finalizeInspectionTx directly in
	// its own transaction; a receipt-bound successor has a different Run ID.
	if state.ContinuationRecoveryStatusID == "" {
		var recoveryActive bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_status_records WHERE source_run_id=? AND recovery_run_id IS NOT NULL)`, state.Run.ID).Scan(&recoveryActive); err != nil {
			return state, err
		}
		if recoveryActive {
			return state, runtimeepoch.ErrFenced
		}
	}
	if state.Finalization == nil {
		var exact bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM project_inspection_agent_claims c JOIN context_probes p ON p.id=c.inspection_id JOIN runs r ON r.id=c.run_id
 WHERE c.run_id=? AND c.inspection_id=? AND c.runtime_epoch=? AND r.task_id=c.task_id AND r.runtime_epoch=c.runtime_epoch
   AND (p.run_id=c.run_id AND r.context_probe_id=p.id
     OR EXISTS(SELECT 1 FROM inspection_recovery_continuations k JOIN recovery_status_records x ON x.id=k.recovery_id WHERE k.successor_run_id=c.run_id AND k.inspection_id=p.id AND x.state='RESOLVED' AND json_extract(x.record_json,'$.assessment.engineeringDisposition')='RECOVERED_PARTIAL'))
)`, state.Run.ID, state.Spec.Probe.ID, epoch).Scan(&exact)
		if err != nil {
			return state, err
		}
		if !exact {
			return state, runtimeepoch.ErrFenced
		}
	}
	if err = fn(tx, &state); err != nil {
		return state, err
	}
	state.UpdatedAt = time.Now().UTC()
	if err = saveInspectionRuntime(ctx, tx, state); err != nil {
		return state, err
	}
	return state, tx.Commit()
}
func (s *InspectionRuntimeStore) BeginInspection(ctx context.Context, spec probeexecution.CanonicalSpec, w workset.RuntimeWorkset, before []recoverystatus.WorktreeObservation) (probeexecution.CanonicalState, error) {
	if w.AgentID != "" || w.AgentConfigFingerprint != "" || w.ExecutionPolicy != "" || w.Validate() != nil || w.ProtocolVersion != workset.RuntimeProtocolVersionProjectInspection || w.InspectionOperationID != spec.Probe.ID || w.InspectionOwnerTaskID != spec.Probe.TaskID || w.ProjectID != spec.Probe.ProjectID || len(before) != len(w.Members) {
		return probeexecution.CanonicalState{}, contextprobe.ErrConflict
	}
	return s.mutate(ctx, w.RunID, s.db.epoch, func(tx *sql.Tx, state *probeexecution.CanonicalState) error {
		if state.State == "RUN_CREATED" && state.Workset.ContentHash == w.ContentHash {
			return nil
		}
		if state.State != "PREPARED" || state.Spec.PayloadHash != spec.PayloadHash {
			return contextprobe.ErrConflict
		}
		if len(state.Spec.Targets) != len(w.Members) {
			return contextprobe.ErrConflict
		}
		if w.ServiceTier != state.Spec.ServiceTier || w.PrimaryMemberID != state.Spec.PrimaryMemberID || w.BackendConfigID != state.Spec.BackendSelection.ConfigID || w.BackendConfigFingerprint != state.Spec.BackendSelection.ConfigFingerprint || w.Model != state.Spec.Model || w.ReasoningEffort != state.Spec.ReasoningEffort {
			return contextprobe.ErrConflict
		}
		for i, m := range w.Members {
			target := state.Spec.Targets[i]
			o := before[i]
			if m.RepositoryID != target.RepositoryID || m.MemberID != target.MemberID || m.MemberVersion != target.MemberVersion || m.OriginCommit != target.OriginCommit || !reflect.DeepEqual(m.IntendedReadScope, target.ReadScope) || m.Write.Kind != "NONE" || o.MemberID != m.MemberID || !o.Available || o.RootIdentity != m.RootFileIdentity || o.HEAD != m.StartHEAD || o.Snapshot != m.StartSnapshot {
				return contextprobe.ErrConflict
			}
			write, _ := json.Marshal(m.Write)
			_, err := tx.ExecContext(ctx, `INSERT INTO run_repository_bindings(run_id,inspection_id,project_id,workspace_id,repository_id,member_id,member_version,origin_commit,worktree_id,preparation,repository_alias,start_head,start_snapshot,intended_read,intended_write_scope_json,runtime_root_identity,root_path,root_identity) VALUES(?,?,?,?,?,?,?,?,?,'READY',?,?,?,1,?,?,?,?)`, w.RunID, spec.Probe.ID, w.ProjectID, w.WorkspaceID, m.RepositoryID, m.MemberID, m.MemberVersion, m.OriginCommit, m.WorktreeID, m.RepositoryAlias, m.StartHEAD, m.StartSnapshot, string(write), m.RuntimeRootIdentity, m.CanonicalRoot, m.RootFileIdentity)
			if err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(w)
		_, err := tx.ExecContext(ctx, `UPDATE runs SET workspace_id=?,runtime_workset_json=?,runtime_workset_hash=?,start_snapshot=(SELECT start_snapshot FROM run_repository_bindings WHERE run_id=? AND member_id=?) WHERE id=? AND status='PREPARING'`, w.WorkspaceID, string(raw), w.ContentHash, w.RunID, w.PrimaryMemberID, w.RunID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE context_probes SET workspace_id=?,runtime_workset_json=?,runtime_workset_hash=? WHERE id=?`, w.WorkspaceID, string(raw), w.ContentHash, spec.Probe.ID)
		if err != nil {
			return err
		}
		state.Workset = w
		state.Before = before
		state.Schema = probeexecution.InspectionReportSchema(probeexecution.InspectionSourceForWorkset(w))
		state.State = "RUN_CREATED"
		state.Run.RuntimeWorkset = &w
		state.Run.WorkspaceID = w.WorkspaceID
		state.Spec.WorkspaceID = w.WorkspaceID
		return nil
	})
}
func inspectionNoCancellationTx(ctx context.Context, tx *sql.Tx, id string) error {
	var blocked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE run_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED','CANCELED'))`, id).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return contextprobe.ErrConflict
	}
	return nil
}
func (s *InspectionRuntimeStore) InspectionBeforeThread(ctx context.Context, id, epoch string) (probeexecution.CanonicalState, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, state *probeexecution.CanonicalState) error {
		if state.State != "RUN_CREATED" || state.Run.ThreadID != "" {
			return contextprobe.ErrConflict
		}
		if err := inspectionNoCancellationTx(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET thread_start_pending=1 WHERE run_id=? AND thread_start_pending=0`, id)
		state.State = "UNKNOWN"
		state.FailureReason = "THREAD_START_PENDING"
		return err
	})
}
func (s *InspectionRuntimeStore) InspectionBindThread(ctx context.Context, id, epoch, thread string) (probeexecution.CanonicalState, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, state *probeexecution.CanonicalState) error {
		if thread == "" || state.State != "UNKNOWN" || state.FailureReason != "THREAD_START_PENDING" || state.Run.ThreadID != "" {
			return contextprobe.ErrConflict
		}
		backend, _ := json.Marshal(state.Spec.BackendSelection)
		_, err := tx.ExecContext(ctx, `UPDATE runs SET thread_id=? WHERE id=? AND thread_id IS NULL`, thread, id)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET backend_thread_id=?,initial_model=?,initial_model_source_run_id=?,initial_model_source_thread_id=?,initial_model_evidence='PROJECT_INSPECTION_CONFIRMED_RUNTIME',backend_selection_json=?,backend_selection_source_run_id=?,backend_selection_source_thread_id=?,backend_selection_evidence='PROJECT_INSPECTION_CONFIRMED_RUNTIME' WHERE id=? AND backend_thread_id IS NULL`, thread, state.Spec.Model, id, thread, string(backend), id, thread, state.Conversation.ID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE context_probes SET thread_id=? WHERE id=? AND thread_id IS NULL`, thread, state.Spec.Probe.ID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET thread_start_pending=0 WHERE run_id=?`, id)
		state.Run.ThreadID = thread
		state.Spec.Probe.ThreadID = thread
		state.Conversation.BackendThreadID = thread
		state.State = "RUN_CREATED"
		state.FailureReason = ""
		return err
	})
}
func (s *InspectionRuntimeStore) InspectionBeforeTurn(ctx context.Context, id, epoch string) (probeexecution.CanonicalState, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, state *probeexecution.CanonicalState) error {
		if state.State != "RUN_CREATED" || state.Run.ThreadID == "" || state.Run.TurnID != "" {
			return contextprobe.ErrConflict
		}
		if err := inspectionNoCancellationTx(ctx, tx, id); err != nil {
			return err
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO run_execution_records(run_id,protocol,created_at) VALUES(?,'NATURAL_V1',?) ON CONFLICT(run_id) DO NOTHING`, id, formatTime(time.Now().UTC())); e != nil {
			return e
		}
		state.Run.ResultProtocol = run.NaturalResultProtocol
		now := time.Now().UTC()
		prompt := state.Spec.Probe.CodexInstruction
		hash := recoverystatus.Hash([]byte(prompt))
		_, err := tx.ExecContext(ctx, `UPDATE runs SET final_codex_prompt=?,prompt_sha256=?,prompt_bytes=?,prompt_sent_at=? WHERE id=? AND final_codex_prompt IS NULL`, prompt, hash, len([]byte(prompt)), formatTime(now), id)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE context_probes SET final_codex_prompt=?,prompt_sha256=?,prompt_bytes=?,prompt_sent_at=? WHERE id=? AND final_codex_prompt IS NULL`, prompt, hash, len([]byte(prompt)), formatTime(now), state.Spec.Probe.ID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET advance_state='UNKNOWN' WHERE run_id=? AND advance_state='NO_ADVANCE'`, id)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET continuation_safe=0 WHERE id=?`, state.Conversation.ID)
		state.State = "UNKNOWN"
		state.FailureReason = "TURN_START_PENDING"
		return err
	})
}
func (s *InspectionRuntimeStore) InspectionBindTurn(ctx context.Context, id, epoch, turn string) (probeexecution.CanonicalState, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, state *probeexecution.CanonicalState) error {
		if turn == "" || state.Run.ThreadID == "" || state.Run.TurnID != "" || state.State != "UNKNOWN" {
			return contextprobe.ErrConflict
		}
		_, err := tx.ExecContext(ctx, `UPDATE runs SET turn_id=?,status='RUNNING',started_at=COALESCE(started_at,?) WHERE id=? AND turn_id IS NULL`, turn, formatTime(time.Now().UTC()), id)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE context_probes SET turn_id=? WHERE id=? AND turn_id IS NULL`, turn, state.Spec.Probe.ID)
		state.Run.TurnID = turn
		state.Run.Status = run.StatusRunning
		state.Spec.Probe.TurnID = turn
		state.State = "RUNNING"
		state.FailureReason = ""
		return err
	})
}
func (s *InspectionRuntimeStore) InspectionSaveReport(ctx context.Context, id, epoch string, report []byte) (probeexecution.CanonicalState, error) {
	return s.mutate(ctx, id, epoch, func(_ *sql.Tx, state *probeexecution.CanonicalState) error {
		if len(report) == 0 || len(report) > 1<<20 || state.Run.TurnID == "" || state.Finalization != nil {
			return contextprobe.ErrConflict
		}
		if len(state.Report) > 0 {
			if !bytes.Equal(state.Report, report) {
				return contextprobe.ErrConflict
			}
			return nil
		}
		state.Report = append([]byte(nil), report...)
		state.ReportHash = recoverystatus.Hash(report)
		state.State = "REPORT_READY"
		return nil
	})
}
func (s *InspectionRuntimeStore) MarkInspectionUnknown(ctx context.Context, id, epoch, reason string) (probeexecution.CanonicalState, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, state *probeexecution.CanonicalState) error {
		if state.Finalization != nil {
			return nil
		}
		state.FailureReason = reason
		if len(state.Report) == 0 {
			state.State = "RECOVERY_REQUIRED"
		}
		// A receipt-bound successor deliberately leaves p.run_id and the original
		// source Turn immutable.  Its current recovery limitation is instead
		// anchored by recovery_successor_run_id.
		_, err := tx.ExecContext(ctx, `UPDATE context_probes SET runtime_limitation='RECOVERY_REQUIRED',failure_message=? WHERE id=? AND (run_id=? OR recovery_successor_run_id=?)`, reason, state.Spec.Probe.ID, id, id)
		state.Spec.Probe.RuntimeLimitation = "RECOVERY_REQUIRED"
		state.Spec.Probe.FailureMessage = reason
		return err
	})
}

func (s *InspectionRuntimeStore) FinalizeInspection(ctx context.Context, in probeexecution.InspectionFinalization) (probeexecution.CanonicalState, error) {
	return s.mutate(ctx, in.RunID, in.RuntimeEpoch, func(tx *sql.Tx, state *probeexecution.CanonicalState) error {
		return s.finalizeInspectionTx(ctx, tx, state, in, false)
	})
}

// finalizeInspectionTx is the single canonical reducer for ordinary and
// receipt-authorized recovery finalization. Recovery callers enter only after
// RecoveryStatusStore has verified its immutable assessment.
func (s *InspectionRuntimeStore) finalizeInspectionTx(ctx context.Context, tx *sql.Tx, state *probeexecution.CanonicalState, in probeexecution.InspectionFinalization, recovery bool) error {
	if recovery && len(state.Report) == 0 {
		// The recovered bytes are admitted only by RecoveryStatusStore after its
		// immutable owner/result assessment. They fill a missing transport slot;
		// source Turn and response-disposition history remain untouched.
		state.Report = append([]byte(nil), in.Report...)
		state.ReportHash = in.ReportHash
		state.Before = append([]recoverystatus.WorktreeObservation(nil), in.Before...)
	}
	if state.Finalization != nil {
		old := state.Finalization
		if old.ReportHash == in.ReportHash && old.TurnID == in.TurnID && old.WorksetHash == in.WorksetHash && reflect.DeepEqual(old.After, in.After) {
			return nil
		}
		return contextprobe.ErrConflict
	}
	if in.InspectionID != state.Spec.Probe.ID || in.ThreadID != state.Run.ThreadID || in.TurnID == "" || in.TurnID != state.Run.TurnID || in.WorksetHash != state.Workset.ContentHash || in.ReportHash != recoverystatus.Hash(in.Report) || !bytes.Equal(in.Report, state.Report) || !reflect.DeepEqual(in.Before, state.Before) || !in.Terminal || !in.Quiesced || in.ObservedAt.IsZero() || time.Since(in.ObservedAt) > time.Minute || in.ObservedAt.After(time.Now().Add(time.Second)) || len(in.After) != len(state.Workset.Members) {
		return contextprobe.ErrConflict
	}
	var cancel bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE run_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))`, in.RunID).Scan(&cancel); err != nil {
		return err
	}
	var runtimeJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT snapshot_json FROM agent_runtime_states WHERE run_id=?`, in.RunID).Scan(&runtimeJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(runtimeJSON) > 0 && !cancel {
		var snapshot agentruntime.Snapshot
		if json.Unmarshal(runtimeJSON, &snapshot) != nil {
			return contextprobe.ErrConflict
		}
		if !recovery && agentruntime.HasUnresolvedResponse(snapshot) {
			return contextprobe.ErrConflict
		}
		for _, q := range snapshot.Requests {
			if agentruntime.OpenRequest(q) && !(recovery && agentruntime.ResponseDisposition(q) == "UNKNOWN" && q.Kind == "CODEX_REQUEST" && q.UserDecision != nil) {
				return contextprobe.ErrConflict
			}
		}
	}
	status, reason := "FAILED", in.Reason
	if in.TerminalStatus != "completed" && in.TerminalStatus != "failed" && in.TerminalStatus != "interrupted" {
		return contextprobe.ErrConflict
	}
	if cancel {
		status = "CANCELED"
		reason = "Explicit Inspection cancellation"
	} else if probeexecution.InspectionObservationStatus(state.Before, in.After) != "UNCHANGED" {
		status = "SCOPE_CONFLICT"
		reason = "Inspection changed its frozen managed workset"
	} else if in.TerminalStatus != "completed" {
		status = "FAILED"
		reason = "Inspection Turn ended with " + in.TerminalStatus
	} else if state.Run.ResultProtocol == run.NaturalResultProtocol && !recovery {
		status = "SUCCEEDED"
		reason = ""
	} else if report, parseErr := probeexecution.ParseInspectionReport(in.Report, state.Workset); parseErr == nil {
		status = report.Status
	} else {
		reason = parseErr.Error()
	}
	for i, o := range in.After {
		m := state.Workset.Members[i]
		if o.MemberID != m.MemberID || o.RepositoryID != m.RepositoryID || o.WorktreeID != m.WorktreeID || o.RuntimeAlias != m.RepositoryAlias {
			return contextprobe.ErrConflict
		}
		paths, _ := json.Marshal(o.ChangedPaths)
		observation := "NO_CHANGE"
		if status == "SCOPE_CONFLICT" {
			observation = "SCOPE_CONFLICT"
		}
		if !o.Available {
			observation = "UNKNOWN"
		}
		_, err = tx.ExecContext(ctx, `UPDATE run_repository_bindings SET end_head=?,end_snapshot=?,end_root_identity=?,changed_paths_json=?,diff_digest=?,observation_state=?,observed_at=? WHERE run_id=? AND member_id=? AND observed_at IS NULL`, o.HEAD, o.Snapshot, o.RootIdentity, string(paths), o.DiffDigest, observation, formatTime(in.ObservedAt), in.RunID, o.MemberID)
		if err != nil {
			return err
		}
	}
	if state.Run.ResultProtocol == run.NaturalResultProtocol && !recovery {
		raw, e := json.Marshal(run.ExecutionEvidence{Commands: in.Commands})
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE run_execution_records SET commands_json=? WHERE run_id=? AND commands_json IS NULL`, string(raw), in.RunID); e != nil {
			return e
		}
	}
	in.Status, in.Reason = status, reason
	state.After = in.After
	state.Finalization = &in
	state.State = "FINALIZED"
	state.FailureReason = reason
	terminal := "FAILED"
	if status == "SUCCEEDED" {
		terminal = "SUCCEEDED"
	}
	if status == "CANCELED" {
		terminal = "CANCELED"
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET status=?,result_text=?,finished_at=?,updated_at=? WHERE id=? AND runtime_epoch=?`, terminal, string(in.Report), formatTime(in.ObservedAt), formatTime(in.ObservedAt), in.RunID, in.RuntimeEpoch)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE context_probes SET status=?,result=?,report_hash=?,result_hash=?,runtime_limitation=?,failure_message=?,finished_at=?,updated_at=? WHERE id=? AND (
  (run_id=? AND runtime_epoch=?) OR EXISTS(
    SELECT 1 FROM inspection_recovery_continuations k
    JOIN recovery_status_records x ON x.id=k.recovery_id
    JOIN project_inspection_agent_claims c ON c.inspection_id=k.inspection_id AND c.run_id=k.successor_run_id
    WHERE k.successor_run_id=? AND k.inspection_id=context_probes.id
      AND context_probes.recovery_successor_run_id=k.successor_run_id
      AND c.runtime_epoch=? AND x.state='RESOLVED'
      AND json_extract(x.record_json,'$.assessment.consistency')='CONSISTENT'
      AND json_extract(x.record_json,'$.assessment.engineeringDisposition')='RECOVERED_PARTIAL'
  )
)`, terminal, string(in.Report), in.ReportHash, in.ReportHash, status, nullString(reason), formatTime(in.ObservedAt), formatTime(in.ObservedAt), in.InspectionID, in.RunID, in.RuntimeEpoch, in.RunID, in.RuntimeEpoch)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET status=?,updated_at=? WHERE id=? AND task_type='INSPECTION' AND task_scope='PROJECT' AND work_owner_id=?`, terminal, formatTime(in.ObservedAt), state.Spec.Probe.TaskID, in.InspectionID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET advance_state='KNOWN_ADVANCE',exit_boundary_turn_id=?,quiesced=1 WHERE run_id=? AND quiesced=0`, in.TurnID, in.RunID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET current_boundary_turn_id=?,continuation_safe=? WHERE id=? AND backend_thread_id=? AND ended_at IS NULL`, in.TurnID, status != "SCOPE_CONFLICT" && status != "FAILED", state.Conversation.ID, in.ThreadID)
	if err != nil {
		return err
	}
	if cancel {
		_, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCELED',backend_disposition='TERMINATED',terminal_observed=1,resolved_at=? WHERE run_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')`, formatTime(in.ObservedAt), in.RunID)
		if err != nil {
			return err
		}
		if err = cancelRuntimeRequestsTx(ctx, tx, in.RunID); err != nil {
			return err
		}
	}
	if state.Run.ResultProtocol == run.NaturalResultProtocol && !recovery {
		payload, _ := event.Payload(map[string]any{"status": terminal, "analysisState": "REQUIRED"})
		if err = appendEvent(ctx, tx, event.Event{Type: event.RunStatusChanged, ProjectID: state.Run.ProjectID, TaskID: state.Run.TaskID, RunID: state.Run.ID, EntityType: "run", EntityID: state.Run.ID, Payload: payload, CreatedAt: in.ObservedAt}); err != nil {
			return err
		}
	}
	state.Run.Status = run.Status(terminal)
	state.Spec.Probe.Status = contextprobe.Status(terminal)
	state.Spec.Probe.Result = string(in.Report)
	state.UpdatedAt = time.Now().UTC()
	if err = saveInspectionRuntime(ctx, tx, *state); err != nil {
		return err
	}
	if err = releaseInspectionClaimTx(ctx, tx, in.InspectionID, in.RunID, in.RuntimeEpoch); err != nil {
		return err
	}
	if err = finalizeRecoveryUsageTx(ctx, tx, in.RunID, in.ObservedAt); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE context_probes SET (token_usage_status,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,model_context_window)=(SELECT status,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,model_context_window FROM run_token_usage WHERE run_id=?) WHERE id=?`, in.RunID, in.InspectionID)
	return err
}

func (s *InspectionRuntimeStore) ListCanonicalInspections(ctx context.Context) ([]string, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT r.inspection_id FROM inspection_runtime_records r WHERE r.state<>'FINALIZED' AND NOT EXISTS(SELECT 1 FROM recovery_status_records x WHERE x.source_run_id=r.run_id) ORDER BY r.inspection_id,r.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		if len(ids) == 0 || ids[len(ids)-1] != id {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func (s *InspectionRuntimeStore) CancelPreparedInspection(ctx context.Context, id, epoch string) (probeexecution.CanonicalState, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, state *probeexecution.CanonicalState) error {
		if state.Finalization != nil && state.Finalization.NoBackendAdvance && state.Finalization.Status == "CANCELED" {
			return nil
		}
		if (state.State != "PREPARED" && state.State != "RUN_CREATED" && state.State != "RECOVERY_REQUIRED") || state.Run.TurnID != "" {
			return contextprobe.ErrConflict
		}
		var allowed bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations c JOIN agent_operation_boundaries b ON b.run_id=c.run_id JOIN runs r ON r.id=c.run_id WHERE c.run_id=? AND c.state='CANCEL_REQUESTED' AND b.advance_state='NO_ADVANCE' AND b.thread_start_pending=0 AND r.turn_id IS NULL AND r.final_codex_prompt IS NULL)`, id).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return contextprobe.ErrConflict
		}
		now := time.Now().UTC()
		state.Finalization = &probeexecution.InspectionFinalization{RunID: id, InspectionID: state.Spec.Probe.ID, RuntimeEpoch: epoch, ThreadID: state.Run.ThreadID, Status: "CANCELED", NoBackendAdvance: true, Quiesced: true, ObservedAt: now}
		state.State = "FINALIZED"
		state.Run.Status = run.StatusCanceled
		state.Spec.Probe.Status = contextprobe.StatusCanceled
		for _, q := range []string{`UPDATE runs SET status='CANCELED',finished_at=?,updated_at=? WHERE id=?`, `UPDATE context_probes SET status='CANCELED',runtime_limitation='CANCELED',token_usage_status='UNAVAILABLE',finished_at=?,updated_at=? WHERE run_id=?`} {
			if _, err := tx.ExecContext(ctx, q, formatTime(now), formatTime(now), id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='CANCELED',updated_at=? WHERE id=? AND task_type='INSPECTION' AND work_owner_id=?`, formatTime(now), state.Spec.Probe.TaskID, state.Spec.Probe.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCELED',backend_disposition='NO_BACKEND',resolved_at=? WHERE run_id=? AND state='CANCEL_REQUESTED'`, formatTime(now), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET quiesced=1 WHERE run_id=? AND advance_state='NO_ADVANCE'`, id); err != nil {
			return err
		}
		if err := saveInspectionRuntime(ctx, tx, *state); err != nil {
			return err
		}
		if err := releaseInspectionClaimTx(ctx, tx, state.Spec.Probe.ID, id, epoch); err != nil {
			return err
		}
		return finalizeRecoveryUsageTx(ctx, tx, id, now)
	})
}
func (s *InspectionRuntimeStore) AdoptInspection(ctx context.Context, id string) (probeexecution.CanonicalState, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return probeexecution.CanonicalState{}, err
	}
	defer tx.Rollback()
	state, err := inspectionRuntimeRead(ctx, tx, id)
	if err != nil {
		return state, err
	}
	if state.Run.RuntimeEpoch == s.db.epoch || state.Finalization != nil {
		return state, nil
	}
	var recoveryActive bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_status_records WHERE source_run_id=? AND state NOT IN ('RESOLVED','FAILED'))`, state.Run.ID).Scan(&recoveryActive); err != nil {
		return state, err
	}
	if recoveryActive {
		return state, nil
	}
	old := state.Run.RuntimeEpoch
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO inspection_runtime_adoptions(run_id,inspection_id,previous_epoch,current_epoch,created_at) VALUES(?,?,?,?,?)`, state.Run.ID, state.Spec.Probe.ID, old, s.db.epoch, formatTime(now))
	if err != nil {
		return state, fmt.Errorf("record Inspection adoption: %w", err)
	}
	for _, q := range []string{`UPDATE runs SET runtime_epoch=? WHERE id=? AND runtime_epoch=?`, `UPDATE project_inspection_agent_claims SET runtime_epoch=? WHERE run_id=? AND runtime_epoch=?`, `UPDATE context_probes SET runtime_epoch=? WHERE run_id=? AND runtime_epoch=?`} {
		if _, err = tx.ExecContext(ctx, q, s.db.epoch, state.Run.ID, old); err != nil {
			return state, fmt.Errorf("adopt Inspection authority (%s): %w", q, err)
		}
	}
	state.Run.RuntimeEpoch = s.db.epoch
	state.Spec.Probe.RuntimeEpoch = s.db.epoch
	state.UpdatedAt = now
	if err = saveInspectionRuntime(ctx, tx, state); err != nil {
		return state, err
	}
	return state, tx.Commit()
}

// ContinueInspection creates the one explicit, receipt-bound continuation of
// a RECOVERED_PARTIAL Project Inspection. The source Run remains immutable;
// the child has no context_probe_id and is linked only by the durable receipt.
func (s *InspectionRuntimeStore) ContinueInspection(ctx context.Context, in probeexecution.InspectionContinuationInput) (probeexecution.CanonicalState, error) {
	if in.RecoveryStatusID == "" || in.RequestID == "" || in.ActorID == "" || in.Instruction == "" || in.PayloadHash != in.ComputePayloadHash() {
		return probeexecution.CanonicalState{}, contextprobe.ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return probeexecution.CanonicalState{}, err
	}
	defer tx.Rollback()
	recovery, err := recoveryReadTx(ctx, tx, in.RecoveryStatusID)
	if err != nil {
		return probeexecution.CanonicalState{}, err
	}
	// A resolved receipt is immutable history and is intentionally not adopted
	// on Core restart. Its authored epoch remains part of the continuation
	// payload; the new successor itself is created under the current epoch.
	if recovery.State != "RESOLVED" || recovery.Spec.OwnerKind != "PROJECT_INSPECTION" || in.ExpectedRuntimeEpoch != recovery.RuntimeEpoch {
		return probeexecution.CanonicalState{}, contextprobe.ErrConflict
	}
	var assessment recoverystatus.Assessment
	if json.Unmarshal(recovery.Assessment, &assessment) != nil || assessment.EngineeringDisposition != "RECOVERED_PARTIAL" || assessment.Consistency != "CONSISTENT" {
		return probeexecution.CanonicalState{}, contextprobe.ErrConflict
	}
	// An independent diagnostic proves the result, but cannot authorize another
	// Turn on its source Thread. Verify the full recorded producer chain before
	// granting the explicit continuation a new conversation.
	var independent bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_child_admissions WHERE recovery_id=?)`, recovery.ID).Scan(&independent); err != nil {
		return probeexecution.CanonicalState{}, err
	}
	if independent {
		if err = validateRecoveryResolutionEvidence(recovery); err != nil {
			return probeexecution.CanonicalState{}, err
		}
		if _, exactIndependent, e := readRecoveryDiagnosticEvidenceTx(ctx, tx, recovery, true); e != nil || !exactIndependent {
			return probeexecution.CanonicalState{}, contextprobe.ErrConflict
		}
	}
	var existing, requestID, payloadHash, instruction, actorID string
	err = tx.QueryRowContext(ctx, `SELECT successor_run_id,request_id,payload_hash,instruction,actor_id FROM inspection_recovery_continuations WHERE recovery_id=? OR request_id=?`, recovery.ID, in.RequestID).Scan(&existing, &requestID, &payloadHash, &instruction, &actorID)
	if err == nil {
		state, readErr := inspectionRuntimeRead(ctx, tx, existing)
		if readErr != nil || requestID != in.RequestID || payloadHash != in.PayloadHash || instruction != in.Instruction || actorID != in.ActorID || state.ContinuationRecoveryStatusID != recovery.ID || state.Spec.Probe.CodexInstruction != in.Instruction || state.ContinuationFreshThread != independent {
			return probeexecution.CanonicalState{}, contextprobe.ErrConflict
		}
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return probeexecution.CanonicalState{}, err
	}
	source, err := inspectionRuntimeRead(ctx, tx, recovery.Spec.RunID)
	if err != nil || source.Spec.Probe.ID != recovery.Spec.OwnerID || source.Workset.ContentHash != recovery.Spec.RuntimeWorksetHash || source.Run.WorkspaceID != recovery.Spec.WorkspaceID {
		return probeexecution.CanonicalState{}, contextprobe.ErrConflict
	}
	var facts recoverystatus.FactualObservation
	if json.Unmarshal(recovery.Facts, &facts) != nil || len(facts.After) != len(source.Workset.Members) {
		return probeexecution.CanonicalState{}, contextprobe.ErrConflict
	}
	now := time.Now().UTC()
	rows, err := tx.QueryContext(ctx, `SELECT repository_id FROM run_repository_bindings WHERE run_id=? ORDER BY repository_id`, source.Run.ID)
	if err != nil {
		return probeexecution.CanonicalState{}, err
	}
	for rows.Next() {
		var repositoryID string
		if err = rows.Scan(&repositoryID); err != nil {
			rows.Close()
			return probeexecution.CanonicalState{}, err
		}
		if err = ensureRepositoryActiveTx(ctx, tx, source.Run.ProjectID, repositoryID); err != nil {
			rows.Close()
			return probeexecution.CanonicalState{}, err
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return probeexecution.CanonicalState{}, err
	}
	if err = rows.Close(); err != nil {
		return probeexecution.CanonicalState{}, err
	}
	id := recovery.ID + ":continuation"
	var attempt int
	if err = tx.QueryRowContext(ctx, `UPDATE tasks SET next_attempt=MAX(next_attempt,(SELECT COALESCE(MAX(attempt),0) FROM runs WHERE task_id=?))+1 WHERE id=? RETURNING next_attempt`, source.Run.TaskID, source.Run.TaskID).Scan(&attempt); err != nil {
		return probeexecution.CanonicalState{}, err
	}
	work := source.Workset
	work.RunID, work.CreatedAt = id, now
	rawWork, _ := json.Marshal(work)
	probe := source.Spec.Probe
	probe.RunID, probe.TurnID, probe.CodexInstruction, probe.RuntimeEpoch = id, "", in.Instruction, s.db.epoch
	state := probeexecution.CanonicalState{ContinuationRecoveryStatusID: recovery.ID, ContinuationSourceTurnID: recovery.RecoveryTurnID, Spec: source.Spec, Run: source.Run, Conversation: source.Conversation, Workset: work, Before: facts.After, Schema: probeexecution.InspectionReportSchema(probeexecution.InspectionSourceForWorkset(work)), State: "RUN_CREATED", UpdatedAt: now}
	state.Spec.Probe, state.Run.ID, state.Run.ContextProbeID, state.Run.Purpose, state.Run.Status, state.Run.TurnID, state.Run.RuntimeWorkset, state.Run.CreatedAt, state.Run.UpdatedAt = probe, id, "", run.PurposeInspection, run.StatusPreparing, "", &work, now, now
	state.Run.RuntimeEpoch, state.Run.Attempt = s.db.epoch, attempt
	if independent {
		if source.Conversation.ID != recovery.Spec.AgentConversationID || source.Conversation.BackendThreadID != recovery.Spec.ThreadID {
			return state, contextprobe.ErrConflict
		}
		ended, e := tx.ExecContext(ctx, `UPDATE agent_conversations SET ended_at=?,rotation_reason='UNSAFE_CONTEXT_BOUNDARY',continuation_safe=0 WHERE id=? AND ended_at IS NULL AND owner_kind='PROJECT_INSPECTION' AND owner_id=? AND project_id=? AND backend_thread_id=?`, formatTime(now), source.Conversation.ID, probe.ID, probe.ProjectID, recovery.Spec.ThreadID)
		if e != nil {
			return state, e
		}
		if n, _ := ended.RowsAffected(); n != 1 {
			return state, contextprobe.ErrConflict
		}
		state.Conversation, err = newInspectionConversationTx(ctx, tx, probe.ProjectID, probe.ID, source.Conversation.ID, now)
		if err != nil {
			return state, err
		}
		state.ContinuationFreshThread = true
		state.ContinuationSourceTurnID, state.Run.ThreadID, state.Spec.Probe.ThreadID = "", "", ""
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO inspection_recovery_continuations(successor_run_id,recovery_id,inspection_id,source_run_id,task_id,project_id,workspace_id,runtime_workset_hash,instruction,instruction_sha256,request_id,actor_id,payload_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, recovery.ID, probe.ID, source.Run.ID, probe.TaskID, probe.ProjectID, work.WorkspaceID, work.ContentHash, in.Instruction, recoverystatus.Hash([]byte(in.Instruction)), in.RequestID, in.ActorID, in.PayloadHash, formatTime(now)); err != nil {
		return state, err
	}
	// Agent Execution is frozen at the semantic Inspection owner (the schema
	// enforces one row per owner).  The successor therefore references the
	// unchanged source identity; the continuation receipt makes the new Run
	// association auditable without inserting or updating agent_executions.
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,project_id,feature_area_id,operation_kind,operation_id,task_id,attempt,status,created_at,updated_at,purpose,codex_instruction,context_probe_id,runtime_epoch,agent_execution_id,workspace_id,thread_id,runtime_workset_json,runtime_workset_hash) VALUES(?,?,NULL,'INSPECTION',?,?,?,'PREPARING',?,?,'INSPECTION',?,NULL,?,?,?,?,?,?)`, id, probe.ProjectID, probe.ID, probe.TaskID, attempt, formatTime(now), formatTime(now), in.Instruction, s.db.epoch, state.Run.AgentExecutionID, work.WorkspaceID, nullString(state.Run.ThreadID), string(rawWork), work.ContentHash); err != nil {
		return state, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO run_token_usage(run_id,status,updated_at) VALUES(?,'PENDING',?)`, id, formatTime(now)); err != nil {
		return state, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO run_repository_bindings(run_id,inspection_id,project_id,workspace_id,repository_id,member_id,member_version,origin_commit,worktree_id,preparation,repository_alias,start_head,start_snapshot,intended_read,intended_write_scope_json,runtime_root_identity,root_path,root_identity) SELECT ?,inspection_id,project_id,workspace_id,repository_id,member_id,member_version,origin_commit,worktree_id,preparation,repository_alias,start_head,start_snapshot,intended_read,intended_write_scope_json,runtime_root_identity,root_path,root_identity FROM run_repository_bindings WHERE run_id=?`, id, source.Run.ID); err != nil {
		return state, err
	}
	raw, _ := json.Marshal(state)
	if _, err = tx.ExecContext(ctx, `INSERT INTO inspection_runtime_records(run_id,inspection_id,runtime_epoch,state,record_json,created_at,updated_at) VALUES(?,?,?,'RUN_CREATED',?,?,?)`, id, probe.ID, s.db.epoch, string(raw), formatTime(now), formatTime(now)); err != nil {
		return state, err
	}
	// A new Thread retains the exact confirmed App Server identity. It must not
	// silently launch another backend from the current process environment.
	if _, err = tx.ExecContext(ctx, `INSERT INTO app_server_run_bindings(run_id,binding_json,checkpoint_json,created_at) SELECT ?,binding_json,?,? FROM app_server_run_bindings WHERE run_id=?`, id, string(raw), formatTime(now), source.Run.ID); err != nil {
		return state, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_operation_boundaries(run_id,agent_conversation_id,entry_boundary_turn_id,advance_state,quiesced,thread_start_pending) VALUES(?,?,?,'NO_ADVANCE',0,0)`, id, state.Conversation.ID, nullString(state.ContinuationSourceTurnID)); err != nil {
		return state, err
	}
	// A continuation has one claim, never a second concurrent Inspection
	// authority. v83 permits this transfer only through the resolved partial
	// receipt inserted above.
	var sourceClaim bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_inspection_agent_claims WHERE inspection_id=? AND run_id=? AND runtime_epoch=?)`, probe.ID, source.Run.ID, source.Run.RuntimeEpoch).Scan(&sourceClaim); err != nil {
		return state, err
	}
	if sourceClaim {
		if err = releaseInspectionClaimTx(ctx, tx, probe.ID, source.Run.ID, source.Run.RuntimeEpoch); err != nil {
			return state, err
		}
	} else if source.Finalization == nil || source.Finalization.Status != "BLOCKED" {
		return state, contextprobe.ErrConflict
	}
	if err = inspectionClaimTx(ctx, tx, probe.ID, probe.TaskID, id, probe.ProjectID, s.db.epoch, formatTime(now)); err != nil {
		return state, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE context_probes SET recovery_successor_run_id=? WHERE id=? AND recovery_successor_run_id IS NULL`, id, probe.ID); err != nil {
		return state, err
	}
	// The immutable source attempt remains terminal.  The canonical operation is
	// now represented by the receipt-bound successor, so only its projection is
	// reactivated for the next Turn.
	if _, err = tx.ExecContext(ctx, `UPDATE context_probes SET status='RUNNING',runtime_limitation='',failure_message='',finished_at=NULL,updated_at=? WHERE id=? AND recovery_successor_run_id=? AND status='FAILED'`, formatTime(now), probe.ID, id); err != nil {
		return state, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='ACTIVE',updated_at=? WHERE id=? AND task_type='INSPECTION' AND work_owner_id=? AND status='FAILED'`, formatTime(now), probe.TaskID, probe.ID); err != nil {
		return state, err
	}
	return state, tx.Commit()
}
