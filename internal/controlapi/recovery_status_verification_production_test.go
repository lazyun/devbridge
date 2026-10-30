package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestRecoveryStatusOfflineVerificationObservedChecks(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "all-required-observed", true: "missing-required-observation"}[missing], func(t *testing.T) { runRecoveryVerification(t, missing) })
	}
}
func TestRecoveryVerificationAcceptanceDeliversWithoutConsumingOldAnswer(t *testing.T) {
	runRecoveryVerification(t, false, true)
}

func runRecoveryVerification(t *testing.T, missing bool, accept ...bool) {
	native := os.Getenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY")
	if native == "" {
		t.Skip("offline installed App Server binary not selected")
	}
	var calls atomic.Int64
	var checks, verificationReport, recoveryReport atomic.Value
	checks.Store([]map[string]any{})
	verificationReport.Store("")
	recoveryReport.Store("")
	var cfg controlplane.Config
	var fault *loseResolvedReceiptStore
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		question, source := waitContinuityQuestion(t, f, "")
		// The native question can overtake the local telemetry projection of
		// the preceding command completion. Lose the answer receipt only after
		// all three actual command observations have reached durable storage.
		deadline := time.Now().Add(10 * time.Second)
		for {
			current, err := f.plane.Runs.Get(f.ctx, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			completed := 0
			if current.AgentRuntime != nil {
				for _, item := range current.AgentRuntime.Items {
					if item.Type == "commandExecution" && item.Status == "completed" {
						completed++
					}
				}
			}
			if completed == 3 {
				source = current
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("source command capture incomplete: %d", completed)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if source.Purpose != run.PurposeVerification {
			t.Fatalf("source is not Verification: %+v", source)
		}
		definition, e := f.plane.Tasks.Get(f.ctx, source.TaskID)
		if e != nil {
			t.Fatal(e)
		}
		work, e := f.plane.Runs.RuntimeWorkset(f.ctx, source.ID)
		if e != nil {
			t.Fatal(e)
		}
		commands := []map[string]any{}
		items := []map[string]any{}
		criteria := []map[string]any{}
		for i, check := range definition.VerificationSpec.Checks {
			root := ""
			for _, m := range work.Members {
				if m.RepositoryID == check.RepositoryID {
					root = m.CanonicalRoot
				}
			}
			commands = append(commands, map[string]any{"cmd": check.Command, "workdir": filepath.Join(root, check.WorkingDirectory), "max_output_tokens": 1000})
			items = append(items, map[string]any{"index": i, "status": "PASSED", "summary": "Declared exact check passed", "evidence": "native command completed with exit code 0", "reason": ""})
		}
		for i := range definition.VerificationSpec.AcceptanceCriteria {
			criteria = append(criteria, map[string]any{"index": i, "status": "PASSED", "summary": "All declared checks passed", "evidence": "native command observations", "reason": ""})
		}
		checks.Store(commands)
		raw, _ := json.Marshal(map[string]any{"version": 2, "summary": "Verification checks completed", "checks": items, "acceptanceCriteria": criteria})
		verificationReport.Store(string(raw))
		binding, e := fault.AppServerBinding(f.ctx, source.ID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, question, "RESPOND", "yes")); e != nil {
			t.Fatal(e)
		}
		until := time.Now().Add(45 * time.Second)
		for time.Now().Before(until) {
			if calls.Load() >= 7 && fault.rejected.Load() > 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if calls.Load() != 7 {
			t.Fatalf("source verification did not complete calls=%d", calls.Load())
		}
		reopenRecoveryFixture(t, f, cfg)
		old := waitUnknownRequest(t, f, question, true)
		source, e = f.plane.Runs.Get(f.ctx, source.ID)
		if e != nil {
			t.Fatal(e)
		}
		src := recoverystatus.Source{ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: old.TaskDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: work.ContentHash, AgentConversationID: old.AgentConversationID, ThreadID: source.ThreadID, AppServerInstanceID: binding.ID}
		report := recoverystatus.Report{Version: recoverystatus.ReportVersion, SourceOperationKind: src.OperationKind, SourceOperationID: src.OperationID, SourceRunID: src.RunID, SourceTurnID: src.TurnID, OverallStatus: "COMPLETED", Summary: "Verification status independently reported", CompletedGoals: []recoverystatus.GoalObservation{}, RemainingGoals: []recoverystatus.GoalObservation{}, RepositoryObservations: []recoverystatus.RepositoryObservation{}, VerificationObservations: []recoverystatus.VerificationObservation{}, UnresolvedIssues: []string{}, RecommendedNextAction: "FINALIZE", VerificationReport: raw}
		goals, e := f.plane.Tasks.ListGoals(f.ctx, source.TaskID)
		if e != nil {
			t.Fatal(e)
		}
		if len(goals) == 0 {
			report.CompletedGoals = append(report.CompletedGoals, recoverystatus.GoalObservation{GoalID: definition.WorkSpec.Goal, Status: "COMPLETED", EvidenceSummary: "approved required checks observed"})
		} else {
			for _, g := range goals {
				report.CompletedGoals = append(report.CompletedGoals, recoverystatus.GoalObservation{GoalID: g.ID, Status: "COMPLETED", EvidenceSummary: "approved required checks observed"})
			}
		}
		for _, m := range work.Members {
			report.RepositoryObservations = append(report.RepositoryObservations, recoverystatus.RepositoryObservation{RuntimeAlias: m.RepositoryAlias, ChangedPaths: []string{}, Completion: "COMPLETED", VerificationState: "PASSED"})
		}
		for i := range commands {
			report.VerificationObservations = append(report.VerificationObservations, recoverystatus.VerificationObservation{CheckID: fmt.Sprintf("check-%d", i+1), Status: "PASSED", EvidenceSummary: "native observed command succeeded"})
		}
		encoded, _ := json.Marshal(report)
		recoveryReport.Store(string(encoded))
		prepared, e := f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, recoverystatus.Spec{Source: src, RequestID: "verification-status", RecoveryReason: "MISSING_VERIFICATION_REPORT", Instruction: "Caller requests the exact original verification status only.", OutputSchemaVersion: recoverystatus.ReportVersion, Generation: 1})
		if e != nil {
			t.Fatalf("prepare %v cause=%v", e, errors.Unwrap(e))
		}
		cmd := controlapi.RecoveryStatusCommand{Source: src, ID: prepared.Record.ID, RequestID: prepared.Record.Spec.RequestID, PayloadHash: prepared.Record.Spec.PayloadHash, ExpectedRecoveryEpoch: prepared.Record.RuntimeEpoch}
		if _, e = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); e != nil {
			t.Fatal(e)
		}
		assessed := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool { return r.State == "ASSESSED" || r.State == "FAILED" })
		var assessment recoverystatus.Assessment
		if e = json.Unmarshal(assessed.Assessment, &assessment); e != nil {
			t.Fatalf("assessment %s %s %v", assessed.State, assessed.FailureReason, e)
		}
		if missing {
			if assessment.EngineeringDisposition == "RECOVERED_COMPLETE" {
				t.Fatal("reportalone credited unobserved check")
			}
			current, e := f.plane.Tasks.Get(f.ctx, source.TaskID)
			if e != nil || current.Phase != task.PhaseRecoveryRequired || current.Status != task.StatusActive {
				t.Fatalf("missingcheck advanced %+v %v", current, e)
			}
		} else {
			if assessment.EngineeringDisposition != "RECOVERED_COMPLETE" {
				t.Fatalf("actual checks not accepted: disposition=%s reasons=%v", assessment.EngineeringDisposition, assessment.ReasonCodes)
			}
			var childSnapshot string
			if e = f.plane.DB.SQL().QueryRow(`SELECT snapshot_json FROM agent_runtime_states WHERE run_id=?`, assessed.RecoveryRunID).Scan(&childSnapshot); e != nil {
				t.Fatal(e)
			}
			if _, e = f.plane.DB.SQL().Exec(`UPDATE agent_runtime_states SET snapshot_json='{}' WHERE run_id=?`, assessed.RecoveryRunID); e != nil {
				t.Fatal(e)
			}
			if _, e = resolveRecoveryFixture(t, f, cmd, assessed); e == nil {
				t.Fatal("malformed child request authority finalized the source")
			}
			unchanged, readErr := f.plane.Runs.Get(f.ctx, source.ID)
			if readErr != nil || unchanged.Status == run.StatusSucceeded || unchanged.VerificationReport != nil {
				t.Fatalf("rejected resolution changed source: status=%s report=%+v err=%v", unchanged.Status, unchanged.VerificationReport, readErr)
			}
			if _, e = f.plane.DB.SQL().Exec(`UPDATE agent_runtime_states SET snapshot_json=? WHERE run_id=?`, childSnapshot, assessed.RecoveryRunID); e != nil {
				t.Fatal(e)
			}
			resolved, e := resolveRecoveryFixture(t, f, cmd, assessed)
			if e != nil {
				t.Fatalf("verification resolve %v cause=%v", e, errors.Unwrap(e))
			}
			if resolved.Record.State != "RESOLVED" {
				t.Fatal(resolved)
			}
			current, e := f.plane.Tasks.Get(f.ctx, source.TaskID)
			if e != nil || current.Phase != task.PhaseAcceptancePending {
				t.Fatalf("verification didnot reach acceptancepending %+v %v", current, e)
			}
			reopenRecoveryFixture(t, f, cfg)
			recovered, e := f.plane.Runs.Get(f.ctx, source.ID)
			if e != nil || recovered.Status != run.StatusSucceeded || recovered.VerificationReport == nil || recovered.VerificationReport.Availability != run.VerificationReportAvailable || run.ValidateVerificationReportV2(*recovered.VerificationReport) != nil {
				t.Fatalf("recovered report did not survive reopen: status=%s report=%+v err=%v", recovered.Status, recovered.VerificationReport, e)
			}
			if len(accept) > 0 && accept[0] {
				assertRecoveredVerificationDelivery(t, f, cfg, recovered)
			}
		}
		if calls.Load() != 8 || assessed.RecoveryTurnID == source.TurnID || assessed.Spec.ThreadID != source.ThreadID {
			t.Fatalf("probe identity/calls %+v calls=%d", assessed, calls.Load())
		}
		return false
	}, func(f *upperLifecycleFixture) {
		roots := map[string]string{}
		rows, e := f.plane.DB.SQL().Query(`SELECT b.repository_id,wt.path FROM run_repository_bindings b JOIN worktrees wt ON wt.id=b.worktree_id WHERE b.run_id=?`, f.run)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var id, path string
			if e = rows.Scan(&id, &path); e != nil {
				t.Fatal(e)
			}
			roots[id] = path
		}
		rows.Close()
		edits := []string{}
		for _, alias := range []string{"app", "sdk"} {
			edits = append(edits, "printf 'coded "+alias+"\\n' > "+recoveryShellQuote(filepath.Join(roots[f.repos[alias]], "same.txt")))
		}
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusUpgradeRequired)
				return
			}
			defer r.Body.Close()
			var input any
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				http.Error(w, "fixture request", 400)
				return
			}
			n := calls.Add(1)
			if n == 3 {
				definition, err := f.plane.Tasks.Get(f.ctx, f.task)
				if err != nil {
					t.Error(err)
					http.Error(w, "definition", 500)
					return
				}
				commands := []map[string]any{}
				for _, check := range definition.VerificationSpec.Checks {
					commands = append(commands, map[string]any{"cmd": check.Command, "workdir": filepath.Join(roots[check.RepositoryID], check.WorkingDirectory), "max_output_tokens": 1000})
				}
				checks.Store(commands)
			}
			var item map[string]any
			command := func(args map[string]any) map[string]any {
				b, _ := json.Marshal(args)
				return map[string]any{"type": "function_call", "call_id": fmt.Sprint("observed-verification-", n), "name": "exec_command", "arguments": string(b)}
			}
			switch n {
			case 1:
				item = command(map[string]any{"cmd": strings.Join(edits, " && "), "max_output_tokens": 1000})
			case 2:
				item = recoveryMessage(`{"resultDisposition":"COMPLETED","summary":"Both approved contracts updated"}`)
			case 6:
				item = map[string]any{"type": "function_call", "call_id": "verify-confirmation", "name": "request_user_input", "arguments": `{"questions":[{"id":"adaptation","header":"Verify","question":"Confirm reporting the approved verification check results?","options":[{"label":"yes","description":"Continue"},{"label":"no","description":"Stop"}]}]}`}
			case 3, 4, 5:
				if missing && n == 5 {
					item = command(map[string]any{"cmd": "true", "workdir": roots[f.repos["app"]], "max_output_tokens": 1000})
				} else {
					item = command(checks.Load().([]map[string]any)[n-3])
				}
			case 7:
				item = recoveryMessage(verificationReport.Load().(string))
			default:
				item = recoveryMessage(recoveryReport.Load().(string))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": fmt.Sprint("verification-recovery-", n)}}, {"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": fmt.Sprint("verification-recovery-", n), "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
				b, _ := json.Marshal(event)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], b)
			}
		}))
		t.Cleanup(provider.Close)
		home := t.TempDir()
		if e = os.WriteFile(filepath.Join(home, "config.toml"), []byte(fmt.Sprintf("openai_base_url=%q\nmodel=\"mock-model\"\nmodel_provider=\"openai\"\n[features]\ndefault_mode_request_user_input=true\nplugins=false\nremote_plugin=false\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n", provider.URL+"/v1")), 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"OPENAI_API_KEY":"offline-fixture"}`), 0600); e != nil {
			t.Fatal(e)
		}
		spec := func(codexconfig.Config) appserver.Spec {
			return appserver.Spec{ExecutablePath: native, CodexHome: home, Env: []string{"OPENAI_BASE_URL=" + provider.URL + "/v1", "OPENAI_API_KEY=offline-fixture"}}
		}
		store := sqlite.NewAgentRuntimeStore(f.plane.DB)
		fault = &loseResolvedReceiptStore{AgentRuntimeStore: store}
		f.plane.Runs.AgentRuntime = agentruntime.NewService(fault)
		f.plane.Coordinator.AppServers = store
		f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: native, AgentRuntime: f.plane.Runs.AgentRuntime}, ContinuityDirectory: f.dataRoot, AppServers: store, AppServerSpec: spec, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
			return codex.DynamicToolCallResponse{Success: false}, nil
		}}
		cfg = controlplane.Config{DataRoot: f.dataRoot, Codex: codex.Config{CodexPath: native}, AppServerContinuity: true, AppServerSpec: spec}
		// This test survives a real Core reopen. Freeze measured native
		// qualification before admission instead of inheriting the subprocess
		// protocol fixture's synthetic descriptor, which cold Open rejects.
		f.plane.RuntimeQualification.BaseCodexPath = native
		configs, e := f.plane.CodexConfigs.List(f.ctx)
		if e != nil {
			t.Fatal(e)
		}
		for _, backend := range configs {
			if !backend.Enabled {
				continue
			}
			if _, e = f.plane.RuntimeQualification.QualifyBackend(f.ctx, backend.ID, "IMPLEMENTATION", "native-verification-recovery-"+backend.ID); e != nil {
				t.Fatal(e)
			}
		}
		f.plane.Coordinator.QualifiedRuntime = f.plane.RuntimeQualification
		f.facade, e = controlapi.NewFromControlPlane(f.plane)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { stopRecoveryFixtureServers(f) })
	})
}

func assertRecoveredVerificationDelivery(t *testing.T, f *upperLifecycleFixture, cfg controlplane.Config, source run.Run) {
	t.Helper()
	var originalRequest string
	if err := f.plane.DB.SQL().QueryRow(`SELECT request_json FROM codex_server_requests WHERE run_id=?`, source.ID).Scan(&originalRequest); err != nil {
		t.Fatal(err)
	}
	var question agentruntime.Request
	if err := json.Unmarshal([]byte(originalRequest), &question); err != nil || agentruntime.ResponseDisposition(question) != "UNKNOWN" {
		t.Fatalf("source answer not UNKNOWN: %+v %v", question, err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "recovery-accept", Purpose: c.Execution, Action: "ACCEPT", Execution: &c.ExecutionBody{Action: "ACCEPT", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: source.ExecutionProposalID, DecisionID: wp.TaskAcceptanceDecisionID(source.ExecutionProposalID)}, Variants: []c.Variant{{ID: "accept", Summary: "Accept the independently recovered verification result"}}})
	if err != nil {
		t.Fatalf("prepare recovered acceptance: %v cause=%v", err, errors.Unwrap(err))
	}
	commit := c.CommitInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "recovery-accept-commit", ContentHash: intent.ContentHash, VariantID: "accept"}
	result, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err != nil || result.State != "COMPLETED" {
		t.Fatalf("recovered acceptance: %+v %v cause=%v", result, err, errors.Unwrap(err))
	}
	assertDelivery := func() string {
		t.Helper()
		current, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil || current.Status != task.StatusSucceeded {
			t.Fatalf("recovered delivery status=%s phase=%s err=%v", current.Status, current.Phase, err)
		}
		var completed bool
		if err = f.plane.DB.SQL().QueryRow(`SELECT completed_at IS NOT NULL FROM task_commit_receipts WHERE task_id=?`, f.task).Scan(&completed); err != nil || !completed {
			t.Fatalf("delivery receipt=%v err=%v", completed, err)
		}
		var members, committed int
		var commits string
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*),sum(CASE WHEN state='COMPLETED' AND resulting_commit<>'' THEN 1 ELSE 0 END),group_concat(repository_id||':'||resulting_commit,'|') FROM (SELECT * FROM task_commit_members WHERE task_id=? ORDER BY repository_id)`, f.task).Scan(&members, &committed, &commits); err != nil || members != 2 || committed != 2 {
			t.Fatalf("delivery members=%d committed=%d err=%v", members, committed, err)
		}
		var request string
		if err = f.plane.DB.SQL().QueryRow(`SELECT request_json FROM codex_server_requests WHERE run_id=?`, source.ID).Scan(&request); err != nil || request != originalRequest {
			t.Fatalf("engineering acceptance changed old response authority: %v", err)
		}
		return commits
	}
	commits := assertDelivery()
	reopenRecoveryFixture(t, f, cfg)
	replayed, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err != nil || replayed.State != "COMPLETED" {
		t.Fatalf("recovered acceptance replay: %+v %v", replayed, err)
	}
	if assertDelivery() != commits {
		t.Fatal("receipt replay changed delivered commits")
	}
}
