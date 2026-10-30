package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestTaskCompletionReleasedPlanRequiresConfirmedSuccessor(t *testing.T) {
	for _, mode := range []string{"complete", "response-loss", "second-release", "stale-confirmation", "concurrent-confirmations"} {
		t.Run(mode, func(t *testing.T) {
			f, owner, e := acceptedExecutionFixture(t, "-released-completion")
			e = terminalExecutionEvidence(t, f, e, run.VerificationCheckPassed, "", true)
			repo, _, _ := attachCommitWorkspace(t, f, owner.ID, e.VerificationRunID)
			ctx := context.Background()
			plan, err := f.store.PrepareTaskCompletionPlan(ctx, f.project, owner.ID, e.ID, owner.CurrentDefinitionRevisionID)
			if err != nil {
				t.Fatal(err)
			}
			advance := func(name string) {
				commitTestWrite(t, filepath.Join(repo, name), name+"\n")
				commitTestGit(t, repo, "add", name)
				commitTestGit(t, repo, "commit", "-m", name)
			}
			advanced := false
			f.store.deliveryFaultHook = func(stage, _ string) error {
				if stage == "AREA_MERGE_BEFORE_GIT" && !advanced {
					advanced = true
					advance("concurrent.txt")
				}
				return nil
			}
			in := wp.TaskAcceptanceInput{AISessionID: f.session, TaskID: owner.ID, FeatureAreaID: f.area, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, ExecutionID: e.ID, DecisionID: wp.TaskAcceptanceDecisionID(e.ID), Option: "ACCEPT", RequestID: "complete", CompletionPlan: &plan}
			out, err := f.store.ResolveTaskAcceptance(ctx, in)
			if err != nil || out.CommitError == "" {
				t.Fatalf("expected accepted but blocked merge: %+v %v", out, err)
			}
			f.store.deliveryFaultHook = nil
			var original string
			if err = f.db.sql.QueryRow(`SELECT plan_json||plan_sha256||acceptance_request_id||merge_request_id FROM task_completion_intents WHERE task_id=?`, owner.ID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			release := func(id string) {
				t.Helper()
				candidates, err := f.store.FeatureAreaMergeRetryCandidates(ctx, f.project, f.area)
				if err != nil || len(candidates) != 1 || !candidates[0].CanRelease {
					t.Fatalf("release candidates: %+v %v", candidates, err)
				}
				c := candidates[0]
				receipt, err := f.store.ReprepareFeatureAreaMerge(ctx, f.project, delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: c.MergeRequestID, RepositoryID: c.RepositoryID, PredecessorOperationID: c.OperationID, RequestID: id, ReleasePlan: true})
				if err != nil || !receipt.PlanReleased {
					t.Fatalf("release: %+v %v", receipt, err)
				}
			}
			release("release-first")
			state, err := f.store.ReadTaskCompletion(ctx, f.project, owner.ID)
			if err != nil || state.State != "REPLAN_REQUIRED" {
				t.Fatalf("missing replan state: %+v %v", state, err)
			}
			if _, err = f.store.FinishTaskCompletion(ctx, f.project, owner.ID); !errors.Is(err, delivery.ErrPlanReleased) {
				t.Fatalf("released plan replay: %v", err)
			}
			cap, err := f.store.CommitCapability(ctx, f.project, f.area)
			if err != nil || !cap.CanCommit || cap.CanRetry {
				t.Fatalf("missing new confirmation capability: %+v %v", cap, err)
			}
			successor, err := f.store.PrepareFeatureAreaDeliveryPlan(ctx, f.project, f.area)
			if err != nil || successor.CompletionTaskID != owner.ID || successor.PredecessorMergeRequestID != state.MergeRequestID {
				t.Fatalf("successor: %+v %v", successor, err)
			}
			head := commitTestGit(t, repo, "rev-parse", "HEAD")
			if successor.Members[0].ExpectedDevHead != head {
				t.Fatal("new target was not frozen")
			}
			if status, _ := deliveryTaskState(t, f, owner.ID); status != task.StatusActive {
				t.Fatal("prepare completed Task")
			}
			request := "confirmed-successor"
			if mode == "stale-confirmation" {
				advance("second.txt")
				if _, err = f.store.CommitFeatureAreaPlan(ctx, f.project, f.area, "stale-successor", successor); err == nil {
					t.Fatal("stale plan committed")
				}
				var count int
				if err = f.db.sql.QueryRow(`SELECT count(*) FROM feature_area_commit_receipts WHERE request_id='stale-successor'`).Scan(&count); err != nil || count != 0 {
					t.Fatal("stale prepare acquired receipt", err)
				}
				successor, err = f.store.PrepareFeatureAreaDeliveryPlan(ctx, f.project, f.area)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "response-loss" {
				f.store.deliveryFaultHook = func(stage, _ string) error {
					if stage == "AREA_MERGE_AFTER_GIT" {
						return errors.New("lost merge projection")
					}
					return nil
				}
				if _, err = f.store.CommitFeatureAreaPlan(ctx, f.project, f.area, request, successor); err == nil {
					t.Fatal("fault not reached")
				}
				if status, _ := deliveryTaskState(t, f, owner.ID); status != task.StatusActive {
					t.Fatal("completed before durable merge projection")
				}
				f.store.deliveryFaultHook = nil
				pending, readErr := f.store.ReadTaskCompletion(ctx, f.project, owner.ID)
				if readErr != nil || pending.MergeRequestID != request || pending.Error != "" {
					t.Fatalf("new plan inherited released parent error: %+v %v", pending, readErr)
				}
			}
			if mode == "second-release" {
				f.store.deliveryFaultHook = func(stage, _ string) error {
					if stage == "AREA_MERGE_BEFORE_GIT" {
						advance("third.txt")
					}
					return nil
				}
				if _, err = f.store.CommitFeatureAreaPlan(ctx, f.project, f.area, request, successor); err == nil {
					t.Fatal("second target change accepted")
				}
				f.store.deliveryFaultHook = nil
				release("release-second")
				successor, err = f.store.PrepareFeatureAreaDeliveryPlan(ctx, f.project, f.area)
				if err != nil {
					t.Fatal(err)
				}
				if successor.PredecessorMergeRequestID != request {
					t.Fatal("lost successor lineage")
				}
				request = "confirmed-second-successor"
			}
			if mode == "concurrent-confirmations" {
				type attempt struct {
					request string
					err     error
				}
				results := make(chan attempt, 2)
				for _, id := range []string{"competing-confirmation-one", "competing-confirmation-two"} {
					go func(id string) {
						_, failure := f.store.CommitFeatureAreaPlan(ctx, f.project, f.area, id, successor)
						results <- attempt{id, failure}
					}(id)
				}
				successes := 0
				for range 2 {
					result := <-results
					if result.err == nil {
						successes++
						request = result.request
					}
				}
				if successes != 1 {
					t.Fatalf("expected exactly one confirmed successor, got %d", successes)
				}
			} else if mode == "response-loss" {
				if _, err = f.store.FinishTaskCompletion(ctx, f.project, owner.ID); err != nil {
					t.Fatal(err)
				}
			} else if _, err = f.store.CommitFeatureAreaPlan(ctx, f.project, f.area, request, successor); err != nil {
				t.Fatal(err)
			}
			final, err := NewTaskStore(f.db).Get(ctx, owner.ID)
			if err != nil || final.Status != task.StatusSucceeded || final.MergedAt == nil {
				t.Fatalf("not completed: %+v %v", final, err)
			}
			merged := commitTestGit(t, repo, "rev-parse", "HEAD")
			if _, err = f.store.CommitFeatureAreaPlan(ctx, f.project, f.area, request, successor); err != nil || merged != commitTestGit(t, repo, "rev-parse", "HEAD") {
				t.Fatal("successor replay changed Git", err)
			}
			var after string
			if err = f.db.sql.QueryRow(`SELECT plan_json||plan_sha256||acceptance_request_id||merge_request_id FROM task_completion_intents WHERE task_id=?`, owner.ID).Scan(&after); err != nil || after != original {
				t.Fatal("rewrote original completion authority", err)
			}
			var count int
			if err = f.db.sql.QueryRow(`SELECT count(*) FROM decision_resolution_receipts WHERE decision_id=?`, in.DecisionID).Scan(&count); err != nil || count != 1 {
				t.Fatal("repeated acceptance", err)
			}
			state, err = f.store.ReadTaskCompletion(ctx, f.project, owner.ID)
			if err != nil || state.State != "COMPLETED" || state.MergeRequestID != request {
				t.Fatalf("lost current merge: %+v %v", state, err)
			}
		})
	}
}

func TestTaskCompletionSuccessorV132PreservesHistoricalDelivery(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "applied"}[applied], func(t *testing.T) {
			v := deliveryRecoveryV116Fixture(t, applied)
			ctx := context.Background()
			if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(131)); err != nil {
				t.Fatal(err)
			}
			history := deliveryMigrationHistoricalSnapshot(t, v.db)
			before := history()
			for range 2 {
				if err := ApplyMigrations(ctx, v.db.sql, migrationsThroughVersion(132)); err != nil {
					t.Fatal(err)
				}
				if history() != before {
					t.Fatal("migration rewrote history")
				}
			}
		})
	}
}
