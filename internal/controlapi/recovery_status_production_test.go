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
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

// The provider emits a real native command call for the source edits. It never
// edits files from the test process or inserts completed results into SQLite.
// The new diagnostic turn uses the real pinned client and original native S1.
func TestRecoveryStatusOfflineProductionReconciliation(t *testing.T) {
	for _, mode := range []string{"complete-old-unknown", "reported-missing-change", "active-pending-blocked", "probe-write-denied", "partial-continuation", "probe-explicit-question", "revision-required", "review-source"} {
		t.Run(mode, func(t *testing.T) { runRecoveryStatusOffline(t, mode) })
	}
}
func runRecoveryStatusOffline(t *testing.T, mode string, hooks ...func(*upperLifecycleFixture, controlplane.Config, controlapi.RecoveryStatusCommand, *atomic.Int64) bool) {
	runRecoveryStatusOfflineProvider(t, mode, nil, hooks...)
}
func runRecoveryStatusOfflineProvider(t *testing.T, mode string, onProvider func(int64), hooks ...func(*upperLifecycleFixture, controlplane.Config, controlapi.RecoveryStatusCommand, *atomic.Int64) bool) {
	runRecoveryStatusOfflineNativeSetup(t, mode, onProvider, nil, hooks...)
}
func runRecoveryStatusOfflineNativeSetup(t *testing.T, mode string, onProvider func(int64), configure func(*upperLifecycleFixture), hooks ...func(*upperLifecycleFixture, controlplane.Config, controlapi.RecoveryStatusCommand, *atomic.Int64) bool) {
	partialMode := mode == "partial-continuation" || mode == "partial-then-verification"
	reviewTargetConflict := strings.HasPrefix(mode, "review-target-")
	reviewMode := mode == "review-source" || mode == "review-then-execution" || mode == "review-evidence-range-valid" || mode == "review-evidence-range-invalid" || reviewTargetConflict
	native := os.Getenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY")
	if native == "" {
		t.Skip("offline installed App Server binary not selected")
	}
	var calls atomic.Int64
	var command, reportBytes, mutationCommand, continuationCommand, successorChecks, successorReport atomic.Value
	successorChecks.Store([]map[string]any{})
	successorReport.Store("")
	command.Store("")
	mutationCommand.Store("")
	continuationCommand.Store("")
	reportBytes.Store("")
	var recoveryProviderInput atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		defer r.Body.Close()
		var input map[string]any
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "fixture malformed request", 400)
			return
		}
		n := calls.Add(1)
		if onProvider != nil {
			onProvider(n)
		}
		var item map[string]any
		switch n {
		case 1:
			item = map[string]any{"type": "function_call", "call_id": "recovery-source-question", "name": "request_user_input", "arguments": `{"questions":[{"id":"adaptation","header":"Fixture","question":"Proceed with approved app/sdk edits?","options":[{"label":"yes","description":"Continue"},{"label":"no","description":"Alternative"}]}]}`}
		case 2:
			args, _ := json.Marshal(map[string]any{"cmd": command.Load().(string), "max_output_tokens": 1000})
			item = map[string]any{"type": "function_call", "call_id": "recovery-approved-source-edit", "name": "exec_command", "arguments": string(args)}
		case 3:
			item = recoveryMessage(`{"resultDisposition":"COMPLETED","summary":"Approved app and sdk edits completed"}`)
		case 4:
			raw, _ := json.Marshal(input)
			recoveryProviderInput.Store(string(raw))
			if mode == "probe-explicit-question" {
				item = map[string]any{"type": "function_call", "call_id": "recovery-probe-question", "name": "request_user_input", "arguments": `{"questions":[{"id":"adaptation","header":"Recovery","question":"Confirm the observed engineering status?","options":[{"label":"yes","description":"Confirm"},{"label":"no","description":"Disagree"}]}]}`}
			} else if mode == "probe-write-denied" || mode == "probe-mutation" {
				args, _ := json.Marshal(map[string]any{"cmd": mutationCommand.Load().(string), "max_output_tokens": 1000})
				item = map[string]any{"type": "function_call", "call_id": "recovery-readonly-violation", "name": "exec_command", "arguments": string(args)}
			} else {
				item = recoveryMessage(reportBytes.Load().(string))
			}
		case 5:
			if mode == "revision-required" {
				item = map[string]any{"type": "function_call", "call_id": "revised-execution-question", "name": "request_user_input", "arguments": `{"questions":[{"id":"adaptation","header":"Successor","question":"Proceed with newly approved scope?","options":[{"label":"yes","description":"Proceed"},{"label":"no","description":"Pause"}]}]}`}
			} else if partialMode {
				args, _ := json.Marshal(map[string]any{"cmd": continuationCommand.Load().(string), "max_output_tokens": 1000})
				item = map[string]any{"type": "function_call", "call_id": "continuation-sdk-edit", "name": "exec_command", "arguments": string(args)}
			} else {
				raw, _ := json.Marshal(input)
				recoveryProviderInput.Store(string(raw))
				item = recoveryMessage(reportBytes.Load().(string))
			}
		default:
			raw, _ := json.Marshal(input)
			if mode != "partial-then-verification" || n <= 6 {
				recoveryProviderInput.Store(string(raw))
			}
			item = recoveryMessage(reportBytes.Load().(string))
			if partialMode {
				item = recoveryMessage(`{"resultDisposition":"COMPLETED","summary":"Same revision continuation finished remaining sdk edit"}`)
			}
		}
		if reviewMode && n >= 2 {
			if n == 2 {
				item = recoveryMessage(os.Getenv("DEVBRIDGE_CODING_V1_REVIEW_RESULT"))
			} else {
				raw, _ := json.Marshal(input)
				recoveryProviderInput.Store(string(raw))
				item = recoveryMessage(reportBytes.Load().(string))
			}
		}
		if mode == "complete-then-verification" && n >= 5 {
			if n <= 7 {
				args, _ := json.Marshal(successorChecks.Load().([]map[string]any)[n-5])
				item = map[string]any{"type": "function_call", "call_id": fmt.Sprint("recovered-verification-check-", n), "name": "exec_command", "arguments": string(args)}
			} else {
				item = recoveryMessage(successorReport.Load().(string))
			}
		}
		if mode == "partial-then-verification" && n >= 7 {
			if n <= 9 {
				args, _ := json.Marshal(successorChecks.Load().([]map[string]any)[n-7])
				item = map[string]any{"type": "function_call", "call_id": fmt.Sprint("partial-verification-check-", n), "name": "exec_command", "arguments": string(args)}
			} else {
				item = recoveryMessage(successorReport.Load().(string))
			}
		}

		if mode == "review-then-execution" && n >= 4 {
			switch n {
			case 4:
				args, _ := json.Marshal(map[string]any{"cmd": command.Load().(string), "max_output_tokens": 1000})
				item = map[string]any{"type": "function_call", "call_id": "approved-review-successor-edit", "name": "exec_command", "arguments": string(args)}
			case 5:
				item = recoveryMessage(`{"resultDisposition":"COMPLETED","summary":"Approved reviewed contracts updated"}`)
			case 6, 7:
				args, _ := json.Marshal(successorChecks.Load().([]map[string]any)[n-6])
				item = map[string]any{"type": "function_call", "call_id": fmt.Sprint("review-successor-check-", n), "name": "exec_command", "arguments": string(args)}
			default:
				item = recoveryMessage(successorReport.Load().(string))
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("recovery-%d", n)}}, {"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("recovery-%d", n), "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
			b, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], b)
		}
	}))
	defer provider.Close()
	home := t.TempDir()
	config := fmt.Sprintf("openai_base_url=%q\nmodel=\"mock-model\"\nmodel_provider=\"openai\"\n[features]\ndefault_mode_request_user_input=true\nplugins=false\nremote_plugin=false\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n", provider.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"OPENAI_API_KEY":"offline-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	serverSpec := func(codexconfig.Config) appserver.Spec {
		return appserver.Spec{ExecutablePath: native, CodexHome: home, Env: []string{"OPENAI_BASE_URL=" + provider.URL + "/v1", "OPENAI_API_KEY=offline-fixture"}}
	}
	var reopen controlplane.Config
	var fault *loseResolvedReceiptStore
	fixture := func(callback func(*upperLifecycleFixture, string) bool, setup func(*upperLifecycleFixture)) {
		if reviewMode {
			runCodingReviewFixture(t, func(f *upperLifecycleFixture, taskID, reviewID, runID string) bool {
				f.task = taskID
				return callback(f, reviewID)
			}, setup)
		} else {
			runCodingDeliveryFixture(t, callback, setup)
		}
	}
	fixture(func(f *upperLifecycleFixture, executionID string) bool {
		q, source := waitContinuityQuestion(t, f, "")
		w, e := f.plane.Runs.RuntimeWorkset(f.ctx, source.ID)
		if e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		commands := []string{}
		for _, m := range w.Members {
			alias := m.RepositoryAlias
			if mode == "complete-then-verification" || mode == "partial-then-verification" {
				for name, id := range f.repos {
					if id == m.RepositoryID {
						alias = name
					}
				}
			}
			commands = append(commands, fmt.Sprintf("printf 'coded %s\\n' > %s", alias, recoveryShellQuote(filepath.Join(m.CanonicalRoot, "same.txt"))))
		}
		command.Store(strings.Join(commands, " && "))
		if partialMode {
			command.Store(commands[0])
			continuationCommand.Store(commands[1])
		}
		mutationCommand.Store("printf 'probe violated read-only intent\\n' > " + recoveryShellQuote(filepath.Join(w.Members[0].CanonicalRoot, "same.txt")))
		binding, e := fault.AppServerBinding(f.ctx, source.ID)
		if e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		if mode == "active-pending-blocked" {
			initial := recoverystatus.Source{ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: q.TaskDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: q.AgentConversationID, ThreadID: source.ThreadID, AppServerInstanceID: binding.ID}
			pendingSpec := recoverystatus.Spec{Source: initial, RequestID: "active-pending-probe", RecoveryReason: "fixture-negative-admission", Instruction: "Caller-authored read-only status check", OutputSchemaVersion: recoverystatus.ReportVersion, Generation: 1}
			p, prepareErr := f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, pendingSpec)
			if prepareErr == nil {
				_, startErr := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, controlapi.RecoveryStatusCommand{Source: initial, ID: p.Record.ID, RequestID: p.Record.Spec.RequestID, PayloadHash: p.Record.Spec.PayloadHash, ExpectedRecoveryEpoch: p.Record.RuntimeEpoch})
				if startErr == nil {
					t.Fatal("active pending Thread accepted recovery")
				}
			}
			if calls.Load() != 1 {
				t.Fatal("pending admission dispatched model")
			}
			return false
		}
		decision, e := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, q, "RESPOND", "yes"))
		if e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		sourceCalls := int64(3)
		if reviewMode {
			sourceCalls = 2
		}
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if calls.Load() >= sourceCalls && fault.rejected.Load() > 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if calls.Load() != sourceCalls || fault.rejected.Load() == 0 {
			t.Fatalf("source call/fault boundary calls=%d faults=%d", calls.Load(), fault.rejected.Load())
		}
		for index, m := range w.Members {
			if reviewMode {
				continue
			}
			if partialMode && index == 1 {
				continue
			}
			b, e := os.ReadFile(filepath.Join(m.CanonicalRoot, "same.txt"))
			alias := m.RepositoryAlias
			if mode == "complete-then-verification" || mode == "partial-then-verification" {
				for name, id := range f.repos {
					if id == m.RepositoryID {
						alias = name
					}
				}
			}
			if e != nil || string(b) != "coded "+alias+"\n" {
				t.Fatalf("native source tool did not perform approved edit %s: %q %v", m.RepositoryAlias, b, e)
			}
		}
		_, protocolPlatform := f.plane.Coordinator.QualifiedRuntime.(*protocolFixturePlatform)
		if e = f.plane.Close(); e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		f.plane = nil
		if e = appserver.Verify(f.ctx, binding); e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		f.plane, e = controlplane.Open(f.ctx, reopen)
		if e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		f.facade, e = controlapi.NewFromControlPlane(f.plane)
		if e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		if protocolPlatform {
			// Cold bootstrap correctly refuses the test-only platform as native
			// qualification. Restore the explicit protocol dependency and retry
			// the original owner through the production recovery entry point.
			// This path supplies no evidence of native bootstrap compatibility.
			installProtocolFixturePlatform(t, f)
			if source.ExecutionProposalID != "" {
				_, e = f.plane.ExecutionProposals.RecoverAppServerExecution(f.ctx, source.ProjectID, source.ExecutionProposalID, source.ID, binding)
			} else {
				_, e = f.plane.Supervisor.RecoverAppServerRun(f.ctx, source.ID, binding)
			}
			if e != nil {
				t.Fatalf("restore protocol fixture owner: %v", e)
			}
		}
		old := waitUnknownRequest(t, f, q, true)
		oldAttempts, _ := json.Marshal(old.DispatchAttempts)
		oldEvidence, _ := json.Marshal(old.RecoveryEvidence)
		oldDecision, _ := json.Marshal(old.UserDecision)
		if old.UserDecision == nil || old.UserDecision.ID != decision.Decision.ID {
			t.Fatal("decision changed on restart")
		}
		// Wait for the original authoritative terminal observation to reach Core B.
		for time.Now().Before(deadline) {
			source, e = f.plane.Runs.Get(f.ctx, source.ID)
			if e != nil {
				t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
			}
			if source.AgentRuntime != nil && source.AgentRuntime.TurnStatus == "completed" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		src := recoverystatus.Source{ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: old.TaskDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: old.AgentConversationID, ThreadID: source.ThreadID, AppServerInstanceID: binding.ID}
		instruction := "Caller-authored recovery status: report the original approved implementation state only.\nUnicode 保留 exact bytes."
		spec := recoverystatus.Spec{Source: src, RequestID: "recovery-status-" + mode, RecoveryReason: "CORE_REOPEN_MISSING_FINALIZATION", Instruction: instruction, OutputSchemaVersion: recoverystatus.ReportVersion, Generation: 1}
		prepared, e := f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, spec)
		if e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		if calls.Load() != sourceCalls || prepared.Record.RecoveryRunID != "" {
			t.Fatal("prepare dispatched runtime")
		}
		replay, e := f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, spec)
		if e != nil || replay.Record.ID != prepared.Record.ID {
			t.Fatalf("prepare replay: %+v %v", replay, e)
		}
		goals, e := f.plane.Tasks.ListGoals(f.ctx, source.TaskID)
		if e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		goalIDs := []string{}
		for _, g := range goals {
			goalIDs = append(goalIDs, g.ID)
		}
		if len(goalIDs) == 0 && !reviewMode {
			definition, e := f.plane.Tasks.Get(f.ctx, source.TaskID)
			if e != nil {
				t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
			}
			goalIDs = append(goalIDs, definition.WorkSpec.Goal)
		}
		report := recoverystatus.Report{Version: recoverystatus.ReportVersion, SourceOperationKind: src.OperationKind, SourceOperationID: src.OperationID, SourceRunID: src.RunID, SourceTurnID: src.TurnID, OverallStatus: "COMPLETED", Summary: "Original app and sdk work independently observed", CompletedGoals: []recoverystatus.GoalObservation{}, RemainingGoals: []recoverystatus.GoalObservation{}, RepositoryObservations: []recoverystatus.RepositoryObservation{}, VerificationObservations: []recoverystatus.VerificationObservation{}, UnresolvedIssues: []string{}, RecommendedNextAction: "FINALIZE"}
		for _, id := range goalIDs {
			report.CompletedGoals = append(report.CompletedGoals, recoverystatus.GoalObservation{GoalID: id, Status: "COMPLETED", EvidenceSummary: "Approved source edits exist in both managed members"})
		}
		for _, m := range w.Members {
			paths := []string{"same.txt"}
			if mode == "reported-missing-change" && len(report.RepositoryObservations) == 1 {
				paths = []string{"missing.txt"}
			}
			report.RepositoryObservations = append(report.RepositoryObservations, recoverystatus.RepositoryObservation{RuntimeAlias: m.RepositoryAlias, ChangedPaths: paths, Completion: "COMPLETED", VerificationState: "NOT_RUN"})
		}
		if partialMode {
			report.OverallStatus = "PARTIAL"
			report.RecommendedNextAction = "CONTINUE"
			report.RemainingGoals = report.CompletedGoals
			report.CompletedGoals = []recoverystatus.GoalObservation{}
			for i := range report.RemainingGoals {
				report.RemainingGoals[i].Status = "PARTIAL"
				report.RemainingGoals[i].EvidenceSummary = "App implemented; sdk remains under same approved goal"
			}
			report.RepositoryObservations[1].ChangedPaths = []string{}
			report.RepositoryObservations[1].Completion = "NOT_STARTED"
		}
		if mode == "revision-required" {
			report.ScopeExpansionRequired = true
			report.RecommendedNextAction = "REVISE"
			report.Summary = "The app requires a new extra.txt path outside original approved scope"
		}
		if reviewMode {
			originalReview, err := f.plane.Reviews.Get(f.ctx, source.ReviewID)
			if err != nil {
				t.Fatal(err)
			}
			originalReport, err := review.ParseModelResult(os.Getenv("DEVBRIDGE_CODING_V1_REVIEW_RESULT"), originalReview.RepositoryTargets, originalReview.PrimaryMemberID)
			if err != nil {
				t.Fatal(err)
			}
			report.ReviewReport = &review.ReviewReport{SchemaVersion: 2, Summary: originalReport.Summary, Disposition: originalReport.ReviewDisposition, RepositoryTargets: originalReport.RepositoryTargets, PrimaryMemberID: originalReport.PrimaryMemberID, Coverage: []string{"Both frozen repository members"}, Findings: []review.Finding{}, Limitations: []string{"Read-only review; implementation requires explicit approval"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}, WorkSpec: &originalReport.WorkSpec, VerificationSpec: &originalReport.VerificationSpec}
			switch mode {
			case "review-target-origin-ref":
				report.ReviewReport.RepositoryTargets[0].OriginRef = "refs/heads/foreign"
			case "review-target-observed-commit":
				report.ReviewReport.RepositoryTargets[0].ObservedCommit = strings.Repeat("f", 40)
			case "review-target-development-cycle":
				report.ReviewReport.RepositoryTargets[0].DevelopmentCycleID = "foreign-cycle"
			}
			if mode == "review-evidence-range-valid" || mode == "review-evidence-range-invalid" {
				member := w.Members[0]
				data, err := os.ReadFile(filepath.Join(member.CanonicalRoot, "same.txt"))
				if err != nil {
					t.Fatal(err)
				}
				end := 1
				if mode == "review-evidence-range-invalid" {
					end = 9999
				}
				report.ReviewReport.EvidenceRefs = []review.EvidenceRef{{EvidenceID: "recovery-source-file", RepositoryID: member.RepositoryID, MemberID: member.MemberID, Path: "same.txt", Digest: recoverystatus.Hash(data), StartLine: 1, EndLine: end}}
			}
			for i := range report.RepositoryObservations {
				report.RepositoryObservations[i].ChangedPaths = []string{}
			}
		}
		raw, _ := json.Marshal(report)
		reportBytes.Store(string(raw))
		f.recoveryReport = append(json.RawMessage(nil), raw...)
		cmd := controlapi.RecoveryStatusCommand{Source: src, ID: prepared.Record.ID, RequestID: prepared.Record.Spec.RequestID, PayloadHash: prepared.Record.Spec.PayloadHash, ExpectedRecoveryEpoch: prepared.Record.RuntimeEpoch}
		for _, hook := range hooks {
			if !hook(f, reopen, cmd, &calls) {
				return false
			}
		}
		beforeRecoveryTask, e := f.plane.Tasks.Get(f.ctx, source.TaskID)
		if e != nil {
			t.Fatal(e)
		}
		if mode == "review-evidence-range-invalid" || mode == "review-evidence-range-valid" {
			owner, err := f.plane.Reviews.Get(f.ctx, source.ReviewID)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("Review source binding: taskRevision=%q frozenRevision=%q reviewRun=%q sourceRun=%q reviewTask=%q sourceTask=%q", beforeRecoveryTask.CurrentDefinitionRevisionID, src.TaskDefinitionRevisionID, owner.RunID, source.ID, owner.TaskID, source.TaskID)
		}
		started, e := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd)
		if e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		if started.Record.RecoveryRunID == "" || started.Record.RecoveryRunID == source.ID {
			t.Fatal("missing distinct recovery run")
		}
		if mode == "probe-explicit-question" {
			var probeQuestion agentruntime.Request
			until := time.Now().Add(30 * time.Second)
			for time.Now().Before(until) {
				state, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, started.Record.RecoveryRunID)
				if err != nil {
					t.Fatalf("probe runtime read %v cause=%v", err, errors.Unwrap(err))
				}
				for _, q := range state.Requests {
					if q.Kind == "CODEX_REQUEST" && q.State == "WAITING_FOR_USER" {
						probeQuestion = q
					}
				}
				if probeQuestion.ID != "" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if probeQuestion.ID == "" {
				t.Fatal("probe real CODEX_REQUEST not routed")
			}
			if probeQuestion.RunID != started.Record.RecoveryRunID || probeQuestion.TurnID == src.TurnID || probeQuestion.ThreadID != src.ThreadID || probeQuestion.UserDecision != nil {
				t.Fatalf("probe question identity/answer changed %+v", probeQuestion)
			}
			for i := 0; i < 3; i++ {
				state, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, probeQuestion.RunID)
				if err != nil || len(state.Requests) != 1 || state.Requests[0].UserDecision != nil || calls.Load() != 4 {
					t.Fatalf("probe read implicitly answered %+v %v", state, err)
				}
			}
			_, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, probeQuestion, "RESPOND", "yes"))
			if err != nil {
				t.Fatalf("explicit probe answer failed %v cause=%v", err, errors.Unwrap(err))
			}
		}
		var result controlapi.RecoveryStatusResult
		deadline = time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			result, e = f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, prepared.Record.ID)
			if e != nil {
				t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
			}
			if result.Record.AnalysisRequired {
				submitRecoveryFixtureAnalysis(t, f, result.Record)
				continue
			}
			if result.Record.State == "ASSESSED" || result.Record.State == "FAILED" || (result.Record.State == "UNKNOWN" && result.Record.FailureReason != "") {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if result.Record.State != "ASSESSED" {
			t.Fatalf("recovery not assessed state=%s failure=%s run=%s turn=%s providerCalls=%d", result.Record.State, result.Record.FailureReason, result.Record.RecoveryRunID, result.Record.RecoveryTurnID, calls.Load())
		}
		expectedCalls := sourceCalls + 1
		if mode == "probe-write-denied" || mode == "probe-explicit-question" || mode == "probe-mutation" {
			expectedCalls = 5
		}
		if calls.Load() != expectedCalls || result.Record.RecoveryTurnID == src.TurnID || result.Record.Spec.ThreadID != src.ThreadID || result.Record.Spec.Instruction != instruction || result.Record.InstructionHash != recoverystatus.Hash([]byte(instruction)) {
			t.Fatalf("identity/prompt/calls failed %+v calls=%d", result.Record, calls.Load())
		}
		var providerRequest struct {
			Input []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if captured, ok := recoveryProviderInput.Load().(string); !ok || json.Unmarshal([]byte(captured), &providerRequest) != nil {
			t.Fatal("missing native recovery provider request")
		}
		latestUser := ""
		for _, item := range providerRequest.Input {
			if item.Role == "user" {
				latestUser = ""
				for _, content := range item.Content {
					if content.Type == "input_text" {
						latestUser += content.Text
					}
				}
			}
		}
		if latestUser != instruction {
			t.Fatalf("native new user input changed: %q", latestUser)
		}
		var assessment recoverystatus.Assessment
		if e = json.Unmarshal(result.Record.Assessment, &assessment); e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		if assessment.SourceResponseDisposition != "UNKNOWN" {
			t.Fatal("old unknown dropped")
		}
		if mode == "review-evidence-range-invalid" || reviewTargetConflict {
			if assessment.Consistency != "INSUFFICIENT" || assessment.EngineeringDisposition != "RECOVERY_INSUFFICIENT" || result.ResolveCommand != nil {
				t.Fatalf("invalid Review evidence or target identity authorized resolution (%s): %+v", mode, assessment)
			}
			current, err := f.plane.Tasks.Get(f.ctx, source.TaskID)
			if err != nil || current.Phase != beforeRecoveryTask.Phase || current.CurrentReviewID != beforeRecoveryTask.CurrentReviewID {
				t.Fatalf("invalid Review evidence advanced owner: %+v %v", current, err)
			}
			return false
		}
		if mode == "probe-mutation" {
			if assessment.Consistency != "CONFLICT" || assessment.EngineeringDisposition != "RECOVERY_FAILED" {
				t.Fatalf("dishonest backend mutation accepted %+v", assessment)
			}
			b, err := os.ReadFile(filepath.Join(w.Members[0].CanonicalRoot, "same.txt"))
			if err != nil || !strings.Contains(string(b), "probe violated") {
				t.Fatalf("mutation not preserved %q %v", b, err)
			}
			current, err := f.plane.Tasks.Get(f.ctx, source.TaskID)
			// A blocking assessment preserves the source lifecycle. Recovery facts
			// and projection retain the blocker without assigning a Task phase.
			if err != nil || current.Status != beforeRecoveryTask.Status || current.Phase != beforeRecoveryTask.Phase {
				t.Fatalf("mutation advanced original source %+v %v", current, err)
			}
			return false
		}
		if mode == "probe-write-denied" {
			for _, m := range w.Members {
				b, err := os.ReadFile(filepath.Join(m.CanonicalRoot, "same.txt"))
				alias := m.RepositoryAlias
				if mode == "partial-then-verification" {
					for name, id := range f.repos {
						if id == m.RepositoryID {
							alias = name
						}
					}
				}
				if err != nil || string(b) != "coded "+alias+"\n" {
					t.Fatalf("unexpected native probe source mutation %q %v", b, err)
				}
			}
		}
		if mode == "reported-missing-change" {
			if assessment.Consistency != "CONFLICT" || assessment.EngineeringDisposition != "RECOVERY_INSUFFICIENT" {
				t.Fatalf("false completed accepted %+v", assessment)
			}
			current, e := f.plane.Tasks.Get(f.ctx, source.TaskID)
			// A false completion remains diagnostic evidence, not Task authority.
			if e != nil || current.Status != beforeRecoveryTask.Status || current.Phase != beforeRecoveryTask.Phase {
				t.Fatalf("false completed advanced %+v %v", current, e)
			}
			return false
		}
		wantDisposition := "RECOVERED_COMPLETE"
		if mode == "revision-required" {
			wantDisposition = "REVISION_REQUIRED"
		}
		if partialMode {
			wantDisposition = "RECOVERED_PARTIAL"
		}
		if assessment.EngineeringDisposition != wantDisposition {
			t.Fatalf("complete insufficient %+v", assessment)
		}
		resolve := controlapi.RecoveryStatusResolveCommand{RecoveryStatusCommand: cmd, AssessmentHash: result.Record.AssessmentHash, ReportHash: result.Record.ReportHash}
		resolve.RequestID = "resolve-" + mode
		resolve.PayloadHash = (recoverystatus.ResolveInput{ID: resolve.ID, RequestID: resolve.RequestID, ActorID: "session:" + f.session, RuntimeEpoch: resolve.ExpectedRecoveryEpoch, AssessmentHash: resolve.AssessmentHash, ReportHash: resolve.ReportHash}).ComputePayloadHash()
		resolved, e := f.facade.ResolveRecoveryStatusForSession(f.ctx, f.session, resolve)
		if e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		if resolved.Record.State != "RESOLVED" {
			t.Fatalf("resolution %+v", resolved)
		}
		if mode == "revision-required" {
			current, e := f.plane.Tasks.Get(f.ctx, source.TaskID)
			if e != nil || current.CurrentDefinitionRevisionID != src.TaskDefinitionRevisionID || calls.Load() != 4 {
				t.Fatalf("revision report self-authorized scope %+v %v", current, e)
			}
			work := current.WorkSpec
			work.Goal = "Add explicitly approved app extra.txt path"
			work.CodexInstruction = "Caller-authored revised work: add app extra.txt within newly approved PATHS."
			targets := workset.CloneTargets(current.RepositoryTargets)
			targets[0].Write.Paths = append(targets[0].Write.Paths, "extra.txt")
			verification := current.VerificationSpec
			proposal, e := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "recovery-revise-prepare", Purpose: c.Execution, Action: "REQUEST_CHANGES", Execution: &c.ExecutionBody{Action: "REQUEST_CHANGES", TaskID: current.ID, FeatureAreaID: current.FeatureAreaID, ReviewID: current.CurrentReviewID, ExecutionID: source.OperationID, TaskDefinitionRevisionID: src.TaskDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(source.OperationID)}, Variants: []c.Variant{{ID: "revised", Summary: "Explicitly approve additional app extra.txt scope", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &work, VerificationSpec: &verification}}})
			if e != nil {
				t.Fatalf("prepare revision %v cause=%v", e, errors.Unwrap(e))
			}
			oldExecution, e := f.facade.GetExecutionProposal(f.ctx, f.session, source.OperationID)
			if e != nil {
				t.Fatal(e)
			}
			runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
			backend := oldExecution.BackendSelection
			confirmed, e := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: proposal.IntentID, Version: proposal.Version, Purpose: proposal.Purpose, Action: proposal.Action, RequestID: "recovery-revise-user-confirmed", ContentHash: proposal.ContentHash, VariantID: "revised", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}})
			if e != nil {
				t.Fatalf("confirmed revision %v cause=%v", e, errors.Unwrap(e))
			}
			current, e = f.plane.Tasks.Get(f.ctx, source.TaskID)
			if e != nil || current.CurrentDefinitionRevisionID == src.TaskDefinitionRevisionID || current.CurrentExecutionID == source.OperationID || len(confirmed.Steps) != 2 {
				t.Fatalf("revision not canonically replaced %+v %+v %v", current, confirmed, e)
			}
			if len(current.RepositoryTargets[0].Write.Paths) != 2 {
				t.Fatalf("new exact PATHS missing %+v", current.RepositoryTargets)
			}
			return false
		}
		if partialMode {
			continuationInstruction := "Caller continuation: finish only the remaining approved sdk change.\n保留 app 已有修改."
			next, e := f.facade.ResolveTaskAcceptance(f.ctx, wp.TaskAcceptanceInput{AISessionID: f.session, TaskID: source.TaskID, FeatureAreaID: source.FeatureAreaID, TaskDefinitionRevisionID: src.TaskDefinitionRevisionID, ExecutionID: source.OperationID, DecisionID: wp.TaskAcceptanceDecisionID(source.OperationID), Option: wp.AcceptanceRetryImplementation, RequestID: "recover-partial-continuation", RecoveryStatusID: resolved.Record.ID, RecoveryContinuationInstruction: continuationInstruction})
			if e != nil {
				t.Fatalf("partial continuation eligibility %v cause=%v", e, errors.Unwrap(e))
			}
			if next.SuccessorExecutionID == "" || next.Task.CurrentDefinitionRevisionID != src.TaskDefinitionRevisionID {
				t.Fatalf("same revision successor missing %+v", next)
			}
			if mode == "partial-then-verification" {
				checks, verification := recoveredVerificationInputs(t, f, source.ID)
				successorChecks.Store(checks)
				successorReport.Store(verification)
			}
			nextRun, e := f.facade.StartApprovedExecution(f.ctx, f.session, next.SuccessorExecutionID)
			if e != nil {
				t.Fatalf("partial continuation start %v cause=%v", e, errors.Unwrap(e))
			}
			deadline = time.Now().Add(45 * time.Second)
			for time.Now().Before(deadline) {
				value, err := f.plane.Runs.Get(f.ctx, nextRun.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				nextRun.Run = value
				if value.Failure != nil {
					t.Fatalf("continuation Run failed: %+v", value.Failure)
				}
				if value.TurnID != "" && calls.Load() >= 6 && (mode != "partial-then-verification" || string(value.Status) == "SUCCEEDED") {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if nextRun.Run.ID == source.ID || nextRun.Run.ID == result.Record.RecoveryRunID || nextRun.Run.TurnID == src.TurnID || nextRun.Run.ThreadID != src.ThreadID || nextRun.Run.FinalCodexPrompt != continuationInstruction {
				t.Fatalf("continuation binding/prompt failed run=%s status=%s thread=%s turn=%s prompt=%q failure=%+v", nextRun.Run.ID, nextRun.Run.Status, nextRun.Run.ThreadID, nextRun.Run.TurnID, nextRun.Run.FinalCodexPrompt, nextRun.Run.Failure)
			}
			captured, _ := recoveryProviderInput.Load().(string)
			if recoveryLatestUserText(captured) != continuationInstruction {
				t.Fatalf("native continuation instruction changed: %q", recoveryLatestUserText(captured))
			}
			nextWorkset, e := f.plane.Runs.RuntimeWorkset(f.ctx, nextRun.Run.ID)
			nextAuthority := nextWorkset
			nextAuthority.ExecutionID = w.ExecutionID
			authorityHash, hashErr := nextAuthority.ComputeContentHash()
			if e != nil || hashErr != nil || authorityHash != w.ContentHash {
				t.Fatalf("continuation workset changed %+v %v", nextWorkset, e)
			}
			for _, m := range w.Members {
				b, err := os.ReadFile(filepath.Join(m.CanonicalRoot, "same.txt"))
				alias := m.RepositoryAlias
				if mode == "partial-then-verification" {
					for name, id := range f.repos {
						if id == m.RepositoryID {
							alias = name
						}
					}
				}
				if err != nil || string(b) != "coded "+alias+"\n" {
					t.Fatalf("continuation did not preserve/complete source %q %v", b, err)
				}
			}
			if mode == "partial-then-verification" {
				verifyRecoveredAutomaticSuccessor(t, f, source.ThreadID, binding, &calls, nextRun.Run.ID, 10)
			}
			return false
		}
		current, e := f.plane.Tasks.Get(f.ctx, source.TaskID)
		expectedPhase := task.PhaseVerificationReady
		if reviewMode {
			expectedPhase = task.PhaseReviewApprovalPending
		}
		if e != nil || current.Phase != expectedPhase {
			t.Fatalf("canonical progression %+v %v", current, e)
		}
		state, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, source.ID)
		if e != nil {
			t.Fatalf("%v cause=%v", e, errors.Unwrap(e))
		}
		oldFound := false
		for _, request := range state.Requests {
			if request.ID == old.ID {
				oldFound = true
				attempts, _ := json.Marshal(request.DispatchAttempts)
				evidence, _ := json.Marshal(request.RecoveryEvidence)
				decisionBytes, _ := json.Marshal(request.UserDecision)
				if agentruntime.ResponseDisposition(request) != "UNKNOWN" || string(attempts) != string(oldAttempts) || string(evidence) != string(oldEvidence) || string(decisionBytes) != string(oldDecision) {
					t.Fatalf("old response history rewritten %+v", request)
				}
			}
		}
		if !oldFound {
			t.Fatal("old runtime request removed")
		}

		if !strings.Contains(resolved.Projection.DisplayMessage, "旧回答") {
			t.Fatalf("dual projection missing %+v", resolved.Projection)
		}
		second, e := f.facade.ResolveRecoveryStatusForSession(f.ctx, f.session, resolve)
		if e != nil || second.Record.ID != resolved.Record.ID || calls.Load() != expectedCalls {
			t.Fatalf("resolution replay failed %+v %v", second, e)
		}
		if mode == "complete-then-verification" {
			checks, report := recoveredVerificationInputs(t, f, source.ID)
			successorChecks.Store(checks)
			successorReport.Store(report)
			verifyRecoveredImplementationSuccessor(t, f, source.ID, source.ThreadID, binding, &calls)
		}
		if mode == "review-then-execution" {
			approveRecoveredReview(t, f, report.ReviewReport)
			checks, verification := recoveredVerificationInputs(t, f, source.ID)
			successorChecks.Store(checks)
			successorReport.Store(verification)
			verifyRecoveredAutomaticSuccessor(t, f, source.ThreadID, binding, &calls, "", 8)
		}
		t.Logf("native original edits + terminal UNKNOWN + new same-Thread Recovery Turn + canonical resolution verified; source=%s recovery=%s", source.ID, result.Record.RecoveryRunID)
		return false
	}, func(f *upperLifecycleFixture) {
		calls.Store(0)
		store := sqlite.NewAgentRuntimeStore(f.plane.DB)
		fault = &loseResolvedReceiptStore{AgentRuntimeStore: store}
		f.plane.Runs.AgentRuntime = agentruntime.NewService(fault)
		f.plane.Coordinator.AppServers = store
		f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: native, AgentRuntime: f.plane.Runs.AgentRuntime}, ContinuityDirectory: f.dataRoot, AppServers: store, AppServerSpec: serverSpec, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
			return codex.DynamicToolCallResponse{Success: false}, nil
		}}
		reopen = controlplane.Config{DataRoot: f.dataRoot, Codex: codex.Config{CodexPath: native}, AppServerContinuity: true, AppServerSpec: serverSpec}
		t.Cleanup(func() { stopRecoveryFixtureServers(f) })
		if configure != nil {
			configure(f)
		}
	})
}
func recoveryMessage(text string) map[string]any {
	return map[string]any{"id": "recovery-message", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}
}
func recoveryShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func recoveryLatestUserText(raw string) string {
	var input struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if json.Unmarshal([]byte(raw), &input) != nil {
		return ""
	}
	last := ""
	for _, item := range input.Input {
		if item.Role == "user" {
			last = ""
			for _, content := range item.Content {
				if content.Type == "input_text" {
					last += content.Text
				}
			}
		}
	}
	return last
}
