package workflowpresentation

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestReviewDepthFactsProjectDurableFloorAndExactHistory(t *testing.T) {
	owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Title: "Semantic title", Status: task.StatusActive, Phase: task.PhaseReviewRequired, RequiredReviewDepth: task.ReviewDepthPointer(task.ReviewDepthDeep), CurrentReviewID: "latest"}
	reviews := []review.Review{
		{ID: "old", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", Ordinal: 1, ReviewDepth: task.ReviewDepthFast, ReviewDisposition: review.DispositionFurtherReviewRequired, RunID: "old-run", Instruction: "Do not replace title"},
		{ID: "latest", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", Ordinal: 2, ReviewDepth: task.ReviewDepthDeep, ReviewDisposition: review.DispositionFurtherReviewRequired, RunID: "latest-run"},
	}
	runs := []run.Run{{ID: "old-run", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ReviewID: "old", Status: run.StatusSucceeded}, {ID: "latest-run", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ReviewID: "latest", Status: run.StatusSucceeded}}
	v := BuildTask(owner, runs, nil, time.Time{})
	AttachTaskReviews(&v, owner, reviews, runs)
	if v.Title != owner.Title || v.Phase != task.PhaseReviewRequired || v.Status != "ACTIVE" || v.PhaseDescription != "需要深度审查" || v.Terminal {
		t.Fatalf("Task authority changed: %+v", v)
	}
	if !reflect.DeepEqual(v.AllowedReviewDepths, []task.ReviewDepth{task.ReviewDepthDeep}) || v.RequiredReviewDepth == nil || *v.RequiredReviewDepth != task.ReviewDepthDeep {
		t.Fatalf("floor: %+v", v)
	}
	if v.CurrentReview.ID != "latest" || v.CurrentReview.ReviewDepth != task.ReviewDepthDeep || v.CurrentReview.ReviewDisposition != review.DispositionFurtherReviewRequired || v.CurrentReview.Status != "SUCCEEDED" {
		t.Fatalf("current: %+v", v.CurrentReview)
	}
	if v.ReviewHistory[0].ReviewDepth != task.ReviewDepthFast || v.ReviewHistory[0].ReviewDisposition != review.DispositionFurtherReviewRequired || len(v.Actions) != 0 {
		t.Fatalf("history: %+v", v.ReviewHistory)
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err = json.Unmarshal(encoded, &data); err != nil {
		t.Fatal(err)
	}
	if data["requiredReviewDepth"] != "DEEP" || data["currentReview"].(map[string]any)["reviewDisposition"] != "FURTHER_REVIEW_REQUIRED" {
		t.Fatalf("JSON parity: %s", encoded)
	}
}

func TestReviewDepthFactsAreNullOutsideReviewWork(t *testing.T) {
	for _, tc := range []struct {
		phase  task.Phase
		status task.Status
		floor  *task.ReviewDepth
		want   []task.ReviewDepth
	}{
		{task.PhaseReviewRequired, task.StatusActive, task.ReviewDepthPointer(task.ReviewDepthFast), []task.ReviewDepth{task.ReviewDepthFast, task.ReviewDepthDeep}},
		{task.PhaseReviewing, task.StatusActive, task.ReviewDepthPointer(task.ReviewDepthDeep), []task.ReviewDepth{task.ReviewDepthDeep}},
		{task.PhaseReviewApprovalPending, task.StatusActive, nil, []task.ReviewDepth{}},
		{task.PhaseImplementationReady, task.StatusActive, nil, []task.ReviewDepth{}},
		{task.PhaseReviewing, task.StatusCanceled, task.ReviewDepthPointer(task.ReviewDepthDeep), []task.ReviewDepth{}},
	} {
		v := BuildTask(task.Task{Type: task.TypeDevelopment, Phase: tc.phase, Status: tc.status, RequiredReviewDepth: tc.floor}, nil, nil, time.Time{})
		if !reflect.DeepEqual(v.AllowedReviewDepths, tc.want) {
			t.Fatalf("%s: %v", tc.phase, v.AllowedReviewDepths)
		}
		encoded, _ := json.Marshal(v)
		var data map[string]any
		_ = json.Unmarshal(encoded, &data)
		if len(tc.want) == 0 && (v.RequiredReviewDepth != nil || data["requiredReviewDepth"] != nil || len(data["allowedReviewDepths"].([]any)) != 0) {
			t.Fatalf("%s: %s", tc.phase, encoded)
		}
	}
}
