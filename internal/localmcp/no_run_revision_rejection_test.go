package localmcp_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestNoRunStopAndReviseRejectsIneligibleLifecycleAndCaller(t *testing.T) {
	for _, state := range []string{"unsettled-admitted", "prepared-run", "canceled-task", "foreign-caller", "missing-operator-access"} {
		t.Run(state, func(t *testing.T) {
			f, sid, predecessor := localMCPScalarAdmission(t)
			backend := &admissionCancelBackend{}
			f.plane.Coordinator.Codex = backend
			cancel := admissionCancelInput(f, sid, predecessor)
			var prepared run.Run
			if state == "prepared-run" {
				// Enter the real preparation transaction without authorizing or
				// dispatching a backend. Keep its actual Task phase for rejection.
				_, prepared = prepareScalarExecutionForLocalMCP(t, f, sid, predecessor)
				if prepared.ID == "" || prepared.ExecutionProposalID != predecessor.ID {
					t.Fatalf("missing exact preparation Run: %+v", prepared)
				}
			} else if state != "unsettled-admitted" {
				receipt, err := f.control.CancelExecutionForSession(f.ctx, cancel)
				if err != nil {
					t.Fatal(err)
				}
				assertAdmissionCancelReceipt(t, predecessor, receipt)
				assertStableCanceledNoRun(t, f, predecessor, cancel)
				if state == "canceled-task" {
					closed, err := f.control.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: sid, ProjectID: f.projectID, OwnerType: "TASK", OwnerID: predecessor.TaskID, RequestID: "cancel-task-before-revision"})
					if err != nil || closed.State != "CANCELED" || admissionRawTask(t, f, predecessor.TaskID)[0] != "CANCELED" {
						t.Fatalf("Task cancellation fixture: %+v err=%v", closed, err)
					}
				}
			}
			before := restartDurableFacts(t, f, predecessor, cancel)
			runtimeBefore := scalarFixtureFacts(t, f, predecessor.ID, prepared)
			if state == "unsettled-admitted" {
				owners := factRows(t, before, "execution")
				if len(owners) != 1 || owners[0]["status"] != "ADMITTED" || owners[0]["outcome"] != "PENDING" || len(factRows(t, before, "cancellations")) != 0 || len(exactExecutionRuns(t, before, predecessor.ID)) != 0 {
					t.Fatalf("fixture is not an unsettled no-Run admission: owner=%+v cancellations=%+v", owners, before["cancellations"])
				}
			}
			in := wp.StopAndReviseNoRunTask{ProjectID: f.projectID, TaskID: predecessor.TaskID, PredecessorExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: predecessor.TaskDefinitionRevisionID}
			ctx := f.ctx
			if state == "foreign-caller" {
				ctx = aisession.WithCallerScope(ctx, "outside-"+sid)
			}
			var err error
			if state == "missing-operator-access" {
				if _, ok := localoperator.FromContext(ctx); ok {
					t.Fatal("fixture unexpectedly has trusted operator access")
				}
				_, err = f.control.StopAndReviseNoRunTaskForOperator(ctx, f.projectID, in)
			} else {
				_, err = f.control.StopAndReviseNoRunTaskForSession(ctx, sid, in)
			}
			switch state {
			case "unsettled-admitted", "prepared-run":
				if err == nil || !strings.Contains(err.Error(), "TASK_STATE_STALE") {
					t.Fatalf("ineligible lifecycle revision: %v", err)
				}
			case "canceled-task":
				if err == nil || !strings.Contains(err.Error(), "TASK_NOT_CURRENT") {
					t.Fatalf("terminal Task revision: %v", err)
				}
			case "foreign-caller":
				if !errors.Is(err, aisession.ErrScopeMismatch) {
					t.Fatalf("foreign caller revision: %v", err)
				}
			case "missing-operator-access":
				if !errors.Is(err, localoperator.ErrAccessDenied) {
					t.Fatalf("unauthenticated operator revision: %v", err)
				}
			}
			assertSameRestartDomain(t, before, restartDurableFacts(t, f, predecessor, cancel))
			if after := scalarFixtureFacts(t, f, predecessor.ID, prepared); !reflect.DeepEqual(runtimeBefore, after) {
				t.Fatalf("rejected revision changed runtime authority: before=%+v after=%+v", runtimeBefore, after)
			}
			if backend.starts.Load() != 0 {
				t.Fatal("rejected revision or fixture preparation dispatched a backend")
			}
		})
	}
}
