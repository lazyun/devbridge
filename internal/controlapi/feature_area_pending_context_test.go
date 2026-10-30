package controlapi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/developmentcycle"
)

func TestPendingMergeProtectsContextUntilColdRetryCompletes(t *testing.T) {
	f := completeFollowupTask(t)
	plan, err := f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	m := plan.Members[0]
	dirty := filepath.Join(m.TargetPath, "pending-context-dirty")
	if err = os.WriteFile(dirty, []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "pending-context-parent"); err == nil {
		t.Fatal("missing pre-effect refusal")
	}
	if err = os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	candidates, err := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	// A pending multi-repository parent protects every member, including one
	// that already completed or has not dispatched yet.
	for _, member := range plan.Members {
		in := developmentcycle.SelectInput{AISessionID: f.session, ProjectID: f.project, RepositoryID: member.RepositoryID, DevelopmentRef: member.DevelopmentRef, RequestID: "reselect-" + member.RepositoryID}
		if _, err = f.facade.SelectDevelopmentBranchForSession(f.ctx, in); err == nil || !strings.Contains(err.Error(), "GIT_FINALIZATION_PENDING") {
			t.Fatalf("selection did not preserve pending context: %v", err)
		}
		var current, originalRequest string
		if err = f.plane.DB.SQL().QueryRow(`SELECT id,request_id FROM development_cycles WHERE repository_id=? AND superseded_at IS NULL`, member.RepositoryID).Scan(&current, &originalRequest); err != nil || current != member.DevelopmentCycleID {
			t.Fatalf("context changed %s %v", current, err)
		}
		in.RequestID = originalRequest
		if replay, e := f.facade.SelectDevelopmentBranchForSession(f.ctx, in); e != nil || replay.ID != current {
			t.Fatalf("historical selection replay: %+v %v", replay, e)
		}
	}
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	v := candidates[0]
	if _, err = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: v.MergeRequestID, RepositoryID: v.RepositoryID, PredecessorOperationID: v.OperationID, RequestID: "pending-context-retry"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "pending-context-parent"); err != nil {
		t.Fatal(err)
	}
	for _, member := range plan.Members {
		cycle, e := f.facade.SelectDevelopmentBranchForSession(f.ctx, developmentcycle.SelectInput{AISessionID: f.session, ProjectID: f.project, RepositoryID: member.RepositoryID, DevelopmentRef: member.DevelopmentRef, RequestID: "reselect-" + member.RepositoryID})
		if e != nil || cycle.ID == member.DevelopmentCycleID {
			t.Fatalf("completed delivery still blocks selection: %+v %v", cycle, e)
		}
	}
}
