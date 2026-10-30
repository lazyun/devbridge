package consoleapi

import (
	"encoding/json"
	"fmt"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	"testing"
)

func TestProductTaskSummaryRetainsExactRunRecovery(t *testing.T) {
	view := p.TaskView{ID: "task", Title: "Task", Status: "ACTIVE", Phase: task.PhaseRecoveryRequired, BusinessPhase: task.PhaseVerifying, CurrentExecution: &p.ExecutionView{ID: "execution"}, Details: p.TaskDiagnostics{TaskDefinitionRevisionID: "revision"}, RunRecovery: &p.TaskRunRecovery{RunID: "verify", Purpose: "VERIFICATION", ResultState: "UNKNOWN", Required: true, Label: "待找回验证结果"}, NextStep: &p.TaskNextStep{Code: "RECOVER_RUN_RESULT", TaskID: "task", ExecutionID: "execution", RunID: "verify", Purpose: "VERIFICATION", TaskDefinitionRevisionID: "revision", Label: "待找回验证结果"}}
	item := ProductTaskSummary{ID: "task"}
	applyProductTaskFacts(&item, &view)
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["currentExecutionId"] != "execution" || wire["current_definition_revision_id"] != "revision" || wire["runRecovery"].(map[string]any)["runId"] != "verify" || wire["nextStep"].(map[string]any)["label"] != "待找回验证结果" {
		t.Fatalf("compact list lost run recovery: %s", raw)
	}
}

func TestSemanticTaskPageFiltersBeforeBounding(t *testing.T) {
	values := []ProductTaskSummary{}
	for i := 0; i < 250; i++ {
		values = append(values, ProductTaskSummary{ID: fmt.Sprintf("task-%03d", i), Title: fmt.Sprintf("Task %03d", i), FeatureAreaID: "area", Status: task.StatusActive, Phase: task.PhaseReviewing})
	}
	page := pageProductTasks(values, ProductTaskFilter{Search: "task", FeatureAreaID: "area", Status: "ACTIVE", Phase: "REVIEWING"}, 25, 225)
	if page.Total != 250 || len(page.Items) != 25 || page.Items[0].ID != "task-225" {
		t.Fatalf("page: %+v", page)
	}
	for _, filter := range []ProductTaskFilter{{FeatureAreaID: "foreign"}, {Status: "SUCCEEDED"}, {Phase: "ACCEPTANCE_PENDING"}, {Search: "Review instruction"}} {
		page = pageProductTasks(values, filter, 25, 0)
		if page.Total != 0 || len(page.Items) != 0 || page.Items == nil {
			t.Fatalf("filter escaped: %+v", page)
		}
	}
	page = pageProductTasks(values, ProductTaskFilter{Search: "Task 249"}, 25, 0)
	if page.Total != 1 || page.Items[0].ID != "task-249" {
		t.Fatalf("filter applied after bound: %+v", page)
	}
	page = pageProductTasks(values, ProductTaskFilter{}, 25, 300)
	if page.Total != 250 || len(page.Items) != 0 {
		t.Fatalf("past-end page: %+v", page)
	}
}

func TestRecoveryTasksRemainInBusinessStageFilter(t *testing.T) {
	values := []ProductTaskSummary{{ID: "verification-interrupted", Status: task.StatusActive, Phase: task.PhaseRecoveryRequired, BusinessPhase: task.PhaseVerifying}, {ID: "unknown-origin", Status: task.StatusActive, Phase: task.PhaseRecoveryRequired}, {ID: "implementation-interrupted", Status: task.StatusActive, Phase: task.PhaseRecoveryRequired, BusinessPhase: task.PhaseImplementing}}
	for phase, want := range map[string]int{"VERIFYING": 1, "IMPLEMENTING": 1, "RECOVERY_REQUIRED": 3, "ACCEPTANCE_PENDING": 0} {
		page := pageProductTasks(values, ProductTaskFilter{Phase: phase}, 25, 0)
		if page.Total != want {
			t.Fatalf("filter %s lost recovery business stage: %+v", phase, page)
		}
	}
}
