package bridgehttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowtest"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// The fixture in TestProductProjectWorkspaceProjectsFeatureAreasTasksExecutionsAndWorkspaces
// admits all four kinds with held/fake backends. Verify the HTTP inventory is
// the same Core result as the control API, including exact Task.Work identities.
func assertUniversalExecutionInventory(t *testing.T, f consoleFixture, body string) {
	t.Helper()
	ctx := f.ctx
	var view consoleapi.ProductProjectWorkspace
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	control, err := controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	projections, err := control.ListAgentExecutions(ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]agentexecution.Projection{}
	for _, p := range projections {
		byID[p.ExecutionID] = p
	}
	seen := map[string]bool{}
	for _, e := range view.Executions {
		p, ok := byID[e.ID]
		if !ok || e.Execution == nil || !reflect.DeepEqual(*e.Execution, p) {
			t.Fatalf("HTTP and Core diverged: %+v / %+v", e, p)
		}
		if e.RunID != p.RunID || e.TaskID != p.TaskID || e.Phase != p.Stage || e.SemanticOwnerID != p.SemanticOwnerID {
			t.Fatalf("ownership diverged: %+v", e)
		}
		seen[e.Kind] = true
		if e.Kind != "DEVELOPMENT" && p.ExecutionProposalID != "" {
			t.Fatalf("authorization fabricated: %+v", p)
		}
	}
	for _, kind := range []string{"REVIEW", "DEVELOPMENT", "INSPECTION"} {
		if !seen[kind] {
			t.Fatalf("missing %s from inventory: %+v", kind, view.Executions)
		}
	}
	for _, v := range view.Tasks {
		if v.Work.RunID == "" {
			continue
		}
		p, ok := byID[v.Work.ExecutionID]
		for _, e := range v.Executions {
			if e.Kind != "DEVELOPMENT" || e.TaskID != v.ID {
				t.Fatalf("non-development operation in Task execution history: %+v", e)
			}
		}
		if p.Kind == agentexecution.KindDevelopment && (v.LatestExecution == nil || v.LatestExecution.ID != v.Work.ExecutionID || v.LatestExecution.RunID != v.Work.RunID) {
			t.Fatalf("Task latest execution missing exact envelope: %+v", v)
		}
		if !ok || p.TaskID != v.ID || p.RunID != v.Work.RunID || p.SemanticOwnerID != v.Work.OwnerID {
			t.Fatalf("Task.Work diverged: %+v / %+v", v.Work, p)
		}
	}
}

func TestReadyCurrentWorkflowRemainsTaskBeforeExecutionAdmission(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	ctx := f.ctx
	current := createConsoleTask(t, f, f.moduleID, task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "fixture"}}, "Pending development")
	current = workflowtest.Approve(t, f.plane.DB, current, f.sessionID, current.WorkSpec, current.VerificationSpec)
	control, err := controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := control.CurrentWorkflowForTaskForSession(ctx, f.sessionID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Task.ID != current.ID || workflow.Task.Phase != task.PhaseImplementationReady {
		t.Fatalf("approval fabricated a pre-admission Execution: %+v", workflow)
	}
	presentation, err := control.TaskPresentationForSession(ctx, f.sessionID, current.FeatureAreaID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if presentation.TaskView.CurrentReview == nil || presentation.TaskView.CurrentExecution == nil || presentation.TaskView.CurrentExecution.Outcome != "PENDING" {
		t.Fatalf("approved Task history: %+v", presentation.TaskView)
	}
	found := false
	for _, c := range presentation.TaskView.Actions {
		if c.Action == "START_EXECUTION" && c.Available && c.Target.TaskID == current.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing Task-owned admission action: %+v", presentation.TaskView.Actions)
	}
}

func TestProductHTTPDevelopmentCancellation(t *testing.T) {
	for _, terminal := range []bool{true} {
		t.Run(fmt.Sprint("terminal=", terminal), func(t *testing.T) {
			f := newConsoleFixture(t)
			defer f.plane.Close()
			taskValue := createConsoleTask(t, f, f.moduleID, task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "fixture"}}, "Cancellation")
			r := startTestRun(t, f, taskValue, terminal)
			receipt := cancelThroughProductHTTP(t, f, r.AgentExecutionID, r.ID, "http-cancel")
			if receipt.OwnerType != "EXECUTION" || receipt.OwnerID != r.ExecutionProposalID || receipt.RunID != r.ID {
				t.Fatalf("wrong canonical owner: %+v", receipt)
			}
			replay := cancelThroughProductHTTP(t, f, r.AgentExecutionID, r.ID, "http-cancel")
			if !reflect.DeepEqual(receipt, replay) {
				t.Fatalf("replay changed: %+v / %+v", receipt, replay)
			}
		})
	}
}
func cancelThroughProductHTTP(t *testing.T, f consoleFixture, id, runID, requestID string) executioncancel.Receipt {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"runId": runID, "requestId": requestID})
	req := httptest.NewRequest(http.MethodPost, "/ui/api/executions/"+id+"/cancel?project_id="+f.projectID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	setTestLoopbackTransport(req)
	rr := httptest.NewRecorder()
	f.handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HTTP cancel %d: %s", rr.Code, rr.Body.String())
	}
	var receipt executioncancel.Receipt
	if err := json.Unmarshal(rr.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(receipt.RequestID, "operator_") || receipt.RequestID == requestID {
		t.Fatalf("missing canonical receipt: %s", rr.Body.String())
	}
	return receipt
}
