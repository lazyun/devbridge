package localmcp_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// The fixture reaches READY through real APPROVE and admission commands. These
// are the frozen READY/successor-required Task cancellation exits, not coverage
// of APPROVE_AND_START's complete T1/T2/T3 protocol.
func TestNoRunTaskCancellationPreservesSettledPredecessorAcrossRestart(t *testing.T) {
	for _, phase := range []string{"IMPLEMENTATION_READY", "NO_RUN_SUCCESSOR_REQUIRED"} {
		t.Run(phase, func(t *testing.T) {
			f, sid, predecessor := localMCPScalarAdmission(t)
			backend := &admissionCancelBackend{}
			f.plane.Coordinator.Codex = backend
			in := admissionCancelInput(f, sid, predecessor)
			var receipt executioncancel.Receipt
			var err error
			if phase == "IMPLEMENTATION_READY" {
				// Keep the committed cancellation's separate Task projection pending.
				var dispatch bool
				receipt, dispatch, err = sqlite.NewExecutionCancellationStore(f.plane.DB).Begin(f.ctx, in, false)
				if dispatch {
					t.Fatal("no-Run cancellation requested backend interruption")
				}
			} else {
				receipt, err = f.control.CancelExecutionForSession(f.ctx, in)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertAdmissionCancelReceipt(t, predecessor, receipt)
			settled := restartDurableFacts(t, f, predecessor, in)
			rows := factRows(t, settled, "task")
			if len(rows) != 1 || rows[0]["status"] != "ACTIVE" || rows[0]["phase"] != phase || rows[0]["current_execution_id"] != predecessor.ID {
				t.Fatalf("wrong pre-cancel lifecycle: %+v", rows)
			}
			wantEligibility := 0
			if phase == "NO_RUN_SUCCESSOR_REQUIRED" {
				wantEligibility = 1
			}
			if len(factRows(t, settled, "eligibilities")) != wantEligibility {
				t.Fatalf("wrong pre-cancel projection facts: %+v", settled["eligibilities"])
			}
			eligible, err := sqlite.NewExecutionCancellationStore(f.plane.DB).PreWorkTaskCancellationEligible(f.ctx, f.projectID, predecessor.TaskID)
			if err != nil || !eligible {
				t.Fatalf("settled predecessor did not permit Task cancellation: eligible=%t err=%v", eligible, err)
			}
			counts := admissionCancelCounts(t, f)
			taskInput := executioncancel.Input{AISessionID: sid, ProjectID: f.projectID, OwnerType: "TASK", OwnerID: predecessor.TaskID, RequestID: "cancel-settled-task", Reason: "close the canceled no-Run task"}
			args := map[string]any{"aiSessionId": sid, "ownerType": taskInput.OwnerType, "ownerId": taskInput.OwnerID, "requestId": taskInput.RequestID, "reason": taskInput.Reason}
			first := noRunTaskCancelReceipt(t, callToolMap(t, f.ctx, connectGPT(t, f.gptURL), "operation_cancel", args))
			if first.OwnerType != "TASK" || first.OwnerID != predecessor.TaskID || first.TaskID != predecessor.TaskID || first.ExecutionID != "" || first.RunID != "" || first.State != "CANCELED" || first.BackendDisposition != "NO_BACKEND" || first.BackendInterruptState != "NOT_SENT" || first.QuiescenceState != "NO_BACKEND" || first.RecoveryRequired || first.ResolvedAt == nil {
				t.Fatalf("Task cancellation receipt: %+v", first)
			}
			canceled := restartDurableFacts(t, f, predecessor, in)
			wantTask := make(map[string]any, len(rows[0]))
			for key, value := range rows[0] {
				wantTask[key] = value
			}
			wantTask["status"] = "CANCELED"
			wantTask["updated_at"] = factRows(t, canceled, "task")[0]["updated_at"]
			if !reflect.DeepEqual([]map[string]any{wantTask}, canceled["task"]) {
				t.Fatalf("Task cancellation changed phase, pointer, revision or other provenance: want=%+v got=%+v", wantTask, canceled["task"])
			}
			for key, value := range settled {
				if key != "task" && key != "cancellations" && !reflect.DeepEqual(value, canceled[key]) {
					t.Fatalf("Task cancellation changed predecessor authority %s", key)
				}
			}
			if len(factRows(t, canceled, "cancellations")) != 2 {
				t.Fatalf("Task cancellation did not add exactly one receipt: %+v", canceled["cancellations"])
			}
			var originalReceiptRows []map[string]any
			for _, row := range factRows(t, canceled, "cancellations") {
				if row["owner_type"] == "EXECUTION" {
					originalReceiptRows = append(originalReceiptRows, row)
				}
			}
			if !reflect.DeepEqual(settled["cancellations"], originalReceiptRows) {
				t.Fatalf("Task cancellation rewrote the original Execution receipt: before=%+v after=%+v", settled["cancellations"], originalReceiptRows)
			}
			replayed := noRunTaskCancelReceipt(t, callToolMap(t, f.ctx, connectLocalMCP(t, f.libreURL), "operation_cancel", args))
			if !reflect.DeepEqual(first, replayed) {
				t.Fatalf("Task receipt differs across adapters: first=%+v replay=%+v", first, replayed)
			}
			for reboot := 0; reboot < 3; reboot++ {
				if reboot > 0 {
					reopenSameNoRunFixture(t, f)
					f.plane.Retirement.Close()
					f.plane.Coordinator.Codex = backend
				}
				current, err := f.plane.Tasks.Get(f.ctx, predecessor.TaskID)
				if err != nil || current.Status != task.StatusCanceled {
					t.Fatalf("reboot %d lost canceled Task: %+v err=%v", reboot, current, err)
				}
				assertSameRestartDomain(t, canceled, restartDurableFacts(t, f, predecessor, in))
				replay, err := f.control.CancelExecutionForSession(f.ctx, taskInput)
				if err != nil || !reflect.DeepEqual(first, replay) {
					t.Errorf("reboot %d Task receipt replay: %+v err=%v", reboot, replay, err)
				}
				original, err := f.control.CancelExecutionForSession(f.ctx, in)
				if err != nil || !reflect.DeepEqual(receipt, original) {
					t.Errorf("reboot %d original Execution receipt replay after Task cancellation: %+v err=%v", reboot, original, err)
				}
				command := wp.PrepareSuccessorExecution{RequestID: "successor-after-task-cancel", PayloadSHA256: strings.Repeat("a", 64), TaskID: predecessor.TaskID, PredecessorExecutionID: predecessor.ID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: predecessor.TaskDefinitionRevisionID}
				created, err := f.control.PrepareSuccessorExecutionForSession(f.ctx, sid, command)
				if err == nil || !strings.Contains(err.Error(), "TASK_NOT_CURRENT") || created.SuccessorExecutionID != "" {
					t.Fatalf("reboot %d canceled Task authorized successor: %+v err=%v", reboot, created, err)
				}
				if _, _, err := f.plane.ExecutionProposals.PrepareAdmittedExecution(f.ctx, sid, predecessor); err == nil {
					t.Fatal("canceled Task allowed the predecessor to prepare a Run")
				}
				assertSameRestartDomain(t, canceled, restartDurableFacts(t, f, predecessor, in))
				if got := admissionCancelCounts(t, f); !reflect.DeepEqual(counts, got) {
					t.Fatalf("cancellation/restart created runtime facts: before=%v after=%v", counts, got)
				}
			}
			assertScalarLayer(t, f, predecessor.ID, 0, 0)
			if backend.starts.Load() != 0 {
				t.Fatal("Task cancellation, replay or restart launched a backend")
			}
		})
	}
}

func noRunTaskCancelReceipt(t *testing.T, value map[string]any) executioncancel.Receipt {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var receipt executioncancel.Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestNoRunTaskCancellationRejectsUnsettledAdmittedOwner(t *testing.T) {
	f, sid, predecessor := localMCPScalarAdmission(t)
	backend := &admissionCancelBackend{}
	f.plane.Coordinator.Codex = backend
	in := admissionCancelInput(f, sid, predecessor)
	before := restartDurableFacts(t, f, predecessor, in)
	store := sqlite.NewExecutionCancellationStore(f.plane.DB)
	eligible, err := store.PreWorkTaskCancellationEligible(f.ctx, f.projectID, predecessor.TaskID)
	if err != nil || eligible {
		t.Errorf("unsettled ADMITTED owner exposed Task cancel eligibility: eligible=%t err=%v", eligible, err)
	}
	taskInput := executioncancel.Input{AISessionID: sid, ProjectID: f.projectID, OwnerType: "TASK", OwnerID: predecessor.TaskID, RequestID: "premature-task-cancel"}
	receipt, err := f.control.CancelExecutionForSession(f.ctx, taskInput)
	if err == nil || !strings.Contains(err.Error(), "RUN_NOT_ACTIVE") || receipt.RequestID != "" {
		t.Errorf("unsettled ADMITTED owner accepted Task cancellation: %+v err=%v", receipt, err)
	}
	assertSameRestartDomain(t, before, restartDurableFacts(t, f, predecessor, in))
	if backend.starts.Load() != 0 {
		t.Fatal("rejected Task cancellation launched a backend")
	}
}
