package controlapi_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/developmentcycle"
)

func TestTaskDeliveryProtectsContextUntilRetryCompletes(t *testing.T) {
	for _, reselect := range []bool{false, true} {
		name := "control"
		if reselect {
			name = "reselect"
		}
		t.Run(name, func(t *testing.T) {
			f, path, content, candidate := rejectedDeliveryV118(t)
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			var oldCycle, ref string
			if err := f.plane.DB.SQL().QueryRow(`SELECT id,development_ref FROM development_cycles WHERE repository_id=? AND superseded_at IS NULL`, candidate.RepositoryID).Scan(&oldCycle, &ref); err != nil {
				t.Fatal(err)
			}
			if reselect {
				// Protect the rejected repository and the undispatched member.
				rows, e := f.plane.DB.SQL().Query(`SELECT repository_id FROM task_commit_members WHERE task_id=?`, f.task)
				if e != nil {
					t.Fatal(e)
				}
				var repos []string
				for rows.Next() {
					var repo string
					if e = rows.Scan(&repo); e != nil {
						t.Fatal(e)
					}
					repos = append(repos, repo)
				}
				if e = rows.Err(); e != nil {
					t.Fatal(e)
				}
				rows.Close()
				for _, repo := range repos {
					var cycleID, cycleRef, originalRequest string
					if e = f.plane.DB.SQL().QueryRow(`SELECT id,development_ref,request_id FROM development_cycles WHERE repository_id=? AND superseded_at IS NULL`, repo).Scan(&cycleID, &cycleRef, &originalRequest); e != nil {
						t.Fatal(e)
					}
					in := developmentcycle.SelectInput{AISessionID: f.session, ProjectID: f.project, RepositoryID: repo, DevelopmentRef: cycleRef, RequestID: "audit-task-reselect-" + repo}
					if _, e = f.facade.SelectDevelopmentBranchForSession(f.ctx, in); !errors.Is(e, developmentcycle.ErrFinalizationPending) {
						t.Fatalf("missing pending Task fence: %v", e)
					}
					if _, e = f.plane.DB.SQL().Exec(`UPDATE development_cycles SET superseded_at=created_at,end_reason='BRANCH_RESELECTED' WHERE id=?`, cycleID); e == nil {
						t.Fatal("database fence bypassed")
					}
					in.RequestID = originalRequest
					if replay, e := f.facade.SelectDevelopmentBranchForSession(f.ctx, in); e != nil || replay.ID != cycleID {
						t.Fatalf("historical selection replay: %+v %v", replay, e)
					}
				}
			}
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
			_, err := f.facade.ReprepareAcceptedTaskCommit(f.ctx, f.session, delivery.RetryInput{TaskID: f.task, RepositoryID: candidate.RepositoryID, PredecessorOperationID: candidate.OperationID, RequestID: "audit-task-retry"})
			if err != nil {
				candidates, ce := f.facade.TaskDeliveryRetryCandidates(f.ctx, f.session, f.task)
				cap, pe := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
				t.Fatalf("retry stranded after context replacement: retry=%v candidates=%+v candidateErr=%v capability=%+v capabilityErr=%v", err, candidates, ce, cap, pe)
			}
			cycle, e := f.facade.SelectDevelopmentBranchForSession(f.ctx, developmentcycle.SelectInput{AISessionID: f.session, ProjectID: f.project, RepositoryID: candidate.RepositoryID, DevelopmentRef: ref, RequestID: "after-task-completion"})
			if e != nil || cycle.ID == oldCycle {
				t.Fatalf("completed delivery still fenced: %+v %v", cycle, e)
			}
		})
	}
}

