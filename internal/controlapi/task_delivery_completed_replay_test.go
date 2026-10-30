package controlapi_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
)

func TestCompletedMultiTaskDeliveryReceiptReplaySurvivesRestartAndMemberDrift(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	state, result := exerciseDirectFileResult(t, f, current)
	in := directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "completed-multi-accept", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, in)
	if err != nil || !accepted.Accepted || accepted.CommitError != "" {
		t.Fatalf("initial acceptance=%+v err=%v", accepted, err)
	}
	readReceipts := func() string {
		t.Helper()
		var parent, children string
		if err := f.plane.DB.SQL().QueryRow(`SELECT json_object('revision',revision_id,'workspace',workspace_id,'result',result_id,'protocol',protocol_version,'completed',completed_at,'error',last_error) FROM task_commit_receipts WHERE task_id=? AND completed_at IS NOT NULL`, f.task).Scan(&parent); err != nil {
			t.Fatal(err)
		}
		if err := f.plane.DB.SQL().QueryRow(`SELECT json_group_array(json_object('repository',repository_id,'member',member_id,'version',member_version,'worktree',worktree_id,'state',state,'commit',resulting_commit,'tree',commit_tree,'completed',completed_at,'error',last_error)) FROM (SELECT * FROM task_commit_members WHERE task_id=? ORDER BY repository_id)`, f.task).Scan(&children); err != nil {
			t.Fatal(err)
		}
		return parent + children
	}
	before := readReceipts()
	for _, phase := range []string{"completed", "cold-reopen", "member-and-workspace-drift"} {
		if phase == "cold-reopen" {
			reopenDirectDeliveryFixture(t, f)
		}
		if phase == "member-and-workspace-drift" {
			if _, err = f.plane.DB.SQL().Exec(`UPDATE workspaces SET runtime_quarantined=1 WHERE id IN (SELECT workspace_id FROM task_commit_receipts WHERE task_id=?)`, f.task); err != nil {
				t.Fatal(err)
			}
			if _, err = f.plane.DB.SQL().Exec(`UPDATE workspace_members SET preparation='FAILED' WHERE workspace_id IN (SELECT workspace_id FROM task_commit_receipts WHERE task_id=?)`, f.task); err != nil {
				t.Fatal(err)
			}
		}
		t.Run(phase, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "git-called")
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf called >> \"$DELIVERY_REPLAY_GIT_MARKER\"\nexit 97\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DELIVERY_REPLAY_GIT_MARKER", marker)
			t.Setenv("PATH", bin)
			assertDeliveryReplayReadOnly(t, f, func(ctx context.Context) {
				got, err := f.facade.RetryAcceptedTaskCommit(ctx, f.session, f.task)
				if err != nil || got.ID != f.task || got.Status != task.StatusSucceeded {
					t.Errorf("completed delivery required writer: task=%s status=%s err=%v", got.ID, got.Status, err)
				}
			})
			got, err := f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task)
			if err != nil || got.ID != f.task || got.Status != task.StatusSucceeded {
				t.Errorf("completed multi retry: task=%s status=%s err=%v", got.ID, got.Status, err)
			}
			replayed, err := f.facade.AcceptFileImplementationResult(f.ctx, in)
			if err != nil || !replayed.Accepted || !replayed.Replay || replayed.CommitError != "" || replayed.ID != accepted.ID {
				t.Errorf("acceptance replay=%+v err=%v", replayed, err)
			}
			if after := readReceipts(); after != before {
				t.Errorf("completed receipts changed: before=%s after=%s", before, after)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("receipt replay invoked Git: %v", err)
			}
		})
	}
}
