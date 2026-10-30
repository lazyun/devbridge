package controlapi_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executioncancel"
)

func TestTaskDeliveryCancellationFencesEachNewGitEffect(t *testing.T) {
	for _, stage := range []string{"before-claim", "claimed-before-git", "between-members"} {
		t.Run(stage, func(t *testing.T) {
			f, current := approveDirectFixture(t, false, true)
			state, result := exerciseDirectFileResult(t, f, current)
			before := map[string]string{}
			roots := map[string]string{}
			for _, m := range result.Members {
				roots[m.Target.RepositoryID] = m.WorktreePath
				before[m.Target.RepositoryID] = gitOutput(t, m.WorktreePath, "rev-parse", "HEAD")
			}
			effects, cancelCalls := 0, 0
			var first string
			cancel := func() (executioncancel.Receipt, error) {
				cancelCalls++
				return f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: f.task, RequestID: "delivery-cancel", Reason: "stop remaining delivery"})
			}
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, repo string) error {
				if stage == "before-claim" && kind == "TASK_COMMIT_BEFORE_CLAIM" && cancelCalls == 0 {
					receipt, err := cancel()
					if err != nil || receipt.State != "CANCELED" {
						t.Fatalf("cancel before claim: %+v %v", receipt, err)
					}
				}
				if stage == "claimed-before-git" && kind == "TASK_COMMIT_BEFORE_GIT" && cancelCalls == 0 {
					receipt, err := cancel()
					if err == nil || !strings.Contains(err.Error(), "RECOVERY_REQUIRED") || receipt.State == "CANCELED" {
						t.Fatalf("unsettled effect was treated as no backend: %+v %v", receipt, err)
					}
					var n int
					if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM execution_cancellations WHERE owner_type='TASK' AND owner_id=?`, f.task).Scan(&n); err != nil || n != 0 {
						t.Fatalf("unsettled cancellation wrote success receipt n=%d %v", n, err)
					}
				}
				if kind == "TASK_COMMIT_AFTER_GIT" {
					effects++
					if first == "" {
						first = repo
					}
					if stage != "before-claim" && effects == 1 {
						receipt, err := cancel()
						if err != nil || receipt.State != "CANCELED" {
							t.Fatalf("settled cancellation: %+v %v", receipt, err)
						}
					}
				}
				return nil
			})
			accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "accept-delivery-cancel", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
			if err != nil || !accepted.Accepted || accepted.CommitError == "" {
				t.Fatalf("accept %+v %v", accepted, err)
			}
			want := 1
			if stage == "before-claim" {
				want = 0
			}
			if effects != want {
				t.Fatalf("Git effects=%d want %d", effects, want)
			}
			for repo, path := range roots {
				head := gitOutput(t, path, "rev-parse", "HEAD")
				if repo == first {
					if head == before[repo] {
						t.Fatal("completed member lost commit")
					}
				} else if head != before[repo] {
					t.Fatalf("new commit after cancellation %s %s -> %s", repo, before[repo], head)
				}
			}
			var status string
			if err = f.plane.DB.SQL().QueryRow(`SELECT status FROM tasks WHERE id=?`, f.task).Scan(&status); err != nil || status != "CANCELED" {
				t.Fatalf("task %s %v", status, err)
			}
			if _, err = f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task); err == nil {
				t.Fatal("canceled task retried delivery")
			}
		})
	}
}

func TestTaskDeliveryUnknownDispatchNeverRepeatsGit(t *testing.T) {
	for _, stage := range []string{"TASK_COMMIT_BEFORE_GIT", "TASK_COMMIT_BEFORE_RESULT"} {
		t.Run(stage, func(t *testing.T) {
			f, current := approveDirectFixture(t, false, true)
			state, result := exerciseDirectFileResult(t, f, current)
			var first, path, head string
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, repo string) error {
				if kind == stage && first == "" {
					first = repo
					for _, m := range result.Members {
						if m.Target.RepositoryID == repo {
							path = m.WorktreePath
							head = gitOutput(t, path, "rev-parse", "HEAD")
						}
					}
					return errors.New("injected unknown delivery outcome")
				}
				return nil
			})
			accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "accept-unknown-delivery", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
			if err != nil || !strings.Contains(accepted.CommitError, "injected unknown") {
				t.Fatalf("accept %+v %v", accepted, err)
			}
			receipt, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: f.task, RequestID: "cancel-unknown", Reason: "stop"})
			if err == nil || receipt.State == "CANCELED" {
				t.Fatalf("unknown effect falsely quiescent: %+v %v", receipt, err)
			}
			reopenDirectDeliveryFixture(t, f)
			_, err = f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task)
			if stage == "TASK_COMMIT_BEFORE_GIT" && err == nil {
				t.Fatal("unknown admission without result redispatched")
			}
			if stage == "TASK_COMMIT_BEFORE_RESULT" && err != nil {
				t.Fatalf("exact committed Git result not observed: %v", err)
			}
			if after := gitOutput(t, path, "rev-parse", "HEAD"); after != head {
				t.Fatalf("unknown effect repeated Git: %s -> %s", head, after)
			}
		})
	}
}
