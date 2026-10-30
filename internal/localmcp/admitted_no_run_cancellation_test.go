package localmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

type admissionCancelBackend struct{ starts atomic.Int32 }

func (b *admissionCancelBackend) Start(context.Context, codexconfig.Config) (execution.CodexSession, error) {
	b.starts.Add(1)
	return nil, errors.New("unexpected backend launch during no-Run cancellation")
}

func admissionCancelInput(f *integrationFixture, sid string, p executionproposal.Proposal) executioncancel.Input {
	return executioncancel.Input{AISessionID: sid, ProjectID: f.projectID, ExecutionID: p.ID, OwnerType: "EXECUTION", OwnerID: p.ID, RequestID: "admitted-cancel", Reason: "cancel exact admitted owner"}
}
func admissionCancelCounts(t *testing.T, f *integrationFixture) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"runs", "runtime_scope_plans", "runtime_preparation_authorities", "runtime_preparation_attempts", "runtime_workspace_observations", "runtime_workset_bindings", "runtime_start_authorities", "execution_proposals"} {
		var n int
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	var worksets int
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM runs WHERE runtime_workset_json IS NOT NULL").Scan(&worksets); err != nil {
		t.Fatal(err)
	}
	out["runRuntimeWorksets"] = worksets
	return out
}
func admissionRawTask(t *testing.T, f *integrationFixture, id string) []string {
	t.Helper()
	v := make([]string, 4)
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT status,phase,COALESCE(current_execution_id,''),COALESCE(current_definition_revision_id,'') FROM tasks WHERE id=?", id).Scan(&v[0], &v[1], &v[2], &v[3]); err != nil {
		t.Fatal(err)
	}
	return v
}
func assertAdmissionCancelReceipt(t *testing.T, p executionproposal.Proposal, r executioncancel.Receipt) {
	t.Helper()
	if r.OwnerType != "EXECUTION" || r.OwnerID != p.ID || r.TaskID != p.TaskID || r.RunID != "" || r.State != "CANCELED" || r.BackendDisposition != "NO_BACKEND" || r.BackendInterruptState != "NOT_SENT" || r.QuiescenceState != "NO_BACKEND" || r.RecoveryRequired || r.ResolvedAt == nil {
		t.Fatalf("ADMITTED no-Run receipt: %+v", r)
	}
}
func assertAdmissionCancelDurable(t *testing.T, f *integrationFixture, p executionproposal.Proposal, counts map[string]int, taskBefore []string, receiptCount int) {
	t.Helper()
	raw, err := f.plane.ExecutionProposals.GetForProject(f.ctx, f.projectID, p.ID)
	if err != nil || raw.Status != executionproposal.StatusAdmitted || raw.Outcome != executionproposal.OutcomeCanceled || raw.RunID != "" {
		t.Fatalf("raw owner: %+v %v", raw, err)
	}
	current := admissionRawTask(t, f, p.TaskID)
	if taskBefore[0] != "ACTIVE" || current[0] != "ACTIVE" || current[1] != "NO_RUN_SUCCESSOR_REQUIRED" || current[2] != p.ID || current[2] != taskBefore[2] || current[3] != taskBefore[3] || !reflect.DeepEqual(counts, admissionCancelCounts(t, f)) {
		t.Fatalf("public cancellation projection or authority changed unexpectedly: task before=%v after=%v counts before=%v after=%v", taskBefore, current, counts, admissionCancelCounts(t, f))
	}
	for name, query := range map[string]string{
		"predecessor Run":                 `SELECT count(*) FROM runs WHERE execution_proposal_id=?`,
		"predecessor preparation attempt": `SELECT count(*) FROM runtime_preparation_attempts a JOIN runtime_scope_plans p ON p.plan_id=a.plan_id WHERE p.owner_id=?`,
		"predecessor workset binding":     `SELECT count(*) FROM runtime_workset_bindings b JOIN runtime_scope_plans p ON p.plan_id=b.plan_id WHERE p.owner_id=?`,
		"predecessor start authority":     `SELECT count(*) FROM runtime_start_authorities a JOIN runtime_scope_plans p ON p.plan_id=a.plan_id WHERE p.owner_id=?`,
		"successor":                       `SELECT count(*) FROM no_run_successor_effect_authorities WHERE predecessor_execution_id=?`,
	} {
		var n int
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, query, p.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s count=%d err=%v", name, n, err)
		}
	}
	var n int
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM execution_cancellations WHERE owner_type='EXECUTION' AND owner_id=?", p.ID).Scan(&n); err != nil || n != receiptCount {
		t.Fatalf("receipt count=%d want=%d err=%v", n, receiptCount, err)
	}
	logScalarFixtureFacts(t, f, p.ID, run.Run{})
}

