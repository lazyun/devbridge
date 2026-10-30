package controlapi_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
)

func TestTaskRetryAuthorizationSurvivesBeforeDispatchRestart(t *testing.T) {
	f, path, content, candidate := rejectedDeliveryV118(t)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	in := delivery.RetryInput{TaskID: f.task, RepositoryID: candidate.RepositoryID, PredecessorOperationID: candidate.OperationID, RequestID: "durable-undispatched-task-retry"}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
		if stage == "TASK_COMMIT_BEFORE_CLAIM" {
			return errors.New("stop after retry authority")
		}
		return nil
	})
	receipt, err := f.facade.ReprepareAcceptedTaskCommit(f.ctx, f.session, in)
	if err == nil || receipt.OperationID == "" {
		t.Fatalf("retry did not stop after authority: %+v %v", receipt, err)
	}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
	for _, cold := range []bool{false, true} {
		if cold {
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
		}
		replay, err := f.facade.ReprepareAcceptedTaskCommit(f.ctx, f.session, in)
		if err != nil || replay != receipt {
			t.Fatalf("retry receipt lost cold=%v: %+v %v", cold, replay, err)
		}
		var dispatches int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_delivery_operation_dispatches WHERE operation_id=?`, receipt.OperationID).Scan(&dispatches); err != nil || dispatches != 0 {
			t.Fatalf("observation dispatched retry: %d %v", dispatches, err)
		}
		cap, err := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
		if err != nil || !cap.CanRetry || cap.RetryTaskID != f.task || cap.RetryOperation != nil {
			t.Fatalf("pending retry not resumable cold=%v: %+v %v", cold, cap, err)
		}
	}
	current, err := f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task)
	if err != nil || current.Status != task.StatusSucceeded {
		t.Fatalf("resume persisted retry: %+v %v", current, err)
	}
	var dispatches int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_delivery_operation_dispatches WHERE operation_id=?`, receipt.OperationID).Scan(&dispatches); err != nil || dispatches != 1 {
		t.Fatalf("retry dispatch count: %d %v", dispatches, err)
	}
}

func TestMergeRetryAuthorizationSurvivesBeforeDispatchRestart(t *testing.T) {
	f := completeFollowupTask(t)
	prepared, input := prepareFollowupMerge(t, f)
	m := prepared.Content.FeatureArea.DeliveryPlan.Members[1]
	path := filepath.Join(m.TargetPath, "same.txt")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("dirty target"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.CommitConfirmation(f.ctx, input); err == nil {
		t.Fatal("dirty target was not rejected")
	}
	candidates, err := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("rejected merge missing: %+v %v", candidates, err)
	}
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	in := delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: candidates[0].MergeRequestID, RepositoryID: m.RepositoryID, PredecessorOperationID: candidates[0].OperationID, RequestID: "durable-undispatched-merge-retry"}
	if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER stop_retry_dispatch BEFORE INSERT ON merge_delivery_dispatches BEGIN SELECT RAISE(ABORT,'stop after retry authority'); END`); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in)
	if err == nil || receipt.OperationID == "" {
		t.Fatalf("retry did not stop after authority: %+v %v", receipt, err)
	}
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER stop_retry_dispatch`); err != nil {
		t.Fatal(err)
	}
	for _, cold := range []bool{false, true} {
		if cold {
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
		}
		replay, err := f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in)
		if err != nil || replay != receipt {
			t.Fatalf("merge retry receipt lost cold=%v: %+v %v", cold, replay, err)
		}
		var dispatches int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM merge_delivery_dispatches WHERE operation_id=?`, receipt.OperationID).Scan(&dispatches); err != nil || dispatches != 0 {
			t.Fatalf("observation dispatched merge: %d %v", dispatches, err)
		}
		cap, err := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
		if err != nil || !cap.CanRecoverMerge || cap.RequestID != in.MergeRequestID {
			t.Fatalf("merge retry not resumable cold=%v: %+v %v", cold, cap, err)
		}
	}
	const snapshot = `SELECT json_object('error',p.last_error,'completed',p.completed_at,'members',(SELECT json_group_array(json_object('repository',repository_id,'state',state,'error',last_error)) FROM (SELECT * FROM feature_area_commit_members WHERE request_id=p.request_id ORDER BY repository_id))) FROM feature_area_commit_receipts p WHERE p.request_id=?`
	var before, after string
	if err = f.plane.DB.SQL().QueryRow(snapshot, in.MergeRequestID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	stale := runtimeepoch.WithFence(f.ctx, "obsolete-merge-worker", "")
	if _, err = f.plane.WorkflowProgression.CommitFeatureAreaPlan(stale, f.project, f.area, in.MergeRequestID, *prepared.Content.FeatureArea.DeliveryPlan); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("stale merge not fenced: %v", err)
	}
	if err = f.plane.DB.SQL().QueryRow(snapshot, in.MergeRequestID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("fenced merge rewrote receipts: %s -> %s", before, after)
	}
	ingress, err := localoperator.NewIngress("resume-durable-merge")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1:4777/action", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4777}))
	ctx, err := ingress.AdmitHTTP(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.RecoverFeatureAreaMergeForOperator(ctx, f.project, f.area, in.MergeRequestID); err != nil {
		t.Fatalf("resume existing merge: %v", err)
	}
	if head := gitOutput(t, m.TargetPath, "rev-parse", "HEAD"); head != m.SourceHead {
		t.Fatalf("merge did not advance: %s", head)
	}
	var dispatches int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM merge_delivery_dispatches WHERE operation_id=?`, receipt.OperationID).Scan(&dispatches); err != nil || dispatches != 1 {
		t.Fatalf("merge retry dispatch count: %d %v", dispatches, err)
	}
}
