package controlapi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/task"
)

func acceptDeliveryV118(t *testing.T, f *upperLifecycleFixture, current task.Task, state directfile.State, result directfile.ImplementationResult) directfile.ImplementationResult {
	t.Helper()
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "v118-accept", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil || !accepted.Accepted {
		t.Fatalf("accept: %+v %v", accepted, err)
	}
	return accepted
}

func TestDeliveryV118ConsoleRetryUsesProjectedAuthorityAndColdReplay(t *testing.T) {
	f, path, content, candidate := rejectedDeliveryV118(t)
	ctx := operatorActionContext(t, f.project, f.task, "delivery-retry-production", "RETRY_TASK_COMMIT")
	reader, err := consoleapi.New(f.plane, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.GetProductTask(ctx, f.project, f.task)
	if err != nil {
		t.Fatal(err)
	}
	var input consoleapi.TaskActionInput
	for _, cap := range page.OperatorActions {
		if cap.Action == "RETRY_TASK_COMMIT" && cap.DomainAvailable {
			input = consoleapi.TaskActionInput{Action: cap.Action, Target: cap.Target, RequestID: "console-new-retry"}
		}
	}
	if input.Target == nil || input.Target.RepositoryID != candidate.RepositoryID || input.Target.OperationID != candidate.OperationID {
		t.Fatalf("lost retry target %+v", page.OperatorActions)
	}
	if err = os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	bad := input
	target := *input.Target
	target.RepositoryID = "foreign"
	bad.Target = &target
	if _, err = reader.ResolveTaskAction(ctx, f.project, f.task, bad); err == nil {
		t.Fatal("foreign member accepted")
	}
	var count int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_delivery_operations WHERE task_id=?`, f.task).Scan(&count); err != nil || count != 1 {
		t.Fatalf("foreign target wrote operation %d %v", count, err)
	}
	value, err := reader.ResolveTaskAction(ctx, f.project, f.task, input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := value.(delivery.RetryReceipt)
	expectedRequest, requestErr := localoperator.RequestID(ctx, f.project, f.task, "RETRY_TASK_COMMIT", input.RequestID)
	if !ok || requestErr != nil || receipt.RequestID != expectedRequest || receipt.OperationID == candidate.OperationID || receipt.PredecessorOperationID != candidate.OperationID {
		t.Fatalf("operator receipt %+v %v", value, requestErr)
	}
	reopenDirectDeliveryFixture(t, f)
	reader, err = consoleapi.New(f.plane, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM task_delivery_operations)||'/'||(SELECT count(*) FROM task_delivery_operation_dispatches)||'/'||(SELECT count(*) FROM task_delivery_operation_outcomes)`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	replay, err := reader.ResolveTaskAction(ctx, f.project, f.task, input)
	if err != nil || !reflect.DeepEqual(receipt, replay) {
		t.Fatalf("cold Console replay %+v %v", replay, err)
	}
	var after string
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM task_delivery_operations)||'/'||(SELECT count(*) FROM task_delivery_operation_dispatches)||'/'||(SELECT count(*) FROM task_delivery_operation_outcomes)`).Scan(&after); err != nil || before != after {
		t.Fatalf("replay wrote %s -> %s %v", before, after, err)
	}
}

func TestDeliveryV118CanceledParentReleasesArea(t *testing.T) {
	for _, stage := range []string{"TASK_COMMIT_BEFORE_CLAIM", "TASK_COMMIT_AFTER_GIT"} {
		t.Run(stage, func(t *testing.T) {
			f, current := approveDirectFixture(t, false, true)
			state, result := exerciseDirectFileResult(t, f, current)
			f.plane.Retirement.Close()
			in := executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: f.task, RequestID: "v118-cancel", Reason: "Explicitly abandon remaining delivery"}
			var receipt executioncancel.Receipt
			var completedRepo, completedPath, completedHead string
			calls := 0
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, repo string) error {
				if kind != stage || calls != 0 {
					return nil
				}
				calls++
				if stage == "TASK_COMMIT_AFTER_GIT" {
					completedRepo = repo
					for _, m := range result.Members {
						if m.Target.RepositoryID == repo {
							completedPath = m.WorktreePath
						}
					}
					completedHead = gitOutput(t, completedPath, "rev-parse", "HEAD")
				}
				var err error
				receipt, err = f.facade.CancelExecutionForSession(f.ctx, in)
				if err != nil || receipt.State != "CANCELED" {
					t.Fatalf("cancel: %+v %v", receipt, err)
				}
				return nil
			})
			accepted := acceptDeliveryV118(t, f, current, state, result)
			if accepted.CommitError == "" || calls != 1 {
				t.Fatalf("interrupted delivery %+v calls=%d", accepted, calls)
			}
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
			var outcome, abandonment, memberJSON string
			if err := f.plane.DB.SQL().QueryRow(`SELECT q.terminal_outcome,a.request_id,a.members_json FROM task_commit_receipts q JOIN task_delivery_abandonments a ON a.task_id=q.task_id WHERE q.task_id=? AND q.completed_at IS NOT NULL`, f.task).Scan(&outcome, &abandonment, &memberJSON); err != nil || outcome != "ABANDONED" || abandonment != in.RequestID {
				t.Fatalf("closed %s %s %v", outcome, abandonment, err)
			}
			if completedRepo != "" {
				var sha, state string
				if err := f.plane.DB.SQL().QueryRow(`SELECT resulting_commit,state FROM task_commit_members WHERE task_id=? AND repository_id=?`, f.task, completedRepo).Scan(&sha, &state); err != nil || state != "COMPLETED" || sha != completedHead {
					t.Fatalf("lost completed member %s %s %v", state, sha, err)
				}
			}
			replay, err := f.facade.CancelExecutionForSession(f.ctx, in)
			if err != nil || !reflect.DeepEqual(receipt, replay) {
				t.Fatalf("cancel replay %+v %v", replay, err)
			}
			if _, err = f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task); !errors.Is(err, delivery.ErrAbandoned) {
				t.Fatalf("abandoned delivery resumed: %v", err)
			}
			reopenDirectDeliveryFixture(t, f)
			replay, err = f.facade.CancelExecutionForSession(f.ctx, in)
			if err != nil || !reflect.DeepEqual(receipt, replay) {
				t.Fatalf("cold cancellation %+v %v", replay, err)
			}
			if _, err = f.facade.RetireFeatureArea(f.ctx, f.session, f.area, "v118-retire-after-cancel"); err != nil {
				t.Fatalf("settled cancellation blocked retirement: %v", err)
			}
			f.plane.Retirement.Advance(f.ctx, f.area)
			var status, after string
			if err = f.plane.DB.SQL().QueryRow(`SELECT status FROM feature_areas WHERE id=?`, f.area).Scan(&status); err != nil || status != "RETIRED" {
				t.Fatalf("retired %s %v", status, err)
			}
			if err = f.plane.DB.SQL().QueryRow(`SELECT members_json FROM task_delivery_abandonments WHERE task_id=?`, f.task).Scan(&after); err != nil || after != memberJSON {
				t.Fatalf("abandonment changed %v", err)
			}
			if completedPath != "" && gitOutput(t, completedPath, "rev-parse", "HEAD") != completedHead {
				t.Fatal("completed Git history changed")
			}
		})
	}
}

func rejectedDeliveryV118(t *testing.T) (*upperLifecycleFixture, string, []byte, delivery.RetryCandidate) {
	t.Helper()
	return rejectedDeliveryV118After(t, 0)
}

func rejectedDeliveryV118After(t *testing.T, completed int) (*upperLifecycleFixture, string, []byte, delivery.RetryCandidate) {
	t.Helper()
	f, current := approveDirectFixture(t, false, true)
	state, result := exerciseDirectFileResult(t, f, current)
	f.plane.Retirement.Close()
	var path string
	var content []byte
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, repo string) error {
		if kind != "TASK_COMMIT_BEFORE_GIT" || path != "" {
			return nil
		}
		if completed > 0 {
			completed--
			return nil
		}
		for _, m := range result.Members {
			if m.Target.RepositoryID == repo {
				path = filepath.Join(m.WorktreePath, "same.txt")
			}
		}
		var err error
		content, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("external drift\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	accepted := acceptDeliveryV118(t, f, current, state, result)
	if !strings.Contains(accepted.CommitError, "DELIVERY_REPREPARE_REQUIRED") || !strings.Contains(accepted.CommitError, "TASK_COMMIT_SNAPSHOT_DRIFT") {
		t.Fatalf("not exact rejected %+v", accepted)
	}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
	candidates, err := f.facade.TaskDeliveryRetryCandidates(f.ctx, f.session, f.task)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates %+v %v", candidates, err)
	}
	return f, path, content, candidates[0]
}

func TestDeliveryV118RetryPreservesCompletedMember(t *testing.T) {
	f, path, content, candidate := rejectedDeliveryV118After(t, 1)
	var repo, head, before string
	if err := f.plane.DB.SQL().QueryRow(`SELECT repository_id,resulting_commit,state||'/'||resulting_commit||'/'||completed_at FROM task_commit_members WHERE task_id=? AND state='COMPLETED'`, f.task).Scan(&repo, &head, &before); err != nil || repo == candidate.RepositoryID || head == "" {
		t.Fatalf("missing prior member %s %s %v", repo, head, err)
	}
	var oldOperation string
	if err := f.plane.DB.SQL().QueryRow(`SELECT operation_id FROM task_delivery_current_operations WHERE task_id=? AND repository_id=?`, f.task, repo).Scan(&oldOperation); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := f.facade.ReprepareAcceptedTaskCommit(f.ctx, f.session, delivery.RetryInput{TaskID: f.task, RepositoryID: candidate.RepositoryID, PredecessorOperationID: candidate.OperationID, RequestID: "retry-second-member"})
	if err != nil {
		t.Fatal(err)
	}
	var after, currentOperation string
	if err = f.plane.DB.SQL().QueryRow(`SELECT state||'/'||resulting_commit||'/'||completed_at FROM task_commit_members WHERE task_id=? AND repository_id=?`, f.task, repo).Scan(&after); err != nil || before != after {
		t.Fatalf("completed member rewritten: %s -> %s %v", before, after, err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT operation_id FROM task_delivery_current_operations WHERE task_id=? AND repository_id=?`, f.task, repo).Scan(&currentOperation); err != nil || oldOperation != currentOperation {
		t.Fatalf("completed member redispatched: %s -> %s %v", oldOperation, currentOperation, err)
	}
}

