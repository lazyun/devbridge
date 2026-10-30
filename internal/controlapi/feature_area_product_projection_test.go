package controlapi

import (
	"testing"

	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestFeatureAreaProductProjectionOwnsStatusTaskPhaseActivityAndActions(t *testing.T) {
	current := &wp.TaskFact{ID: "task-current", Status: string(task.StatusActive), Phase: task.PhaseVerifying}
	latest := &wp.TaskFact{ID: "task-latest", Status: string(task.StatusSucceeded), Phase: task.PhaseAcceptancePending}
	value := p.Projection{
		FeatureArea: p.Identity{ID: "area", Name: "Chat"}, LifecycleStatus: "DISCOVERING",
		CurrentTask: current, LatestTask: latest,
		CurrentOperation: &p.Operation{Kind: "EXECUTION", ID: "execution", RunID: "run", Status: "RUNNING"},
		AgentOperation:   p.AgentOperation{Status: "BUSY", Message: "Verification is running."},
		NextAction:       wp.Action{Code: "RESOLVE_DECISION", Required: true, Message: "Choose"},
		Capabilities:     []p.Capability{{Action: "CREATE_TASK", Available: true}, {Action: "START_EXECUTION", Available: false}},
	}
	populateFeatureAreaProductProjection(&value, false)
	product := value.Product
	if product.DomainStatus != "ACTIVE" || product.InternalStatus != "DISCOVERING" || product.CurrentTask != current || product.LatestTask != latest || product.TaskPhase != string(task.PhaseVerifying) {
		t.Fatalf("identity/status/task mismatch: %+v", product)
	}
	if product.OperationActivity == nil || product.OperationActivity.OperationID != "execution" || product.OperationActivity.RunID != "run" || product.OperationActivity.State != "BUSY" {
		t.Fatalf("activity mismatch: %+v", product.OperationActivity)
	}
	if product.RequiredAction == nil || product.RequiredAction.Code != "RESOLVE_DECISION" || len(product.AvailableActions) != 1 || product.AvailableActions[0] != "CREATE_TASK" {
		t.Fatalf("actions mismatch: %+v", product)
	}

	value.AgentOperation = p.AgentOperation{Status: "READY", Message: "Agent context is ready for work."}
	populateFeatureAreaProductProjection(&value, false)
	if value.Product.OperationActivity != nil {
		t.Fatalf("READY leaked into product activity: %+v", value.Product.OperationActivity)
	}
	value.CurrentTask = nil
	value.AgentOperation.Status = "BUSY"
	populateFeatureAreaProductProjection(&value, false)
	if value.Product.TaskPhase != "" || value.Product.OperationActivity != nil {
		t.Fatalf("taskless product leaked phase/activity: %+v", value.Product)
	}
}
