package controlapi_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

func TestRetirementReportsPendingMergeFence(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "rejected"
		if unknown {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			f := completeFollowupTask(t)
			plan, err := f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
			if err != nil {
				t.Fatal(err)
			}
			member := plan.Members[0]
			dirty := filepath.Join(member.TargetPath, "retirement-rejection")
			if unknown {
				f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
					if stage == "AREA_MERGE_BEFORE_RESULT" {
						return errors.New("lost merge outcome")
					}
					return nil
				})
			} else if err = os.WriteFile(dirty, []byte("dirty"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "retirement-merge"); err == nil {
				t.Fatal("missing interrupted merge")
			}
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
			if !unknown {
				if err = os.Remove(dirty); err != nil {
					t.Fatal(err)
				}
			}
			for _, cold := range []bool{false, true} {
				if cold {
					reopenDirectDeliveryFixture(t, f)
					f.plane.Retirement.Close()
				}
				assertRetirementGitBlocked(t, f)
			}
			var operation string
			if err = f.plane.DB.SQL().QueryRow(`SELECT operation_id FROM merge_delivery_current_operations WHERE merge_request_id=? AND repository_id=?`, "retirement-merge", member.RepositoryID).Scan(&operation); err != nil {
				t.Fatal(err)
			}
			before := gitOutput(t, member.TargetPath, "rev-parse", "HEAD")
			_, err = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: "retirement-merge", RepositoryID: member.RepositoryID, PredecessorOperationID: operation, RequestID: "release-before-retire", ReleasePlan: true})
			if unknown {
				if err == nil {
					t.Fatal("UNKNOWN released")
				}
				assertRetirementGitBlocked(t, f)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gitOutput(t, member.TargetPath, "rev-parse", "HEAD") != before {
				t.Fatal("release changed Git")
			}
			cap, err := f.facade.FeatureAreaRetireCapabilityForSession(f.ctx, f.session, f.area)
			if err != nil || !cap.Available {
				t.Fatalf("released plan blocks retirement: %+v %v", cap, err)
			}
			if _, err = f.facade.RetireFeatureArea(f.ctx, f.session, f.area, "retire-with-merge"); err != nil {
				t.Fatal(err)
			}
			store := sqlite.NewFeatureAreaStore(f.plane.DB)
			if err = store.FinishRetirement(f.ctx, f.area, ""); err != nil {
				t.Fatal(err)
			}
			area, err := store.Get(f.ctx, f.area)
			if err != nil || area.Status != featurearea.StatusRetired {
				t.Fatalf("retirement did not converge: %+v %v", area, err)
			}
		})
	}
}

func assertRetirementGitBlocked(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	cap, err := f.facade.FeatureAreaRetireCapabilityForSession(f.ctx, f.session, f.area)
	if err != nil || cap.Available || cap.Reason == "" {
		t.Errorf("minimal retirement capability: %+v %v", cap, err)
	}
	p, err := f.facade.FeatureAreaPresentationForSession(f.ctx, f.session, f.area)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cap := range p.Capabilities {
		if cap.Action == "RETIRE" {
			found = true
			if cap.Available || cap.Reason == "" {
				t.Errorf("full retirement capability: %+v", cap)
			}
		}
	}
	if !found {
		t.Fatal("missing retirement capability")
	}
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	if _, err = f.facade.RetireFeatureArea(f.ctx, f.session, f.area, "retire-with-merge"); !errors.Is(err, developmentcycle.ErrFinalizationPending) {
		t.Errorf("retirement must report domain blocker: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "blocked retirement cannot consume intent or delivery")
}

func TestRetirementReportsPendingTaskDeliveryFence(t *testing.T) {
	f, _, _, _ := rejectedDeliveryV118(t)
	f.plane.Retirement.Close()
	for _, cold := range []bool{false, true} {
		if cold {
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
		}
		assertRetirementGitBlocked(t, f)
	}
}
