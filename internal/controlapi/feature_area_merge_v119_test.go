package controlapi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

func prepareFollowupMerge(t *testing.T, f *upperLifecycleFixture) (c.Intent, c.CommitInput) {
	t.Helper()
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "followup-prepare-merge", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area}, Variants: []c.Variant{{ID: "merge", Summary: "Merge exact completed Task delivery"}}})
	if err != nil || prepared.Content.FeatureArea == nil || prepared.Content.FeatureArea.DeliveryPlan == nil {
		t.Fatalf("prepare merge %+v %v", prepared, err)
	}
	return prepared, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "followup-confirm-merge", ContentHash: prepared.ContentHash, VariantID: "merge"}
}

func completeFollowupTask(t *testing.T) *upperLifecycleFixture {
	t.Helper()
	f, current := approveDirectFixture(t, false, true)
	state, result := exerciseDirectFileResult(t, f, current)
	accepted := acceptDeliveryV118(t, f, current, state, result)
	if accepted.CommitError != "" {
		t.Fatal(accepted.CommitError)
	}
	f.plane.Retirement.Close()
	return f
}

// Cancellation must preserve partial history without admitting any merge.
func TestMergeV119CanceledPartialTaskCannotMerge(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	state, result := exerciseDirectFileResult(t, f, current)
	f.plane.Retirement.Close()
	calls := 0
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, repo string) error {
		if kind != "TASK_COMMIT_AFTER_GIT" || calls != 0 {
			return nil
		}
		calls++
		receipt, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: f.task, RequestID: "followup-cancel-after-first", Reason: "Abandon remaining delivery"})
		if err != nil || receipt.State != "CANCELED" {
			t.Fatalf("cancel %+v %v", receipt, err)
		}
		return nil
	})
	accepted := acceptDeliveryV118(t, f, current, state, result)
	if accepted.CommitError == "" || calls != 1 {
		t.Fatalf("did not stop partial delivery %+v calls=%d", accepted, calls)
	}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
	var status, outcome string
	var applied, pending int
	err := f.plane.DB.SQL().QueryRow(`SELECT t.status,q.terminal_outcome,(SELECT count(*) FROM task_commit_members WHERE task_id=t.id AND state='COMPLETED'),(SELECT count(*) FROM task_commit_members WHERE task_id=t.id AND state='PENDING') FROM tasks t JOIN task_commit_receipts q ON q.task_id=t.id WHERE t.id=?`, f.task).Scan(&status, &outcome, &applied, &pending)
	if err != nil || status != "CANCELED" || outcome != "ABANDONED" || applied != 1 || pending != 1 {
		t.Fatalf("not partial abandoned %s %s %d %d %v", status, outcome, applied, pending, err)
	}
	cap, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitCapability(f.ctx, f.project, f.area)
	if err != nil || cap.CanCommit || cap.Reason != "TASK_COMMIT_PENDING" {
		t.Fatalf("expected conservative cap %+v %v", cap, err)
	}
	if _, err = f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "blocked-partial-merge", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area}, Variants: []c.Variant{{ID: "merge", Summary: "Merge"}}}); err == nil {
		t.Fatal("canceled partial Task admitted a merge")
	}
	var parents int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_commit_receipts WHERE feature_area_id=?`, f.area).Scan(&parents); err != nil || parents != 0 {
		t.Fatalf("merge admission wrote parent %d %v", parents, err)
	}

}

// A Git effect occurred but its Core member result was not saved. An ordinary
// external rewind makes HEAD look like the pre-effect state; that is not fresh
// authority to run the same UNKNOWN operation a second time.
func TestMergeV119UnknownNeverRedispatchesAfterRewind(t *testing.T) {
	f := completeFollowupTask(t)
	prepared, input := prepareFollowupMerge(t, f)
	plan := prepared.Content.FeatureArea.DeliveryPlan
	m := plan.Members[0]
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, repo string) error {
		if kind == "AREA_MERGE_BEFORE_RESULT" {
			return errors.New("response lost after applied merge")
		}
		return nil
	})
	partial, err := f.facade.CommitConfirmation(f.ctx, input)
	if err == nil || partial.State != "UNKNOWN" {
		t.Fatalf("not UNKNOWN %+v %v", partial, err)
	}
	var memberState, result string
	request := partial.Steps[0].RequestID
	if err = f.plane.DB.SQL().QueryRow(`SELECT state,resulting_dev_head FROM feature_area_commit_members WHERE request_id=? AND repository_id=?`, request, m.RepositoryID).Scan(&memberState, &result); err != nil || memberState != "UNKNOWN" || result != "" {
		t.Fatalf("unexpected saved result %s %s %v", memberState, result, err)
	}
	if actual := gitOutput(t, m.TargetPath, "rev-parse", "HEAD"); actual != m.SourceHead {
		t.Fatalf("no initial real effect %s != %s", actual, m.SourceHead)
	}
	gitOutput(t, m.TargetPath, "reset", "--hard", m.ExpectedDevHead)
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	recovered, err := f.facade.CommitConfirmation(f.ctx, input)
	if err == nil || recovered.State == "COMPLETED" {
		t.Fatalf("old UNKNOWN unexpectedly progressed %+v %v", recovered, err)
	}
	reflog := gitOutput(t, m.TargetPath, "reflog", "show", "--format=%H %gs", m.DevelopmentRef)
	count := 0
	for _, line := range strings.Split(reflog, "\n") {
		if strings.HasPrefix(line, m.SourceHead+" ") && strings.Contains(line, "merge "+m.SourceHead+":") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("must retain exactly one merge effect count=%d reflog=%s", count, reflog)
	}
	var parentCount int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_commit_receipts WHERE feature_area_id=?`, f.area).Scan(&parentCount); err != nil || parentCount != 1 {
		t.Fatalf("not same operation %d %v", parentCount, err)
	}
	t.Logf("WITNESS: same request=%s parent count=%d, UNKNOWN with no result, after cold reopen kept the original UNKNOWN merge fenced; reflog:\n%s", request, parentCount, reflog)
}

