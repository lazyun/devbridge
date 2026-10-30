package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func TestRecoveryStatusOfflineNewQuestionCanonicalCancellation(t *testing.T) {
	runRecoveryCancellationEvidence(t, false, false)
}

func TestRecoveryStatusOfflineCancellationLostInterruptReceipt(t *testing.T) {
	runRecoveryCancellationEvidence(t, true, false)
}

func TestRecoveryStatusOfflineCancellationLostReceiptAcrossRestart(t *testing.T) {
	runRecoveryCancellationEvidence(t, true, true)
}

type lostRecoveryInterruptReceipt struct {
	*sqlite.RecoveryStatusStore
	calls atomic.Int64
}

func (s *lostRecoveryInterruptReceipt) RecordCancelInterrupt(ctx context.Context, id string, in executioncancel.Input, outcome string) (executioncancel.Receipt, error) {
	s.calls.Add(1)
	if outcome == "ACKNOWLEDGED" {
		// The backend consumed the interrupt, but its ACK cannot be retained.
		// Keep only the conservative outcome and force an exact observation retry.
		receipt, err := s.RecoveryStatusStore.RecordCancelInterrupt(ctx, id, in, "OUTCOME_UNKNOWN")
		return receipt, errors.Join(errors.New("injected lost interrupt receipt"), err)
	}
	return s.RecoveryStatusStore.RecordCancelInterrupt(ctx, id, in, outcome)
}

