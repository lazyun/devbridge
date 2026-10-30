package controlapi

import (
	"context"
	"fmt"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
)

func TestCreationContinuityScopesUnfinishedTasksAndBoundsInventory(t *testing.T) {
	values := scopeTasks{}
	for i := 0; i < 23; i++ {
		id := fmt.Sprintf("task-%02d", i)
		values[id] = task.Task{ID: id, Title: id, ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Status: task.StatusActive, Phase: task.PhaseVerifying}
	}
	for _, tc := range []struct {
		id, project, area string
		status            task.Status
		kind              task.Type
		legacy            bool
	}{
		{"foreign", "foreign", "area", task.StatusActive, task.TypeDevelopment, false},
		{"another-area", "project", "another-area", task.StatusActive, task.TypeDevelopment, false},
		{"done", "project", "area", task.StatusSucceeded, task.TypeDevelopment, false},
		{"canceled", "project", "area", task.StatusCanceled, task.TypeDevelopment, false},
		{"failed", "project", "area", task.StatusFailed, task.TypeDevelopment, false},
		{"inspection", "project", "area", task.StatusActive, task.TypeInspection, false},
		{"legacy", "project", "area", task.StatusActive, task.TypeDevelopment, true},
	} {
		values[tc.id] = task.Task{ID: tc.id, ProjectID: tc.project, FeatureAreaID: tc.area, Status: tc.status, Type: tc.kind, LegacyReadOnly: tc.legacy}
	}
	f := &Facade{deps: Dependencies{Tasks: values, FeatureAreas: compactAreas{}}}
	intent := c.Intent{AISessionID: "session", ProjectID: "project", Purpose: c.Task, Content: c.PrepareInput{Task: &c.TaskBody{FeatureAreaID: "area"}}}
	got := f.confirmationTaskContinuity(context.Background(), intent)
	if got.Availability != "AVAILABLE" || got.Total != 23 || len(got.Tasks) != 20 || got.Tasks[0].TaskID != "task-00" || got.Tasks[19].TaskID != "task-19" {
		t.Fatalf("unexpected inventory: %+v", got)
	}
	// A different project cannot borrow the area or disclose its Task inventory.
	intent.ProjectID = "foreign"
	got = f.confirmationTaskContinuity(context.Background(), intent)
	if got.Availability != "UNAVAILABLE" || got.Total != 0 || len(got.Tasks) != 0 {
		t.Fatalf("foreign inventory: %+v", got)
	}
	intent.Purpose = c.Review
	if f.confirmationTaskContinuity(context.Background(), intent) != nil {
		t.Fatal("non-creation acquired continuation options")
	}
}

func TestCreationContinuityReadFailureIsNotAnEmptyInventory(t *testing.T) {
	f := &Facade{deps: Dependencies{Tasks: &compactOnlyTasks{}, FeatureAreas: compactAreas{}}}
	intent := c.Intent{AISessionID: "session", ProjectID: "project", Purpose: c.Task, Content: c.PrepareInput{Task: &c.TaskBody{FeatureAreaID: "area"}}}
	got := f.confirmationTaskContinuity(context.Background(), intent)
	if got.Availability != "UNAVAILABLE" || got.ReadFailureCode == "" || got.Tasks == nil {
		t.Fatalf("read failure concealed: %+v", got)
	}
}
