package localmcp_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/storage/sqlite"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// The formal approval fixture reaches READY with a durable qualified binding.
// Stop between cancellation and its Task projection, then verify cold replay
// retains the exact authorization needed by the first successor.
func TestReadyNoRunCancellationProjectsAfterRestartBeforeFirstSuccessor(t *testing.T) {
	f, sid, predecessor := localMCPQualifiedAdmission(t)
	in := admissionCancelInput(f, sid, predecessor)
	backend := &admissionCancelBackend{}
	f.plane.Coordinator.Codex = backend
	before := restartDurableFacts(t, f, predecessor, in)
	tasks := factRows(t, before, "task")
	if len(tasks) != 1 || tasks[0]["status"] != "ACTIVE" || tasks[0]["phase"] != "IMPLEMENTATION_READY" || tasks[0]["current_execution_id"] != predecessor.ID {
		t.Fatalf("fixture did not reach the real READY admission: %+v", tasks)
	}
	counts := admissionCancelCounts(t, f)
	// Stop exactly after the cancellation transaction commits. Calling the
	// public facade here would also perform the projection and miss this window.
	receipt, interrupt, err := sqlite.NewExecutionCancellationStore(f.plane.DB).Begin(f.ctx, in, false)
	if err != nil || interrupt {
		t.Fatalf("durable cancellation: %+v interrupt=%t err=%v", receipt, interrupt, err)
	}
	assertAdmissionCancelReceipt(t, predecessor, receipt)
	persisted := restartDurableFacts(t, f, predecessor, in)
	if !reflect.DeepEqual(before["task"], persisted["task"]) || len(factRows(t, persisted, "eligibilities")) != 0 {
		t.Fatal("fixture already projected cancellation before restart")
	}
	var projected map[string]any
	for reboot := 0; reboot < 2; reboot++ {
		reopenSameNoRunFixture(t, f)
		f.plane.Retirement.Close()
		f.plane.Coordinator.Codex = backend
		after := assertStableCanceledNoRun(t, f, predecessor, in)
		if reboot == 0 {
			wantTask := make(map[string]any, len(tasks[0]))
			for key, value := range tasks[0] {
				wantTask[key] = value
			}
			wantTask["phase"] = "NO_RUN_SUCCESSOR_REQUIRED"
			if !reflect.DeepEqual([]map[string]any{wantTask}, after["task"]) {
				t.Fatalf("boot changed more than Task phase: want=%+v got=%+v", wantTask, after["task"])
			}
			projected = after
		} else {
			assertSameRestartDomain(t, projected, after)
		}
		for _, key := range []string{"cancellations", "execution", "admission", "plans", "preparationAuthority"} {
			if !reflect.DeepEqual(persisted[key], after[key]) {
				t.Fatalf("restart %d changed immutable %s", reboot, key)
			}
		}
		if got := admissionCancelCounts(t, f); !reflect.DeepEqual(counts, got) {
			t.Fatalf("restart created runtime facts: before=%v after=%v", counts, got)
		}
		replay, err := f.control.CancelExecutionForSession(f.ctx, in)
		if err != nil || !reflect.DeepEqual(receipt, replay) {
			t.Fatalf("original cancellation receipt replay: %+v err=%v", replay, err)
		}
		assertSameRestartDomain(t, after, restartDurableFacts(t, f, predecessor, in))
	}

	command := wp.PrepareSuccessorExecution{
		RequestID: "ready-restart-first-successor", PayloadSHA256: strings.Repeat("a", 64),
		TaskID: predecessor.TaskID, PredecessorExecutionID: predecessor.ID,
		ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: predecessor.ID,
		ExpectedDefinitionRevisionID: predecessor.TaskDefinitionRevisionID,
	}
	created, err := f.control.PrepareSuccessorExecutionForSession(f.ctx, sid, command)
	if err != nil || created.Outcome != wp.NoRunSuccessorCreated || created.SuccessorExecutionID == "" || created.SuccessorExecutionID == predecessor.ID {
		t.Fatalf("first successor after reboot: %+v err=%v", created, err)
	}
	afterCreate := restartDurableFacts(t, f, predecessor, in)
	rawTask := admissionRawTask(t, f, predecessor.TaskID)
	if !reflect.DeepEqual(rawTask, []string{"ACTIVE", "IMPLEMENTATION_PREPARING", created.SuccessorExecutionID, predecessor.TaskDefinitionRevisionID}) {
		t.Fatalf("successor Task identity: %v", rawTask)
	}
	if len(factRows(t, afterCreate, "allTaskExecutions")) != 2 || len(factRows(t, afterCreate, "effects")) != 1 || len(factRows(t, afterCreate, "receipts")) != 1 {
		t.Fatal("successor command did not create exactly one effect and receipt")
	}
	for _, key := range []string{"cancellations", "execution", "admission", "plans", "preparationAuthority"} {
		if !reflect.DeepEqual(persisted[key], afterCreate[key]) {
			t.Fatalf("successor creation changed predecessor %s", key)
		}
	}
	replay, err := f.control.PrepareSuccessorExecutionForSession(f.ctx, sid, command)
	if err != nil || replay != created {
		t.Fatalf("successor replay: %+v err=%v", replay, err)
	}
	assertSameRestartDomain(t, afterCreate, restartDurableFacts(t, f, predecessor, in))
	assertScalarLayer(t, f, created.SuccessorExecutionID, 0, 0)
	for table, column := range map[string]string{
		"runtime_preparation_attempts": "plan_id", "runtime_workset_bindings": "plan_id", "runtime_start_authorities": "plan_id",
	} {
		var count int
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT count(*) FROM "+table+" WHERE "+column+"=?", created.RuntimeScopePlanID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("successor has %s count=%d err=%v", table, count, err)
		}
	}
	if backend.starts.Load() != 0 {
		t.Fatal("no-Run reconciliation or successor creation launched a backend")
	}
}
