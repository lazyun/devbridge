//go:build !windows

package controlapi_test

// Kill the separate Core-owning process after its real choice transaction
// commits, while its final confirmation journal writer is blocked. The parent
// owns all fixture directories and keeps the original pre-dispatch identities.

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codex"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/observability"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	driversqlite "modernc.org/sqlite"
)

const crashBarrierFunction = "devbridge_test_only_process_crash_barrier"
const crashBarrierTrigger = "devbridge_test_only_process_crash"

type processCrashPlan struct {
	DataRoot, Evidence, Prefix, TriggerSQL string
	Request                                c.CommitInput
}
type processCrashEvent struct {
	Kind       string
	PID        int
	At         time.Time
	Epoch, URL string
	Entries    int32
	Witness    []string
	Error      string
}
type processCrashService struct {
	*mcpserver.StaticService
	expected c.CommitInput
	entries  atomic.Int32
	emit     func(processCrashEvent) error
}

func (s *processCrashService) CommitConfirmation(ctx context.Context, in c.CommitInput) (c.Intent, error) {
	entries := s.entries.Add(1)
	if err := s.emit(processCrashEvent{Kind: "service_entry", Entries: entries}); err != nil {
		return c.Intent{}, err
	}
	if entries != 1 || c.ValueHash(in) != c.ValueHash(s.expected) {
		return c.Intent{}, errors.New("process-crash fixture refused unexpected or repeated commit")
	}
	out, err := s.StaticService.CommitConfirmation(ctx, in)
	_ = s.emit(processCrashEvent{Kind: "service_return", Entries: entries})
	return out, err
}

func TestSimulatedContinuationProcessCrash(t *testing.T) {
	runSimulatedContinuationGate(t, (*continuationGate).exerciseProcessCrash)
}

