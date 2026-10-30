package controlapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/localoperator"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

func TestCompletedMultiMergePresentation(t *testing.T) {
	f := completeFollowupTask(t)
	if _, err := f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "audit-complete"); err != nil {
		t.Fatal(err)
	}
	got, err := f.facade.FeatureAreaPresentationForSession(f.ctx, f.session, f.area)
	if err != nil {
		t.Fatal(err)
	}
	if got.Commit == nil || got.CommitView == nil || got.Commit.CommitStatus != "COMMITTED" || got.CommitView.Details.Delivery.State != "COMPLETED" {
		t.Fatalf("fixture not completed: %+v %+v", got.Commit, got.CommitView)
	}
	t.Logf("commitStatus=%s delivery=%s members=%d mergedCommit=%q summary=%q", got.Commit.CommitStatus, got.CommitView.Details.Delivery.State, len(got.CommitView.Details.Delivery.Members), got.Commit.MergedCommit, got.CommitView.Summary)
	taskView, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
	if err != nil {
		t.Fatal(err)
	}
	if taskView.TaskView == nil || taskView.TaskView.CommitStatus != "COMMITTED" || taskView.TaskView.DevIntegrationStatus != "MERGED" {
		t.Fatalf("Task receipts not projected: %+v", taskView.TaskView)
	}
	if taskView.TaskView != nil {
		t.Logf("Task commitStatus=%s summary=%q", taskView.TaskView.CommitStatus, taskView.TaskView.CommitSummary)
	}
	inventory, e := f.facade.DevelopmentWorkspaceForSession(f.ctx, f.session, f.area, f.task)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, area := range inventory.FeatureAreas {
		if area.ID == f.area {
			found = true
			if area.LatestTaskCommitStatus != "COMMITTED" || area.LatestTaskDevIntegrationStatus != "MERGED" {
				t.Fatalf("inventory still uses scalar status: %+v", area)
			}
		}
	}
	if !found {
		t.Fatal("missing inventory area")
	}
	if strings.Contains(got.CommitView.Summary, "未提交") {
		t.Fatal("completed multi-repository merge is displayed as unsubmitted")
	}
}

func TestMergeRecoveryHasAction(t *testing.T) {
	for _, kind := range []string{"rejected", "applied-receipt"} {
		t.Run(kind, func(t *testing.T) {
			f := completeFollowupTask(t)
			plan, err := f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "rejected" {
				dirty := filepath.Join(plan.Members[0].TargetPath, "audit-untracked.txt")
				if err = os.WriteFile(dirty, []byte("dirty"), 0600); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(dirty)
			} else {
				f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
					if stage == "AREA_MERGE_AFTER_GIT" {
						return errors.New("lost projection acknowledgement")
					}
					return nil
				})
			}
			if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "audit-recover"); err == nil {
				t.Fatal("no interruption")
			}
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
			got, err := f.facade.FeatureAreaPresentationForSession(f.ctx, f.session, f.area)
			if err != nil {
				t.Fatal(err)
			}
			candidates, err := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
			if err != nil {
				t.Fatal(err)
			}
			var outcomes int
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM merge_delivery_outcomes WHERE outcome='APPLIED'`).Scan(&outcomes); err != nil {
				t.Fatal(err)
			}
			if kind == "rejected" && len(candidates) != 1 {
				t.Fatalf("missing retry witness: %+v", candidates)
			}
			if kind == "applied-receipt" && outcomes != 1 {
				t.Fatalf("missing applied witness: %d", outcomes)
			}
			current := p.CurrentAction(got)
			actions := []string{}
			for _, a := range got.Capabilities {
				if a.Available {
					actions = append(actions, a.Action)
				}
			}
			t.Logf("kind=%s reason=%s canCommit=%t canRetry=%t viewActivity=%s next=%s actions=%v retryCandidates=%d appliedReceipts=%d", kind, got.Commit.Reason, got.Commit.CanCommit, got.Commit.CanRetry, got.CommitView.Activity, current.Code, actions, len(candidates), outcomes)
			actionable := false
			for _, a := range actions {
				if a == "COMMIT_FEATURE_AREA" || a == "REPREPARE_FEATURE_AREA_MERGE" || a == "RECOVER_FEATURE_AREA_MERGE" {
					actionable = true
				}
			}
			if kind == "applied-receipt" && got.CommitView.Activity == "RUNNING" {
				t.Fatal("interrupted merge still shown as running")
			}
			if !actionable {
				t.Errorf("recoverable merge has no public projection recovery action; next=%s", current.Code)
			}
			ingress, e := localoperator.NewIngress("merge-recovery-test")
			if e != nil {
				t.Fatal(e)
			}
			req := httptest.NewRequest("POST", "http://127.0.0.1:4777/action", nil)
			req.RemoteAddr = "127.0.0.1:50000"
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4777}))
			opctx, e := ingress.AdmitHTTP(req)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "rejected" {
				if err = os.Remove(filepath.Join(plan.Members[0].TargetPath, "audit-untracked.txt")); err != nil {
					t.Fatal(err)
				}
				v := candidates[0]
				_, err = f.facade.ReprepareFeatureAreaMergeForOperator(opctx, f.project, delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: v.MergeRequestID, RepositoryID: v.RepositoryID, PredecessorOperationID: v.OperationID, RequestID: "audit-explicit-repair"})
			} else {
				_, err = f.facade.RecoverFeatureAreaMergeForOperator(opctx, f.project, f.area, "audit-recover")
			}
			if err != nil {
				t.Fatalf("backend recovery control failed: %v", err)
			}
			var complete int
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_commit_receipts WHERE completed_at IS NOT NULL`).Scan(&complete); err != nil || complete != 1 {
				t.Fatalf("recovery not completed: %d %v", complete, err)
			}
			t.Log("CONTROL: exact backend recovery completed the persisted merge after cold reopen")
		})
	}
}