func TestAdmittedNoRunCancellationIngress(t *testing.T) {
	for _, transport := range []string{"HTTP", "MCP"} {
		t.Run(transport, func(t *testing.T) {
			f, sid, p := localMCPScalarAdmission(t)
			if p.Status != executionproposal.StatusAdmitted || p.RunID != "" {
				t.Fatal("not modern admission", p)
			}
			assertScalarLayer(t, f, p.ID, 0, 0)
			counts, taskBefore := admissionCancelCounts(t, f), admissionRawTask(t, f, p.TaskID)
			backend := &admissionCancelBackend{}
			f.plane.Coordinator.Codex = backend
			var first, replay executioncancel.Receipt
			if transport == "HTTP" {
				web, err := bridgehttp.New(f.plane, false)
				if err != nil {
					t.Fatal(err)
				}
				first = cancelHTTP(t, web.Handler(), f.projectID, p.ID, "", "admitted-http")
				replay = cancelHTTP(t, web.Handler(), f.projectID, p.ID, "", "admitted-http")
			} else {
				args := map[string]any{"aiSessionId": sid, "executionId": p.ID, "requestId": "admitted-mcp", "reason": "cancel exact admitted owner"}
				a := callToolMap(t, f.ctx, connectGPT(t, f.gptURL), "operation_cancel", args)
				b := callToolMap(t, f.ctx, connectLocalMCP(t, f.libreURL), "operation_cancel", args)
				raw, _ := json.Marshal(a)
				if err := json.Unmarshal(raw, &first); err != nil {
					t.Fatal(err)
				}
				raw, _ = json.Marshal(b)
				if err := json.Unmarshal(raw, &replay); err != nil {
					t.Fatal(err)
				}
				args["reason"] = "changed payload"
				if got := callToolErrorText(t, f.ctx, connectLocalMCP(t, f.libreURL), "operation_cancel", args); !strings.Contains(got, "REQUEST_CONFLICT") {
					t.Fatal(got)
				}
			}
			assertAdmissionCancelReceipt(t, p, first)
			if !reflect.DeepEqual(first, replay) {
				t.Fatalf("lost-response replay differs: %+v %+v", first, replay)
			}
			assertAdmissionCancelDurable(t, f, p, counts, taskBefore, 1)
			assertDevelopmentProjectionClients(t, f, sid, p.ID, "CANCELED", "NO_BACKEND", "CANCELED", false)
			if backend.starts.Load() != 0 {
				t.Fatal("backend started")
			}
			if _, _, err := f.plane.ExecutionProposals.PrepareAdmittedExecution(f.ctx, sid, p); err == nil {
				t.Fatal("canceled admission prepared a Run")
			}
			assertAdmissionCancelDurable(t, f, p, counts, taskBefore, 1)
		})
	}
}

func TestAdmittedNoRunCancellationConcurrentReplay(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			f, sid, p := localMCPScalarAdmission(t)
			counts, taskBefore := admissionCancelCounts(t, f), admissionRawTask(t, f, p.TaskID)
			in := admissionCancelInput(f, sid, p)
			type result struct {
				receipt executioncancel.Receipt
				err     error
			}
			start := make(chan struct{})
			results := make(chan result, 2)
			for i := 0; i < 2; i++ {
				input := in
				if changed && i == 1 {
					input.Reason = "competing payload"
				}
				go func() { <-start; r, e := f.control.CancelExecutionForSession(f.ctx, input); results <- result{r, e} }()
			}
			close(start)
			a, b := <-results, <-results
			if changed {
				if (a.err == nil) == (b.err == nil) {
					t.Fatalf("want one winner one conflict: %v %v", a.err, b.err)
				}
				failure := a.err
				if failure == nil {
					failure = b.err
				}
				if !strings.Contains(failure.Error(), "REQUEST_CONFLICT") {
					t.Fatal(failure)
				}
			} else {
				if a.err != nil || b.err != nil || !reflect.DeepEqual(a.receipt, b.receipt) {
					t.Fatalf("same request: %+v %+v", a, b)
				}
				assertAdmissionCancelReceipt(t, p, a.receipt)
			}
			assertAdmissionCancelDurable(t, f, p, counts, taskBefore, 1)
		})
	}
}

