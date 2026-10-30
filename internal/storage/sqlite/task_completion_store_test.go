package sqlite

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskCompletionAcceptsCommitsAndMergesBeforeSucceeded(t *testing.T) {
	for _, loss := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "merge-response-loss"}[loss], func(t *testing.T) {
			f, owner, e := acceptedExecutionFixture(t, "-complete-merge")
			e = terminalExecutionEvidence(t, f, e, run.VerificationCheckPassed, "", true)
			repo, source, base := attachCommitWorkspace(t, f, owner.ID, e.VerificationRunID)
			ctx := context.Background()
			plan, err := f.store.PrepareTaskCompletionPlan(ctx, f.project, owner.ID, e.ID, owner.CurrentDefinitionRevisionID)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Valid() || len(plan.Members) != 1 || plan.Members[0].ExpectedDevHead != base {
				t.Fatalf("bad frozen targets: %+v", plan)
			}
			called := false
			f.store.deliveryFaultHook = func(stage, repo string) error {
				if stage == "AREA_MERGE_BEFORE_GIT" {
					called = true
					if status, phase := deliveryTaskState(t, f, owner.ID); status != task.StatusActive || phase != task.PhaseDelivering {
						t.Fatalf("completed before merge: %s/%s", status, phase)
					}
				}
				if loss && stage == "AREA_MERGE_AFTER_GIT" {
					return errors.New("lost merge response")
				}
				return nil
			}
			in := wp.TaskAcceptanceInput{AISessionID: f.session, TaskID: owner.ID, FeatureAreaID: f.area, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, ExecutionID: e.ID, DecisionID: wp.TaskAcceptanceDecisionID(e.ID), Option: "ACCEPT", RequestID: "complete-one", CompletionPlan: &plan}
			out, err := f.store.ResolveTaskAcceptance(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatalf("merge not called: %+v", out)
			}
			if loss {
				if out.CommitError == "" {
					t.Fatal("fault lost")
				}
				if status, phase := deliveryTaskState(t, f, owner.ID); status != task.StatusActive || phase != task.PhaseDelivering {
					t.Fatalf("fault marked Task complete: %s/%s", status, phase)
				}
				f.store.deliveryFaultHook = nil
				out, err = f.store.ResolveTaskAcceptance(ctx, in)
				if err != nil {
					t.Fatal(err)
				}
			}
			if out.CommitError != "" || out.Task.Status != task.StatusSucceeded || out.Task.MergedAt == nil {
				t.Fatalf("completion not integrated: %+v", out)
			}
			head := commitTestGit(t, repo, "rev-parse", "HEAD")
			if head == base || commitTestGit(t, source, "rev-parse", "HEAD") == base {
				t.Fatal("missing commit/merge")
			}
			status, err := f.store.ReadTaskCompletion(ctx, f.project, owner.ID)
			if err != nil || status.State != "COMPLETED" {
				t.Fatalf("missing receipt: %+v %v", status, err)
			}
			replay, err := f.store.ResolveTaskAcceptance(ctx, in)
			if err != nil || replay.Task.Status != task.StatusSucceeded || commitTestGit(t, repo, "rev-parse", "HEAD") != head {
				t.Fatalf("replay mutated delivery: %+v %v", replay, err)
			}
			bad := in
			bad.CompletionPlan = nil
			if _, err = f.store.ResolveTaskAcceptance(ctx, bad); err == nil {
				t.Fatal("plain acceptance replay lost integration identity")
			}
		})
	}
}

func TestTaskCompletionRejectsFrozenSourceOrTargetDrift(t *testing.T) {
	for _, targetDrift := range []bool{false, true} {
		t.Run(map[bool]string{false: "source", true: "development-target"}[targetDrift], func(t *testing.T) {
			f, owner, e := acceptedExecutionFixture(t, "-completion-drift")
			e = terminalExecutionEvidence(t, f, e, run.VerificationCheckPassed, "", true)
			repo, source, _ := attachCommitWorkspace(t, f, owner.ID, e.VerificationRunID)
			ctx := context.Background()
			plan, err := f.store.PrepareTaskCompletionPlan(ctx, f.project, owner.ID, e.ID, owner.CurrentDefinitionRevisionID)
			if err != nil {
				t.Fatal(err)
			}
			path := source
			if targetDrift {
				path = repo
			}
			if err = os.WriteFile(filepath.Join(path, "drift.txt"), []byte("out of frozen plan\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if targetDrift {
				commitTestGit(t, path, "add", "drift.txt")
				commitTestGit(t, path, "commit", "-m", "concurrent development change")
			}
			_, err = f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, TaskID: owner.ID, FeatureAreaID: f.area, ExecutionID: e.ID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(e.ID), Option: "ACCEPT", RequestID: "stale-completion", CompletionPlan: &plan})
			if err == nil {
				t.Fatal("stale completion was accepted")
			}
			var count int
			if err = f.db.sql.QueryRow(`SELECT count(*) FROM task_completion_intents WHERE task_id=?`, owner.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("stale plan created completion intent: %d %v", count, err)
			}
		})
	}
}
