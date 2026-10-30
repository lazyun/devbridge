package controlapi_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/featurearea"
)

func TestCompletedMergeReplayIsReadOnly(t *testing.T) {
	f := completeFollowupTask(t)
	plan, err := f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	request := "completed-merge-readonly"
	if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, request); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		err := f.plane.DB.SQL().QueryRow(`SELECT json_object('completed',p.completed_at,'error',p.last_error,'released',p.released_at,'members',(SELECT json_group_array(json_object('repository',repository_id,'state',state,'head',resulting_dev_head,'completed',completed_at,'error',last_error)) FROM (SELECT * FROM feature_area_commit_members WHERE request_id=p.request_id ORDER BY repository_id)),'dispatches',(SELECT count(*) FROM merge_delivery_dispatches),'outcomes',(SELECT count(*) FROM merge_delivery_outcomes)) FROM feature_area_commit_receipts p WHERE p.request_id=?`, request).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	for _, phase := range []string{"warm", "cold", "workspace-drift"} {
		if phase == "cold" {
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
		}
		if phase == "workspace-drift" {
			if _, err = f.plane.DB.SQL().Exec(`UPDATE workspaces SET runtime_quarantined=1 WHERE id=?`, plan.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}
		t.Run(phase, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "git-called")
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf called >> \"$MERGE_READBACK_GIT_MARKER\"\nexit 97\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MERGE_READBACK_GIT_MARKER", marker)
			t.Setenv("PATH", bin)
			calls := []struct {
				name string
				call func(context.Context) (featurearea.FeatureArea, error)
			}{
				{"public submit", func(ctx context.Context) (featurearea.FeatureArea, error) {
					return f.facade.CommitFeatureArea(ctx, f.session, f.area, request)
				}},
				{"exact plan", func(ctx context.Context) (featurearea.FeatureArea, error) {
					return f.plane.WorkflowProgression.CommitFeatureAreaPlan(ctx, f.project, f.area, request, plan)
				}},
				{"existing recovery", func(ctx context.Context) (featurearea.FeatureArea, error) {
					return f.plane.WorkflowProgression.RecoverFeatureAreaMerge(ctx, f.project, f.area, request)
				}},
			}
			assertDeliveryReplayReadOnly(t, f, func(ctx context.Context) {
				for _, entry := range calls {
					got, err := entry.call(ctx)
					if err != nil || got.ID != f.area || got.ProjectID != f.project {
						t.Errorf("%s replay required mutation: %+v %v", entry.name, got, err)
					}
				}
				changed := plan
				changed.WorkspaceID = "different-workspace"
				if err := changed.Canonicalize(); err != nil {
					t.Fatal(err)
				}
				if got, err := f.plane.WorkflowProgression.CommitFeatureAreaPlan(ctx, f.project, f.area, request, changed); err == nil || got.ID != "" {
					t.Errorf("changed plan borrowed completed result: %+v %v", got, err)
				}
				if got, err := f.plane.WorkflowProgression.CommitFeatureAreaPlan(ctx, "foreign-project", f.area, request, plan); err == nil || got.ID != "" {
					t.Errorf("foreign project borrowed completed result: %+v %v", got, err)
				}
			})
			if after := snapshot(); after != before {
				t.Errorf("replay changed receipts: %s -> %s", before, after)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("completed replay invoked Git: %v", err)
			}
		})
	}
	t.Run("inconsistent-terminal-member", func(t *testing.T) {
		// Model damaged historical evidence in this disposable database. A
		// completed parent alone must not authorize repair or redispatch.
		if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER area_commit_member_identity`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.plane.DB.SQL().Exec(`UPDATE feature_area_commit_members SET state='PENDING' WHERE request_id=? AND repository_id=?`, request, plan.Members[0].RepositoryID); err != nil {
			t.Fatal(err)
		}
		before := snapshot()
		assertDeliveryReplayReadOnly(t, f, func(ctx context.Context) {
			if got, err := f.facade.CommitFeatureArea(ctx, f.session, f.area, request); err == nil || got.ID != "" {
				t.Errorf("incomplete historical evidence reported success: %+v %v", got, err)
			}
		})
		if after := snapshot(); after != before {
			t.Errorf("observation repaired historical evidence: %s -> %s", before, after)
		}
	})
}
