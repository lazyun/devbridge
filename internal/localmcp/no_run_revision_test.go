package localmcp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestNoRunStopAndReviseAcrossTransportsAndRestart(t *testing.T) {
	for _, transport := range []string{"HTTP", "GPT", "LOCAL_MCP"} {
		t.Run(transport, func(t *testing.T) {
			f, sid, predecessor := localMCPScalarAdmission(t)
			backend := &admissionCancelBackend{}
			f.plane.Coordinator.Codex = backend
			cancel := admissionCancelInput(f, sid, predecessor)
			receipt, err := f.control.CancelExecutionForSession(f.ctx, cancel)
			if err != nil {
				t.Fatal(err)
			}
			before := assertStableCanceledNoRun(t, f, predecessor, cancel)
			in := wp.StopAndReviseNoRunTask{ProjectID: f.projectID, TaskID: predecessor.TaskID, PredecessorExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: predecessor.TaskDefinitionRevisionID}
			for _, invalid := range []wp.StopAndReviseNoRunTask{
				{ProjectID: "foreign-project", TaskID: in.TaskID, PredecessorExecutionID: in.PredecessorExecutionID, ExpectedDefinitionRevisionID: in.ExpectedDefinitionRevisionID},
				{ProjectID: in.ProjectID, TaskID: in.TaskID, PredecessorExecutionID: "wrong-owner", ExpectedDefinitionRevisionID: in.ExpectedDefinitionRevisionID},
				{ProjectID: in.ProjectID, TaskID: in.TaskID, PredecessorExecutionID: in.PredecessorExecutionID, ExpectedDefinitionRevisionID: "stale-revision"},
			} {
				if _, err = f.control.StopAndReviseNoRunTaskForSession(f.ctx, sid, invalid); err == nil {
					t.Fatal("revision accepted a different Project, owner or revision")
				}
				assertSameRestartDomain(t, before, restartDurableFacts(t, f, predecessor, cancel))
			}
			var revised task.Task
			if transport == "HTTP" {
				web, err := bridgehttp.New(f.plane, false)
				if err != nil {
					t.Fatal(err)
				}
				payload, err := json.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/v1/tasks/stop-and-revise", bytes.NewReader(payload))
				setLocalOperatorLoopback(request)
				web.Handler().ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("revision HTTP %d: %s", recorder.Code, recorder.Body.String())
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &revised); err != nil {
					t.Fatal(err)
				}
			} else {
				args := map[string]any{"aiSessionId": sid, "taskId": in.TaskID, "predecessorExecutionId": in.PredecessorExecutionID, "expectedDefinitionRevisionId": in.ExpectedDefinitionRevisionID}
				client := connectGPT(t, f.gptURL)
				if transport == "LOCAL_MCP" {
					client = connectLocalMCP(t, f.libreURL)
				}
				out := callToolMap(t, f.ctx, client, "task_stop_and_revise", args)
				payload, err := json.Marshal(out)
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(payload, &revised); err != nil {
					t.Fatal(err)
				}
			}
			if revised.Phase != task.PhaseReviewRequired || revised.Status != task.StatusActive || revised.CurrentExecutionID != predecessor.ID || revised.CurrentDefinitionRevisionID != predecessor.TaskDefinitionRevisionID {
				t.Fatalf("revision must preserve exact canceled owner: %+v", revised)
			}
			after := restartDurableFacts(t, f, predecessor, cancel)
			wantTask := factRows(t, before, "task")[0]
			wantTask["phase"] = string(task.PhaseReviewRequired)
			// All captured durable rows, including Task timestamps, receipts and
			// eligibility, must be unchanged except this one explicit phase.
			assertSameRestartDomain(t, before, after)
			for reboot := 0; reboot < 2; reboot++ {
				replay, err := f.control.StopAndReviseNoRunTaskForSession(f.ctx, sid, in)
				if err != nil || !bytes.Equal(evidenceCanonicalBytes(t, revised), evidenceCanonicalBytes(t, replay)) {
					t.Fatalf("same identity revision replay: %+v %v", replay, err)
				}
				cancelReplay, err := f.control.CancelExecutionForSession(f.ctx, cancel)
				if err != nil || !reflect.DeepEqual(receipt, cancelReplay) {
					t.Fatalf("old cancellation after revision: %+v %v", cancelReplay, err)
				}
				oldCommand := wp.PrepareSuccessorExecution{RequestID: "old-successor-after-revision", PayloadSHA256: strings.Repeat("a", 64), TaskID: in.TaskID, PredecessorExecutionID: in.PredecessorExecutionID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: in.PredecessorExecutionID, ExpectedDefinitionRevisionID: in.ExpectedDefinitionRevisionID}
				if _, err = f.control.PrepareSuccessorExecutionForSession(f.ctx, sid, oldCommand); err == nil {
					t.Fatal("old successor command crossed the explicit revision boundary")
				}
				assertSameRestartDomain(t, after, restartDurableFacts(t, f, predecessor, cancel))
				reopenSameNoRunFixture(t, f)
				f.plane.Retirement.Close()
				f.plane.Coordinator.Codex = backend
				assertSameRestartDomain(t, after, restartDurableFacts(t, f, predecessor, cancel))
			}
			if backend.starts.Load() != 0 {
				t.Fatal("phase-only revision dispatched runtime")
			}
		})
	}
}
