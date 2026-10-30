package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"reflect"
	"testing"
	"time"
)

func TestTaskPhaseAuthorityIgnoresChildHistory(t *testing.T) {
	phases := []task.Phase{task.PhaseReviewRequired, task.PhaseReviewing, task.PhaseReviewApprovalPending, task.PhaseImplementationReady, task.PhaseImplementing, task.PhaseVerificationReady, task.PhaseVerifying, task.PhaseAcceptancePending, task.PhaseDelivering, task.PhaseRecoveryRequired}
	for _, phase := range phases {
		if !phase.Valid() {
			t.Fatalf("fixture phase %s is not durable vocabulary", phase)
		}
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			owner := task.Task{ID: "task", Type: task.TypeDevelopment, ProjectID: "project", FeatureAreaID: "area", Title: "Original engineering intent", Goal: "Implement intent", Phase: phase, Status: task.StatusActive, CurrentExecutionID: "current"}
			baseline := BuildTask(owner, nil, nil, time.Time{})
			children := []run.Run{{ID: "historical-review", TaskID: owner.ID, ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, Purpose: run.PurposeScopeReview, Status: run.StatusSucceeded}, {ID: "unrelated-execution", TaskID: owner.ID, ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, Purpose: run.PurposeVerification, Status: run.StatusInfrastructureFailed}}
			view := BuildTask(owner, children, nil, time.Time{})
			if view.Title != owner.Title || view.Phase != phase || view.Status != "ACTIVE" || view.Terminal || !reflect.DeepEqual(view.Progress, baseline.Progress) {
				t.Fatalf("children reduced Task phase: %+v", view)
			}
			if view.Activity.Runtime != nil {
				t.Fatal("historical child selected as current activity")
			}
		})
	}
}

// Every durable Phase.Valid() member must resolve in the shared display
// vocabulary; a missing case may not silently render an earlier rail stage.
func TestTaskPhaseLabelAndRailCoverEveryValidPhase(t *testing.T) {
	for _, phase := range []task.Phase{task.PhaseReviewRequired, task.PhaseReviewing, task.PhaseReviewApprovalPending, task.PhaseImplementationReady, task.PhaseImplementing, task.PhaseVerificationReady, task.PhaseVerifying, task.PhaseAcceptancePending, task.PhaseDelivering, task.PhaseRecoveryRequired} {
		label, description := TaskPhaseLabel(phase, task.StatusActive)
		if label == "" || description == "" {
			t.Fatalf("phase %s has empty display vocabulary: %q/%q", phase, label, description)
		}
		if label == string(phase) {
			t.Fatalf("phase %s fell through the shared label vocabulary", phase)
		}
	}
	delivering := TaskPhaseProgress(task.PhaseDelivering, task.StatusActive)
	if delivering[0].Status != "COMPLETE" || delivering[3].Status != "COMPLETE" || delivering[4].Status != "CURRENT" {
		t.Fatalf("DELIVERING rail=%+v", delivering)
	}
	for _, step := range TaskPhaseProgress(task.PhaseDelivering, task.StatusSucceeded) {
		if step.Status != "COMPLETE" {
			t.Fatalf("delivered Task rail=%+v", step)
		}
	}
	recovery := TaskPhaseProgress(task.PhaseRecoveryRequired, task.StatusActive)
	for _, step := range recovery {
		if step.Status != "PENDING" {
			t.Fatalf("Unknown recovery origin guessed a stage: %+v", recovery)
		}
	}
	if label, description := TaskPhaseLabel(task.PhaseDelivering, task.StatusActive); label != "Delivering" || description != "交付中" {
		t.Fatalf("DELIVERING label=%q/%q", label, description)
	}
	if label, description := TaskPhaseLabel(task.PhaseRecoveryRequired, task.StatusActive); label != "Recovery required" || description != "需要恢复" {
		t.Fatalf("RECOVERY_REQUIRED label=%q/%q", label, description)
	}
}

func TestTaskPhaseRailTerminalAndPendingGates(t *testing.T) {
	for _, phase := range []task.Phase{task.PhaseImplementationReady, task.PhaseAcceptancePending} {
		for _, step := range TaskPhaseProgress(phase, task.StatusSucceeded) {
			if step.Status != "COMPLETE" {
				t.Fatalf("accepted Task has pending step: %+v", step)
			}
		}
	}
	pending := TaskPhaseProgress(task.PhaseAcceptancePending, task.StatusActive)
	if pending[3].Status != "COMPLETE" || pending[4].Status != "PENDING" {
		t.Fatal(pending)
	}
	for _, status := range []task.Status{task.StatusCanceled, task.StatusFailed} {
		for _, step := range TaskPhaseProgress(task.PhaseVerifying, status) {
			if step.Status == "CURRENT" {
				t.Fatalf("terminal Task has active phase: %+v", step)
			}
		}
	}
}

func TestRecoveryBusinessStageUsesOnlyDurableOrigin(t *testing.T) {
	for _, phase := range []task.Phase{task.PhaseReviewing, task.PhaseImplementing, task.PhaseVerifying, task.PhaseDelivering} {
		owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Status: task.StatusActive, Phase: task.PhaseRecoveryRequired, RecoveryOrigin: &task.RecoveryOrigin{Phase: phase, SourceEventID: "exact-recovery-event"}}
		v := BuildTask(owner, nil, nil, time.Time{})
		if v.Phase != task.PhaseRecoveryRequired || v.BusinessPhase != phase || v.BusinessPhaseSourceEventID != "exact-recovery-event" {
			t.Fatalf("Recovery replaced canonical authority: %+v", v)
		}
		if phase == task.PhaseVerifying && (v.Progress[2].Status != "COMPLETE" || v.Progress[3].Status != "BLOCKED" || v.Progress[4].Status != "PENDING") {
			t.Fatalf("Verification recovery regressed to implementation: %+v", v.Progress)
		}
	}
	owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Status: task.StatusActive, Phase: task.PhaseRecoveryRequired}
	for _, origin := range []*task.RecoveryOrigin{nil, {Phase: task.PhaseVerifying}, {Phase: task.PhaseRecoveryRequired, SourceEventID: "bad"}, {Phase: "UNRECOGNIZED", SourceEventID: "bad"}} {
		owner.RecoveryOrigin = origin
		v := BuildTask(owner, []run.Run{{ID: "historical-verify", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", Purpose: run.PurposeVerification, Status: run.StatusInfrastructureFailed}}, nil, time.Time{})
		if v.BusinessPhase != "" {
			t.Fatalf("Unproven recovery origin: %+v", v)
		}
		for _, p := range v.Progress {
			if p.Status != "PENDING" {
				t.Fatalf("Historical child guessed recovery stage: %+v", v.Progress)
			}
		}
	}
	owner.Phase = task.PhaseAcceptancePending
	owner.RecoveryOrigin = &task.RecoveryOrigin{Phase: task.PhaseImplementing, SourceEventID: "old"}
	if v := BuildTask(owner, nil, nil, time.Time{}); v.BusinessPhase != task.PhaseAcceptancePending {
		t.Fatal("Old recovery origin changed advanced phase")
	}
}