func TestDeliveryV118RejectedCommitUsesNewAuthority(t *testing.T) {
	f, path, content, candidate := rejectedDeliveryV118(t)
	in := delivery.RetryInput{TaskID: f.task, RepositoryID: candidate.RepositoryID, PredecessorOperationID: candidate.OperationID, RequestID: "v118-new-retry"}
	if _, err := f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task); !errors.Is(err, delivery.ErrRejected) {
		t.Fatalf("old retry must only read rejection: %v", err)
	}
	if _, err := f.facade.ReprepareAcceptedTaskCommit(f.ctx, f.session, in); err == nil {
		t.Fatal("drifted workset got new authority")
	}
	var before int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_delivery_operations WHERE task_id=?`, f.task).Scan(&before); err != nil || before != 1 {
		t.Fatalf("rejected preflight wrote operation %d %v", before, err)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.facade.ReprepareAcceptedTaskCommit(f.ctx, f.session, in)
	if err != nil || receipt.OperationID == candidate.OperationID || receipt.RequestID != in.RequestID {
		t.Fatalf("new retry %+v %v", receipt, err)
	}
	var outcome, oldPayload, newPayload, status string
	if err = f.plane.DB.SQL().QueryRow(`SELECT r.outcome,o.payload_json,n.payload_json,t.status FROM task_delivery_operations o JOIN task_delivery_operation_outcomes r ON r.operation_id=o.operation_id JOIN task_delivery_operations n ON n.predecessor_operation_id=o.operation_id JOIN tasks t ON t.id=o.task_id WHERE o.operation_id=?`, candidate.OperationID).Scan(&outcome, &oldPayload, &newPayload, &status); err != nil || outcome != "REJECTED_NOT_CONSUMED" || oldPayload != newPayload || status != "SUCCEEDED" {
		t.Fatalf("lineage/outcome %s %s %v", outcome, status, err)
	}
	if _, err = f.facade.ReprepareAcceptedTaskCommit(f.ctx, f.session, delivery.RetryInput{TaskID: f.task, RepositoryID: candidate.RepositoryID, PredecessorOperationID: candidate.OperationID, RequestID: "v118-second-request"}); !errors.Is(err, delivery.ErrRetryConflict) {
		t.Fatalf("second successor %v", err)
	}
	bad := in
	bad.RepositoryID = "foreign"
	if _, err = f.facade.ReprepareAcceptedTaskCommit(f.ctx, f.session, bad); !errors.Is(err, delivery.ErrRequestConflict) {
		t.Fatalf("payload replay %v", err)
	}
	var countsBefore string
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM task_delivery_operations)||'/'||(SELECT count(*) FROM task_delivery_operation_dispatches)||'/'||(SELECT count(*) FROM task_delivery_operation_outcomes)`).Scan(&countsBefore); err != nil {
		t.Fatal(err)
	}
	reopenDirectDeliveryFixture(t, f)
	assertDeliveryReplayReadOnly(t, f, func(ctx context.Context) {
		replay, err := f.facade.ReprepareAcceptedTaskCommit(ctx, f.session, in)
		if err != nil || replay != receipt {
			t.Fatalf("read-only task retry replay: %+v %v", replay, err)
		}
		changed := in
		changed.RepositoryID = "wrong-repository"
		if _, err = f.facade.ReprepareAcceptedTaskCommit(ctx, f.session, changed); !errors.Is(err, delivery.ErrRequestConflict) {
			t.Fatalf("changed task retry: %v", err)
		}
		fresh := in
		fresh.RequestID = "new-request-without-writer"
		if _, err = f.facade.ReprepareAcceptedTaskCommit(ctx, f.session, fresh); err == nil {
			t.Fatal("fresh delivery command bypassed writer fence")
		}

	})
	replay, err := f.facade.ReprepareAcceptedTaskCommit(f.ctx, f.session, in)
	if err != nil || !reflect.DeepEqual(receipt, replay) {
		t.Fatalf("cold replay %+v %v", replay, err)
	}
	var countsAfter string
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM task_delivery_operations)||'/'||(SELECT count(*) FROM task_delivery_operation_dispatches)||'/'||(SELECT count(*) FROM task_delivery_operation_outcomes)`).Scan(&countsAfter); err != nil || countsBefore != countsAfter {
		t.Fatalf("replay wrote %s -> %s %v", countsBefore, countsAfter, err)
	}
}

func TestDeliveryV118KnownRejectionCanBeCanceled(t *testing.T) {
	f, _, _, candidate := rejectedDeliveryV118(t)
	receipt, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: f.task, RequestID: "v118-cancel-rejected", Reason: "Abandon exact rejected effect"})
	if err != nil || receipt.State != "CANCELED" {
		t.Fatalf("cancel rejected %+v %v", receipt, err)
	}
	var outcome string
	if err = f.plane.DB.SQL().QueryRow(`SELECT outcome FROM task_delivery_operation_outcomes WHERE operation_id=?`, candidate.OperationID).Scan(&outcome); err != nil || outcome != "REJECTED_NOT_CONSUMED" {
		t.Fatalf("negative history changed %v", err)
	}
	if _, err = f.facade.RetireFeatureArea(f.ctx, f.session, f.area, "v118-retire-rejected"); err != nil {
		t.Fatalf("retire rejected: %v", err)
	}
}

func TestDeliveryV118UnknownCannotAuthorizeSuccessor(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	state, result := exerciseDirectFileResult(t, f, current)
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, repo string) error {
		if kind == "TASK_COMMIT_BEFORE_GIT" {
			return errors.New("unknown crash boundary")
		}
		return nil
	})
	accepted := acceptDeliveryV118(t, f, current, state, result)
	if !strings.Contains(accepted.CommitError, "unknown crash") {
		t.Fatal(accepted)
	}
	var operation, repo string
	if err := f.plane.DB.SQL().QueryRow(`SELECT operation_id,repository_id FROM task_delivery_operations WHERE task_id=?`, f.task).Scan(&operation, &repo); err != nil {
		t.Fatal(err)
	}
	candidates, err := f.facade.TaskDeliveryRetryCandidates(f.ctx, f.session, f.task)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("unknown advertised %+v %v", candidates, err)
	}
	_, err = f.facade.ReprepareAcceptedTaskCommit(f.ctx, f.session, delivery.RetryInput{TaskID: f.task, RepositoryID: repo, PredecessorOperationID: operation, RequestID: "v118-not-authorized"})
	if err == nil {
		t.Fatal("unknown got successor")
	}
	_, err = f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: f.task, RequestID: "v118-unknown-cancel", Reason: "Cannot prove settled"})
	if err == nil {
		t.Fatal("unknown canceled")
	}
	var n int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_delivery_operations WHERE task_id=?`, f.task).Scan(&n); err != nil || n != 1 {
		t.Fatalf("unknown successor %d %v", n, err)
	}
}
