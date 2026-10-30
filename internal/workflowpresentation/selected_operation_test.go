package workflowpresentation

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/task"
	"strings"
	"testing"
	"time"
)

func TestSelectedOperationEntityDomain(t *testing.T) {
	now := time.Now().UTC()
	v := TaskView{ID: "task-x", Phase: task.PhaseReviewing, CurrentReview: &ReviewView{ID: "review-x", TaskID: "task-x", Status: "RUNNING", Run: &VerificationRunView{ID: "review-run"}, UpdatedAt: now}}
	check := func(kind, id, run string) {
		t.Helper()
		op := SelectedOperationFromTaskView(v)
		if op == nil || op.Kind != kind || op.OperationID != id || op.OwnerTaskID != "task-x" || op.RunID != run || op.UpdatedAt != now {
			t.Fatalf("%+v", op)
		}
		b, _ := json.Marshal(WorkspaceProjection{SelectedTaskID: v.ID, SelectedWork: op})
		if strings.Contains(string(b), `"id":"task-x"`) || strings.Contains(string(b), `"kind":"TASK"`) {
			t.Fatal(string(b))
		}
	}
	check("REVIEW", "review-x", "review-run")
	v.CurrentReview.TaskID = "wrong"
	if SelectedOperationFromTaskView(v) != nil {
		t.Fatal("foreign Review owner")
	}
	v.CurrentReview.TaskID = v.ID
	v.Phase = task.PhaseReviewRequired
	if SelectedOperationFromTaskView(v) != nil {
		t.Fatal("previous Review became current operation")
	}
	v.Phase = task.PhaseImplementing
	v.CurrentExecution = &ExecutionView{ID: "execution-x", State: "CONSUMED", Outcome: "PENDING", ImplementationRunID: "impl-run", VerificationRunID: "verify-run", UpdatedAt: now}
	check("EXECUTION", "execution-x", "impl-run")
	v.Phase = task.PhaseVerifying
	check("EXECUTION", "execution-x", "verify-run")
	v.CurrentExecution = nil
	if SelectedOperationFromTaskView(v) != nil {
		t.Fatal("Task filled empty operation")
	}
	op := SelectedOperationFromAgentExecution(agentexecution.Projection{Kind: agentexecution.KindInspection, SemanticOwnerID: "probe-x", TaskID: "internal-envelope", RunID: "probe-run", Status: "SUCCEEDED", UpdatedAt: now})
	if op == nil || op.Kind != "INSPECTION" || op.OperationID != "probe-x" || op.OwnerTaskID != "" {
		t.Fatal(op)
	}
	b, _ := json.Marshal(WorkspaceProjection{})
	if !strings.Contains(string(b), `"selectedWork":null`) {
		t.Fatal(string(b))
	}
}