func TestMergeV119RejectedReplayCannotBorrowNewContext(t *testing.T) {
	f := completeFollowupTask(t)
	prepared, input := prepareFollowupMerge(t, f)
	m := prepared.Content.FeatureArea.DeliveryPlan.Members[1]
	dirty := filepath.Join(m.TargetPath, "same.txt")
	original, err := os.ReadFile(dirty)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dirty, []byte("external dirty target\n"), 0600); err != nil {
		t.Fatal(err)
	}
	partial, err := f.facade.CommitConfirmation(f.ctx, input)
	if err == nil || partial.State != "UNKNOWN" {
		t.Fatalf("expected known pre-merge rejection %+v %v", partial, err)
	}
	var memberState, lastError string
	request := partial.Steps[0].RequestID
	if err = f.plane.DB.SQL().QueryRow(`SELECT state,last_error FROM feature_area_commit_members WHERE request_id=? AND repository_id=?`, request, m.RepositoryID).Scan(&memberState, &lastError); err != nil || memberState != "UNKNOWN" || !strings.Contains(lastError, "DIRTY") {
		t.Fatalf("not dirty pre-effect error %s %s %v", memberState, lastError, err)
	}
	if actual := gitOutput(t, m.TargetPath, "rev-parse", "HEAD"); actual != m.ExpectedDevHead {
		t.Fatalf("dirty rejection touched Git %s", actual)
	}
	if err = os.WriteFile(dirty, original, 0600); err != nil {
		t.Fatal(err)
	}
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	newRef := "refs/heads/followup-reselected"
	gitOutput(t, m.TargetPath, "branch", strings.TrimPrefix(newRef, "refs/heads/"), m.ExpectedDevHead)
	if _, err = f.facade.SelectDevelopmentBranchForSession(f.ctx, developmentcycle.SelectInput{AISessionID: f.session, ProjectID: f.project, RepositoryID: m.RepositoryID, DevelopmentRef: newRef, RequestID: "followup-reselect"}); err == nil || !strings.Contains(err.Error(), "GIT_FINALIZATION_PENDING") {
		t.Fatalf("pending merge must retain context: %v", err)
	}
	resumed, err := f.facade.CommitConfirmation(f.ctx, input)
	if err == nil || resumed.State == "COMPLETED" {
		t.Fatalf("rejected request unexpectedly redispatched %+v %v", resumed, err)
	}
	if got := gitOutput(t, m.TargetPath, "rev-parse", m.DevelopmentRef); got != m.ExpectedDevHead {
		t.Fatalf("old context was unexpectedly merged %s", got)
	}
	if got := gitOutput(t, m.TargetPath, "rev-parse", newRef); got != m.ExpectedDevHead {
		t.Fatalf("new mapping changed %s", got)
	}
	var parentCount int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_commit_receipts WHERE feature_area_id=?`, f.area).Scan(&parentCount); err != nil || parentCount != 1 {
		t.Fatalf("not same request %d %v", parentCount, err)
	}
	candidates, err := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates %+v %v", candidates, err)
	}
	in := delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: request, RepositoryID: m.RepositoryID, PredecessorOperationID: candidates[0].OperationID, RequestID: "new-context-retry"}
	if _, err = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in); err != nil {
		t.Fatalf("retry with protected original context: %v", err)
	}
	var retries int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM merge_delivery_operations WHERE predecessor_operation_id IS NOT NULL`).Scan(&retries); err != nil || retries != 1 {
		t.Fatalf("expected one exact retry %d %v", retries, err)
	}
}

