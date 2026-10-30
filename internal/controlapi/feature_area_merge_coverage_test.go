package controlapi_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/mcpserver"
)

func TestMergeRefusesMissingTaskCommit(t *testing.T) {
	for _, timing := range []string{"before-plan", "after-plan"} {
		t.Run(timing, func(t *testing.T) {
			f := completeFollowupTask(t)
			plan, err := f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
			if err != nil {
				t.Fatal(err)
			}
			m := plan.Members[0]
			var input c.CommitInput
			if timing == "after-plan" {
				_, input = prepareFollowupMerge(t, f)
			}
			gitOutput(t, m.SourcePath, "reset", "--hard", m.ExpectedDevHead)
			if timing == "before-plan" {
				tables := definitionFormalEveryTable(t, f)
				before := definitionFormalSnapshot(t, f, tables)
				_, err = f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "missing-prepare", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area}, Variants: []c.Variant{{ID: "merge", Summary: "Merge completed tasks"}}})
				assertDefinitionFormalSnapshot(t, f, tables, before, "missing commit refused without writes")
			} else {
				_, err = f.facade.CommitConfirmation(f.ctx, input)
			}
			want := "RECOVERY_REQUIRED"
			if timing == "after-plan" {
				want = "CONFIRMATION_FACTS_UNAVAILABLE"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("missing commit not refused: %v", err)
			}
			var completed int
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_commit_members WHERE state='COMPLETED'`).Scan(&completed); err != nil || completed != 0 {
				t.Fatalf("false completion: %d %v", completed, err)
			}
			for _, member := range plan.Members {
				if head := gitOutput(t, member.TargetPath, "rev-parse", "HEAD"); head != member.ExpectedDevHead {
					t.Fatalf("unexpected merge: %s", head)
				}
			}
		})
	}
}

func TestMergeAllowsTaskCommitAlreadyInTarget(t *testing.T) {
	f := completeFollowupTask(t)
	plan, err := f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	m := plan.Members[0]
	gitOutput(t, m.TargetPath, "merge", "--ff-only", m.SourceHead)
	gitOutput(t, m.SourcePath, "reset", "--hard", m.ExpectedDevHead)
	_, input := prepareFollowupMerge(t, f)
	if got, err := f.facade.CommitConfirmation(f.ctx, input); err != nil || got.State != "COMPLETED" {
		t.Fatalf("already integrated commit blocked: %+v %v", got, err)
	}
	if head := gitOutput(t, m.TargetPath, "rev-parse", "HEAD"); head != m.SourceHead {
		t.Fatalf("lost integrated commit: %s", head)
	}
}

func TestAdvertisedAreaSubmitSupportsV2(t *testing.T) {
	for _, surface := range []string{"facade", "mcp-service", "operator"} {
		t.Run(surface, func(t *testing.T) {
			f := completeFollowupTask(t)
			plan, err := f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
			if err != nil {
				t.Fatal(err)
			}
			p, err := f.facade.FeatureAreaPresentationForSession(f.ctx, f.session, f.area)
			if err != nil || p.Commit == nil || !p.Commit.CanCommit {
				t.Fatalf("not ready: %+v %v", p.Commit, err)
			}
			advertised := false
			for _, action := range p.Capabilities {
				if action.Action == "COMMIT_FEATURE_AREA" && action.Available {
					advertised = true
				}
			}
			if !advertised {
				t.Fatal("submit capability missing")
			}
			ingress, err := localoperator.NewIngress("merge-operator")
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "http://127.0.0.1:4777/action", nil)
			req.RemoteAddr = "127.0.0.1:50000"
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4777}))
			operatorCtx, err := ingress.AdmitHTTP(req)
			if err != nil {
				t.Fatal(err)
			}
			submit := func() error {
				switch surface {
				case "mcp-service":
					service, e := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "test", ApplicationVersion: "1", Ready: true}, f.plane.AIContext, f.facade)
					if e != nil {
						return e
					}
					_, e = service.CommitFeatureArea(f.ctx, f.session, f.area, "submit-v2")
					return e
				case "operator":
					_, e := f.facade.CommitFeatureAreaForOperator(operatorCtx, f.project, f.area, "submit-v2")
					return e
				default:
					_, e := f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "submit-v2")
					return e
				}
			}
			if err = submit(); err != nil {
				t.Fatal(err)
			}
			for _, m := range plan.Members {
				if head := gitOutput(t, m.TargetPath, "rev-parse", "HEAD"); head != m.SourceHead {
					t.Fatalf("member not merged: %s", head)
				}
			}
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
			m := plan.Members[0]
			offline := m.TargetPath + "-offline"
			if err = os.Rename(m.TargetPath, offline); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Rename(offline, m.TargetPath); err != nil {
					t.Error(err)
				}
			}()
			if err = submit(); err != nil {
				t.Fatalf("cold exact replay depended on live Git: %v", err)
			}
			var parents, dispatches, completed int
			if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM feature_area_commit_receipts),(SELECT count(*) FROM merge_delivery_dispatches),(SELECT count(*) FROM feature_area_commit_members WHERE state='COMPLETED')`).Scan(&parents, &dispatches, &completed); err != nil || parents != 1 || dispatches != 2 || completed != 2 {
				t.Fatalf("replay wrote new effects: %d/%d/%d %v", parents, dispatches, completed, err)
			}
		})
	}
}

func TestAreaSubmitV2UnknownCannotRedispatch(t *testing.T) {
	f := completeFollowupTask(t)
	plan, err := f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
		if stage == "AREA_MERGE_BEFORE_RESULT" {
			return errors.New("lost result")
		}
		return nil
	})
	if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "unknown-submit"); err == nil {
		t.Fatal("no injected failure")
	}
	m := plan.Members[0]
	gitOutput(t, m.TargetPath, "reset", "--hard", m.ExpectedDevHead)
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	for _, request := range []string{"unknown-submit", "new-submit"} {
		if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, request); err == nil {
			t.Fatalf("UNKNOWN bypassed by %s", request)
		}
	}
	if head := gitOutput(t, m.TargetPath, "rev-parse", "HEAD"); head != m.ExpectedDevHead {
		t.Fatalf("UNKNOWN was redispatched: %s", head)
	}
	var dispatches int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM merge_delivery_dispatches`).Scan(&dispatches); err != nil || dispatches != 1 {
		t.Fatalf("unexpected dispatches %d %v", dispatches, err)
	}
}