// Only the driver launches this exact helper. No production binary, existing
// service, real Agent executable, model or credentials are used; no business row
// is fabricated. Existing canonical business rows are the subject of the test.
func TestSimulatedContinuationProcessCrashHelper(t *testing.T) {
	if os.Getenv("DEVBRIDGE_CONTINUATION_CRASH_HELPER") != "1" {
		t.Skip("owned subprocess helper only")
	}
	var plan processCrashPlan
	raw, err := os.ReadFile(os.Getenv("DEVBRIDGE_CONTINUATION_CRASH_PLAN"))
	if err == nil {
		err = json.Unmarshal(raw, &plan)
	}
	if err != nil {
		t.Fatal(err)
	}
	pipe := os.NewFile(3, "crash-events")
	if pipe == nil {
		t.Fatal("missing private parent event pipe")
	}
	defer pipe.Close()
	log, err := os.OpenFile(filepath.Join(plan.Evidence, plan.Prefix+"-child-events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	var emitMu sync.Mutex
	emit := func(e processCrashEvent) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		e.PID = os.Getpid()
		e.At = time.Now().UTC()
		if err := json.NewEncoder(log).Encode(e); err != nil {
			return err
		}
		// Flush the witness before notifying the parent. No DB operation or test
		// assertion is made from the scalar callback.
		if err := log.Sync(); err != nil {
			return err
		}
		return json.NewEncoder(pipe).Encode(e)
	}
	defer func() { _ = emit(processCrashEvent{Kind: "graceful_helper_exit"}) }()
	logger := observability.New(observability.Options{Process: "test-only-crash-core", Directory: plan.Evidence, Filename: plan.Prefix + "-core.jsonl"})
	observability.SetDefault(logger)
	defer logger.Close()
	var service *processCrashService
	var witnesses atomic.Int32
	err = driversqlite.RegisterScalarFunction(crashBarrierFunction, 7, func(_ *driversqlite.FunctionContext, values []driver.Value) (driver.Value, error) {
		args := make([]string, len(values))
		for i, v := range values {
			var ok bool
			args[i], ok = v.(string)
			if !ok {
				return nil, errors.New("crash witness requires text")
			}
		}
		if witnesses.Add(1) != 1 || service == nil || service.entries.Load() != 1 {
			return nil, errors.New("unexpected crash barrier entry")
		}
		if err := emit(processCrashEvent{Kind: "writer_paused", Entries: service.entries.Load(), Witness: args}); err != nil {
			return nil, err
		}
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		<-timer.C
		_ = emit(processCrashEvent{Kind: "writer_timeout"})
		return nil, errors.New("TEST_ONLY_CRASH_BARRIER_TIMEOUT: parent did not kill owned Core")
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(aisession.WithCallerScope(context.Background(), "data-instance-local"), 90*time.Second)
	defer cancel()
	// Fail closed before Open: even accidental runtime admission cannot use a
	// PATH/default native binary. The persisted fixture configs have no override.
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: plan.DataRoot, Codex: codex.Config{CodexPath: filepath.Join(os.Getenv("HOME"), "native-agent-disabled-does-not-exist")}})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	configs, err := plane.CodexConfigs.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range configs {
		if config.CodexPath != "" {
			t.Fatal("unexpected persisted executable override")
		}
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	base, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test-only-process-crash", Ready: true}, plane.AIContext, facade)
	if err != nil {
		t.Fatal(err)
	}
	service = &processCrashService{StaticService: base, expected: plan.Request, emit: emit}
	server, err := mcpserver.New(mcpserver.Config{RequestTimeout: continuationRequestTimeout}, service)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plane.DB.SQL().ExecContext(ctx, plan.TriggerSQL); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	if err = emit(processCrashEvent{Kind: "ready", Epoch: plane.DB.RuntimeEpoch(), URL: host.URL + "/mcp"}); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	t.Fatal("owned crash Core exceeded its lifetime without expected SIGKILL")
}

// Observation and duplicate protection only; do not replace response bytes,
// cancel the request, or turn process death into a fabricated business result.
type processCrashTransport struct {
	base                http.RoundTripper
	expected            c.CommitInput
	mu                  sync.Mutex
	attempts, forwarded int
}

func (p *processCrashTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.Body == nil {
		return p.base.RoundTrip(req)
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, (1<<20)+1))
	_ = req.Body.Close()
	if err != nil || len(body) > 1<<20 {
		return nil, errors.New("process-crash request capture failed")
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	var rpc struct {
		Method string `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err = json.Unmarshal(body, &rpc); err != nil {
		return nil, err
	}
	if rpc.Method == "tools/call" {
		p.mu.Lock()
		p.attempts++
		hash, err := c.InputHash(rpc.Params.Arguments)
		exact := rpc.Params.Name == "confirmation_commit" && err == nil && hash == c.ValueHash(p.expected) && p.forwarded == 0
		if exact {
			p.forwarded++
		}
		p.mu.Unlock()
		if !exact {
			return nil, errors.New("process-crash transport refused unexpected or repeated business request")
		}
	}
	return p.base.RoundTrip(req)
}

func (s *continuationGate) crashChoice(pending c.Intent, in c.CommitInput, ordinal int) journalRecoverySnapshot {
	s.t.Helper()
	prefix := fmt.Sprintf("process-crash-%d-%s", ordinal, in.Action)
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	ddl := fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF intent_json ON confirmation_intents
 WHEN NEW.intent_id=%s AND NEW.ai_session_id=%s
 AND json_extract(OLD.intent_json,'$.state')='CONFIRMING' AND json_extract(OLD.intent_json,'$.steps[0].state')='UNKNOWN'
 AND json_extract(NEW.intent_json,'$.state')='COMPLETED' AND json_array_length(json_extract(NEW.intent_json,'$.steps'))=1
 AND json_extract(NEW.intent_json,'$.steps[0].state')='COMPLETED' AND json_extract(NEW.intent_json,'$.steps[0].effect')='COMMAND_COMPLETED'
 AND json_extract(NEW.intent_json,'$.steps[0].kind')='RECORD_TASK_CHOICE' AND json_extract(NEW.intent_json,'$.action')=%s
 AND json_extract(OLD.intent_json,'$.commit.requestId')=%s AND json_extract(NEW.intent_json,'$.commit.requestId')=%s
 AND json_extract(OLD.intent_json,'$.steps[0].requestId')=json_extract(NEW.intent_json,'$.steps[0].requestId')
 AND EXISTS(SELECT 1 FROM task_hold_receipts WHERE actor=%s AND request_id=json_extract(NEW.intent_json,'$.steps[0].requestId') AND task_id=%s AND execution_id=%s AND revision_id=%s AND action=%s)
 BEGIN SELECT %s(NEW.intent_id,NEW.ai_session_id,json_extract(NEW.intent_json,'$.commit.requestId'),json_extract(NEW.intent_json,'$.steps[0].requestId'),json_extract(NEW.intent_json,'$.action'),OLD.intent_json,NEW.intent_json); END`, crashBarrierTrigger, quote(in.IntentID), quote(in.AISessionID), quote(in.Action), quote(in.RequestID), quote(in.RequestID), quote("session:"+in.AISessionID), quote(s.f.task), quote(s.executionID), quote(s.revision), quote(in.Action), crashBarrierFunction)
	plan := processCrashPlan{DataRoot: s.f.dataRoot, Evidence: s.evidence, Prefix: prefix, TriggerSQL: ddl, Request: in}
	s.save(prefix+"-plan.json", plan)
	// Capture Agent protocol logs before the handoff: continuation must never
	// create another Agent request or response in any of the three Core epochs.
	agentLogs := map[string][]byte{}
	for _, name := range []string{"agent-wire.jsonl", "agent-responses.jsonl"} {
		b, err := os.ReadFile(filepath.Join(s.evidence, name))
		if err != nil {
			s.t.Fatal(err)
		}
		agentLogs[name] = b
	}
	oldEpoch := s.f.plane.DB.RuntimeEpoch()
	s.closeWire()
	s.closeWire = nil
	if err := s.f.plane.Close(); err != nil {
		s.t.Fatal(err)
	}
	s.f.plane = nil
	binary, err := os.Executable()
	if err != nil {
		s.t.Fatal(err)
	}
	home := s.t.TempDir()
	reader, writer, err := os.Pipe()
	if err != nil {
		s.t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	output, err := os.OpenFile(filepath.Join(s.evidence, prefix+"-child.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		s.t.Fatal(err)
	}
	defer output.Close()
	cmd := exec.Command(binary, "-test.run=^TestSimulatedContinuationProcessCrashHelper$", "-test.v", "-test.timeout=2m")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home, "TMPDIR=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "OTEL_SDK_DISABLED=true", "DEVBRIDGE_CONTINUATION_CRASH_HELPER=1", "DEVBRIDGE_CONTINUATION_CRASH_PLAN=" + filepath.Join(s.evidence, prefix+"-plan.json")}
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.ExtraFiles = []*os.File{writer}
	if err = cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	_ = writer.Close()
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()
	events := make(chan processCrashEvent, 16)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(events)
		decoder := json.NewDecoder(reader)
		for {
			var event processCrashEvent
			err := decoder.Decode(&event)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					events <- processCrashEvent{Kind: "pipe_error", Error: err.Error()}
				}
				return
			}
			events <- event
		}
	}()
	var trace []processCrashEvent
	// Drain while the decoder is joining, so even an unexpected flood cannot
	// block it on a full channel. A failed join never falls into an unbounded range.
	drainEvents := func() bool {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return true
				}
				trace = append(trace, event)
			case <-timer.C:
				return false
			}
		}
	}
	var waitErr error
	joined := false
	var client *sdkmcp.ClientSession
	var call *arbitrationCall
	results := make(chan arbitrationCall, 1)
	callStarted := false
	callCtx, callCancel := context.WithCancel(s.ctx)
	// On every failure kill/join the owned process before canceling or joining the
	// caller and before Core/temp cleanup. Success never cancels a live request.
	defer func() {
		if !joined {
			_ = cmd.Process.Kill()
			select {
			case waitErr = <-processDone:
				joined = true
			case <-time.After(10 * time.Second):
				s.t.Error("crash Core cleanup join timeout")
			}
		}
		callCancel()
		if callStarted && call == nil {
			select {
			case r := <-results:
				call = &r
			case <-time.After(10 * time.Second):
				s.t.Error("crash client cleanup join timeout")
			}
		}
		if client != nil {
			_ = client.Close()
		}
		_ = reader.Close()
		if !drainEvents() {
			s.t.Error("crash event drain cleanup timeout")
		}
		select {
		case <-readerDone:
		case <-time.After(2 * time.Second):
			s.t.Error("crash event reader cleanup join timeout")
		}
		s.save(prefix+"-trace.json", map[string]any{"events": trace, "processJoined": joined, "clientJoined": call != nil, "pid": cmd.Process.Pid})
		if call != nil {
			entry := map[string]any{"actor": "fixture_user", "tool": "confirmation_commit", "input": in, "startedAt": call.StartedAt, "finishedAt": call.FinishedAt, "result": call.Result, "testOnlyFault": "owned Core SIGKILL after canonical commit, before final journal persistence"}
			if call.Err != nil {
				entry["transportError"] = call.Err.Error()
			}
			if err := json.NewEncoder(s.transcript).Encode(entry); err != nil {
				s.t.Error(err)
			}
		}
	}()
	await := func(kind string, budget time.Duration) processCrashEvent {
		timer := time.NewTimer(budget)
		defer timer.Stop()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					s.t.Fatalf("crash helper pipe closed before %s", kind)
				}
				trace = append(trace, event)
				if event.PID != cmd.Process.Pid {
					s.t.Fatalf("crash helper PID mismatch: %+v", event)
				}
				if event.Kind == kind {
					return event
				}
				if event.Kind != "service_entry" {
					s.t.Fatalf("unexpected crash event before %s: %+v", kind, event)
				}
			case <-timer.C:
				s.t.Fatalf("crash helper rendezvous deadline: %s", kind)
			case <-s.ctx.Done():
				s.t.Fatal(s.ctx.Err())
			}
		}
	}
	ready := await("ready", 45*time.Second)
	s.check(ready.Epoch != "" && ready.Epoch != oldEpoch, "separate Core process owns same durable data in a distinct epoch", ready)
	transport := &processCrashTransport{base: &http.Transport{}, expected: in}
	defer transport.base.(*http.Transport).CloseIdleConnections()
	client, err = sdkmcp.NewClient(&sdkmcp.Implementation{Name: "fixture-user-process-crash", Version: "test-only"}, nil).Connect(callCtx, &sdkmcp.StreamableClientTransport{Endpoint: ready.URL, HTTPClient: &http.Client{Transport: transport}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	callStarted = true
	go func() {
		at := time.Now().UTC()
		result, err := client.CallTool(callCtx, &sdkmcp.CallToolParams{Name: "confirmation_commit", Arguments: in})
		results <- arbitrationCall{"confirmation_commit", at, time.Now().UTC(), result, err}
	}()
	paused := await("writer_paused", 10*time.Second)
	s.check(paused.Entries == 1 && len(paused.Witness) == 7 && reflect.DeepEqual(paused.Witness[:3], []string{in.IntentID, in.AISessionID, in.RequestID}) && paused.Witness[4] == in.Action, "owned Core paused at exact original final journal writer", paused)
	// The parent neither cancels the client nor asks Core to shut down. This is
	// the actual fault: SIGKILL bypasses the writer's return and all Core cleanup.
	select {
	case r := <-results:
		call = &r
		s.t.Fatal("caller returned before the witnessed process crash")
	default:
		s.check(true, "original caller remains pending before SIGKILL", nil)
	}
	killedAt := time.Now().UTC()
	if err = cmd.Process.Kill(); err != nil {
		s.t.Fatal(err)
	}
	select {
	case waitErr = <-processDone:
		joined = true
	case <-time.After(10 * time.Second):
		s.t.Fatal("SIGKILLed Core failed to join")
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	s.check(waitErr != nil && ok && status.Signaled() && status.Signal() == syscall.SIGKILL, "actual owned Core process terminated by SIGKILL, not cancel or graceful close", cmd.ProcessState.String())
	select {
	case r := <-results:
		call = &r
	case <-time.After(10 * time.Second):
		s.t.Fatal("client did not observe Core process death")
	}
	s.check(call.Result == nil && call.Err != nil && callCtx.Err() == nil && call.FinishedAt.After(killedAt), "live caller observes process-loss error with no usable result, not caller cancellation", call)
	_ = client.Close()
	client = nil
	transport.mu.Lock()
	attempts, forwarded := transport.attempts, transport.forwarded
	transport.mu.Unlock()
	s.check(attempts == 1 && forwarded == 1, "exact original commit forwarded once with no retry after process loss", []int{attempts, forwarded})
	s.check(drainEvents(), "crash event stream closed within bounded drain", nil)
	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
		s.t.Fatal("crash event reader did not join")
	}
	kinds := []string{}
	for _, event := range trace {
		kinds = append(kinds, event.Kind)
	}
	s.check(reflect.DeepEqual(kinds, []string{"ready", "service_entry", "writer_paused"}), "witnessed process death has no service return, barrier timeout or graceful helper cleanup", trace)
	s.save(prefix+"-death.json", map[string]any{"pid": cmd.Process.Pid, "signal": status.Signal().String(), "signaled": status.Signaled(), "killedAt": killedAt, "joinedAt": time.Now().UTC(), "attempts": attempts, "forwarded": forwarded, "bootstrapHandoff": "parent wire and Core gracefully closed before child Open; only child death is the tested crash", "epochs": []string{oldEpoch, ready.Epoch}})
	// First observation of persisted state occurs BEFORE Core startup/recovery.
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(s.f.dataRoot, "state", "devbridge.db"), RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	before := s.journalRecoverySnapshotFromDB(db, in, ordinal)
	var triggerCount int
	if err = db.QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, crashBarrierTrigger).Scan(&triggerCount); err != nil {
		s.t.Fatal(err)
	}
	s.check(triggerCount == 1 && before.IntentJSON == paused.Witness[5] && before.ReceiptRequest == paused.Witness[3], "pre-Open raw WAL preserves unfinished journal and exact canonical receipt; barrier persisted through crash", before)
	var attempted c.Intent
	gateDecode(s.t, json.RawMessage(paused.Witness[6]), &attempted)
	s.check(attempted.State == "COMPLETED" && len(attempted.Steps) == 1 && attempted.Steps[0].State == "COMPLETED" && attempted.Steps[0].RequestID == before.ReceiptRequest, "attempted final journal differs from durable pre-Open UNKNOWN", attempted)
	s.save(prefix+"-before-open.json", before)
	if err = db.Close(); err != nil {
		s.t.Fatal(err)
	}
	s.f.plane, err = controlplane.Open(s.f.ctx, controlplane.Config{DataRoot: s.f.dataRoot, Codex: codex.Config{CodexPath: filepath.Join(home, "native-agent-disabled-does-not-exist")}})
	if err != nil {
		s.t.Fatal(err)
	}
	reopenedEpoch := s.f.plane.DB.RuntimeEpoch()
	s.check(reopenedEpoch != "" && reopenedEpoch != ready.Epoch && reopenedEpoch != oldEpoch, "post-SIGKILL Core obtains new writer lock and distinct epoch", reopenedEpoch)
	s.check(reflect.DeepEqual(s.journalRecoverySnapshot(in, ordinal), before), "Core startup leaves original crashed journal, canonical receipt, hold and event unchanged", nil)
	if _, err = s.f.plane.DB.SQL().ExecContext(s.ctx, "DROP TRIGGER "+crashBarrierTrigger); err != nil {
		s.t.Fatal(err)
	}
	if err = s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, crashBarrierTrigger).Scan(&triggerCount); err != nil {
		s.t.Fatal(err)
	}
	s.check(triggerCount == 0, "only scoped test barrier removed; no business row repaired", nil)
	s.install()
	s.connect()
	// Recovery identities are the parent's pre-dispatch originals, never an
	// identity selected from the oracle-only callback or the newest intent.
	got := s.confirmationRead(pending, in.RequestID)
	s.checkJournalRecoveredChoice(got, pending, in, before)
	s.checkCheckpoint(in.Action == "DEFER_TASK", in.Action == "CONTINUE_TASK")
	after := s.journalRecoverySnapshot(in, ordinal)
	s.check(reflect.DeepEqual(after, before), "post-crash receipt/options/workspace recovery is read-only with no redispatch", after)
	for name, want := range agentLogs {
		got, err := os.ReadFile(filepath.Join(s.evidence, name))
		s.check(err == nil && bytes.Equal(got, want), "crash/recovery emits no additional Agent protocol activity: "+name, err)
	}
	s.save(prefix+"-after-read.json", after)
	s.save(prefix+"-epochs.json", []string{oldEpoch, ready.Epoch, reopenedEpoch})
	return before
}