func TestMergeV119ExplicitRetryAndColdReplay(t *testing.T) {
	f := completeFollowupTask(t)
	prepared, input := prepareFollowupMerge(t, f)
	m := prepared.Content.FeatureArea.DeliveryPlan.Members[1]
	dirty := filepath.Join(m.TargetPath, "same.txt")
	original, err := os.ReadFile(dirty)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dirty, []byte("dirty target"), 0600); err != nil {
		t.Fatal(err)
	}
	partial, err := f.facade.CommitConfirmation(f.ctx, input)
	if err == nil || partial.State != "UNKNOWN" {
		t.Fatalf("missing refusal %+v %v", partial, err)
	}
	candidates, err := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates %+v %v", candidates, err)
	}
	in := delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: candidates[0].MergeRequestID, RepositoryID: m.RepositoryID, PredecessorOperationID: candidates[0].OperationID, RequestID: "explicit-new-merge"}
	if _, err = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in); err == nil {
		t.Fatal("dirty retry passed live validation")
	}
	var retries int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM merge_delivery_operations WHERE predecessor_operation_id IS NOT NULL`).Scan(&retries); err != nil || retries != 0 {
		t.Fatalf("failed validation consumed retry %d %v", retries, err)
	}
	if err = os.WriteFile(dirty, original, 0600); err != nil {
		t.Fatal(err)
	}
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	if _, err = f.facade.CommitConfirmation(f.ctx, input); err == nil {
		t.Fatal("old rejected request redispatched")
	}
	type attempt struct {
		receipt delivery.MergeRetryReceipt
		err     error
	}
	attempts := make(chan attempt, 2)
	for i := 0; i < 2; i++ {
		go func() { v, e := f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in); attempts <- attempt{v, e} }()
	}
	first, second := <-attempts, <-attempts
	receipt := first.receipt
	if first.err != nil || second.err != nil || receipt.OperationID == "" || receipt != second.receipt {
		t.Fatalf("concurrent exact retry results: %+v / %+v", first, second)
	}
	if head := gitOutput(t, m.TargetPath, "rev-parse", "HEAD"); head != m.SourceHead {
		t.Fatalf("retry did not merge %s", head)
	}
	var outcomes, dispatches int
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM merge_delivery_dispatches),(SELECT count(*) FROM merge_delivery_outcomes)`).Scan(&dispatches, &outcomes); err != nil || dispatches != 3 || outcomes != 3 {
		t.Fatalf("one retry per rejected member: %d/%d %v", dispatches, outcomes, err)
	}
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	assertDeliveryReplayReadOnly(t, f, func(ctx context.Context) {
		replay, err := f.facade.ReprepareFeatureAreaMerge(ctx, f.session, in)
		if err != nil || replay != receipt {
			t.Fatalf("read-only merge retry replay: %+v %v", replay, err)
		}
		changed := in
		changed.ReleasePlan = true
		if _, err = f.facade.ReprepareFeatureAreaMerge(ctx, f.session, changed); !errors.Is(err, delivery.ErrRequestConflict) {
			t.Fatalf("retry/release request conflict: %v", err)
		}
		changed = in
		changed.RepositoryID = "wrong-repository"
		if _, err = f.facade.ReprepareFeatureAreaMerge(ctx, f.session, changed); !errors.Is(err, delivery.ErrRequestConflict) {
			t.Fatalf("changed merge retry: %v", err)
		}
		fresh := in
		fresh.RequestID = "new-request-without-writer"
		if _, err = f.facade.ReprepareFeatureAreaMerge(ctx, f.session, fresh); err == nil {
			t.Fatal("fresh delivery command bypassed writer fence")
		}

	})
	replay, err := f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in)
	if err != nil || replay != receipt {
		t.Fatalf("immutable cold replay %+v %v", replay, err)
	}
	changed := in
	changed.RepositoryID = "foreign"
	if _, err = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, changed); err == nil {
		t.Fatal("same request changed identity")
	}
	changed = in
	changed.RequestID = "competing-successor"
	if _, err = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, changed); err == nil {
		t.Fatal("second retry of consumed predecessor")
	}
	completed, err := f.facade.CommitConfirmation(f.ctx, input)
	if err != nil || completed.State != "COMPLETED" {
		t.Fatalf("original confirmation not finalized %+v %v", completed, err)
	}
	for _, table := range []string{"merge_delivery_operations", "merge_delivery_dispatches", "merge_delivery_outcomes"} {
		if _, err = f.plane.DB.SQL().Exec(`DELETE FROM ` + table); err == nil {
			t.Fatalf("mutable history %s", table)
		}
	}
}

