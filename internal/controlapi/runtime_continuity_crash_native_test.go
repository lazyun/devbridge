package controlapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

const continuityCrashChildInputEnv = "DEVBRIDGE_CONTINUITY_CRASH_CHILD_INPUT"

type continuityCrashChildInput struct {
	DataRoot    string
	Native      string
	RuntimeHome string
	CallerScope string
	ReadyPath   string
	Start       controlapi.ApprovedTaskStartInput
}

type continuityCrashReady struct {
	CorePID       int
	RuntimeEpoch  string
	Run           run.Run
	Binding       appserver.Binding
	StartReceipts map[string]string
}

func continuityCrashPlaneConfig(in continuityCrashChildInput) controlplane.Config {
	return controlplane.Config{DataRoot: in.DataRoot, Codex: codex.Config{CodexPath: in.Native}, AppServerContinuity: true, AppServerSpec: func(codexconfig.Config) appserver.Spec {
		return appserver.Spec{ExecutablePath: in.Native, CodexHome: in.RuntimeHome}
	}}
}

// This helper is a separate Core process, not a fake App Server. It deliberately
// leaves Close and all cleanup uncalled: the parent kills this exact PID while
// the installed native process owns an unfinished Responses stream.
func TestRuntimeContinuityNativeCrashChild(t *testing.T) {
	inputPath := os.Getenv(continuityCrashChildInputEnv)
	if inputPath == "" {
		t.Skip("owned crash-probe child only")
	}
	var in continuityCrashChildInput
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	ctx := aisession.WithCallerScope(context.Background(), in.CallerScope)
	plane, err := controlplane.Open(ctx, continuityCrashPlaneConfig(in))
	if err != nil {
		t.Fatal(err)
	}
	plane.Retirement.Close()
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	started, err := facade.StartApprovedTaskExecution(ctx, in.Start)
	if err != nil || started.Run.ID == "" {
		t.Fatalf("public start in crash child: %+v %v", started, err)
	}
	f := &upperLifecycleFixture{t: t, ctx: ctx, dataRoot: in.DataRoot, plane: plane, facade: facade, session: in.Start.AISessionID, task: in.Start.TaskID}
	active := waitQualifiedContinuityTurn(t, f, started.Run.ID)
	binding, err := sqlite.NewAgentRuntimeStore(plane.DB).AppServerBinding(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	ready := continuityCrashReady{CorePID: os.Getpid(), RuntimeEpoch: plane.DB.RuntimeEpoch(), Run: active, Binding: binding}
	ready.StartReceipts = readContinuityCrashStartReceipts(t, plane.DB.SQL(), active.ID)
	writeContinuityCrashJSON(t, in.ReadyPath+".tmp", ready)
	if err = os.Rename(in.ReadyPath+".tmp", in.ReadyPath); err != nil {
		t.Fatal(err)
	}
	<-time.After(90 * time.Second)
	t.Fatal("parent did not kill the owned Core process within the bounded crash window")
}

func TestRuntimeContinuityQualifiedNativeCoreCrash(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Skip("explicit installed native binary required; loopback provider only")
	}
	evidenceRoot := os.Getenv("DEVBRIDGE_CONTINUITY_EVIDENCE_DIR")
	if evidenceRoot == "" {
		evidenceRoot = "../../build/codex-continuity-01592-2026-10-02"
	}
	evidenceRoot, err := filepath.Abs(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(evidenceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(evidenceRoot, "core-crash-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("qualified native Core SIGKILL evidence:", root)
	t.Setenv("TMPDIR", root)
	var calls atomic.Int64
	providerStarted, releaseProvider := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProvider) }) }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		defer r.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if e != nil {
			http.Error(w, e.Error(), http.StatusBadRequest)
			return
		}
		n := calls.Add(1)
		if e = os.WriteFile(filepath.Join(root, fmt.Sprintf("provider-request-%d.json", n)), raw, 0600); e != nil {
			t.Error(e)
		}
		if n != 1 {
			t.Errorf("Core crash caused duplicate model dispatch: %d", n)
			http.Error(w, "duplicate model dispatch", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(value map[string]any) {
			data, _ := json.Marshal(value)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", value["type"], data)
		}
		event(map[string]any{"type": "response.created", "response": map[string]any{"id": "native-core-crash-response"}})
		w.(http.Flusher).Flush()
		close(providerStarted)
		select {
		case <-releaseProvider:
		case <-r.Context().Done():
			return
		}
		item := map[string]any{"id": "native-core-crash-result", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": `{"resultDisposition":"SCOPE_CONFLICT","summary":"Original native turn completed after Core SIGKILL without repository changes"}`}}}
		event(map[string]any{"type": "response.output_item.done", "item": item})
		event(map[string]any{"type": "response.completed", "response": map[string]any{"id": "native-core-crash-response", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
	}))
	t.Cleanup(func() { release(); provider.Close() })
	home := filepath.Join(root, "runtime-home")
	if err = os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("openai_base_url=%q\nmodel_provider=\"continuity_offline\"\n[model_providers.continuity_offline]\nname=\"Core crash loopback\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\nrequest_max_retries=0\nstream_max_retries=0\n[features]\nresponses_websockets=false\nresponses_websockets_v2=false\nplugins=false\nremote_plugin=false\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n", provider.URL+"/v1", provider.URL+"/v1")
	if err = os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"HOME": home, "CODEX_HOME": home, "HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "", "OPENAI_API_KEY": "", "CODEX_API_KEY": "", "OPENAI_BASE_URL": provider.URL + "/v1", "OTEL_SDK_DISABLED": "true"} {
		t.Setenv(key, value)
	}
	scope := "native-core-crash-" + t.Name()
	f := newUpperLifecycleFixtureScoped(t, scope)
	f.plane.Retirement.Close()
	runtime, backend, authority := prepareQualifiedContinuityExecution(t, f, native, root)
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	input := continuityCrashChildInput{DataRoot: f.dataRoot, Native: native, RuntimeHome: home, CallerScope: scope, ReadyPath: filepath.Join(root, "child-ready.json"), Start: controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: f.task, RequestID: "qualified-native-core-crash-start", ExpectedTarget: expected, RuntimeConfig: runtime, Backend: backend.Selection("SESSION_INVENTORY").Input()}}
	inputPath := filepath.Join(root, "child-input.json")
	writeContinuityCrashJSON(t, inputPath, input)
	databasePath := filepath.Join(f.plane.DataPaths.State, "devbridge.db")
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane = nil
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(root, "child.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	child := exec.Command(executable, "-test.run=^TestRuntimeContinuityNativeCrashChild$", "-test.v")
	child.Env = append(os.Environ(), continuityCrashChildInputEnv+"="+inputPath)
	child.Stdout, child.Stderr = log, log
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = child.Process.Kill()
			<-childDone
		}
		if f.plane != nil {
			stopContinuityFixtureServers(f)
		} else {
			// Failure before ready still has an owned binding in SQLite. Read only
			// that fixture database after killing its child, and stop its instances.
			stopCrashNativeBindings(databasePath)
		}
	})
	var ready continuityCrashReady
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case childErr := <-childDone:
			waited = true
			raw, _ := os.ReadFile(filepath.Join(root, "child.log"))
			t.Fatalf("Core child exited before crash boundary: %v\n%s", childErr, raw)
		default:
		}
		if raw, readErr := os.ReadFile(input.ReadyPath); readErr == nil {
			if err = json.Unmarshal(raw, &ready); err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ready.CorePID != child.Process.Pid || ready.Run.ID == "" || ready.RuntimeEpoch == "" || ready.Run.AgentRuntime == nil {
		raw, _ := os.ReadFile(filepath.Join(root, "child.log"))
		t.Fatalf("no exact child active Run: %+v\n%s", ready, raw)
	}
	select {
	case <-providerStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("native process did not start the held original response")
	}
	if ready.Binding.CodexVersion != "0.159.2" || ready.Binding.ExecutablePath != native || ready.Binding.PID == ready.CorePID || len(ready.Run.AgentRuntime.Requests) != 0 {
		t.Fatalf("unexpected native active owner: %+v", ready)
	}
	if err = appserver.Verify(f.ctx, ready.Binding); err != nil {
		t.Fatal(err)
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	childErr := <-childDone
	waited = true
	status, ok := child.ProcessState.Sys().(syscall.WaitStatus)
	if childErr == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("Core child did not exit by SIGKILL: status=%v err=%v", status, childErr)
	}
	if err = appserver.Verify(f.ctx, ready.Binding); err != nil {
		t.Fatalf("Core SIGKILL killed or replaced detached native instance: %v", err)
	}
	f.plane, err = controlplane.Open(f.ctx, continuityCrashPlaneConfig(input))
	if err != nil {
		t.Fatal(err)
	}
	f.plane.Retirement.Close()
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	recovered := waitQualifiedContinuityTurn(t, f, ready.Run.ID)
	if f.plane.DB.RuntimeEpoch() == ready.RuntimeEpoch || recovered.RuntimeEpoch != f.plane.DB.RuntimeEpoch() || recovered.ThreadID != ready.Run.ThreadID || recovered.TurnID != ready.Run.TurnID || recovered.AgentExecutionID != ready.Run.AgentExecutionID || recovered.ExecutionProposalID != ready.Run.ExecutionProposalID || recovered.AgentRuntime.ConnectionID == ready.Run.AgentRuntime.ConnectionID || len(recovered.AgentRuntime.Requests) != 0 {
		t.Fatalf("crash recovery changed execution or retained dead Core connection: old=%+v new=%+v", ready, recovered)
	}
	observation := recovered.AgentRuntime.Observation
	if observation == nil || observation.InstanceStatus != "ALIVE" || observation.ThreadStatus != "active" || observation.ConnectionState != "CONNECTED" || observation.ThreadID != ready.Run.ThreadID || observation.TurnID != ready.Run.TurnID || recovered.AgentRuntime.TurnStatus != "inProgress" {
		t.Fatalf("recovery did not observe exact original active turn: %+v", observation)
	}
	currentAuthority, err := sqlite.NewRuntimeSelectionStore(f.plane.DB).ReadCurrent(f.ctx, authority.Owner)
	if err != nil || !reflect.DeepEqual(currentAuthority, authority) {
		t.Fatalf("crash recovery replaced exact qualification: %+v %v", currentAuthority, err)
	}
	if recoveredReceipts := readContinuityCrashStartReceipts(t, f.plane.DB.SQL(), ready.Run.ID); !reflect.DeepEqual(recoveredReceipts, ready.StartReceipts) {
		t.Fatalf("crash recovery rewrote immutable original start authority: old=%v new=%v", ready.StartReceipts, recoveredReceipts)
	}
	assertQualifiedContinuityIdentity(t, f, ready.Run, ready.Binding, 1)
	writeContinuityCrashJSON(t, filepath.Join(root, "recovered-active-run.json"), recovered)
	if calls.Load() != 1 {
		t.Fatalf("crash recovery restarted native model call: %d", calls.Load())
	}
	release()
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		current, e := f.plane.Runs.Get(f.ctx, ready.Run.ID)
		if e != nil {
			t.Fatal(e)
		}
		if current.AgentRuntime != nil && current.AgentRuntime.TurnStatus == "completed" {
			if current.ThreadID != ready.Run.ThreadID || current.TurnID != ready.Run.TurnID || current.AgentExecutionID != ready.Run.AgentExecutionID || calls.Load() != 1 {
				t.Fatalf("completion replaced original active turn: %+v calls=%d", current, calls.Load())
			}
			assertQualifiedContinuityIdentity(t, f, ready.Run, ready.Binding, 1)
			if completedReceipts := readContinuityCrashStartReceipts(t, f.plane.DB.SQL(), ready.Run.ID); !reflect.DeepEqual(completedReceipts, ready.StartReceipts) {
				t.Fatalf("completion rewrote immutable original start authority: old=%v new=%v", ready.StartReceipts, completedReceipts)
			}
			var bindingCount int
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_selection_bindings WHERE task_id=?`, f.task).Scan(&bindingCount); err != nil || bindingCount != 1 {
				t.Fatalf("qualification binding duplication: count=%d err=%v", bindingCount, err)
			}
			writeQualifiedContinuityEvidence(t, root, ready.Binding, current)
			writeContinuityCrashJSON(t, filepath.Join(root, "result.json"), map[string]any{"result": "PASS", "coreExitSignal": "SIGKILL", "childCorePID": ready.CorePID, "nativePID": ready.Binding.PID, "originalRuntimeEpoch": ready.RuntimeEpoch, "recoveredRuntimeEpoch": f.plane.DB.RuntimeEpoch(), "sameAppServerBinding": true, "sameRun": true, "sameThread": true, "sameTurn": true, "sameQualificationBinding": true, "immutableStartReceiptsUnchanged": true, "providerRequestCount": calls.Load()})
			t.Logf("SIGKILL Core PID=%d; original native PID=%d Run=%s Thread=%s Turn=%s completed, providerCalls=%d", ready.CorePID, ready.Binding.PID, current.ID, current.ThreadID, current.TurnID, calls.Load())
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	current, _ := f.plane.Runs.Get(f.ctx, ready.Run.ID)
	t.Fatalf("original turn did not complete after Core SIGKILL: run=%+v runtime=%+v calls=%d", current, current.AgentRuntime, calls.Load())
}

func writeContinuityCrashJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		err = os.WriteFile(path, raw, 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func readContinuityCrashStartReceipts(t *testing.T, db *sql.DB, runID string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"runtime_start_authorities", "runtime_workset_bindings"} {
		rows, err := db.Query(`SELECT * FROM `+table+` WHERE run_id=?`, runID)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var records [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err = rows.Scan(destinations...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			records = append(records, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || len(records) != 1 {
			t.Fatalf("exact original %s receipt: rows=%d err=%v", table, len(records), err)
		}
		raw, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		result[table] = string(raw)
	}
	return result
}

func stopCrashNativeBindings(databasePath string) {
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: databasePath, RawQuery: "mode=ro"}).String())
	if err != nil {
		return
	}
	defer db.Close()
	rows, err := db.Query(`SELECT binding_json FROM app_server_run_bindings`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var binding appserver.Binding
		if rows.Scan(&raw) == nil && json.Unmarshal(raw, &binding) == nil {
			_ = appserver.Stop(context.Background(), binding)
		}
	}
}