func TestTaskAppliedOutcomeProjectsBeforeWorkspaceValidation(t *testing.T) {
	for _, name := range []string{"control", "changed-root", "offline", "existing-recovery"} {
		drift := name == "changed-root" || name == "existing-recovery"
		t.Run(name, func(t *testing.T) {
			f, current := approveDirectFixture(t, false, true)
			state, result := exerciseDirectFileResult(t, f, current)
			f.plane.Retirement.Close()
			count := 0
			var affectedRepo string
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, repo string) error {
				if stage == "TASK_COMMIT_AFTER_GIT" {
					count++
					if count == 2 {
						affectedRepo = repo
						return errors.New("audit lost projection")
					}
				}
				return nil
			})
			accepted := acceptDeliveryV118(t, f, current, state, result)
			if !strings.Contains(accepted.CommitError, "audit lost projection") {
				t.Fatalf("fixture: %+v", accepted)
			}
			var path, outcome, sha, memberState string
			if err := f.plane.DB.SQL().QueryRow(`SELECT m.workspace_path,r.outcome,r.commit_oid,m.state FROM task_commit_members m JOIN task_delivery_current_operations c ON c.task_id=m.task_id AND c.repository_id=m.repository_id JOIN task_delivery_operation_outcomes r ON r.operation_id=c.operation_id WHERE m.task_id=? AND m.repository_id=?`, f.task, affectedRepo).Scan(&path, &outcome, &sha, &memberState); err != nil || outcome != "APPLIED" || memberState != "PENDING" {
				t.Fatalf("not pending exact success: %s %s %v", outcome, memberState, err)
			}
			before := gitOutput(t, path, "rev-parse", "HEAD")
			if before != sha {
				t.Fatal("receipt mismatch")
			}
			restore := func() {}
			if drift {
				entries, e := os.ReadDir(path)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(path, path+"-saved"); e != nil {
					t.Fatal(e)
				}
				if e = os.Mkdir(path, 0700); e != nil {
					t.Fatal(e)
				}
				for _, entry := range entries {
					if e = os.Rename(filepath.Join(path+"-saved", entry.Name()), filepath.Join(path, entry.Name())); e != nil {
						t.Fatal(e)
					}
				}
				restored := false
				restore = func() {
					if restored {
						return
					}
					restored = true
					for _, entry := range entries {
						if e = os.Rename(filepath.Join(path, entry.Name()), filepath.Join(path+"-saved", entry.Name())); e != nil {
							t.Fatal(e)
						}
					}
					if e = os.Remove(path); e != nil {
						t.Fatal(e)
					}
					if e = os.Rename(path+"-saved", path); e != nil {
						t.Fatal(e)
					}
				}
				defer restore()
				if head := gitOutput(t, path, "rev-parse", "HEAD"); head != sha {
					t.Fatal("Git content changed")
				}
			}
			if name == "offline" {
				if e := os.Rename(path, path+"-offline"); e != nil {
					t.Fatal(e)
				}
				restore = func() {
					if e := os.Rename(path+"-offline", path); e != nil {
						t.Fatal(e)
					}
				}
			}
			if name == "existing-recovery" {
				// Reproduce the historical phase written by the old root check.
				if _, e := f.plane.DB.SQL().Exec(`UPDATE tasks SET phase='RECOVERY_REQUIRED' WHERE id=?`, f.task); e != nil {
					t.Fatal(e)
				}
			}
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
			if name == "existing-recovery" {
				cap, e := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
				if e != nil || !cap.CanRetry || cap.RetryTaskID != f.task {
					t.Fatalf("saved success has no recovery action: %+v %v", cap, e)
				}
			}
			var beforeCounts string
			query := `SELECT (SELECT count(*) FROM task_delivery_operations)||'/'||(SELECT count(*) FROM task_delivery_operation_dispatches)||'/'||(SELECT count(*) FROM task_delivery_operation_outcomes)`
			if e := f.plane.DB.SQL().QueryRow(query).Scan(&beforeCounts); e != nil {
				t.Fatal(e)
			}
			got, err := f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task)
			if name == "offline" {
				restore()
			}
			if err != nil || string(got.Status) != "SUCCEEDED" {
				var detail string
				f.plane.DB.SQL().QueryRow(`SELECT w.status||'/'||w.runtime_quarantined||'/'||wt.status||'/'||m.state FROM task_commit_members m JOIN workspaces w ON w.id=m.workspace_id JOIN worktrees wt ON wt.id=m.worktree_id WHERE m.task_id=? AND m.repository_id=?`, f.task, affectedRepo).Scan(&detail)
				t.Fatalf("exact APPLIED not projected: status=%s err=%v durable=%s", got.Status, err, detail)
			}
			var afterCounts string
			if e := f.plane.DB.SQL().QueryRow(query).Scan(&afterCounts); e != nil || afterCounts != beforeCounts {
				t.Fatalf("projection dispatched work: %s -> %s %v", beforeCounts, afterCounts, e)
			}
			if head := gitOutput(t, path, "rev-parse", "HEAD"); head != sha {
				t.Fatal("projection changed Git")
			}
			if _, err = f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task); err != nil {
				t.Fatalf("completed replay: %v", err)
			}
		})
	}
}

func TestTaskReceiptOnlyRecoveryCannotDispatchMissingOutcomes(t *testing.T) {
	for _, stage := range []string{"TASK_COMMIT_BEFORE_CLAIM", "TASK_COMMIT_BEFORE_RESULT"} {
		t.Run(stage, func(t *testing.T) {
			f, current := approveDirectFixture(t, false, true)
			state, result := exerciseDirectFileResult(t, f, current)
			f.plane.Retirement.Close()
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, _ string) error {
				if kind == stage {
					return errors.New("missing exact outcome")
				}
				return nil
			})
			accepted := acceptDeliveryV118(t, f, current, state, result)
			if !strings.Contains(accepted.CommitError, "missing exact outcome") {
				t.Fatalf("fixture %+v", accepted)
			}
			if _, err := f.plane.DB.SQL().Exec(`UPDATE tasks SET phase='RECOVERY_REQUIRED' WHERE id=?`, f.task); err != nil {
				t.Fatal(err)
			}
			heads := map[string]string{}
			for _, m := range result.Members {
				heads[m.WorktreePath] = gitOutput(t, m.WorktreePath, "rev-parse", "HEAD")
			}
			query := `SELECT (SELECT count(*) FROM task_delivery_operations)||'/'||(SELECT count(*) FROM task_delivery_operation_dispatches)||'/'||(SELECT count(*) FROM task_delivery_operation_outcomes)`
			var before string
			if err := f.plane.DB.SQL().QueryRow(query).Scan(&before); err != nil {
				t.Fatal(err)
			}
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
			if _, err := f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task); err == nil {
				t.Fatal("missing outcome authorized completion")
			}
			var after, status string
			if err := f.plane.DB.SQL().QueryRow(query).Scan(&after); err != nil || before != after {
				t.Fatalf("recovery wrote operation evidence %s -> %s %v", before, after, err)
			}
			if err := f.plane.DB.SQL().QueryRow(`SELECT status FROM tasks WHERE id=?`, f.task).Scan(&status); err != nil || status != "ACTIVE" {
				t.Fatalf("task completed without evidence: %s %v", status, err)
			}
			for path, head := range heads {
				if gitOutput(t, path, "rev-parse", "HEAD") != head {
					t.Fatal("receipt-only recovery changed Git")
				}
			}
		})
	}
}
