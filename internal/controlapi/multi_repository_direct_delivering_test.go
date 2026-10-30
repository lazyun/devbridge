package controlapi_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/task"
)

func directDeliveryFault(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	effects := 0
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, _ string) error {
		if kind == "TASK_COMMIT_AFTER_GIT" {
			effects++
			if effects == 2 {
				return errors.New("injected member receipt loss")
			}
		}
		return nil
	})
}

func acceptDirectWithFault(t *testing.T, f *upperLifecycleFixture, current task.Task, state directfile.State, resultID, requestID string) directfile.ImplementationResult {
	t.Helper()
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{
		AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID,
		WorkflowID: state.WorkflowID, ResultID: resultID, RequestID: requestID, ProtocolVersion: 2,
		RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration,
	})
	if err != nil || !accepted.Accepted || !strings.Contains(accepted.CommitError, "injected member receipt loss") {
		logDeliveryErrorChain(t, err)
		t.Fatalf("faulted accept result=%+v err=%v", accepted, err)
	}
	return accepted
}

type directMemberRow struct {
	repositoryID, path, branch, expectedHead, state, resultingCommit string
}

func readDirectMembers(t *testing.T, f *upperLifecycleFixture) []directMemberRow {
	t.Helper()
	rows, err := f.plane.DB.SQL().Query(`SELECT repository_id,workspace_path,branch,expected_head,state,resulting_commit FROM task_commit_members WHERE task_id=? ORDER BY repository_id`, f.task)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var members []directMemberRow
	for rows.Next() {
		var value directMemberRow
		if err = rows.Scan(&value.repositoryID, &value.path, &value.branch, &value.expectedHead, &value.state, &value.resultingCommit); err != nil {
			t.Fatal(err)
		}
		members = append(members, value)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return members
}

func taskLifecycleColumns(t *testing.T, f *upperLifecycleFixture) (string, string) {
	t.Helper()
	var status, phase string
	if err := f.plane.DB.SQL().QueryRow(`SELECT status,phase FROM tasks WHERE id=?`, f.task).Scan(&status, &phase); err != nil {
		t.Fatal(err)
	}
	return status, phase
}

func parentReceiptComplete(t *testing.T, f *upperLifecycleFixture) bool {
	t.Helper()
	var done bool
	if err := f.plane.DB.SQL().QueryRow(`SELECT completed_at IS NOT NULL FROM task_commit_receipts WHERE task_id=? AND protocol_version=2`, f.task).Scan(&done); err != nil {
		t.Fatal(err)
	}
	return done
}

// Partial multi-member delivery must keep the Task ACTIVE+DELIVERING. Once the
// exact receipt recovers, one completion transaction terminalizes it and the
// already-committed member effect is never replayed.
func TestMultiRepositoryDirectDeliveryStaysDeliveringUntilAllMembersComplete(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	state, result := exerciseDirectFileResult(t, f, current)
	directDeliveryFault(t, f)
	acceptDirectWithFault(t, f, current, state, result.ID, "delivering-accept")

	status, phase := taskLifecycleColumns(t, f)
	if status != string(task.StatusActive) || phase != string(task.PhaseDelivering) {
		t.Fatalf("partial delivery persisted %s/%s, want ACTIVE/DELIVERING", status, phase)
	}
	members := readDirectMembers(t, f)
	if len(members) != 2 || members[0].state != "COMPLETED" || members[1].state != "PENDING" {
		t.Fatalf("partial task receipts: %+v", members)
	}
	if members[0].resultingCommit == "" {
		t.Fatal("completed member lost its immutable commit")
	}
	if parentReceiptComplete(t, f) {
		t.Fatal("parent receipt completed with an unfinished member")
	}
	heads := map[string]string{}
	for _, value := range members {
		heads[value.repositoryID] = gitOutput(t, value.path, "rev-parse", "HEAD")
	}

	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
	recovered, err := f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task)
	if err != nil {
		logDeliveryErrorChain(t, err)
		t.Fatalf("recover partial delivery: %v", err)
	}
	if recovered.Status != task.StatusSucceeded || recovered.Phase != task.PhaseDelivering {
		t.Fatalf("recovered task=%+v", recovered)
	}
	if !parentReceiptComplete(t, f) {
		t.Fatal("parent receipt remained incomplete after all members finished")
	}
	for _, value := range readDirectMembers(t, f) {
		if value.state != "COMPLETED" {
			t.Fatalf("member did not finish: %+v", value)
		}
		if got := gitOutput(t, value.path, "rev-parse", "HEAD"); got != heads[value.repositoryID] {
			t.Fatalf("repository %s was committed twice", value.repositoryID)
		}
		if count := gitOutput(t, value.path, "rev-list", "--count", value.expectedHead+"..HEAD"); count != "1" {
			t.Fatalf("repository %s effect count after recovery=%s", value.repositoryID, count)
		}
	}
}

// Live member validation gates new effects; exact success is projected without
// repeating Git, even when the checkout has since been recreated.
func TestMultiRepositoryDirectStaleMemberMarksTaskRecoveryRequired(t *testing.T) {
	for _, applied := range []bool{false, true} {
		name := "before-effect"
		if applied {
			name = "applied-before-projection"
		}
		t.Run(name, func(t *testing.T) {
			f, current := approveDirectFixture(t, false, true)
			state, result := exerciseDirectFileResult(t, f, current)
			calls := 0
			stage := "TASK_COMMIT_BEFORE_CLAIM"
			if applied {
				stage = "TASK_COMMIT_AFTER_GIT"
			}
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, _ string) error {
				if kind == stage {
					calls++
					if calls == 2 {
						return errors.New("injected member receipt loss")
					}
				}
				return nil
			})
			acceptDirectWithFault(t, f, current, state, result.ID, "stale-accept")
			members := readDirectMembers(t, f)
			if len(members) != 2 || members[0].state != "COMPLETED" || members[1].state == "COMPLETED" {
				t.Fatalf("partial members: %+v", members)
			}
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
			var repositoryPath string
			if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM repositories WHERE id=?`, members[1].repositoryID).Scan(&repositoryPath); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(members[1].path); err != nil {
				t.Fatal(err)
			}
			gitOutput(t, repositoryPath, "worktree", "prune")
			gitOutput(t, repositoryPath, "worktree", "add", members[1].path, members[1].branch)
			head := gitOutput(t, members[1].path, "rev-parse", "HEAD")
			firstCommit := members[0].resultingCommit
			got, err := f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task)
			if applied {
				if err != nil || got.Status != task.StatusSucceeded || !parentReceiptComplete(t, f) {
					t.Fatalf("APPLIED result not projected: %+v %v", got, err)
				}
			} else {
				if !errors.Is(err, git.ErrRecoveryRequired) {
					t.Fatalf("new effect accepted stale root: %v", err)
				}
				status, phase := taskLifecycleColumns(t, f)
				if status != "ACTIVE" || phase != "RECOVERY_REQUIRED" || parentReceiptComplete(t, f) {
					t.Fatalf("stale admission %s/%s", status, phase)
				}
				if _, err = f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task); !errors.Is(err, git.ErrRecoveryRequired) {
					t.Fatalf("missing proof recovered: %v", err)
				}
			}
			if gitOutput(t, members[1].path, "rev-parse", "HEAD") != head {
				t.Fatal("recovery repeated Git")
			}
			for _, m := range readDirectMembers(t, f) {
				if m.repositoryID == members[0].repositoryID && (m.state != "COMPLETED" || m.resultingCommit != firstCommit) {
					t.Fatal("previous completed member changed")
				}
			}
		})
	}
}
