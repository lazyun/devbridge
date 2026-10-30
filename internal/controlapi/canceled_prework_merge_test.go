package controlapi_test

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"strings"
	"testing"
)

func TestCanceledPreWorkTaskPreservesAreaMergeReadiness(t *testing.T) {
	for _, mode := range []string{"v1-before", "v2-before", "v1-after", "v2-after", "v1-unproven", "v2-unproven"} {
		t.Run(mode, func(t *testing.T) {
			protocol := strings.Split(mode, "-")[0]
			alreadyMerged := strings.HasSuffix(mode, "after")
			var f *upperLifecycleFixture
			if protocol == "v1" {
				f = mixedLegacyTask(t)
			} else {
				f = completeFollowupTask(t)
			}
			originalTask := f.task
			cap, e := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
			if e != nil || !cap.CanCommit {
				t.Fatalf("initial complete task cannot merge: %+v %v", cap, e)
			}
			if alreadyMerged {
				if _, e = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "original-merge"); e != nil {
					t.Fatal(e)
				}
			}
			prepared, e := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "unused-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: f.area, Title: "Withdrawn before review", ProblemStatement: "Unused follow-up", Constraints: []string{"no execution"}}, Variants: []c.Variant{{ID: "create", Summary: "Create Task"}}})
			if e != nil {
				t.Fatal(e)
			}
			created, e := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "create-unused-task", ContentHash: prepared.ContentHash, VariantID: "create"})
			if e != nil || created.State != "COMPLETED" {
				t.Fatalf("create: %+v %v", created, e)
			}
			unused := created.Steps[0].TaskID
			active, e := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
			if e != nil || active.CanCommit || active.CommitStatus != "UNCOMMITTED" {
				t.Fatalf("active Task no longer blocks: %+v %v", active, e)
			}
			unproven := strings.HasSuffix(mode, "unproven")
			if unproven {
				// Historical terminal status without an exact cancellation receipt
				// is not proof that no engineering effect occurred.
				if _, e = f.plane.DB.SQL().Exec(`UPDATE tasks SET status='CANCELED' WHERE id=?`, unused); e != nil {
					t.Fatal(e)
				}
			} else {
				receipt, e := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: unused, RequestID: "cancel-unused", Reason: "Withdraw before any review or implementation"})
				if e != nil || receipt.State != "CANCELED" {
					t.Fatalf("cancel: %+v %v", receipt, e)
				}
			}
			var runs, commits int
			if e = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM runs WHERE task_id=?),(SELECT count(*) FROM task_commit_receipts WHERE task_id=?)`, unused, unused).Scan(&runs, &commits); e != nil || runs != 0 || commits != 0 {
				t.Fatalf("unexpected work: runs=%d commits=%d err=%v", runs, commits, e)
			}
			for _, cold := range []bool{false, true} {
				if cold {
					reopenDirectDeliveryFixture(t, f)
					f.plane.Retirement.Close()
				}
				cap, e = f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
				if e != nil {
					t.Fatal(e)
				}
				t.Logf("cold=%v completed Task=%s canceled Task=%s runs=%d commits=%d capability=%+v", cold, originalTask, unused, runs, commits, cap)
				if alreadyMerged && cap.CommitStatus != "COMMITTED" {
					t.Errorf("canceled unused Task regressed completed area: %+v", cap)
				}
				if unproven && (cap.CanCommit || cap.Reason != "TASK_COMMIT_PENDING") {
					t.Fatalf("unproven terminal status bypassed admission: %+v", cap)
				}
				if !unproven && !alreadyMerged && !cap.CanCommit {
					t.Errorf("terminal no-effect Task blocks completed sibling merge (cold=%v): %s", cold, cap.Reason)
				}
			}
			current, e := f.facade.GetTask(f.ctx, unused)
			if e != nil || string(current.Status) != "CANCELED" || current.CommitSHA != "" {
				t.Fatalf("historical cancellation changed: %+v %v", current, e)
			}
			if unproven {
				if _, e = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "unproven-merge"); e == nil {
					t.Fatal("unproven cancellation merged")
				}
				return
			}
			if alreadyMerged {
				return
			}
			if _, e = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "merge-after-noop-cancel"); e != nil {
				t.Errorf("public merge blocked by untouched canceled Task: %v", e)
			}
		})
	}
}