func TestMergeV119UnknownBeforeGitCannotAcquireRetry(t *testing.T) {
	f := completeFollowupTask(t)
	prepared, input := prepareFollowupMerge(t, f)
	m := prepared.Content.FeatureArea.DeliveryPlan.Members[0]
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
		if stage == "AREA_MERGE_BEFORE_GIT" {
			return errors.New("lost dispatch acknowledgement")
		}
		return nil
	})
	partial, err := f.facade.CommitConfirmation(f.ctx, input)
	if err == nil {
		t.Fatal("missing interruption")
	}
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	if _, err = f.facade.CommitConfirmation(f.ctx, input); err == nil {
		t.Fatal("UNKNOWN dispatch repeated")
	}
	candidates, err := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("unknown promoted to refusal %+v %v", candidates, err)
	}
	var operation string
	if err = f.plane.DB.SQL().QueryRow(`SELECT operation_id FROM merge_delivery_current_operations WHERE merge_request_id=? AND repository_id=?`, partial.Steps[0].RequestID, m.RepositoryID).Scan(&operation); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: partial.Steps[0].RequestID, RepositoryID: m.RepositoryID, PredecessorOperationID: operation, RequestID: "forged-unknown-retry"}); err == nil {
		t.Fatal("UNKNOWN acquired retry")
	}
	if _, err = f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "new-merge-bypass", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area}, Variants: []c.Variant{{ID: "merge", Summary: "merge"}}}); err == nil {
		t.Fatal("new confirmation bypassed UNKNOWN")
	}
	if got := gitOutput(t, m.TargetPath, "rev-parse", "HEAD"); got != m.ExpectedDevHead {
		t.Fatalf("unexpected Git effect %s", got)
	}
	var negatives int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM merge_delivery_outcomes`).Scan(&negatives); err != nil || negatives != 0 {
		t.Fatalf("fabricated result %d %v", negatives, err)
	}
}

func TestMergeV119AppliedReceiptRecoversWithoutGitCheckout(t *testing.T) {
	f := completeFollowupTask(t)
	prepared, input := prepareFollowupMerge(t, f)
	m := prepared.Content.FeatureArea.DeliveryPlan.Members[1]
	count := 0
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
		if stage == "AREA_MERGE_AFTER_GIT" {
			count++
			if count == 2 {
				return errors.New("lost member projection after exact outcome")
			}
		}
		return nil
	})
	if _, err := f.facade.CommitConfirmation(f.ctx, input); err == nil {
		t.Fatal("missing interruption")
	}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
	offline := m.TargetPath + "-offline"
	if err := os.Rename(m.TargetPath, offline); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Rename(offline, m.TargetPath); err != nil {
			t.Error(err)
		}
	}()
	// Read the exact saved result; even Git's common-directory lookup is forbidden
	// here because the checkout is unavailable after its already-durable outcome.
	resumed, err := f.facade.CommitConfirmation(f.ctx, input)
	if err != nil || resumed.State != "COMPLETED" {
		t.Fatalf("applied receipt depended on live checkout: %+v %v", resumed, err)
	}
}
