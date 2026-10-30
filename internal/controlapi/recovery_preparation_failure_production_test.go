package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/worktree"
)

func qualifyRecoveryPreparationFixture(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	f.plane.RuntimeQualification.BaseCodexPath = os.Getenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY")
	f.plane.RuntimeQualification.EvidenceRoot = filepath.Join(t.TempDir(), "native-qualification")
	configs, err := f.plane.CodexConfigs.List(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range configs {
		if backend.Enabled {
			if _, err = f.plane.RuntimeQualification.QualifyBackend(f.ctx, backend.ID, "IMPLEMENTATION", "preparation-native-"+backend.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.plane.Coordinator.QualifiedRuntime = f.plane.RuntimeQualification
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
}

func recoveryPreparationSourceHistory(t *testing.T, f *upperLifecycleFixture, source string) []string {
	t.Helper()
	var history []string
	for _, query := range []string{
		`SELECT request_json FROM codex_server_requests WHERE run_id=? ORDER BY connection_id,request_id`,
		`SELECT decision_json FROM runtime_user_decisions WHERE run_id=? ORDER BY id`,
		`SELECT attempt_json FROM runtime_response_dispatch_attempts WHERE run_id=? ORDER BY id`,
		`SELECT evidence_json FROM runtime_response_evidence WHERE run_id=? ORDER BY id`,
		`SELECT json_object('conversation',a.id,'thread',a.backend_thread_id,'turn',a.current_boundary_turn_id,'safe',a.continuation_safe,'ended',a.ended_at,'boundary',b.advance_state,'entry',b.entry_boundary_turn_id,'exit',b.exit_boundary_turn_id,'quiesced',b.quiesced) FROM agent_operation_boundaries b JOIN agent_conversations a ON a.id=b.agent_conversation_id WHERE b.run_id=?`,
	} {
		rows, err := f.plane.DB.SQL().QueryContext(f.ctx, query, source)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			history = append(history, raw)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return history
}

func TestRecoveryPreparationSettledFailurePublicCancellation(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(map[bool]string{false: "warm-source", true: "cold-child"}[cold], func(t *testing.T) {
			runRecoveryStatusOfflineNativeSetup(t, "complete-old-unknown", nil, func(f *upperLifecycleFixture) { qualifyRecoveryPreparationFixture(t, f) }, func(f *upperLifecycleFixture, cfg controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
				history := recoveryPreparationSourceHistory(t, f, cmd.RunID)
				original := f.plane.Coordinator.Evidence
				failure := &upperPurposeSnapshotFailure{EvidenceCollector: original, fail: true}
				f.plane.Coordinator.Evidence = failure
				failed, err := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd)
				if err == nil || !strings.Contains(err.Error(), "definite snapshot failure") || failure.calls != 1 || failed.Record.State != "FAILED" {
					t.Fatalf("failure not settled: state=%s calls=%d err=%v", failed.Record.State, failure.calls, err)
				}
				f.plane.Coordinator.Evidence = original
				initialEpoch := failed.Record.RuntimeEpoch
				for _, table := range []string{"recovery_child_thread_attempts", "recovery_child_turn_attempts"} {
					var count int
					if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM `+table+` WHERE recovery_id=?`, cmd.ID).Scan(&count); err != nil || count != 0 {
						t.Fatalf("%s=%d %v", table, count, err)
					}
				}
				var inventory string
				if err = f.plane.DB.SQL().QueryRow(`SELECT artifact_inventory_json FROM recovery_child_preparation_failures WHERE recovery_id=?`, cmd.ID).Scan(&inventory); err != nil {
					t.Fatal(err)
				}
				if _, err = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); err != nil || failure.calls != 1 || calls.Load() != 3 {
					t.Fatalf("failure replay dispatched: calls=%d err=%v", calls.Load(), err)
				}
				if cold {
					reopenRecoveryFixture(t, f, cfg)
				}
				view, err := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, cmd.ID)
				if err != nil || view.Record.RuntimeEpoch != initialEpoch || view.Record.State != "FAILED" {
					t.Fatalf("failed attempt adopted or revived: %+v %v", view.Record, err)
				}
				found := false
				for _, action := range view.Projection.AvailableActions {
					found = found || action == "CANCEL_OPERATION"
				}
				if !found || view.StartCommand != nil {
					t.Fatalf("missing public cancellation: %+v", view)
				}
				runID := cmd.RunID
				if cold {
					runID = failed.Record.RecoveryRunID
				}
				in := executioncancel.Input{AISessionID: f.session, OwnerType: cmd.OperationKind, OwnerID: cmd.OperationID, RunID: runID, RequestID: "cancel-settled-recovery", Reason: "Explicitly abandon failed preparation"}
				wrong := in
				wrong.OwnerID = "foreign-owner"
				wrong.ExecutionID = "foreign-owner"
				if _, err = f.facade.CancelExecutionForSession(f.ctx, wrong); err == nil {
					t.Fatal("wrong owner canceled preparation")
				}
				receipt, err := f.facade.CancelExecutionForSession(f.ctx, in)
				if err != nil || receipt.State != "CANCELED" || receipt.QuiescenceState != "QUIESCED" || receipt.BackendDisposition != "NO_BACKEND" || receipt.BackendInterruptState != "NOT_SENT" || receipt.RecoveryRequired {
					t.Fatalf("cancel=%+v err=%v", receipt, err)
				}
				replay, err := f.facade.CancelExecutionForSession(f.ctx, in)
				if err != nil || !reflect.DeepEqual(replay, receipt) {
					t.Fatalf("receipt replay changed: %+v %v", replay, err)
				}
				in.Reason = "changed payload"
				if _, err = f.facade.CancelExecutionForSession(f.ctx, in); err == nil {
					t.Fatal("changed cancellation request accepted")
				}
				for _, id := range []string{cmd.RunID, failed.Record.RecoveryRunID} {
					value, e := f.plane.Runs.Get(f.ctx, id)
					if e != nil || value.Status != "CANCELED" {
						t.Fatalf("run %s not canceled: %s %v", id, value.Status, e)
					}
					if id == failed.Record.RecoveryRunID && (value.ThreadID != "" || value.TurnID != "" || value.RuntimeWorkset != nil || value.RuntimeEpoch != initialEpoch) {
						t.Fatalf("child identity fabricated: %+v", value)
					}
				}
				if !reflect.DeepEqual(history, recoveryPreparationSourceHistory(t, f, cmd.RunID)) {
					t.Fatal("source answer/conversation history rewritten")
				}
				guidance, err := f.facade.WorkflowGuidance(f.ctx, f.session, f.area)
				if err != nil || guidance.CurrentState == "RECOVERY_REQUIRED" || guidance.CurrentState == "FEATURE_AREA_BUSY" {
					t.Fatalf("source remains blocked: %+v %v", guidance, err)
				}
				if calls.Load() != 3 {
					t.Fatalf("unexpected backend calls=%d", calls.Load())
				}
				var after string
				if err = f.plane.DB.SQL().QueryRow(`SELECT artifact_inventory_json FROM recovery_child_preparation_failures WHERE recovery_id=?`, cmd.ID).Scan(&after); err != nil || after != inventory {
					t.Fatal("retained artifacts changed")
				}
				var artifacts []struct{ Root string }
				if err = json.Unmarshal([]byte(inventory), &artifacts); err != nil {
					t.Fatal(err)
				}
				for _, artifact := range artifacts {
					if _, err = os.Stat(artifact.Root); err != nil {
						t.Fatal("retained worktree removed", err)
					}
				}
				return false
			})
		})
	}
}

func TestRecoveryPreparationUnknownObservationCannotCancel(t *testing.T) {
	for _, mode := range []string{"snapshot-timeout", "worktree-response-loss"} {
		t.Run(mode, func(t *testing.T) {
			runRecoveryStatusOfflineNativeSetup(t, "complete-old-unknown", nil, func(f *upperLifecycleFixture) { qualifyRecoveryPreparationFixture(t, f) }, func(f *upperLifecycleFixture, _ controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
				unknown := &upperPurposeUnknownSnapshot{EvidenceCollector: f.plane.Coordinator.Evidence}
				create := &recoveryPreparationLostCreate{WorktreeManager: f.plane.Coordinator.Worktrees}
				if mode == "snapshot-timeout" {
					f.plane.Coordinator.Evidence = unknown
				} else {
					f.plane.Coordinator.Worktrees = create
				}
				if _, err := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); err == nil {
					t.Fatal("deadline failure accepted")
				}
				var count int
				if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM recovery_child_preparation_failures WHERE recovery_id=?`, cmd.ID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("unknown settled: %d %v", count, err)
				}
				if _, err := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); err == nil || unknown.calls+create.calls != 1 {
					t.Fatalf("unknown preparation repeated: calls=%d err=%v", unknown.calls+create.calls, err)
				}
				if _, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: cmd.OperationKind, OwnerID: cmd.OperationID, RunID: cmd.RunID, RequestID: "unknown-preparation-cancel"}); err == nil {
					t.Fatal("unknown preparation canceled without quiescence")
				}
				if calls.Load() != 3 {
					t.Fatal("unexpected dispatch")
				}
				return false
			})
		})
	}
}

type recoveryPreparationLostCreate struct {
	execution.WorktreeManager
	calls int
}

func (s *recoveryPreparationLostCreate) Create(ctx context.Context, in worktree.CreateInput) (worktree.Worktree, error) {
	s.calls++
	value, err := s.WorktreeManager.Create(ctx, in)
	if err != nil {
		return value, err
	}
	return value, errors.New("worktree creation outcome unavailable")
}
