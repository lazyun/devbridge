package controlapi_test

import (
	"errors"
	"testing"
)

func TestLegacyPartialMergeProjectsCompletedRepository(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	if f.repos["app"] > f.repos["sdk"] {
		f.repos["app"], f.repos["sdk"] = f.repos["sdk"], f.repos["app"]
		f.heads["app"], f.heads["sdk"] = f.heads["sdk"], f.heads["app"]
	}
	f = mixedLegacyTaskFromFixture(t, f)
	legacyTask := f.task
	legacy, err := f.facade.GetTask(f.ctx, legacyTask)
	if err != nil {
		t.Fatal(err)
	}
	next := mixedNextTask(t, f, "next", []string{"sdk"})
	current := approveDirectOnFixture(t, next, "next")
	acceptCompetingDirectTask(t, next, current, "next")
	plan, err := f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
	if err != nil || len(plan.Members) != 2 || plan.Members[0].RepositoryID != legacy.RepositoryID {
		t.Fatalf("fixture: %+v %v", plan, err)
	}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, repo string) error {
		if stage == "AREA_MERGE_BEFORE_GIT" && repo == f.repos["sdk"] {
			return errors.New("second merge outcome unknown")
		}
		return nil
	})
	if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "partial-merge"); err == nil {
		t.Fatal("expected incomplete second member")
	}
	var state, sha string
	if err = f.plane.DB.SQL().QueryRow(`SELECT state,resulting_dev_head FROM feature_area_commit_members WHERE request_id='partial-merge' AND repository_id=?`, legacy.RepositoryID).Scan(&state, &sha); err != nil || state != "COMPLETED" || sha == "" {
		t.Fatalf("missing first success: %s %s %v", state, sha, err)
	}
	if got := gitOutput(t, plan.Members[0].TargetPath, "merge-base", legacy.CommitSHA, "HEAD"); got != legacy.CommitSHA {
		t.Fatal("first commit not in target")
	}
	for _, cold := range []bool{false, true} {
		if cold {
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
		}
		p, e := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, legacyTask)
		if e != nil || p.TaskView == nil {
			t.Fatalf("projection: %v", e)
		}
		t.Logf("cold=%v legacy member=%s target=%s task status=%s summary=%s", cold, state, sha, p.TaskView.DevIntegrationStatus, p.TaskView.DevSummary)
		if p.TaskView.DevIntegrationStatus != "MERGED" || p.TaskView.MergedCommit != sha || p.TaskView.Details.MergedCommit != sha || p.TaskView.Details.MergedAt == nil || p.TaskView.CommitSHA != legacy.CommitSHA {
			t.Errorf("completed legacy member is still presented as unmerged (cold=%v)", cold)
		}
		inventory, e := f.facade.DevelopmentWorkspaceForSession(f.ctx, f.session, f.area, legacyTask)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, area := range inventory.FeatureAreas {
			if area.ID == f.area {
				found = true
				if area.LatestTaskID != next.task || area.LatestTaskDevIntegrationStatus != "UNMERGED" || area.LatestTaskMergedCommit != "" || area.CommitStatus != "UNCOMMITTED" {
					t.Fatalf("legacy integration leaked into the other Task or parent: %+v", area)
				}
			}
		}
		if !found {
			t.Fatal("area absent from inventory")
		}
		facts, e := f.plane.WorkflowProgression.CommitInventoryFacts(f.ctx, f.project, f.area)
		if e != nil {
			t.Fatal(e)
		}
		fact, ok := facts.Task(legacyTask)
		if !ok || fact.IntegrationState != "MERGED" || fact.MergedCommit != sha {
			t.Fatalf("inventory facts differ from detail: %+v", fact)
		}
		// Reading facts must not advance Task checkpoints or terminalize parent.
		stored, e := f.facade.GetTask(f.ctx, legacyTask)
		if e != nil || stored.MergedCommit != "" {
			t.Fatalf("read mutated scalar checkpoint: %+v %v", stored, e)
		}
		cap, e := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
		if e != nil || cap.CommitStatus != "UNCOMMITTED" || cap.MergeRecoveryState != "UNKNOWN" {
			t.Fatalf("parent control: %+v %v", cap, e)
		}
	}
}