func (s *continuationGate) exerciseProcessCrash() {
	view := s.checkCheckpoint(false, false)
	pending := s.prepare(s.choice("DEFER_TASK", "crash-prepare-defer", "Retain exact verified fixture evidence; not acceptance or permission to merge.", int(view.ActionNode.Generation)))
	in := s.commitInput(pending, "crash-commit-defer-once")
	s.crashChoice(pending, in, 1)
	deferred := s.confirmationRead(pending, in.RequestID)
	view = s.checkCheckpoint(true, false)
	next := s.prepare(s.choice("CONTINUE_TASK", "crash-prepare-continue", "", int(view.ActionNode.Generation)))
	continueInput := s.commitInput(next, "crash-commit-continue-once")
	before := s.crashChoice(next, continueInput, 2)
	historical := s.confirmationRead(pending, in.RequestID)
	s.check(reflect.DeepEqual(historical.Commit, deferred.Commit) && reflect.DeepEqual(historical.Steps, deferred.Steps) && reflect.DeepEqual(historical.Submission, deferred.Submission), "old DEFER receipt remains historical after actual CONTINUE crash; no hold recreation", historical)
	s.checkCheckpoint(false, true)
	s.check(reflect.DeepEqual(s.journalRecoverySnapshot(continueInput, 2), before), "historical reads after both crashes preserve original durable UNKNOWN and exact two choices", nil)
	s.save("final-workspace.json", s.workspace())
}