func runRecoveryCancellationEvidence(t *testing.T, loseReceipt, restartPending bool) {
	runRecoveryStatusOfflineNativeSetup(t, "probe-explicit-question", nil, func(f *upperLifecycleFixture) {
		native := os.Getenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY")
		f.plane.RuntimeQualification.BaseCodexPath = native
		f.plane.RuntimeQualification.EvidenceRoot = filepath.Join(t.TempDir(), "native-qualification")
		configs, err := f.plane.CodexConfigs.List(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, backend := range configs {
			if backend.Enabled {
				if _, err = f.plane.RuntimeQualification.QualifyBackend(f.ctx, backend.ID, "IMPLEMENTATION", "cancel-native-qualification-"+backend.ID); err != nil {
					t.Fatal(err)
				}
			}
		}
		f.plane.Coordinator.QualifiedRuntime = f.plane.RuntimeQualification
		f.facade, err = controlapi.NewFromControlPlane(f.plane)
		if err != nil {
			t.Fatal(err)
		}
	}, func(f *upperLifecycleFixture, cfg controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
		var fault *lostRecoveryInterruptReceipt
		if loseReceipt {
			fault = &lostRecoveryInterruptReceipt{RecoveryStatusStore: sqlite.NewRecoveryStatusStore(f.plane.DB)}
			f.plane.RecoveryStatus.Store = fault
		}
		started, e := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd)
		if e != nil {
			t.Fatal(e)
		}
		var question agentruntime.Request
		until := time.Now().Add(30 * time.Second)
		for time.Now().Before(until) {
			state, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, started.Record.RecoveryRunID)
			if e != nil {
				t.Fatal(e)
			}
			for _, q := range state.Requests {
				if q.Kind == "CODEX_REQUEST" && q.State == "WAITING_FOR_USER" {
					question = q
				}
			}
			if question.ID != "" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if question.ID == "" || question.UserDecision != nil || calls.Load() != 4 {
			t.Fatalf("new question absent %+v calls=%d", question, calls.Load())
		}
		old, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, cmd.RunID)
		if e != nil {
			t.Fatal(e)
		}
		oldRequests, e := json.Marshal(old.Requests)
		if e != nil {
			t.Fatal(e)
		}
		ledger := func() []string {
			var out []string
			for _, query := range []string{`SELECT request_json FROM codex_server_requests WHERE run_id=? ORDER BY connection_id,request_id`, `SELECT decision_json FROM runtime_user_decisions WHERE run_id=? ORDER BY id`, `SELECT attempt_json FROM runtime_response_dispatch_attempts WHERE run_id=? ORDER BY id`, `SELECT evidence_json FROM runtime_response_evidence WHERE run_id=? ORDER BY id`} {
				rows, err := f.plane.DB.SQL().QueryContext(f.ctx, query, cmd.RunID)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var b string
					if err = rows.Scan(&b); err != nil {
						t.Fatal(err)
					}
					out = append(out, b)
				}
				if err = rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
			}
			return out
		}
		beforeLedger, _ := json.Marshal(ledger())
		var sourceConversation, sourceBoundary string
		var sourceSafe bool
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT a.id,COALESCE(a.current_boundary_turn_id,''),a.continuation_safe FROM agent_conversations a JOIN agent_operation_boundaries b ON b.agent_conversation_id=a.id WHERE b.run_id=?`, cmd.RunID).Scan(&sourceConversation, &sourceBoundary, &sourceSafe); err != nil {
			t.Fatal(err)
		}
		if question.RecoveryStatusID != started.Record.ID || question.AgentConversationID != "" || question.ExecutionID != started.Record.Spec.OperationID {
			t.Fatalf("independent diagnostic question borrowed source identity: %+v", question.Identity)
		}
		decision := explicitDecision(t, f, question, "CANCEL_OPERATION", "")
		result, e := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, decision)
		var interruptEpoch string
		if loseReceipt {
			if e == nil || fault.calls.Load() != 1 {
				t.Fatalf("interrupt receipt loss not exercised: calls=%d err=%v", fault.calls.Load(), e)
			}
			competing := executioncancel.Input{AISessionID: f.session, ProjectID: question.ProjectID, OwnerType: question.OperationKind, OwnerID: question.OperationID, RunID: question.RunID, RequestID: "competing-cancellation", Reason: "Cancel through another entrypoint"}
			if _, err := f.facade.CancelExecutionForSession(f.ctx, competing); !errors.Is(err, recoverystatus.ErrIneligible) || fault.calls.Load() != 1 {
				t.Fatalf("competing cancellation escaped pending request fence: calls=%d err=%v", fault.calls.Load(), err)
			}
			if restartPending {
				if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT runtime_epoch FROM execution_cancellations WHERE run_id=?`, started.Record.RecoveryRunID).Scan(&interruptEpoch); err != nil {
					t.Fatal(err)
				}
				reopenRecoveryFixture(t, f, cfg)
			}
			result, e = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, decision)
			if e != nil || fault.calls.Load() != 1 {
				t.Fatalf("same user decision retry failed or resent interrupt: calls=%d err=%v", fault.calls.Load(), e)
			}
		}
		if e != nil {
			t.Fatalf("recovery cancellation %v cause=%v", e, errors.Unwrap(e))
		}
		if result.Cancellation == nil || result.Cancellation.State != "CANCELED" || result.Cancellation.QuiescenceState != "QUIESCED" {
			t.Fatalf("not canonical cancellation %+v", result)
		}
		wantInterrupt := "ACKNOWLEDGED"
		if loseReceipt {
			wantInterrupt = "OUTCOME_UNKNOWN"
		}
		if result.Cancellation.BackendInterruptState != wantInterrupt {
			t.Fatalf("terminal observation changed interrupt history: %+v", result.Cancellation)
		}
		if restartPending {
			var currentEpoch string
			var count int
			if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT count(*),min(runtime_epoch) FROM execution_cancellations WHERE run_id=?`, started.Record.RecoveryRunID).Scan(&count, &currentEpoch); err != nil || count != 1 || currentEpoch != interruptEpoch {
				t.Fatalf("restart changed cancellation provenance: count=%d epoch=%s want=%s %v", count, currentEpoch, interruptEpoch, err)
			}
		}
		frozenCancellation, err := json.Marshal(result.Cancellation)
		if err != nil {
			t.Fatal(err)
		}
		current, e := f.plane.Runs.Get(f.ctx, started.Record.RecoveryRunID)
		if e != nil || current.Status != run.StatusCanceled {
			t.Fatalf("child didnotcancel %+v %v", current, e)
		}
		source, e := f.plane.Runs.Get(f.ctx, cmd.RunID)
		if e != nil || source.Status != run.StatusCanceled {
			t.Fatalf("source operation stranded %+v %v", source, e)
		}
		after, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, cmd.RunID)
		if e != nil {
			t.Fatal(e)
		}
		// Projection activity may change when the claim is released; request/decision
		// transport facts must remain byte-identical in the authoritative source rows.
		for _, q := range after.Requests {
			if q.Kind == "CODEX_REQUEST" && agentruntime.ResponseDisposition(q) != "UNKNOWN" {
				t.Fatalf("oldanswer changed %+v original=%s", q, oldRequests)
			}
		}
		r, e := f.plane.RecoveryStatus.Read(f.ctx, cmd.ID)
		if e != nil || r.State != "FAILED" || r.FailureReason != "RECOVERY_CANCELED" {
			t.Fatalf("recovery cancellation receipt %+v %v", r, e)
		}
		view, e := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, cmd.ID)
		if e != nil || view.Projection.Required || view.Projection.DisplayTitle != "工程状态恢复已取消" || view.Projection.SourceResponseDisposition != "UNKNOWN" {
			t.Fatalf("canceled projection lost canonical receipt or old UNKNOWN: %+v %v", view.Projection, e)
		}
		afterLedger, _ := json.Marshal(ledger())
		if string(beforeLedger) != string(afterLedger) {
			t.Fatal("canonical cancellation rewrote original request/decision/transport ledger")
		}
		if calls.Load() != 4 {
			t.Fatal("cancel implicitlyanswered new request")
		}
		if _, e = f.plane.RecoveryStatus.Start(f.ctx, r.ID); e != nil {
			t.Fatal(e)
		}
		final, e := f.plane.RecoveryStatus.Read(f.ctx, r.ID)
		if e != nil || final.RecoveryTurnID != r.RecoveryTurnID || final.SourceResponseDisposition != "UNKNOWN" {
			t.Fatal("cancel restart changed history")
		}
		var afterBoundary, cancelThread, cancelTurn string
		var afterSafe bool
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COALESCE(current_boundary_turn_id,''),continuation_safe FROM agent_conversations WHERE id=?`, sourceConversation).Scan(&afterBoundary, &afterSafe); err != nil {
			t.Fatal(err)
		}
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT thread_id,turn_id FROM execution_cancellations WHERE run_id=?`, started.Record.RecoveryRunID).Scan(&cancelThread, &cancelTurn); err != nil {
			t.Fatal(err)
		}
		if cancelThread != current.ThreadID || cancelTurn != current.TurnID {
			t.Fatalf("cancellation ledger does not identify exact child: (%s,%s)", cancelThread, cancelTurn)
		}
		if afterBoundary != sourceBoundary || afterSafe != sourceSafe {
			t.Fatal("child cancellation rewrote source conversation")
		}
		for _, cold := range []bool{false, true} {
			if cold {
				reopenRecoveryFixture(t, f, cfg)
			}
			replay, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, decision)
			if err != nil || replay.Decision.ID != result.Decision.ID {
				t.Fatalf("terminal decision replay failed (cold=%v): %+v %v", cold, replay, err)
			}
			replayedCancellation, err := json.Marshal(replay.Cancellation)
			if err != nil || string(replayedCancellation) != string(frozenCancellation) || loseReceipt && fault.calls.Load() != 1 {
				t.Fatalf("terminal replay changed receipt or resent interrupt (cold=%v): %s %v", cold, replayedCancellation, err)
			}
			changed := decision
			changed.Action = "STOP_AND_REVISE"
			changed.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(changed)
			if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, changed); !errors.Is(err, agentruntime.ErrRequestConflict) {
				t.Fatalf("changed decision replay did not report conflict (cold=%v): %v", cold, err)
			}
			owner, err := f.plane.Tasks.Get(f.ctx, source.TaskID)
			if err != nil || owner.Phase == task.PhaseRecoveryRequired {
				t.Fatalf("canceled owner reentered task recovery (cold=%v): %+v %v", cold, owner, err)
			}
			guidance, err := f.facade.WorkflowGuidance(f.ctx, f.session, f.area)
			if err != nil || guidance.CurrentState == "RECOVERY_REQUIRED" || guidance.CurrentState == "TASK_RECOVERY_REQUIRED" || guidance.CurrentState == "FEATURE_AREA_BUSY" {
				t.Fatalf("canceled independent child stranded guidance (cold=%v): %+v %v", cold, guidance, err)
			}
			afterLedger, _ = json.Marshal(ledger())
			if string(beforeLedger) != string(afterLedger) || calls.Load() != 4 {
				t.Fatal("cold cancellation resume changed source response or dispatched more model work")
			}
		}
		return false
	})
}
