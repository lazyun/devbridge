package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInspectionRecoveryOfflineProduction(t *testing.T) {
	for _, mode := range []string{"complete", "partial", "conflict", "mutation", "failed-restart", "question-restart", "child-response-loss", "cancel", "cancel-restart", "revision", "owner-concurrency"} {
		t.Run(mode, func(t *testing.T) { runInspectionRecoveryOffline(t, mode) })
	}
}
func TestInspectionRecoveryOfflinePersistenceWindows(t *testing.T) {
	for _, mode := range []string{"prepared-no-run", "created-no-turn", "uncertain-start", "report-not-saved", "report-saved", "assessment-saved", "resolution-reply-lost"} {
		t.Run(mode, func(t *testing.T) { runInspectionRecoveryOffline(t, "window:"+mode) })
	}
}
func runInspectionRecoveryOffline(t *testing.T, mode string) {
	entered, release := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer unblock()
	var report atomic.Value
	var continuationReport atomic.Value
	report.Store("")
	const instruction = "Observe the confirmed Inspection repositories read-only and return the exact structured recovery result."
	const continuationInstruction = "Continue the remaining confirmed read-only Inspection and return the canonical report."
	runProjectInspectionOfflineHooks(t, "unknown-response", func(n int64, state probeexecution.CanonicalState, input map[string]any) map[string]any {
		if mode == "owner-concurrency" && n == 3 {
			close(entered)
			select {
			case <-release:
			case <-time.After(45 * time.Second):
			}
		}
		raw, _ := json.Marshal(input)
		if mode == "failed-restart" {
			return recoveryMessage("not a structured recovery report")
		}
		expected := instruction
		if n > 3 && mode == "partial" {
			expected = continuationInstruction
		}
		if (mode == "question-restart" || mode == "child-response-loss" || mode == "cancel" || mode == "cancel-restart") && n == 3 {
			return map[string]any{"type": "function_call", "call_id": "inspection-recovery-question", "name": "request_user_input", "arguments": `{"questions":[{"id":"recover","header":"Recover","question":"Confirm the recovered Inspection?","options":[{"label":"yes","description":"Confirm"},{"label":"no","description":"Keep pending"}]}]}`}
		}
		if recoveryLatestUserText(string(raw)) != expected {
			t.Errorf("recovery instruction changed")
		}
		if n > 3 && mode == "partial" {
			var continued probeexecution.InspectionReport
			if err := json.Unmarshal([]byte(continuationReport.Load().(string)), &continued); err != nil {
				t.Error(err)
			}
			continued.Source = probeexecution.InspectionSourceForWorkset(state.Workset)
			raw, _ := json.Marshal(continued)
			return recoveryMessage(string(raw))
		}
		if mode == "mutation" {
			if err := os.WriteFile(filepath.Join(state.Workset.Members[0].CanonicalRoot, "recovery-mutation-evidence"), []byte("preserve"), 0600); err != nil {
				t.Error(err)
			}
		}
		return recoveryMessage(report.Load().(string))
	}, func(f *projectInspectionAPIFixture, state probeexecution.CanonicalState, calls *atomic.Int64) {
		var err error
		f.facade, err = controlapi.NewFromControlPlane(f.plane)
		if err != nil {
			t.Fatal(err)
		}
		runtime := sqlite.NewAgentRuntimeStore(f.plane.DB)
		binding, err := runtime.AppServerBinding(f.ctx, state.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("native App Server version=%s instance=%s source=%s", binding.CodexVersion, binding.ID, state.Run.ID)
		before, err := f.plane.Runs.AgentRuntime.Read(f.ctx, state.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		source := recoverystatus.Source{ProjectID: state.Run.ProjectID, OwnerKind: "PROJECT_INSPECTION", OwnerID: state.Run.OperationID, TaskID: state.Run.TaskID, OperationKind: "INSPECTION", OperationID: state.Run.OperationID, RunID: state.Run.ID, TurnID: state.Run.TurnID, RuntimeEpoch: state.Run.RuntimeEpoch, RuntimeWorksetHash: state.Workset.ContentHash, WorkspaceID: state.Workset.WorkspaceID, AgentConversationID: state.Conversation.ID, ThreadID: state.Run.ThreadID, AppServerInstanceID: binding.ID}
		nested := probeexecution.InspectionReport{SchemaVersion: "project-inspection-v1", Source: probeexecution.InspectionSourceForWorkset(state.Workset), Status: "SUCCEEDED", Summary: "Recovered read-only findings", Findings: []probeexecution.InspectionFinding{}, RepositoryObservations: []probeexecution.InspectionRepositoryObservation{}, BlockedReasons: []string{}, Relationships: []probeexecution.InspectionRelationship{}, RecommendedNextAction: "NONE"}
		recovered := recoverystatus.Report{Version: "v2", SourceOperationKind: "INSPECTION", SourceOperationID: source.OperationID, SourceRunID: source.RunID, SourceTurnID: source.TurnID, OverallStatus: "COMPLETED", Summary: "Recovered canonical Inspection", CompletedGoals: []recoverystatus.GoalObservation{}, RemainingGoals: []recoverystatus.GoalObservation{}, RepositoryObservations: []recoverystatus.RepositoryObservation{}, VerificationObservations: []recoverystatus.VerificationObservation{}, UnresolvedIssues: []string{}, RecommendedNextAction: "FINALIZE"}
		for _, m := range state.Workset.Members {
			nested.RepositoryObservations = append(nested.RepositoryObservations, probeexecution.InspectionRepositoryObservation{RepositoryAlias: m.RepositoryAlias, Summary: "Read only", RelevantPaths: []string{"same.txt"}})
			recovered.RepositoryObservations = append(recovered.RepositoryObservations, recoverystatus.RepositoryObservation{RuntimeAlias: m.RepositoryAlias, ChangedPaths: []string{}, Completion: "COMPLETED", VerificationState: "NOT_RUN"})
		}
		completeBytes, _ := json.Marshal(nested)
		continuationReport.Store(string(completeBytes))
		if mode == "partial" {
			nested.Status = "BLOCKED"
			nested.BlockedReasons = []string{"Remaining confirmed inspection needs explicit continuation"}
			nested.RecommendedNextAction = "INSPECT_MORE"
			recovered.OverallStatus = "PARTIAL"
			recovered.RecommendedNextAction = "CONTINUE"
			recovered.UnresolvedIssues = []string{"Remaining confirmed read-only inspection"}
		}
		if mode == "revision" {
			nested.Status = "BLOCKED"
			nested.BlockedReasons = []string{"New scope requires formal confirmation"}
			nested.RecommendedNextAction = "INSPECT_MORE"
			recovered.OverallStatus = "PARTIAL"
			recovered.ScopeExpansionRequired = true
			recovered.RecommendedNextAction = "REVISE"
			recovered.UnresolvedIssues = []string{"Scope expansion is not authorized"}
		}
		recovered.InspectionResult, _ = json.Marshal(nested)
		if mode == "conflict" {
			recovered.RepositoryObservations[0].ChangedPaths = []string{"not-observed.txt"}
		}
		raw, _ := json.Marshal(recovered)
		report.Store(string(raw))
		spec := recoverystatus.Spec{Source: source, RequestID: "inspection-recovery", RecoveryReason: "Lost engineering result with consumed response receipt UNKNOWN", Instruction: instruction, OutputSchemaVersion: "v2", Generation: 1}
		spec.PayloadHash = spec.ComputePayloadHash()
		prepared, err := f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, spec)
		if err != nil {
			t.Fatalf("prepare: %v cause=%v", err, errors.Unwrap(err))
		}
		if prepared.StartCommand == nil {
			t.Fatal("missing explicit start")
		}
		window := strings.TrimPrefix(mode, "window:")
		var injected *recoveryWindowStore
		reopen := func() {
			root := f.plane.DataPaths.Root
			if e := f.plane.Close(); e != nil {
				t.Fatal(e)
			}
			f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			f.facade, err = controlapi.NewFromControlPlane(f.plane)
			if err != nil {
				t.Fatal(err)
			}
			runtime = sqlite.NewAgentRuntimeStore(f.plane.DB)
		}
		if strings.HasPrefix(mode, "window:") {
			injected = &recoveryWindowStore{Store: f.plane.RecoveryStatus.Store, window: window}
			f.plane.RecoveryStatus.Store = injected
			if window == "prepared-no-run" {
				old := *prepared.StartCommand
				reopen()
				prepared, err = f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, prepared.Record.ID)
				if err != nil || prepared.Record.RecoveryRunID != "" || calls.Load() != 2 {
					t.Fatal("prepared restart dispatched")
				}
				if _, e := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, old); e == nil {
					t.Fatal("stale epoch accepted")
				}
			}
		}
		var responseFault *loseResolvedReceiptStore
		if mode == "child-response-loss" {
			responseFault = &loseResolvedReceiptStore{AgentRuntimeStore: sqlite.NewAgentRuntimeStore(f.plane.DB)}
			f.plane.Runs.AgentRuntime = agentruntime.NewService(responseFault)
			f.plane.RecoveryStatus.Config.AgentRuntime = f.plane.Runs.AgentRuntime
		}
		if mode == "complete" {
			f.plane.RecoveryStatus.Store = &inspectionChildGuardStore{RecoveryStatusStore: sqlite.NewRecoveryStatusStore(f.plane.DB), t: t, db: f.plane.DB}
		}
		started, err := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, *prepared.StartCommand)
		if injected != nil && window == "created-no-turn" {
			if err == nil || !injected.hit.Load() {
				t.Fatal("create reply fault not hit")
			}
			old, readErr := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, prepared.Record.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			reopen()
			prepared, err = f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, prepared.Record.ID)
			if err != nil || prepared.Record.RecoveryRunID != old.Record.RecoveryRunID || calls.Load() != 2 {
				t.Fatal("created restart changed child")
			}
			started, err = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, *prepared.StartCommand)
		}
		if err != nil {
			t.Fatalf("start: %v cause=%v", err, errors.Unwrap(err))
		}
		if mode == "owner-concurrency" {
			select {
			case <-entered:
			case <-time.After(20 * time.Second):
				t.Fatal("active child not reached")
			}
			if _, e := sqlite.NewInspectionRuntimeStore(f.plane.DB).InspectionSaveReport(f.ctx, source.RunID, source.RuntimeEpoch, completeBytes); e == nil {
				t.Fatal("obsolete source callback bypassed Recovery authority")
			}
			contender := spec
			contender.RequestID = "same-owner-contender"
			contender.Generation++
			contender.PayloadHash = contender.ComputePayloadHash()
			if _, e := f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, contender); e == nil {
				t.Fatal("same-owner active contender accepted")
			}
			peer := confirmedRuntimeInspection(t, f, "independent-inspection-owner")
			store := sqlite.NewInspectionRuntimeStore(f.plane.DB)
			peerSpec, e := store.ConfirmedInspection(f.ctx, f.session, peer.ID)
			if e != nil {
				t.Fatal(e)
			}
			reserved, e := store.ReserveInspection(f.ctx, peerSpec)
			if e != nil {
				t.Fatal(e)
			}
			work, before, e := f.plane.ContextProbeRunner.Canonical.Materializer.MaterializeInspection(f.ctx, reserved.Spec)
			if e != nil {
				t.Fatal(e)
			}
			peerState, e := store.BeginInspection(f.ctx, reserved.Spec, work, before)
			if e != nil {
				t.Fatal(e)
			}
			if peerState.Conversation.ID == state.Conversation.ID || peerState.Workset.WorkspaceID == state.Workset.WorkspaceID || peerState.Run.ID == state.Run.ID {
				t.Fatal("independent owners share authority/resources")
			}
			var count int
			if e = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM project_inspection_agent_claims WHERE inspection_id IN (?,?)`, source.OperationID, peer.ID).Scan(&count); e != nil || count != 2 {
				t.Fatalf("independent claims missing %d %v", count, e)
			}
			if calls.Load() != 3 {
				t.Fatal("reserve contender dispatched backend")
			}
			unblock()
		}
		if injected != nil && window != "prepared-no-run" && window != "created-no-turn" && window != "resolution-reply-lost" {
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) && !injected.hit.Load() {
				time.Sleep(25 * time.Millisecond)
			}
			if !injected.hit.Load() {
				t.Fatal("persistence window not reached")
			}
			time.Sleep(100 * time.Millisecond)
			old, e := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, started.Record.ID)
			if e != nil {
				t.Fatal(e)
			}
			reopen()
			fresh, e := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, started.Record.ID)
			if e != nil || fresh.Record.RecoveryRunID != old.Record.RecoveryRunID || fresh.Record.Spec.RuntimeEpoch != source.RuntimeEpoch {
				t.Fatal("restart changed durable source/child identity")
			}
			if window == "uncertain-start" {
				if fresh.Record.RecoveryTurnID != "" || fresh.Record.State != "UNKNOWN" || calls.Load() != 3 {
					t.Fatalf("uncertain start redispatched: %s calls=%d", fresh.Record.State, calls.Load())
				}
				return
			}
		}
		if mode == "question-restart" || mode == "child-response-loss" || mode == "cancel" || mode == "cancel-restart" {
			wait := func(old string) agentruntime.Request {
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) {
					snapshot, e := f.plane.Runs.AgentRuntime.Read(f.ctx, started.Record.RecoveryRunID)
					if e == nil {
						for _, q := range snapshot.Requests {
							if q.State == "WAITING_FOR_USER" && q.ConnectionID != old && len(q.AvailableActions) > 0 {
								return q
							}
						}
					}
					time.Sleep(25 * time.Millisecond)
				}
				snapshot, e := f.plane.Runs.AgentRuntime.Read(f.ctx, started.Record.RecoveryRunID)
				record, re := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, started.Record.ID)
				states := []string{}
				for _, q := range snapshot.Requests {
					states = append(states, string(q.State)+":"+q.ConnectionID)
				}
				t.Fatalf("recovery question unavailable old=%s calls=%d requests=%v waiting=%s err=%v recordState=%s failure=%s read=%v", old, calls.Load(), states, snapshot.WaitingState, e, record.Record.State, record.Record.FailureReason, re)
				return agentruntime.Request{}
			}
			old := wait("")
			if mode == "child-response-loss" {
				decision := agentruntime.UserDecisionInput{Identity: old.Identity, RuntimeRequestID: old.ID, SubmissionRequestID: "child-response-loss-decision", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: "yes", SelectedOptionID: "yes"}
				decision.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(decision)
				if _, e := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, decision); e != nil {
					t.Fatal(e)
				}
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) && (calls.Load() != 4 || responseFault.rejected.Load() == 0) {
					time.Sleep(25 * time.Millisecond)
				}
				if calls.Load() != 4 || responseFault.rejected.Load() == 0 {
					t.Fatal("child response persistence loss not reached")
				}
				reopen()
				snapshot, e := f.plane.Runs.AgentRuntime.Read(f.ctx, started.Record.RecoveryRunID)
				if e != nil {
					t.Fatal(e)
				}
				unknown := false
				for _, q := range snapshot.Requests {
					if q.ID == old.ID && q.UserDecision != nil && agentruntime.ResponseDisposition(q) == "UNKNOWN" {
						unknown = true
					}
				}
				if !unknown {
					t.Fatal("child consumed response inferred from terminal engineering result")
				}
				fresh, e := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, started.Record.ID)
				if e != nil {
					t.Fatal(e)
				}
				cmd := *prepared.StartCommand
				cmd.ExpectedRecoveryEpoch = fresh.Record.RuntimeEpoch
				if _, e = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); e != nil {
					t.Fatal(e)
				}
				time.Sleep(100 * time.Millisecond)
				if calls.Load() != 4 {
					t.Fatal("child UNKNOWN response resent")
				}
				return
			}
			if mode == "cancel" {
				receipt, e := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, ProjectID: f.project, OwnerType: "INSPECTION", OwnerID: source.OperationID, RunID: source.RunID, RequestID: "cancel-inspection-recovery", Reason: "Stop exact recovery child"})
				if e != nil {
					t.Fatalf("cancel: %v cause=%v", e, errors.Unwrap(e))
				}
				if receipt.State != "CANCELED" {
					t.Fatalf("cancel did not converge: %+v", receipt)
				}
				after, e := f.plane.Runs.AgentRuntime.Read(f.ctx, source.RunID)
				if e != nil {
					t.Fatal(e)
				}
				for _, q := range after.Requests {
					if q.UserDecision != nil && agentruntime.ResponseDisposition(q) != "UNKNOWN" {
						t.Fatal("cancel rewrote original unknown")
					}
				}
				return
			}
			dataRoot := f.plane.DataPaths.Root
			if err = f.plane.Close(); err != nil {
				t.Fatal(err)
			}
			f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: dataRoot})
			if err != nil {
				t.Fatal(err)
			}
			f.facade, err = controlapi.NewFromControlPlane(f.plane)
			if err != nil {
				t.Fatal(err)
			}
			runtime = sqlite.NewAgentRuntimeStore(f.plane.DB)
			q := wait(old.ConnectionID)
			wrong := q.Identity
			wrong.TurnID = "unrelated-turn"
			wrong.ConnectionID = "unrelated-connection"
			if _, e := runtime.Reattach(f.ctx, wrong, binding); e == nil {
				t.Fatal("storage reattached wrong child Turn")
			}
			if q.ThreadID != old.ThreadID || q.TurnID != old.TurnID || calls.Load() != 3 {
				t.Fatal("recovery question reattached wrong identity")
			}
			if mode == "cancel-restart" {
				receipt, e := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, ProjectID: f.project, OwnerType: "INSPECTION", OwnerID: source.OperationID, RunID: source.RunID, RequestID: "cancel-restarted-inspection-recovery", Reason: "Stop exact reattached recovery child"})
				if e != nil || receipt.State != "CANCELED" {
					t.Fatalf("restarted cancel: %+v %v cause=%v", receipt, e, errors.Unwrap(e))
				}
				var claims int
				if e = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM project_inspection_agent_claims WHERE inspection_id=?`, source.OperationID).Scan(&claims); e != nil || claims != 0 {
					t.Fatalf("restarted cancel retained source authority: %d %v", claims, e)
				}
				return
			}
			decision := agentruntime.UserDecisionInput{Identity: q.Identity, RuntimeRequestID: q.ID, SubmissionRequestID: "inspection-recovery-decision", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: "yes", SelectedOptionID: "yes"}
			decision.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(decision)
			if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, decision); err != nil {
				t.Fatal(err)
			}
		}
		var result controlapi.RecoveryStatusResult
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			result, err = f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, started.Record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Record.State == "ASSESSED" || result.Record.State == "FAILED" {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if result.ResolveCommand == nil {
			if mode == "failed-restart" {
				if result.Record.State != "FAILED" {
					t.Fatalf("malformed report not failed: %s", result.Record.State)
				}
				reopen()
				fresh, e := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, result.Record.ID)
				if e != nil || fresh.Record.State != "FAILED" || fresh.Record.Spec.RuntimeEpoch != source.RuntimeEpoch || calls.Load() != 3 {
					t.Fatalf("failed recovery resumed source/redispatched: %v calls=%d", e, calls.Load())
				}
				retry := spec
				retry.RequestID = "forbidden-second-probe"
				retry.Generation++
				retry.PayloadHash = retry.ComputePayloadHash()
				if _, e = f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, retry); e == nil {
					t.Fatal("failed child admitted another diagnostic generation")
				}
				// A failed child is not resumed, but its advertised explicit owner
				// cancellation must still close the exact old child after restart.
				receipt, e := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, ProjectID: f.project, OwnerType: "INSPECTION", OwnerID: source.OperationID, RunID: source.RunID, RequestID: "cancel-failed-cold-inspection", Reason: "Close failed diagnostic after restart"})
				if e != nil || receipt.State != "CANCELED" {
					t.Fatalf("failed cold cancellation: %+v %v", receipt, e)
				}
				closed, e := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, result.Record.ID)
				if e != nil || closed.Record.RuntimeEpoch != result.Record.RuntimeEpoch || closed.Record.RecoveryTurnID != result.Record.RecoveryTurnID || closed.Record.FailureReason != "RECOVERY_CANCELED" || calls.Load() != 3 {
					t.Fatalf("failed cancellation rewrote epoch or dispatched: %+v calls=%d err=%v", closed.Record, calls.Load(), e)
				}
				return
			}
			if mode == "conflict" || mode == "mutation" {
				if result.Projection.Consistency != "CONFLICT" {
					t.Fatalf("expected factual conflict: %s", result.Record.Assessment)
				}
				current, e := sqlite.NewInspectionRuntimeStore(f.plane.DB).InspectionState(f.ctx, source.OperationID)
				if e != nil || current.Finalization != nil {
					t.Fatalf("conflict advanced owner: %+v %v", current, e)
				}
				return
			}
			t.Fatalf("not resolvable: state=%s reason=%s assessment=%s", result.Record.State, result.Record.FailureReason, result.Record.Assessment)
		}
		if mode == "conflict" || mode == "mutation" {
			t.Fatal("conflicting report offered resolution")
		}
		_, err = f.facade.ResolveRecoveryStatusForSession(f.ctx, f.session, *result.ResolveCommand)
		if injected != nil && window == "resolution-reply-lost" {
			if err == nil || !injected.hit.Load() {
				t.Fatal("resolution reply fault not hit")
			}
			reopen()
			fresh, e := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, result.Record.ID)
			if e != nil || fresh.Record.State != "RESOLVED" {
				t.Fatal("committed resolution lost after reply loss")
			}
			err = nil
		}
		if err != nil {
			t.Fatalf("resolve: %v cause=%v", err, errors.Unwrap(err))
		}
		if mode == "revision" {
			in := probeexecution.InspectionContinuationInput{RecoveryStatusID: result.Record.ID, RequestID: "invalid-revision-continuation", Instruction: continuationInstruction, ExpectedRuntimeEpoch: result.Record.RuntimeEpoch}
			in.PayloadHash = in.ComputePayloadHash()
			if _, err = f.facade.ContinueInspectionRecoveryForSession(f.ctx, f.session, in); err == nil {
				t.Fatal("revision accepted same-operation continuation")
			}
			current, e := sqlite.NewInspectionRuntimeStore(f.plane.DB).InspectionState(f.ctx, source.OperationID)
			if e != nil || current.Workset.ContentHash != state.Workset.ContentHash || current.Workset.WorkspaceID != state.Workset.WorkspaceID {
				t.Fatal("revision mutated original workset")
			}
			config, e := f.plane.CodexConfigs.Get(f.ctx, "codex_config_default")
			if e != nil {
				t.Fatal(e)
			}
			plan, e := f.facade.PrepareProjectInspection(f.ctx, contextprobe.PrepareInspectionInput{AISessionID: f.session, RequestID: "formally-confirmed-revision", CodexInstruction: "Inspect app, sdk, and newly confirmed backend read-only.", Backend: config.Selection("test").Input(), Model: "gpt-5.6-terra", ReasoningEffort: "high", PrimaryRepositoryID: f.repos["app"], Targets: []contextprobe.TargetSelection{{RepositoryID: f.repos["app"], Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}}, {RepositoryID: f.repos["sdk"], Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}}, {RepositoryID: f.repos["backend"], Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}}}})
			if e != nil {
				t.Fatal(e)
			}
			successor, e := f.facade.ConfirmProjectInspection(f.ctx, contextprobe.ConfirmInspectionInput{Plan: plan, PayloadSHA256: plan.PayloadSHA256})
			if e != nil {
				t.Fatal(e)
			}
			if successor.ID == source.OperationID || successor.TaskID == source.TaskID || successor.Canonical == nil || successor.RunID != "" || calls.Load() != 3 {
				t.Fatal("revision successor bypassed formal fresh confirmation")
			}
			return
		}
		if mode == "partial" {
			in := probeexecution.InspectionContinuationInput{RecoveryStatusID: result.Record.ID, RequestID: "explicit-inspection-continuation", Instruction: continuationInstruction, ExpectedRuntimeEpoch: result.Record.RuntimeEpoch}
			in.PayloadHash = in.ComputePayloadHash()
			successor, e := f.facade.ContinueInspectionRecoveryForSession(f.ctx, f.session, in)
			if e != nil {
				t.Fatalf("continue: %v cause=%v", e, errors.Unwrap(e))
			}
			var sourceExecution, successorExecution string
			if e = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT agent_execution_id FROM runs WHERE id=?`, source.RunID).Scan(&sourceExecution); e != nil {
				t.Fatal(e)
			}
			if e = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT agent_execution_id FROM runs WHERE id=?`, successor.RunID).Scan(&successorExecution); e != nil || sourceExecution == "" || successorExecution != sourceExecution {
				t.Fatalf("continuation changed frozen Agent Execution source=%q successor=%q err=%v", sourceExecution, successorExecution, e)
			}
			deadline = time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				current, e := sqlite.NewInspectionRuntimeStore(f.plane.DB).InspectionState(f.ctx, source.OperationID)
				if e == nil && current.Run.ID != source.RunID && current.FailureReason != "" && current.FailureReason != "TURN_START_PENDING" && current.FailureReason != "THREAD_START_PENDING" {
					t.Fatalf("continuation runtime failed: %s calls=%d", current.FailureReason, calls.Load())
				}
				if e == nil && current.Finalization != nil && current.Run.ID != source.RunID {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			current, e := sqlite.NewInspectionRuntimeStore(f.plane.DB).InspectionState(f.ctx, source.OperationID)
			if e != nil || current.Finalization == nil || !current.ContinuationFreshThread || current.ContinuationSourceTurnID != "" || current.Run.ThreadID == "" || current.Run.ThreadID == source.ThreadID || current.Run.TurnID == "" || current.Conversation.ID == source.AgentConversationID || current.Conversation.PredecessorID != source.AgentConversationID {
				t.Fatalf("partial continuation did not create an exact fresh boundary: state=%s finalized=%v fresh=%v thread=%s conversation=%s predecessor=%s calls=%d err=%v", current.State, current.Finalization != nil, current.ContinuationFreshThread, current.Run.ThreadID, current.Conversation.ID, current.Conversation.PredecessorID, calls.Load(), e)
			}
			continuedBinding, e := runtime.AppServerBinding(f.ctx, current.Run.ID)
			if e != nil || continuedBinding.ID != binding.ID || continuedBinding.PID != binding.PID {
				t.Fatalf("continuation replaced the frozen App Server: %v", e)
			}
			var freshBoundary bool
			if e = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN agent_conversations a ON a.id=b.agent_conversation_id JOIN agent_conversations old ON old.id=a.predecessor_id JOIN runs child ON child.id=? WHERE b.run_id=? AND b.entry_boundary_turn_id IS NULL AND a.backend_thread_id=? AND a.id=? AND old.id=? AND old.ended_at IS NOT NULL AND old.continuation_safe=0 AND child.thread_id<>a.backend_thread_id)`, result.Record.RecoveryRunID, current.Run.ID, current.Run.ThreadID, current.Conversation.ID, source.AgentConversationID).Scan(&freshBoundary); e != nil || !freshBoundary {
				t.Fatalf("partial continuation borrowed a prior Thread boundary: %v", e)
			}
			if replay, e := f.facade.ContinueInspectionRecoveryForSession(f.ctx, f.session, in); e != nil || replay.RunID != current.Run.ID || calls.Load() != 4 {
				t.Fatalf("partial continuation replay changed its receipt or dispatched: %+v %v calls=%d", replay, e, calls.Load())
			}
			changed := in
			changed.Instruction += " changed"
			changed.PayloadHash = changed.ComputePayloadHash()
			if _, err = f.facade.ContinueInspectionRecoveryForSession(f.ctx, f.session, changed); err == nil {
				t.Fatal("changed continuation replay accepted")
			}
		}
		after, err := f.plane.Runs.AgentRuntime.Read(f.ctx, state.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(before.Requests) != len(after.Requests) {
			t.Fatal("source requests changed")
		}
		for i, q := range before.Requests {
			if agentruntime.ResponseDisposition(q) != "UNKNOWN" {
				continue
			}
			a, _ := json.Marshal(q.UserDecision)
			b, _ := json.Marshal(after.Requests[i].UserDecision)
			if string(a) != string(b) || agentruntime.ResponseDisposition(after.Requests[i]) != "UNKNOWN" {
				t.Fatal("UNKNOWN history changed")
			}
		}
		childBinding, err := runtime.AppServerBinding(f.ctx, result.Record.RecoveryRunID)
		if err != nil || childBinding.ID != binding.ID || childBinding.PID != binding.PID {
			t.Fatalf("S1 changed: %v", err)
		}
		canonical, err := sqlite.NewInspectionRuntimeStore(f.plane.DB).InspectionState(f.ctx, source.OperationID)
		if err != nil || canonical.Finalization == nil || canonical.Finalization.Status != "SUCCEEDED" {
			t.Fatalf("canonical result missing: state=%s reason=%s finalized=%v err=%v", canonical.State, canonical.FailureReason, canonical.Finalization != nil, err)
		}
		probe, e := f.plane.ContextProbes.Get(f.ctx, source.OperationID)
		if e != nil || probe.Status != contextprobe.StatusSucceeded {
			t.Fatalf("canonical owner result not converged: status=%s err=%v", probe.Status, e)
		}
		expectedCalls := int64(3)
		if mode == "partial" || mode == "question-restart" {
			expectedCalls = 4
		}
		if calls.Load() != expectedCalls {
			t.Fatalf("unexpected native turns %d", calls.Load())
		}
	})
}
func TestRealCodexInspectionRecoveryOptIn(t *testing.T) {
	if os.Getenv("DEVBRIDGE_REAL_CODEX_INSPECTION_RECOVERY") != "1" {
		t.Skip("NOT_RUN: real model Inspection recovery requires explicit opt-in")
	}
	native, authPath := os.Getenv("DEVBRIDGE_REAL_CODEX_INSPECTION_RECOVERY_BINARY"), os.Getenv("DEVBRIDGE_REAL_CODEX_INSPECTION_RECOVERY_AUTH_FILE")
	if native == "" || authPath == "" {
		t.Fatal("explicit test binary and test auth file required")
	}
	auth, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err = os.WriteFile(filepath.Join(home, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "config.toml"), []byte("[features]\nplugins=false\nremote_plugin=false\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := newProjectInspectionAPIFixture(t)
	p := confirmedRuntimeInspection(t, f)
	runner := f.plane.ContextProbeRunner.Canonical
	runner.Store = inspectionLostReportStore{runner.Store}
	runner.AppServerSpec = func(codexconfig.Config) appserver.Spec {
		return appserver.Spec{ExecutablePath: native, CodexHome: home}
	}
	started, err := runner.StartConfirmed(f.ctx, f.session, p.ID, p.CanonicalRequestID, p.CanonicalPayloadSHA256)
	if err != nil {
		t.Fatal(err)
	}
	var state probeexecution.CanonicalState
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		state, err = sqlite.NewInspectionRuntimeStore(f.plane.DB).InspectionState(f.ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.State == "RECOVERY_REQUIRED" && state.Run.TurnID != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if state.State != "RECOVERY_REQUIRED" {
		t.Fatal("real source did not reach report-loss boundary")
	}
	binding, err := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer appserver.Stop(f.ctx, binding)
	source := recoverystatus.Source{ProjectID: f.project, OwnerKind: "PROJECT_INSPECTION", OwnerID: p.ID, TaskID: p.TaskID, OperationKind: "INSPECTION", OperationID: p.ID, RunID: state.Run.ID, TurnID: state.Run.TurnID, RuntimeEpoch: state.Run.RuntimeEpoch, RuntimeWorksetHash: state.Workset.ContentHash, WorkspaceID: state.Workset.WorkspaceID, AgentConversationID: state.Conversation.ID, ThreadID: state.Run.ThreadID, AppServerInstanceID: binding.ID}
	spec := recoverystatus.Spec{Source: source, RequestID: "real-inspection-recovery", RecoveryReason: "Injected loss of terminal structured source result", Instruction: "Reconcile the prior completed read-only Inspection without editing files. Return the v2 recovery schema with completed findings in inspectionResult, exact source identities, every runtime repository observation, no invented changes, no scope expansion, and FINALIZE only when complete.", OutputSchemaVersion: "v2", Generation: 1}
	spec.PayloadHash = spec.ComputePayloadHash()
	prepared, err := f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, spec)
	if err != nil {
		t.Fatal(err)
	}
	root := f.plane.DataPaths.Root
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err = f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, prepared.Record.ID)
	if err != nil || prepared.StartCommand == nil {
		t.Fatalf("prepared restart: %v", err)
	}
	if _, err = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, *prepared.StartCommand); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		result, e := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, prepared.Record.ID)
		if e != nil {
			t.Fatal(e)
		}
		if result.Record.State == "ASSESSED" {
			if result.ResolveCommand == nil {
				t.Fatalf("real assessment: %s", result.Record.Assessment)
			}
			if _, e = f.facade.ResolveRecoveryStatusForSession(f.ctx, f.session, *result.ResolveCommand); e != nil {
				t.Fatal(e)
			}
			child, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, result.Record.RecoveryRunID)
			if e != nil || child.ID != binding.ID {
				t.Fatal("real recovery replaced S1")
			}
			return
		}
		if result.Record.State == "FAILED" {
			t.Fatalf("real recovery failed: %s", result.Record.FailureReason)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("real recovery did not converge")
}

type inspectionLostReportStore struct{ probeexecution.CanonicalStore }

func (s inspectionLostReportStore) InspectionSaveReport(ctx context.Context, runID, epoch string, raw []byte) (probeexecution.CanonicalState, error) {
	return probeexecution.CanonicalState{}, errors.New("isolated real fixture: terminal report write lost")
}

func TestInspectionRecoveryLegacyFailsClosed(t *testing.T) {
	f := newProjectInspectionAPIFixture(t)
	legacy, err := f.plane.ContextProbes.Create(f.ctx, contextprobe.CreateInput{AISessionID: f.session, RepositoryID: f.repos["app"], CodexConfigID: "codex_config_default", CodexInstruction: "Historical legacy Inspection", Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	spec := recoverystatus.Spec{Source: recoverystatus.Source{ProjectID: f.project, OwnerKind: "PROJECT_INSPECTION", OwnerID: legacy.ID, TaskID: legacy.TaskID, OperationKind: "INSPECTION", OperationID: legacy.ID, RunID: legacy.RunID}, RequestID: "legacy-recovery", RecoveryReason: "legacy evidence unavailable", Instruction: "Observe only", OutputSchemaVersion: "v2", Generation: 1}
	spec.PayloadHash = spec.ComputePayloadHash()
	if _, err = f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, spec); err == nil {
		t.Fatal("legacy recovery fabricated canonical source")
	}
	read, err := f.plane.ContextProbes.Get(f.ctx, legacy.ID)
	if err != nil || read.Canonical != nil || read.CodexInstruction != legacy.CodexInstruction {
		t.Fatal("legacy history changed")
	}
	for _, table := range []string{"recovery_status_records", "app_server_run_bindings", "agent_conversations", "project_inspection_agent_claims"} {
		var count int
		if err = f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("legacy recovery side effect %s=%d %v", table, count, err)
		}
	}
}