func TestAdmittedNoRunCancellationPreparationRace(t *testing.T) {
	for _, order := range []string{"cancel-first", "prepare-first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			f, sid, p := localMCPScalarAdmission(t)
			in := admissionCancelInput(f, sid, p)
			var receipt executioncancel.Receipt
			var prepared run.Run
			var cancelErr, prepareErr error
			cancel := func() { receipt, cancelErr = f.control.CancelExecutionForSession(f.ctx, in) }
			prepare := func() { _, prepared, prepareErr = f.plane.ExecutionProposals.PrepareAdmittedExecution(f.ctx, sid, p) }
			switch order {
			case "cancel-first":
				cancel()
				prepare()
			case "prepare-first":
				prepare()
				cancel()
			default:
				start := make(chan struct{})
				done := make(chan struct{}, 2)
				go func() { <-start; cancel(); done <- struct{}{} }()
				go func() { <-start; prepare(); done <- struct{}{} }()
				close(start)
				<-done
				<-done
			}
			if cancelErr == nil {
				assertAdmissionCancelReceipt(t, p, receipt)
				if prepareErr == nil || prepared.ID != "" {
					t.Fatalf("both authorities won: %+v %v", prepared, prepareErr)
				}
				assertScalarLayer(t, f, p.ID, 0, 0)
			} else {
				if prepareErr != nil || prepared.ID == "" || !strings.Contains(cancelErr.Error(), "RUN_NOT_ACTIVE") {
					t.Fatalf("prepare=%+v/%v cancel=%v", prepared, prepareErr, cancelErr)
				}
				var n int
				if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM execution_cancellations WHERE owner_id=?", p.ID).Scan(&n); err != nil || n != 0 {
					t.Fatalf("losing cancel wrote receipt %d %v", n, err)
				}
			}
			t.Logf("order=%s cancel=%v preparation=%v run=%s", order, cancelErr, prepareErr, prepared.ID)
		})
	}
}

func TestAdmittedNoRunCancellationRestart(t *testing.T) {
	stableNoRunRestart(t)
}

func TestAdmittedCancellationRuntimeWriterFence(t *testing.T) {
	retired, err := sqlite.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stale := retired.RuntimeEpoch()
	if err = retired.Close(); err != nil {
		t.Fatal(err)
	}
	f, sid, p := localMCPScalarAdmission(t)
	p, r := prepareScalarExecutionForLocalMCP(t, f, sid, p)
	in := admissionCancelInput(f, sid, p)
	in.RunID = r.ID
	before := scalarFixtureFacts(t, f, p.ID, r)
	_, _, err = sqlite.NewExecutionCancellationStore(f.plane.DB).Begin(runtimeepoch.WithFence(f.ctx, stale, r.ID), in, true)
	if !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("stale writer=%v want RUNTIME_EPOCH_FENCED", err)
	}
	if !reflect.DeepEqual(before, scalarFixtureFacts(t, f, p.ID, r)) {
		t.Fatal("stale writer mutated runtime")
	}
	var n int
	if err = f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM execution_cancellations WHERE owner_id=?", p.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stale writer receipt %d %v", n, err)
	}
}