func TestMergeRecoveryHTTPExactIdentityAndUnknownFence(t *testing.T) {
	for _, kind := range []string{"rejected", "applied", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			f := completeFollowupTask(t)
			plan, err := f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
			if err != nil {
				t.Fatal(err)
			}
			dirty := filepath.Join(plan.Members[0].TargetPath, "recovery-dirty")
			if kind == "rejected" {
				if err = os.WriteFile(dirty, []byte("dirty"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
					if kind == "applied" && stage == "AREA_MERGE_AFTER_GIT" || kind == "unknown" && stage == "AREA_MERGE_BEFORE_RESULT" {
						return errors.New("lost acknowledgement")
					}
					return nil
				})
			}
			if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "http-original"); err == nil {
				t.Fatal("missing interruption")
			}
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
			body := map[string]string{"action": "RECOVER_FEATURE_AREA_MERGE", "mergeRequestId": "http-original", "requestId": "http-repair"}
			if kind == "rejected" {
				if err = os.Remove(dirty); err != nil {
					t.Fatal(err)
				}
				candidates, e := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
				if e != nil || len(candidates) != 1 {
					t.Fatalf("%+v %v", candidates, e)
				}
				body["action"] = "REPREPARE_FEATURE_AREA_MERGE"
				body["repositoryId"] = candidates[0].RepositoryID
				body["predecessorOperationId"] = candidates[0].OperationID
			}
			server, err := bridgehttp.New(f.plane, false)
			if err != nil {
				t.Fatal(err)
			}
			call := func(payload map[string]string) *httptest.ResponseRecorder {
				raw, _ := json.Marshal(payload)
				req := httptest.NewRequest("POST", "http://127.0.0.1:4777/ui/api/featureAreas/"+f.area+"/action?project_id="+f.project, bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				req.RemoteAddr = "127.0.0.1:50000"
				req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4777}))
				rec := httptest.NewRecorder()
				server.Handler().ServeHTTP(rec, req)
				return rec
			}
			if kind == "unknown" {
				p, e := f.facade.FeatureAreaPresentationForSession(f.ctx, f.session, f.area)
				if e != nil {
					t.Fatal(e)
				}
				if p.Commit.CanRecoverMerge || p.Commit.MergeRetryOperation != nil || p.CommitView.Activity != "OUTCOME_UNKNOWN" {
					t.Fatalf("unknown exposed write capability: %+v %+v", p.Commit, p.CommitView)
				}
				tables := definitionFormalEveryTable(t, f)
				before := definitionFormalSnapshot(t, f, tables)
				rec := call(body)
				if rec.Code < 400 {
					t.Fatalf("UNKNOWN allowed recovery: %d %s", rec.Code, rec.Body.String())
				}
				assertDefinitionFormalSnapshot(t, f, tables, before, "UNKNOWN HTTP recovery makes no writes")
				return
			}
			bad := map[string]string{}
			for k, v := range body {
				bad[k] = v
			}
			bad["mergeRequestId"] = "foreign-parent"
			tables := definitionFormalEveryTable(t, f)
			before := definitionFormalSnapshot(t, f, tables)
			rec := call(bad)
			if rec.Code < 400 {
				t.Fatalf("foreign parent accepted: %d %s", rec.Code, rec.Body.String())
			}
			assertDefinitionFormalSnapshot(t, f, tables, before, "wrong parent cannot retarget recovery")
			for i := 0; i < 2; i++ {
				rec = call(body)
				if rec.Code != 200 {
					t.Fatalf("HTTP recovery/replay %d: %d %s", i, rec.Code, rec.Body.String())
				}
			}
			var completed int
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_commit_receipts WHERE completed_at IS NOT NULL`).Scan(&completed); err != nil || completed != 1 {
				t.Fatalf("HTTP recovery incomplete %d %v", completed, err)
			}
		})
	}
}
