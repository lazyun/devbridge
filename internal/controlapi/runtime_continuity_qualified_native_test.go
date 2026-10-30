package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime/pprof"
	"strings"
	"sync"
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
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// These scenarios enter the public API through a real authored Review, measured
// installed-native qualification, formal approval, exact binding, and explicit
// start. Only the loopback Responses model is synthetic; no protocol fixture or
// SQL-inserted qualification supplies execution authority.
func TestRuntimeContinuityQualifiedNativeCoreReopen(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Skip("explicit installed native binary required; loopback provider only")
	}
	runQualifiedNativeContinuity(t, native, []string{"waiting", "recorded", "lost", "inflight"})
}

func runQualifiedNativeContinuity(t *testing.T, native string, scenarios []string) {
	t.Helper()
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			evidenceRoot := os.Getenv("DEVBRIDGE_CONTINUITY_EVIDENCE_DIR")
			if evidenceRoot == "" {
				evidenceRoot = "../../build/codex-continuity-01592-2026-10-02"
			}
			root, err := filepath.Abs(evidenceRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			root, err = os.MkdirTemp(root, "core-"+scenario+"-")
			if err != nil {
				t.Fatal(err)
			}
			t.Log("qualified native continuity evidence:", root)

			questionCall, responseCall, wantBindings := int64(1), int64(2), 1
			phase := task.PhaseImplementing
			if scenario == "verification" {
				questionCall, responseCall, wantBindings = 2, 3, 2
				phase = task.PhaseVerifying
			}
			var calls atomic.Int64
			providerStarted := make(chan struct{})
			releaseProvider := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseProvider) }) }
			t.Cleanup(release)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusUpgradeRequired)
					return
				}
				defer r.Body.Close()
				raw, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
				if e != nil {
					http.Error(w, e.Error(), 400)
					return
				}
				n := calls.Add(1)
				if e = os.WriteFile(filepath.Join(root, fmt.Sprintf("provider-request-%d.json", n)), raw, 0600); e != nil {
					t.Error(e)
				}
				if n == 1 && scenario != "inflight" {
					close(providerStarted)
				}
				if scenario == "inflight" && n == 1 {
					// Establish the actual stream before detaching Core. Holding the
					// first response bytes would test a different timeout boundary.
					w.Header().Set("Content-Type", "text/event-stream")
					created, _ := json.Marshal(map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("qualified-continuity-response-%d", n)}})
					fmt.Fprintf(w, "event: response.created\ndata: %s\n\n", created)
					w.(http.Flusher).Flush()
					close(providerStarted)
					select {
					case <-releaseProvider:
					case <-r.Context().Done():
						return
					}
				}
				var item map[string]any
				if n == questionCall && scenario != "inflight" {
					args := `{"questions":[{"id":"adaptation","header":"Fixture","question":"Choose the fixture path?","options":[{"label":"yes","description":"Continue"},{"label":"no","description":"Alternative"}]}]}`
					item = map[string]any{"type": "function_call", "call_id": "qualified-continuity-question", "name": "request_user_input", "arguments": args}
				} else if scenario == "verification" {
					result := "Verification Turn completed without an unobserved check-success claim"
					if n == 1 {
						result = `{"resultDisposition":"COMPLETED","summary":"Implementation precondition complete; no file changes"}`
					}
					item = map[string]any{"id": "qualified-verification-result", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": result}}}
				} else {
					item = map[string]any{"id": "qualified-continuity-result", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": `{"resultDisposition":"SCOPE_CONFLICT","summary":"Native continuity completed the original Turn without repository changes"}`}}}
				}
				if n > responseCall || (scenario == "inflight" && n > 1) {
					t.Errorf("unexpected duplicate model dispatch: %d", n)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				id := fmt.Sprintf("qualified-continuity-response-%d", n)
				events := []map[string]any{{"type": "response.created", "response": map[string]any{"id": id}}, {"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": id, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}}
				if scenario == "inflight" && n == 1 {
					events = events[1:]
				}
				for _, event := range events {
					data, _ := json.Marshal(event)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
				}
			}))
			t.Cleanup(func() { release(); provider.Close() })
			home := filepath.Join(root, "runtime-home")
			if err = os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			configText := fmt.Sprintf("openai_base_url=%q\nmodel_provider=\"continuity_offline\"\n[model_providers.continuity_offline]\nname=\"Qualified native continuity loopback\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\nrequest_max_retries=0\nstream_max_retries=0\n[features]\ndefault_mode_request_user_input=true\nresponses_websockets=false\nresponses_websockets_v2=false\nplugins=false\nremote_plugin=false\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n", provider.URL+"/v1", provider.URL+"/v1")
			if err = os.WriteFile(filepath.Join(home, "config.toml"), []byte(configText), 0600); err != nil {
				t.Fatal(err)
			}
			for k, v := range map[string]string{"HOME": home, "CODEX_HOME": home, "HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "", "OPENAI_API_KEY": "", "CODEX_API_KEY": "", "OPENAI_BASE_URL": provider.URL + "/v1", "OTEL_SDK_DISABLED": "true"} {
				t.Setenv(k, v)
			}
			f := newUpperLifecycleFixture(t)
			defer func() {
				if !t.Failed() {
					return
				}
				if out, e := os.Create(filepath.Join(root, "failure-goroutines.txt")); e == nil {
					_ = pprof.Lookup("goroutine").WriteTo(out, 2)
					_ = out.Close()
				}
				if f.plane != nil {
					if values, e := f.plane.Runs.ListByTask(f.ctx, f.task); e == nil {
						if raw, e := json.MarshalIndent(values, "", "  "); e == nil {
							_ = os.WriteFile(filepath.Join(root, "failure-runs.json"), raw, 0600)
						}
					}
				}
			}()
			t.Cleanup(func() {
				paths, _ := filepath.Glob(filepath.Join(f.dataRoot, "appservers", "*", "process.log"))
				for _, path := range paths {
					if raw, e := os.ReadFile(path); e == nil {
						_ = os.WriteFile(filepath.Join(root, "appserver-"+filepath.Base(filepath.Dir(path))+".log"), raw, 0600)
					}
				}
			})
			f.plane.Retirement.Close()
			runtime, backend, authority := prepareQualifiedContinuityExecution(t, f, native, root)
			spec := func(codexconfig.Config) appserver.Spec {
				return appserver.Spec{ExecutablePath: native, CodexHome: home}
			}
			planeConfig := controlplane.Config{DataRoot: f.dataRoot, Codex: codex.Config{CodexPath: native}, AppServerContinuity: true, AppServerSpec: spec}
			reopen := func() {
				if f.plane != nil {
					if e := f.plane.Close(); e != nil {
						t.Fatal(e)
					}
					f.plane = nil
				}
				f.plane, err = controlplane.Open(f.ctx, planeConfig)
				if err != nil {
					t.Fatal(err)
				}
				f.plane.Retirement.Close()
				f.facade, err = controlapi.NewFromControlPlane(f.plane)
				if err != nil {
					t.Fatal(err)
				}
			}
			reopen()
			t.Cleanup(func() { stopContinuityFixtureServers(f) })
			expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
			if err != nil {
				t.Fatal(err)
			}
			startInput := controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: f.task, RequestID: "qualified-continuity-start", ExpectedTarget: expected, RuntimeConfig: runtime, Backend: backend.Selection("SESSION_INVENTORY").Input()}
			started, err := f.facade.StartApprovedTaskExecution(f.ctx, startInput)
			if err != nil || started.Run.ID == "" {
				t.Fatalf("public qualified start: %+v %v", started, err)
			}
			var request agentruntime.Request
			var original run.Run
			if scenario == "inflight" {
				select {
				case <-providerStarted:
				case <-time.After(45 * time.Second):
					t.Fatal("no native inflight provider call")
				}
				original = waitQualifiedContinuityTurn(t, f, started.Run.ID)
				if original.AgentRuntime == nil || len(original.AgentRuntime.Requests) != 0 {
					t.Fatalf("inflight unexpectedly waiting: %+v", original.AgentRuntime)
				}
			} else {
				request, original = waitContinuityQuestion(t, f, "")
				assertNoAutomaticAnswer(t, f, request, phase)
			}
			if (scenario != "verification" && original.ID != started.Run.ID) || original.ExecutionProposalID != started.Proposal.ID || (scenario == "verification" && (original.Purpose != run.PurposeVerification || original.ID == started.Run.ID)) {
				t.Fatal("runtime replaced public start identity")
			}
			binding, err := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, original.ID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = appserver.Stop(context.Background(), binding) })
			if binding.CodexVersion != "0.159.2" || binding.ExecutablePath != native {
				t.Fatalf("unexpected measured native instance: %+v", binding)
			}
			originalFence := runtimeepoch.WithFence(f.ctx, original.RuntimeEpoch, original.ID)
			if scenario == "verification" {
				if err = f.plane.Runs.ValidateRunExecution(originalFence, original); err != nil {
					t.Fatalf("fresh verification continuation lost execution authority: %v", err)
				}
				if original.RuntimeWorkset == nil || original.RuntimeWorkset.Validate() != nil {
					t.Fatal("verification continuation lacks frozen workset")
				}
			} else {
				if err = f.plane.Runs.ValidatePreparedStart(originalFence, original.ID); err != nil {
					t.Fatalf("fresh native start lost its original authority: %v", err)
				}
				if err = f.plane.Runs.ValidatePreparedRejoin(originalFence, original.ID); !errors.Is(err, sqlite.ErrConflict) {
					t.Fatalf("rejoin without an exact reattachment receipt must fail: %v", err)
				}
			}
			preparationTables := []string{"runtime_start_authorities", "runtime_workset_bindings"}
			originalPreparation := definitionFormalSnapshot(t, f, preparationTables)
			if err = os.WriteFile(filepath.Join(root, "preparation-authority-before.json"), []byte(originalPreparation), 0600); err != nil {
				t.Fatal(err)
			}
			assertOriginalPreparation := func() {
				t.Helper()
				current := definitionFormalSnapshot(t, f, preparationTables)
				if current != originalPreparation {
					t.Fatal("Core recovery rewrote immutable start authority or workset binding")
				}
				if e := os.WriteFile(filepath.Join(root, "preparation-authority-after.json"), []byte(current), 0600); e != nil {
					t.Fatal(e)
				}
			}
			assertRejoinAuthority := func(current run.Run) {
				t.Helper()
				fence := runtimeepoch.WithFence(f.ctx, current.RuntimeEpoch, current.ID)
				if scenario == "verification" {
					if e := f.plane.Runs.ValidateRunExecution(fence, current); e != nil {
						t.Fatalf("verification rejoin lost execution authority: %v", e)
					}
					if !reflect.DeepEqual(current.RuntimeWorkset, original.RuntimeWorkset) {
						t.Fatal("verification rejoin rewrote frozen workset")
					}
				} else {
					if e := f.plane.Runs.ValidatePreparedStart(fence, current.ID); !errors.Is(e, sqlite.ErrConflict) {
						t.Fatalf("old start authority authorized a new start after recovery: %v", e)
					}
					if e := f.plane.Runs.ValidatePreparedRejoin(fence, current.ID); e != nil {
						t.Fatalf("exact original-turn reattachment chain lost authority: %v", e)
					}
				}
				assertOriginalPreparation()
			}
			var decision agentruntime.UserDecision
			if scenario == "recorded" || scenario == "lost" {
				in := explicitDecision(t, f, request, "RESPOND", "no")
				in.SubmittedBy = "session:" + f.session
				decision, err = f.plane.Runs.SubmitUserDecision(f.ctx, in)
				if err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 1 {
					t.Fatal("recording decision dispatched answer")
				}
			}
			if err = f.plane.Close(); err != nil {
				t.Fatal(err)
			}
			f.plane = nil
			if err = appserver.Verify(f.ctx, binding); err != nil {
				t.Fatalf("Core close killed original App Server: %v", err)
			}
			if scenario == "lost" {
				if err = appserver.Stop(f.ctx, binding); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 100 && appserver.Verify(f.ctx, binding) == nil; i++ {
					time.Sleep(10 * time.Millisecond)
				}
				replacementSpec := spec(backend)
				replacementSpec.DataRoot = f.dataRoot
				replacementSpec.BackendConfigID, replacementSpec.BackendFingerprint = binding.BackendConfigID, binding.BackendFingerprint
				replacement, e := appserver.Launch(f.ctx, replacementSpec)
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { _ = appserver.Stop(context.Background(), replacement) })
				if replacement.ID == binding.ID || replacement.Epoch == binding.Epoch || replacement.PID == binding.PID {
					t.Fatal("replacement reused original identity")
				}
			}
			reopen()
			assertOriginalPreparation()
			currentAuthority, err := sqlite.NewRuntimeSelectionStore(f.plane.DB).ReadCurrent(f.ctx, authority.Owner)
			if err != nil || !reflect.DeepEqual(currentAuthority, authority) {
				t.Fatalf("reopen rebound qualification: %+v %v", currentAuthority, err)
			}
			if scenario == "lost" {
				current, e := f.plane.Runs.Get(f.ctx, original.ID)
				if e != nil {
					t.Fatal(e)
				}
				state := current.AgentRuntime
				if state == nil || state.Observation == nil || state.Observation.InstanceStatus != "LOST" || len(state.Requests) != 1 || state.Requests[0].Projection == nil || state.Requests[0].Projection.ReasonCode != "CODEX_RUNTIME_SESSION_LOST" || len(state.Requests[0].AvailableActions) != 0 || state.Requests[0].UserDecision == nil || state.Requests[0].UserDecision.ID != decision.ID || calls.Load() != 1 {
					t.Fatalf("lost original promoted replacement: %+v calls=%d", state, calls.Load())
				}
				owner, e := f.facade.GetTask(f.ctx, f.task)
				if e != nil || owner.Phase != task.PhaseRecoveryRequired {
					t.Fatalf("lost original instance did not require Task recovery: phase=%s error=%v", owner.Phase, e)
				}
				assertQualifiedContinuityIdentity(t, f, original, binding, wantBindings)
				writeQualifiedContinuityEvidence(t, root, binding, current)
				return
			}
			if scenario == "waiting" || scenario == "verification" {
				recovered, current := waitContinuityQuestion(t, f, request.ConnectionID)
				if recovered.ID != request.ID || current.ID != original.ID || current.ThreadID != original.ThreadID || current.TurnID != original.TurnID || recovered.RuntimeEpoch == request.RuntimeEpoch {
					t.Fatalf("original request was replaced: old=%+v new=%+v", request, recovered)
				}
				assertNoAutomaticAnswer(t, f, recovered, phase)
				assertRejoinAuthority(current)
				// A second Core restart must trace two immutable reattachment
				// receipts back to the original start epoch, without issuing a
				// replacement request, turn, or preparation authority.
				reopen()
				twiceRecovered, twiceCurrent := waitContinuityQuestion(t, f, recovered.ConnectionID)
				if twiceRecovered.ID != request.ID || twiceCurrent.ID != original.ID || twiceCurrent.ThreadID != original.ThreadID || twiceCurrent.TurnID != original.TurnID || twiceRecovered.RuntimeEpoch == recovered.RuntimeEpoch || twiceRecovered.RuntimeEpoch == request.RuntimeEpoch || string(twiceRecovered.RequestID) != string(request.RequestID) {
					t.Fatalf("two-hop recovery replaced original request or reused writer epoch: first=%+v second=%+v", recovered, twiceRecovered)
				}
				assertNoAutomaticAnswer(t, f, twiceRecovered, phase)
				assertRejoinAuthority(twiceCurrent)
				var receiptCount int
				if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM app_server_reattachments WHERE run_id=?`, original.ID).Scan(&receiptCount); e != nil || receiptCount != 2 {
					t.Fatalf("expected two exact recovery receipts: count=%d error=%v", receiptCount, e)
				}
				answer, e := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, twiceRecovered, "RESPOND", "no"))
				if e != nil {
					t.Fatal(e)
				}
				decision = answer.Decision
			}
			if scenario == "inflight" {
				current := waitQualifiedContinuityTurn(t, f, original.ID)
				assertRejoinAuthority(current)
				release()
			}
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				current, e := f.plane.Runs.Get(f.ctx, original.ID)
				if e != nil {
					t.Fatal(e)
				}
				if current.AgentRuntime != nil && current.AgentRuntime.TurnStatus == "completed" {
					wantCalls := responseCall
					if scenario == "inflight" {
						wantCalls = 1
					} else {
						if len(current.AgentRuntime.Requests) != 1 {
							t.Fatalf("request duplication: %+v", current.AgentRuntime.Requests)
						}
						q := current.AgentRuntime.Requests[0]
						if q.ID != request.ID || q.State != "RESPONDED" || q.UserDecision == nil || q.UserDecision.ID != decision.ID {
							t.Fatalf("decision not preserved: %+v", q)
						}
						// The model request after the answer must contain exactly one answered
						// original tool call, including the explicit selected answer.
						checkQualifiedContinuityAnswer(t, root, responseCall)
					}
					if calls.Load() != wantCalls {
						t.Fatalf("duplicate native turn dispatch: calls=%d want=%d", calls.Load(), wantCalls)
					}
					if current.ThreadID != original.ThreadID || current.TurnID != original.TurnID {
						t.Fatal("active turn replaced")
					}
					assertOriginalPreparation()
					assertQualifiedContinuityIdentity(t, f, original, binding, wantBindings)
					writeQualifiedContinuityEvidence(t, root, binding, current)
					t.Logf("native=%s same AppServer=%s PID=%d Run=%s Thread=%s Turn=%s Request=%s Decision=%s providerCalls=%d", binding.CodexVersion, binding.ID, binding.PID, current.ID, current.ThreadID, current.TurnID, request.ID, decision.ID, calls.Load())
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			current, _ := f.plane.Runs.Get(f.ctx, original.ID)
			t.Fatalf("original turn did not finish: run=%+v runtime=%+v calls=%d", current, current.AgentRuntime, calls.Load())
		})
	}
}

func prepareQualifiedContinuityExecution(t *testing.T, f *upperLifecycleFixture, native, root string) (codexruntime.NextRunRuntimeConfig, codexconfig.Config, runtimeselection.CurrentAuthority) {
	t.Helper()
	targets, work, verification, _ := submitLifecycleApprovalReady(t, f, "qualified-continuity-review-result")
	parent, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || parent.CurrentDefinitionRevisionID == "" {
		t.Fatalf("authored review parent missing: %+v %v", parent, err)
	}
	backend, err := f.plane.CodexConfigs.Create(f.ctx, codexconfig.CreateInput{Name: "Measured native continuity", CodexPath: native, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	f.plane.RuntimeQualification.EvidenceRoot = filepath.Join(root, "platform-evidence")
	qualified, err := f.plane.RuntimeQualification.QualifyBackend(f.ctx, backend.ID, "IMPLEMENTATION", "qualified-continuity-qualification")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "qualified-continuity-prepare", Purpose: c.Execution, Action: "APPROVE_AND_START", Execution: &c.ExecutionBody{Action: "APPROVE_AND_START", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: parent.CurrentDefinitionRevisionID, ExecutionID: parent.CurrentExecutionID}, Variants: []c.Variant{{ID: "approve", Summary: "Approve exact measured native continuity execution", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}}})
	if err != nil {
		t.Fatal(err)
	}
	options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	if err != nil || len(options.RuntimeOptions) == 0 {
		t.Fatalf("runtime options: %+v %v", options, err)
	}
	runtime := codexruntime.NextRunRuntimeConfig{Model: options.RuntimeOptions[0].ModelID, ReasoningEffort: options.RuntimeOptions[0].SupportedReasoningEfforts[0]}
	completed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "qualified-continuity-commit", ContentHash: prepared.ContentHash, VariantID: "approve", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ID, ConfigFingerprint: backend.Fingerprint()}}})
	if err != nil || completed.State != "COMPLETED" || len(completed.Steps) != 1 || completed.Steps[0].Effect != "PREPARATION_READY" || completed.Steps[0].RunID != "" {
		t.Fatalf("formal qualified approval: %+v %v", completed, err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || current.Phase != task.PhaseImplementationReady {
		t.Fatalf("formal ready: %+v %v", current, err)
	}
	authority, err := sqlite.NewRuntimeSelectionStore(f.plane.DB).ReadCurrent(f.ctx, runtimeselection.Owner{ProjectID: f.project, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"})
	if err != nil || authority.Platform != qualified.Platform || authority.Values.BackendIdentity != backend.ID || authority.Values.Model != runtime.Model {
		t.Fatalf("measured exact runtime authority: %+v %v", authority, err)
	}
	t.Logf("real native qualification=%s descriptor=%s binding=%s revision=%s", qualified.Platform.QualificationID, qualified.Platform.DescriptorID, authority.BindingID, current.CurrentDefinitionRevisionID)
	return runtime, backend, authority
}

func waitQualifiedContinuityTurn(t *testing.T, f *upperLifecycleFixture, id string) run.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v, e := f.plane.Runs.Get(f.ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if v.Failure != nil {
			t.Fatalf("native run failed: %+v", v.Failure)
		}
		if v.Status == run.StatusRunning && v.ThreadID != "" && v.TurnID != "" && v.AgentRuntime != nil {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("native active turn identity not persisted")
	return run.Run{}
}

func assertQualifiedContinuityIdentity(t *testing.T, f *upperLifecycleFixture, original run.Run, binding appserver.Binding, want int) {
	t.Helper()
	current, err := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, original.ID)
	if err != nil || !reflect.DeepEqual(current, binding) {
		t.Fatalf("original App Server receipt changed: %+v %v", current, err)
	}
	var count int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM app_server_run_bindings`).Scan(&count); err != nil || count != want {
		t.Fatalf("replacement App Server bound: count=%d err=%v", count, err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=? AND purpose='IMPLEMENTATION'`, f.task).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate implementation Run: count=%d err=%v", count, err)
	}
}

func checkQualifiedContinuityAnswer(t *testing.T, root string, responseCall ...int64) {
	t.Helper()
	number := int64(2)
	if len(responseCall) > 0 {
		number = responseCall[0]
	}
	raw, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("provider-request-%d.json", number)))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"input"`
	}
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range body.Input {
		if item.Type == "function_call_output" && item.CallID == "qualified-continuity-question" {
			count++
			if !strings.Contains(item.Output, `"no"`) {
				t.Fatalf("native lost explicit user answer: %s", item.Output)
			}
		}
	}
	if count != 1 {
		t.Fatalf("original answer count=%d want=1", count)
	}
}

func writeQualifiedContinuityEvidence(t *testing.T, root string, binding appserver.Binding, current run.Run) {
	t.Helper()
	for name, value := range map[string]any{"appserver-binding.json": binding, "final-run.json": current} {
		raw, err := json.MarshalIndent(value, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(root, name), raw, 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