func TestAdmittedNoRunCancellationDifferentRequestAddsTerminalReceipt(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	counts, taskBefore := admissionCancelCounts(t, f), admissionRawTask(t, f, p.TaskID)
	in := admissionCancelInput(f, sid, p)
	first, err := f.control.CancelExecutionForSession(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	assertAdmissionCancelReceipt(t, p, first)
	assertAdmissionCancelDurable(t, f, p, counts, taskBefore, 1)
	in.RequestID = "different-new-request"
	second, err := f.control.CancelExecutionForSession(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if second.RequestID != in.RequestID || second.OwnerType != "EXECUTION" || second.OwnerID != p.ID || second.RunID != "" || second.State != "ALREADY_TERMINAL" || second.BackendDisposition != "NO_BACKEND" || second.BackendInterruptState != "NOT_SENT" || second.ExecutionStatus != "CANCELED" || second.RecoveryRequired || second.ResolvedAt == nil {
		t.Fatalf("independent terminal command receipt: %+v", second)
	}
	var n int
	if err = f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM execution_cancellations WHERE owner_id=?", p.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("distinct request receipts=%d want=2", n)
	}
	original, err := f.plane.ExecutionCancellations.Get(f.ctx, f.projectID, first.RequestID)
	if err != nil || !reflect.DeepEqual(original, first) {
		t.Fatalf("first receipt changed: %+v %v", original, err)
	}
	replay, err := f.control.CancelExecutionForSession(f.ctx, in)
	if err != nil || !reflect.DeepEqual(replay, second) {
		t.Fatalf("terminal command replay changed: %+v %v", replay, err)
	}
	assertAdmissionCancelDurable(t, f, p, counts, taskBefore, 2)
	// CancellationState exposes the latest command result; Status remains the
	// authoritative canceled owner lifecycle.
	assertDevelopmentProjectionClients(t, f, sid, p.ID, "CANCELED", "NO_BACKEND", "ALREADY_TERMINAL", false)
}

func TestAdmittedNoRunCancellationDistinctRequestRace(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	counts, taskBefore := admissionCancelCounts(t, f), admissionRawTask(t, f, p.TaskID)
	type result struct {
		receipt executioncancel.Receipt
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, id := range []string{"distinct-a", "distinct-b"} {
		in := admissionCancelInput(f, sid, p)
		in.RequestID = id
		go func() {
			<-start
			r, err := f.control.CancelExecutionForSession(f.ctx, in)
			results <- result{r, err}
		}()
	}
	close(start)
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.receipt.RequestID == b.receipt.RequestID {
		t.Fatalf("distinct commands: %+v %+v", a, b)
	}
	states := map[string]int{a.receipt.State: 1}
	states[b.receipt.State]++
	if states["CANCELED"] != 1 || states["ALREADY_TERMINAL"] != 1 {
		t.Fatalf("one owner terminalization, two command outcomes: %+v %+v", a, b)
	}
	for _, receipt := range []executioncancel.Receipt{a.receipt, b.receipt} {
		if receipt.BackendDisposition != "NO_BACKEND" || receipt.BackendInterruptState != "NOT_SENT" || receipt.RunID != "" {
			t.Fatalf("runtime effect in no-Run race: %+v", receipt)
		}
	}
	assertAdmissionCancelDurable(t, f, p, counts, taskBefore, 2)
	assertDevelopmentProjectionClients(t, f, sid, p.ID, "CANCELED", "NO_BACKEND", "ALREADY_TERMINAL", false)
}

func TestAdmittedNoRunCancellationDistinctRequestAfterRestart(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	in := admissionCancelInput(f, sid, p)
	first, err := f.control.CancelExecutionForSession(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	root := f.plane.DataPaths.Root
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := controlplane.Open(f.ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	f.plane = reopened
	fresh, err := controlapi.NewFromControlPlane(reopened)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = "after-restart-distinct"
	second, err := fresh.CancelExecutionForSession(f.ctx, in)
	if err != nil || second.State != "ALREADY_TERMINAL" || second.BackendDisposition != "NO_BACKEND" || second.BackendInterruptState != "NOT_SENT" || second.RunID != "" {
		t.Fatalf("terminal command after restart: %+v %v", second, err)
	}
	original, err := reopened.ExecutionCancellations.Get(f.ctx, f.projectID, first.RequestID)
	if err != nil || !reflect.DeepEqual(original, first) {
		t.Fatalf("original receipt changed after restart: %+v %v", original, err)
	}
	owner, err := reopened.ExecutionProposals.GetForProject(f.ctx, f.projectID, p.ID)
	if err != nil || owner.Outcome != executionproposal.OutcomeCanceled || owner.RunID != "" {
		t.Fatalf("owner changed after restart: %+v %v", owner, err)
	}
	assertScalarLayer(t, f, p.ID, 0, 0)
	var n int
	if err = reopened.DB.SQL().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM execution_cancellations WHERE owner_id=?", p.ID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("restart command receipts=%d err=%v", n, err)
	}
}
