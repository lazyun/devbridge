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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

type inspectionFinalizationHook struct {
	probeexecution.CanonicalStore
	before func()
}

func (s inspectionFinalizationHook) FinalizeInspection(ctx context.Context, in probeexecution.InspectionFinalization) (probeexecution.CanonicalState, error) {
	s.before()
	return s.CanonicalStore.FinalizeInspection(ctx, in)
}

func confirmedRuntimeInspection(t *testing.T, f *projectInspectionAPIFixture, requestIDs ...string) contextprobe.Probe {
	t.Helper()
	config, err := f.plane.CodexConfigs.Get(f.ctx, "codex_config_default")
	if err != nil {
		t.Fatal(err)
	}
	requestID := "runtime-inspection"
	if len(requestIDs) > 0 {
		requestID = requestIDs[0]
	}
	plan, err := f.facade.PrepareProjectInspection(f.ctx, contextprobe.PrepareInspectionInput{AISessionID: f.session, RequestID: requestID, CodexInstruction: "Inspect app and sdk. Preserve this instruction byte for byte.\n", Backend: config.Selection("test").Input(), Model: "gpt-5.6-terra", ReasoningEffort: "high", PrimaryRepositoryID: f.repos["app"], Targets: []contextprobe.TargetSelection{{RepositoryID: f.repos["app"], Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}}, {RepositoryID: f.repos["sdk"], Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.facade.ConfirmProjectInspection(f.ctx, contextprobe.ConfirmInspectionInput{Plan: plan, PayloadSHA256: plan.PayloadSHA256})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectInspectionClaimAndConversationConcurrency(t *testing.T) {
	f := newProjectInspectionAPIFixture(t)
	p := confirmedRuntimeInspection(t, f)
	store := sqlite.NewInspectionRuntimeStore(f.plane.DB)
	spec, err := store.ConfirmedInspection(f.ctx, f.session, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan probeexecution.CanonicalState, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, e := store.ReserveInspection(f.ctx, spec)
			if e != nil {
				failures <- e
			} else {
				results <- state
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		t.Error(e)
	}
	first := ""
	for state := range results {
		if first == "" {
			first = state.Run.ID
		}
		if state.Run.ID != first {
			t.Fatal("concurrent start allocated replacement Run")
		}
	}
	counts := projectInspectionCounts(t, f)
	if counts["runs"] != 1 || counts["agent_conversations"] != 1 || counts["project_inspection_agent_claims"] != 1 {
		t.Fatalf("same owner not serialized: %v", counts)
	}
	contender := spec
	contender.PayloadHash = "changed-payload"
	if _, e := store.ReserveInspection(f.ctx, contender); e == nil {
		t.Fatal("different same-owner contender admitted")
	}
	other := confirmedRuntimeInspection(t, f, "independent-inspection")
	otherSpec, e := store.ConfirmedInspection(f.ctx, f.session, other.ID)
	if e != nil {
		t.Fatal(e)
	}
	second, e := store.ReserveInspection(f.ctx, otherSpec)
	if e != nil || second.Run.ID == first || second.Conversation.ID == "" {
		t.Fatalf("independent Project Inspection blocked: %+v %v", second, e)
	}
	counts = projectInspectionCounts(t, f)
	if counts["runs"] != 2 || counts["agent_conversations"] != 2 || counts["project_inspection_agent_claims"] != 2 {
		t.Fatalf("owner namespaces conflated: %v", counts)
	}
	firstState, e := store.InspectionState(f.ctx, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	firstWorkset, firstBefore, e := f.plane.ContextProbeRunner.Canonical.Materializer.MaterializeInspection(f.ctx, firstState.Spec)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.BeginInspection(f.ctx, firstState.Spec, firstWorkset, firstBefore); e != nil {
		t.Fatal(e)
	}
	secondWorkset, secondBefore, e := f.plane.ContextProbeRunner.Canonical.Materializer.MaterializeInspection(f.ctx, second.Spec)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.BeginInspection(f.ctx, second.Spec, secondWorkset, secondBefore); e != nil {
		t.Fatal(e)
	}
	if firstWorkset.WorkspaceID == secondWorkset.WorkspaceID || firstState.Conversation.ID == second.Conversation.ID {
		t.Fatal("distinct owners share workspace/conversation")
	}
	for _, a := range firstWorkset.Members {
		for _, b := range secondWorkset.Members {
			if a.CanonicalRoot == b.CanonicalRoot || a.WorktreeID == b.WorktreeID {
				t.Fatal("distinct owners share managed root")
			}
		}
	}
	var areas int
	if e = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM feature_areas`).Scan(&areas); e != nil || areas != 0 {
		t.Fatalf("fake FeatureArea created: %d %v", areas, e)
	}
}

func TestProjectInspectionLegacyReadOnlyRuntimeBoundary(t *testing.T) {
	f := newProjectInspectionAPIFixture(t)
	legacy, e := f.plane.ContextProbes.Create(f.ctx, contextprobe.CreateInput{AISessionID: f.session, RepositoryID: f.repos["app"], CodexConfigID: "codex_config_default", CodexInstruction: "Historical legacy Inspection", Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}})
	if e != nil {
		t.Fatal(e)
	}
	before := projectInspectionCounts(t, f)
	read, e := f.plane.ContextProbes.Get(f.ctx, legacy.ID)
	if e != nil || read.CodexInstruction != legacy.CodexInstruction || read.Canonical != nil {
		t.Fatalf("legacy unreadable: %+v %v", read, e)
	}
	if _, e = f.plane.ContextProbeRunner.Start(f.ctx, f.session, legacy.ID); e == nil {
		t.Fatal("legacy start dispatched")
	}
	if _, e = sqlite.NewInspectionRuntimeStore(f.plane.DB).ConfirmedInspection(f.ctx, f.session, legacy.ID); e == nil || !strings.Contains(e.Error(), "LEGACY_INSPECTION_NO_CANONICAL_RUNTIME") {
		t.Fatalf("legacy runtime limitation missing: %v", e)
	}
	after := projectInspectionCounts(t, f)
	for _, table := range []string{"runs", "workspaces", "worktrees", "agent_conversations", "project_inspection_agent_claims"} {
		if after[table] != before[table] {
			t.Fatalf("legacy runtime backfilled %s", table)
		}
	}
	_ = confirmedRuntimeInspection(t, f, "new-after-legacy")
}

func TestProjectInspectionReservedPreparationReplay(t *testing.T) {
	f := newProjectInspectionAPIFixture(t)
	p := confirmedRuntimeInspection(t, f)
	store := sqlite.NewInspectionRuntimeStore(f.plane.DB)
	spec, err := store.ConfirmedInspection(f.ctx, f.session, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ReserveInspection(f.ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := f.plane.DataPaths.Root
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	store = sqlite.NewInspectionRuntimeStore(f.plane.DB)
	if err = f.plane.ContextProbeRunner.Canonical.Recover(f.ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := store.ReserveInspection(f.ctx, spec)
	if err != nil || replay.Run.ID != first.Run.ID || replay.State != "PREPARED" || replay.Run.RuntimeEpoch == first.Run.RuntimeEpoch {
		t.Fatalf("reservation replay: %+v %v", replay, err)
	}
	counts := projectInspectionCounts(t, f)
	if counts["runs"] != 1 || counts["worktrees"] != 0 || counts["agent_conversations"] != 1 {
		t.Fatalf("preparation effects: %v", counts)
	}
	canceled, e := f.plane.ContextProbeRunner.Canonical.CancelExecution(f.ctx, executioncancel.Input{AISessionID: f.session, ProjectID: f.project, OwnerType: "INSPECTION", OwnerID: p.ID, RunID: first.Run.ID, RequestID: "cancel-preparation", Reason: "Cancel before model effects"})
	if e != nil || canceled.State != "CANCELED" {
		t.Fatalf("prepared cancellation: %+v %v", canceled, e)
	}
	state, e := store.InspectionState(f.ctx, p.ID)
	if e != nil || state.Finalization == nil || !state.Finalization.NoBackendAdvance || state.Run.ThreadID != "" || state.Run.TurnID != "" {
		t.Fatalf("prepared cancel invented backend result: %+v %v", state, e)
	}
}

// Native transport, managed materialization and terminal reduction use production
// components. Only the provider is local and deterministic; no external model.
func TestProjectInspectionOfflineNativeRuntime(t *testing.T) {
	for _, mode := range []string{"complete", "restart-question", "lost-server", "cancel-question", "cancel-terminal-race", "unknown-response", "mutation", "malformed"} {
		t.Run(mode, func(t *testing.T) { runProjectInspectionOffline(t, mode) })
	}
}
func runProjectInspectionOffline(t *testing.T, mode string) {
	runProjectInspectionOfflineHooks(t, mode, nil, nil)
}
func runProjectInspectionOfflineHooks(t *testing.T, mode string, providerHook func(int64, probeexecution.CanonicalState, map[string]any) map[string]any, unknownHook func(*projectInspectionAPIFixture, probeexecution.CanonicalState, *atomic.Int64)) {
	native := os.Getenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY")
	if native == "" {
		t.Skip("offline installed App Server binary not selected")
	}
	f := newProjectInspectionAPIFixture(t)
	p := confirmedRuntimeInspection(t, f)
	store := sqlite.NewInspectionRuntimeStore(f.plane.DB)
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		defer r.Body.Close()
		var input map[string]any
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "bad input", 400)
			return
		}
		n := calls.Add(1)
		state, err := sqlite.NewInspectionRuntimeStore(f.plane.DB).InspectionState(f.ctx, p.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		raw, _ := json.Marshal(input)
		if n <= 2 && recoveryLatestUserText(string(raw)) != p.CodexInstruction {
			t.Errorf("native prompt changed: %q", recoveryLatestUserText(string(raw)))
		}
		report := probeexecution.InspectionReport{SchemaVersion: "project-inspection-v1", Source: probeexecution.InspectionSourceForWorkset(state.Workset), Status: "SUCCEEDED", Summary: "Observed both frozen repositories", Findings: []probeexecution.InspectionFinding{}, RepositoryObservations: []probeexecution.InspectionRepositoryObservation{}, BlockedReasons: []string{}, Relationships: []probeexecution.InspectionRelationship{}, RecommendedNextAction: "NONE"}
		for _, m := range state.Workset.Members {
			report.RepositoryObservations = append(report.RepositoryObservations, probeexecution.InspectionRepositoryObservation{RepositoryAlias: m.RepositoryAlias, Summary: "Read only", RelevantPaths: []string{"same.txt"}})
		}
		text, _ := json.Marshal(report)
		item := recoveryMessage(string(text))
		if mode == "malformed" && n > 1 {
			item = recoveryMessage("not a structured report")
		}
		if mode == "mutation" && n > 1 {
			if err := os.WriteFile(filepath.Join(state.Workset.Members[0].CanonicalRoot, "changed-by-concurrent-writer"), []byte("preserve this evidence"), 0600); err != nil {
				t.Error(err)
			}
		}
		if n == 1 {
			commands := []string{}
			for _, m := range state.Workset.Members {
				commands = append(commands, "cat -- "+recoveryShellQuote(filepath.Join(m.CanonicalRoot, "same.txt")))
			}
			args, _ := json.Marshal(map[string]any{"cmd": strings.Join(commands, " && "), "max_output_tokens": 1000})
			item = map[string]any{"type": "function_call", "call_id": "inspection-real-read", "name": "exec_command", "arguments": string(args)}
			if mode == "restart-question" || mode == "lost-server" || mode == "cancel-question" || mode == "unknown-response" {
				item = map[string]any{"type": "function_call", "call_id": "inspection-question", "name": "request_user_input", "arguments": `{"questions":[{"id":"inspection","header":"Inspect","question":"Continue this inspection?","options":[{"label":"yes","description":"Continue"},{"label":"no","description":"Stop"}]}]}`}
			}
		}
		if providerHook != nil && n > 2 {
			item = providerHook(n, state, input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": "inspection"}}, {"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": "inspection", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
			b, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], b)
		}
	}))
	defer provider.Close()
	home := t.TempDir()
	config := fmt.Sprintf("openai_base_url=%q\nmodel_provider=\"openai\"\n[features]\ndefault_mode_request_user_input=true\nplugins=false\nremote_plugin=false\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n", provider.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"OPENAI_API_KEY":"offline-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	runner := f.plane.ContextProbeRunner.Canonical
	if mode == "cancel-terminal-race" {
		runner.Store = inspectionFinalizationHook{CanonicalStore: store, before: func() {
			state, e := store.InspectionState(f.ctx, p.ID)
			if e != nil {
				t.Error(e)
				return
			}
			if _, e = f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, ProjectID: f.project, OwnerType: "INSPECTION", OwnerID: p.ID, RunID: state.Run.ID, RequestID: "cancel-terminal", Reason: "Cancel won before finalization commit"}); e != nil {
				t.Error(e)
			}
		}}
	}
	var fault *loseResolvedReceiptStore
	if mode == "unknown-response" {
		fault = &loseResolvedReceiptStore{AgentRuntimeStore: sqlite.NewAgentRuntimeStore(f.plane.DB)}
		f.plane.Runs.AgentRuntime = agentruntime.NewService(fault)
		runner.AgentRuntime = f.plane.Runs.AgentRuntime
	}
	runner.AppServerSpec = func(codexconfig.Config) appserver.Spec {
		return appserver.Spec{ExecutablePath: native, CodexHome: home, Env: []string{"OPENAI_BASE_URL=" + provider.URL + "/v1", "OPENAI_API_KEY=offline-fixture"}}
	}
	started, err := runner.StartConfirmed(f.ctx, f.session, p.ID, p.CanonicalRequestID, p.CanonicalPayloadSHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if b, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, started.RunID); e == nil {
			_ = appserver.Stop(f.ctx, b)
		}
	}()
	if mode == "restart-question" || mode == "lost-server" || mode == "cancel-question" || mode == "unknown-response" {
		waitQuestion := func(oldConnection string) agentruntime.Request {
			t.Helper()
			until := time.Now().Add(20 * time.Second)
			for time.Now().Before(until) {
				snapshot, e := f.plane.Runs.AgentRuntime.Read(f.ctx, started.RunID)
				if e == nil {
					for _, q := range snapshot.Requests {
						if q.State == "WAITING_FOR_USER" && q.ConnectionID != oldConnection && len(q.AvailableActions) > 0 {
							return q
						}
					}
				}
				time.Sleep(25 * time.Millisecond)
			}
			state, _ := store.InspectionState(f.ctx, p.ID)
			t.Fatalf("question unavailable: %+v", state)
			return agentruntime.Request{}
		}
		old := waitQuestion("")
		if mode == "unknown-response" {
			decision := agentruntime.UserDecisionInput{Identity: old.Identity, RuntimeRequestID: old.ID, SubmissionRequestID: "inspection-unknown-answer", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: "yes", SelectedOptionID: "yes"}
			decision.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(decision)
			if _, e := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, decision); e != nil {
				t.Fatal(e)
			}
			until := time.Now().Add(20 * time.Second)
			for time.Now().Before(until) && (calls.Load() != 2 || fault.rejected.Load() == 0) {
				time.Sleep(25 * time.Millisecond)
			}
			if calls.Load() != 2 || fault.rejected.Load() == 0 {
				t.Fatal("response receipt fault not reached")
			}
			dataRoot := f.plane.DataPaths.Root
			if e := f.plane.Close(); e != nil {
				t.Fatal(e)
			}
			var e error
			f.plane, e = controlplane.Open(f.ctx, controlplane.Config{DataRoot: dataRoot})
			if e != nil {
				t.Fatal(e)
			}
			store = sqlite.NewInspectionRuntimeStore(f.plane.DB)
			runner = f.plane.ContextProbeRunner.Canonical
			until = time.Now().Add(5 * time.Second)
			unknown := false
			for time.Now().Before(until) {
				snapshot, e := f.plane.Runs.AgentRuntime.Read(f.ctx, started.RunID)
				if e == nil {
					for _, q := range snapshot.Requests {
						if q.ID == old.ID && agentruntime.ResponseDisposition(q) == "UNKNOWN" {
							unknown = true
						}
					}
				}
				if unknown {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			state, e := store.InspectionState(f.ctx, p.ID)
			if e != nil || !unknown || state.Finalization != nil || state.Run.ThreadID != old.ThreadID || state.Run.TurnID != old.TurnID || calls.Load() != 2 {
				t.Fatalf("UNKNOWN advanced or redispatched: state=%+v unknown=%v calls=%d err=%v", state, unknown, calls.Load(), e)
			}
			if _, e = runner.StartConfirmed(f.ctx, f.session, p.ID, p.CanonicalRequestID, p.CanonicalPayloadSHA256); e != nil || calls.Load() != 2 {
				t.Fatalf("UNKNOWN start replay: %v calls=%d", e, calls.Load())
			}
			if unknownHook != nil {
				unknownHook(f, state, &calls)
			}
			return
		} else if mode == "cancel-question" {
			if _, e := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, ProjectID: f.project, OwnerType: "INSPECTION", OwnerID: p.ID, RunID: started.RunID, RequestID: "cancel-inspection", Reason: "User canceled the exact Inspection"}); e != nil {
				t.Fatal(e)
			}
		} else {
			binding, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, started.RunID)
			if e != nil {
				t.Fatal(e)
			}
			dataRoot := f.plane.DataPaths.Root
			if e = f.plane.Close(); e != nil {
				t.Fatal(e)
			}
			if mode == "lost-server" {
				if e = appserver.Stop(f.ctx, binding); e != nil {
					t.Fatal(e)
				}
			}
			f.plane, e = controlplane.Open(f.ctx, controlplane.Config{DataRoot: dataRoot})
			if e != nil {
				t.Fatal(e)
			}
			store = sqlite.NewInspectionRuntimeStore(f.plane.DB)
			runner = f.plane.ContextProbeRunner.Canonical
			if mode == "lost-server" {
				until := time.Now().Add(5 * time.Second)
				var state probeexecution.CanonicalState
				for time.Now().Before(until) {
					state, e = store.InspectionState(f.ctx, p.ID)
					if e != nil {
						t.Fatal(e)
					}
					if state.State == "RECOVERY_REQUIRED" {
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
				if state.State != "RECOVERY_REQUIRED" || state.Finalization != nil || state.Run.ThreadID != old.ThreadID || state.Run.TurnID != old.TurnID {
					t.Fatalf("lost server fabricated completion: %+v", state)
				}
				if _, e = runner.StartConfirmed(f.ctx, f.session, p.ID, p.CanonicalRequestID, p.CanonicalPayloadSHA256); e != nil || calls.Load() != 1 {
					t.Fatalf("lost server redispatched: %v calls=%d", e, calls.Load())
				}
				var bindings int
				if e = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM app_server_run_bindings WHERE run_id=?`, started.RunID).Scan(&bindings); e != nil || bindings != 1 {
					t.Fatalf("replacement binding created: %d %v", bindings, e)
				}
				return
			}
			f.facade, e = controlapi.NewFromControlPlane(f.plane)
			if e != nil {
				t.Fatal(e)
			}
			current := waitQuestion(old.ConnectionID)
			same, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, started.RunID)
			if e != nil || same.ID != binding.ID || same.PID != binding.PID || current.ID != old.ID || current.ThreadID != old.ThreadID || current.TurnID != old.TurnID || calls.Load() != 1 {
				t.Fatalf("restart identity changed: old=%+v current=%+v calls=%d err=%v", old, current, calls.Load(), e)
			}
			decision := agentruntime.UserDecisionInput{Identity: current.Identity, RuntimeRequestID: current.ID, SubmissionRequestID: "inspection-answer", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: "yes", SelectedOptionID: "yes"}
			decision.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(decision)
			if _, e = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, decision); e != nil {
				t.Fatal(e)
			}
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	var state probeexecution.CanonicalState
	for time.Now().Before(deadline) {
		state, err = store.InspectionState(f.ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Finalization != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	expected := "SUCCEEDED"
	if mode == "mutation" {
		expected = "SCOPE_CONFLICT"
	}
	if mode == "cancel-question" || mode == "cancel-terminal-race" {
		expected = "CANCELED"
	}
	if state.Finalization == nil || state.Finalization.Status != expected {
		t.Fatalf("native Inspection failed: state=%s reason=%s receipt=%+v", state.State, state.FailureReason, state.Finalization)
	}
	if len(state.Schema) == 0 || state.Run.ThreadID == "" || state.Run.TurnID == "" || len(state.Workset.Members) != 2 {
		t.Fatalf("missing canonical runtime: %+v", state)
	}
	source, e := f.plane.Runs.Get(f.ctx, state.Run.ID)
	if e != nil || source.ContextProbeID != p.ID || source.TaskID != p.TaskID || source.FeatureAreaID != "" || source.RuntimeEpoch == "" || source.RuntimeWorkset == nil || source.RuntimeWorkset.ContentHash != state.Workset.ContentHash {
		t.Fatalf("future source facts unavailable: %+v %v", source, e)
	}
	if expected == "SUCCEEDED" {
		analysis, err := f.facade.GetRunAnalysis(f.ctx, f.session, state.Run.ID)
		if err != nil || analysis.State != "REQUIRED" || analysis.Kind != "INSPECTION" {
			t.Fatalf("inspection analysis source %+v %v", analysis, err)
		}
		formal := probeexecution.InspectionReport{SchemaVersion: "project-inspection-v1", Source: probeexecution.InspectionSourceForWorkset(state.Workset), Status: "SUCCEEDED", Summary: "GPT independently analyzed the immutable Inspection output and command evidence", Findings: []probeexecution.InspectionFinding{}, RepositoryObservations: []probeexecution.InspectionRepositoryObservation{}, BlockedReasons: []string{}, Relationships: []probeexecution.InspectionRelationship{}, RecommendedNextAction: "NONE"}
		for _, m := range state.Workset.Members {
			formal.RepositoryObservations = append(formal.RepositoryObservations, probeexecution.InspectionRepositoryObservation{RepositoryAlias: m.RepositoryAlias, Summary: "Frozen member read", RelevantPaths: []string{"same.txt"}})
		}
		raw, _ := json.Marshal(formal)
		if _, err = f.facade.SubmitRunAnalysis(f.ctx, runanalysis.Input{AISessionID: f.session, RunID: state.Run.ID, RequestID: "gpt-inspection-analysis", SourceHash: analysis.SourceHash, Report: raw}); err != nil {
			t.Fatalf("inspection analysis failed: %v cause=%v", err, errors.Unwrap(err))
		}
		native, err := f.plane.Runs.Get(f.ctx, state.Run.ID)
		if err != nil || native.Result.AgentClaim.Message != source.Result.AgentClaim.Message {
			t.Fatal("analysis replaced native output", err)
		}
	}
	binding, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, state.Run.ID)
	if e != nil || binding.ID == "" || state.Conversation.ID == "" || state.Workset.WorkspaceID == "" || len(state.After) != 2 {
		t.Fatalf("future source linkage missing: %+v %v", binding, e)
	}
	var children int
	if e = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM recovery_status_records WHERE source_run_id=?`, state.Run.ID).Scan(&children); e != nil || children != 0 {
		t.Fatalf("Inspection created Recovery child: %d %v", children, e)
	}
	receipt := *state.Finalization
	readback, e := store.FinalizeInspection(f.ctx, receipt)
	if e != nil || readback.Finalization.ReportHash != receipt.ReportHash {
		t.Fatalf("finalization reply-loss replay: %+v %v", readback, e)
	}
	stale := receipt
	stale.RuntimeEpoch = "old-runtime-epoch"
	if _, e = store.FinalizeInspection(f.ctx, stale); e == nil {
		t.Fatal("stale callback finalized")
	}
	if _, e = sqlite.NewContextProbeStore(f.plane.DB).Update(f.ctx, p.ID, func(p *contextprobe.Probe) error { p.Result = "forged generic result"; return nil }); e == nil {
		t.Fatal("generic probe mutation bypassed canonical reducer")
	}
	var claims int
	if e = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM project_inspection_agent_claims WHERE inspection_id=?`, p.ID).Scan(&claims); e != nil || claims != 0 {
		t.Fatalf("terminal claim retained: %d %v", claims, e)
	}
	replay, err := runner.StartConfirmed(f.ctx, f.session, p.ID, p.CanonicalRequestID, p.CanonicalPayloadSHA256)
	expectedCalls := int64(2)
	if mode == "cancel-question" {
		expectedCalls = 1
	}
	if err != nil || replay.RunID != started.RunID || calls.Load() != expectedCalls {
		t.Fatalf("start replay redispatched: %+v %v calls=%d", replay, err, calls.Load())
	}
}

// Deliberately NOT_RUN unless the operator supplies both an explicit opt-in and
// a test credential file. It never reads the user's default Codex home.
func TestProjectInspectionRealCodexOptIn(t *testing.T) {
	if os.Getenv("DEVBRIDGE_REAL_CODEX_PROJECT_INSPECTION") != "1" {
		t.Skip("NOT_RUN: DEVBRIDGE_REAL_CODEX_PROJECT_INSPECTION=1 is required")
	}
	native := os.Getenv("DEVBRIDGE_REAL_CODEX_PROJECT_INSPECTION_BINARY")
	authPath := os.Getenv("DEVBRIDGE_REAL_CODEX_PROJECT_INSPECTION_AUTH_FILE")
	if native == "" || authPath == "" {
		t.Fatal("explicit test binary and test auth file are required")
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
	runner.AppServerSpec = func(codexconfig.Config) appserver.Spec {
		return appserver.Spec{ExecutablePath: native, CodexHome: home}
	}
	started, err := runner.StartConfirmed(f.ctx, f.session, p.ID, p.CanonicalRequestID, p.CanonicalPayloadSHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if b, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, started.RunID); e == nil {
			_ = appserver.Stop(f.ctx, b)
		}
	}()
	store := sqlite.NewInspectionRuntimeStore(f.plane.DB)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		state, e := store.InspectionState(f.ctx, p.ID)
		if e != nil {
			t.Fatal(e)
		}
		if state.Finalization != nil {
			if state.Finalization.Status != "SUCCEEDED" {
				t.Fatalf("real Inspection ended: %+v", state.Finalization)
			}
			return
		}
		if state.State == "RECOVERY_REQUIRED" {
			t.Fatalf("real Inspection requires recovery: %s", state.FailureReason)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("real Inspection did not finalize before test deadline")
}
