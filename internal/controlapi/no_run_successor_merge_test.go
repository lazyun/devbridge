package controlapi_test

import (
	"errors"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Continue the no-Run cancellation path through its actual downstream consumers:
// frozen merge confirmation, interrupted Git projection, cold replay and RETIRE.
func TestNoRunSuccessorMergeRecoveryAndRetirement(t *testing.T) {
	var f *upperLifecycleFixture
	var successor wp.SuccessorCommandReceipt
	runNoRunSuccessorDelivery(t, func(current *upperLifecycleFixture, receipt wp.SuccessorCommandReceipt) wp.SuccessorCommandReceipt {
		f, successor = current, receipt
		reopenNoRunSuccessorProtocolFixture(t, f)
		return receipt
	})
	f.plane.Retirement.Close()
	history := func() string {
		return definitionFormalSnapshot(t, f, []string{"execution_cancellations"})
	}
	originalHistory := history()
	prepared, input := prepareFollowupMerge(t, f)
	plan := prepared.Content.FeatureArea.DeliveryPlan
	if plan == nil || len(plan.Members) != 2 {
		t.Fatalf("successor missing exact two-repository merge: %+v", plan)
	}
	effects := 0
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
		if stage == "AREA_MERGE_AFTER_GIT" {
			effects++
			if effects == 2 {
				return errors.New("lost successor member projection")
			}
		}
		return nil
	})
	partial, err := f.facade.CommitConfirmation(f.ctx, input)
	if err == nil || partial.State != "UNKNOWN" || effects != 2 || len(partial.Steps) != 1 {
		t.Fatalf("missing interrupted successor merge: %+v effects=%d err=%v", partial, effects, err)
	}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
	request := partial.Steps[0].RequestID
	for _, m := range plan.Members {
		if head := gitOutput(t, m.TargetPath, "rev-parse", "HEAD"); head != m.SourceHead {
			t.Fatalf("merge did not apply source head for %s: %s", m.RepositoryID, head)
		}
	}
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	for _, phase := range []string{"recover", "replay"} {
		done, err := f.facade.CommitConfirmation(f.ctx, input)
		if err != nil || done.State != "COMPLETED" {
			t.Fatalf("%s successor merge: %+v %v", phase, done, err)
		}
		for _, m := range plan.Members {
			if head := gitOutput(t, m.TargetPath, "rev-parse", "HEAD"); head != m.SourceHead {
				t.Fatalf("%s changed applied Git for %s: %s", phase, m.RepositoryID, head)
			}
		}
	}
	var parents, outcomes, dispatches, runs int
	if err = f.plane.DB.SQL().QueryRow(`SELECT
 (SELECT count(*) FROM feature_area_commit_receipts WHERE feature_area_id=? AND completed_at IS NOT NULL),
 (SELECT count(*) FROM merge_delivery_outcomes o JOIN merge_delivery_operations p ON p.operation_id=o.operation_id WHERE p.merge_request_id=? AND o.outcome='APPLIED'),
 (SELECT count(*) FROM merge_delivery_dispatches d JOIN merge_delivery_operations p ON p.operation_id=d.operation_id WHERE p.merge_request_id=?),
 (SELECT count(*) FROM runs WHERE execution_proposal_id=?)`, f.area, request, request, successor.SuccessorExecutionID).Scan(&parents, &outcomes, &dispatches, &runs); err != nil || parents != 1 || outcomes != 2 || dispatches != 2 || runs != 2 {
		t.Fatalf("duplicate/missing effects: parents=%d outcomes=%d dispatches=%d runs=%d err=%v", parents, outcomes, dispatches, runs, err)
	}
	p, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
	if err != nil || p.TaskView == nil || p.TaskView.CommitStatus != "COMMITTED" || p.TaskView.DevIntegrationStatus != "MERGED" {
		t.Fatalf("merged successor presentation: %+v %v", p.TaskView, err)
	}
	if _, err = f.facade.RetireFeatureArea(f.ctx, f.session, f.area, "retire-delivered-successor"); err != nil {
		t.Fatal(err)
	}
	store := sqlite.NewFeatureAreaStore(f.plane.DB)
	if err = store.FinishRetirement(f.ctx, f.area, ""); err != nil {
		t.Fatal(err)
	}
	area, err := store.Get(f.ctx, f.area)
	if err != nil || area.Status != featurearea.StatusRetired {
		t.Fatalf("successor retirement incomplete: %+v %v", area, err)
	}
	for _, cold := range []bool{false, true} {
		if cold {
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
		}
		receipt, err := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash})
		if err != nil || receipt.State != "COMPLETED" {
			t.Fatalf("retired merge confirmation cold=%v: %+v %v", cold, receipt, err)
		}
		if after := history(); after != originalHistory {
			t.Fatalf("delivery/retirement rewrote original cancellation: %s -> %s", originalHistory, after)
		}
	}
}

func TestNoRunSuccessorUnknownMergeKeepsOriginalAuthority(t *testing.T) {
	var f *upperLifecycleFixture
	runNoRunSuccessorDelivery(t, func(current *upperLifecycleFixture, receipt wp.SuccessorCommandReceipt) wp.SuccessorCommandReceipt {
		f = current
		return receipt
	})
	f.plane.Retirement.Close()
	prepared, input := prepareFollowupMerge(t, f)
	member := prepared.Content.FeatureArea.DeliveryPlan.Members[0]
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
		if stage == "AREA_MERGE_BEFORE_RESULT" {
			return errors.New("merge outcome lost")
		}
		return nil
	})
	partial, err := f.facade.CommitConfirmation(f.ctx, input)
	if err == nil || partial.State != "UNKNOWN" || len(partial.Steps) != 1 {
		t.Fatalf("missing UNKNOWN: %+v %v", partial, err)
	}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
	request := partial.Steps[0].RequestID
	var operation string
	if err = f.plane.DB.SQL().QueryRow(`SELECT operation_id FROM merge_delivery_current_operations WHERE merge_request_id=? AND repository_id=?`, request, member.RepositoryID).Scan(&operation); err != nil {
		t.Fatal(err)
	}
	if head := gitOutput(t, member.TargetPath, "rev-parse", "HEAD"); head != member.SourceHead {
		t.Fatalf("missing real Git effect: %s", head)
	}
	// Even after Git visibly reaches the target, only an exact durable outcome
	// can settle the operation. None of these public commands may infer one.
	for _, cold := range []bool{false, true} {
		if cold {
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
		}
		tables := definitionFormalEveryTable(t, f)
		before := definitionFormalSnapshot(t, f, tables)
		for _, release := range []bool{false, true} {
			_, err = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: request, RepositoryID: member.RepositoryID, PredecessorOperationID: operation, RequestID: "unknown-successor-retry", ReleasePlan: release})
			if err == nil {
				t.Fatalf("UNKNOWN reprepare admitted cold=%v release=%v", cold, release)
			}
		}
		if _, err = f.facade.RetireFeatureArea(f.ctx, f.session, f.area, "unknown-retirement"); !errors.Is(err, developmentcycle.ErrFinalizationPending) {
			t.Fatalf("UNKNOWN retirement: %v", err)
		}
		if _, err = f.facade.SelectDevelopmentBranchForSession(f.ctx, developmentcycle.SelectInput{AISessionID: f.session, RepositoryID: member.RepositoryID, DevelopmentRef: member.DevelopmentRef, RequestID: "unknown-reselect"}); !errors.Is(err, developmentcycle.ErrFinalizationPending) {
			t.Fatalf("UNKNOWN branch reselect: %v", err)
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "UNKNOWN cannot release, retry, retire or reselect")
		replayed, err := f.facade.CommitConfirmation(f.ctx, input)
		if err == nil || replayed.State == "COMPLETED" {
			t.Fatalf("UNKNOWN confirmation inferred completion: %+v %v", replayed, err)
		}
		if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "new-merge-cannot-bypass"); err == nil {
			t.Fatal("new request bypassed UNKNOWN")
		}
		p, err := f.facade.FeatureAreaPresentationForSession(f.ctx, f.session, f.area)
		if err != nil || p.Commit == nil || p.Commit.CanCommit || p.Commit.CanRecoverMerge || p.Commit.MergeRetryOperation != nil || p.CommitView == nil || p.CommitView.Activity != "OUTCOME_UNKNOWN" {
			t.Fatalf("UNKNOWN exposed continuation: %+v %+v %v", p.Commit, p.CommitView, err)
		}
		var parents, dispatches, outcomes int
		if err = f.plane.DB.SQL().QueryRow(`SELECT
 (SELECT count(*) FROM feature_area_commit_receipts WHERE feature_area_id=?),
 (SELECT count(*) FROM merge_delivery_dispatches d JOIN merge_delivery_operations o ON o.operation_id=d.operation_id WHERE o.merge_request_id=?),
 (SELECT count(*) FROM merge_delivery_outcomes r JOIN merge_delivery_operations o ON o.operation_id=r.operation_id WHERE o.merge_request_id=?)`, f.area, request, request).Scan(&parents, &dispatches, &outcomes); err != nil || parents != 1 || dispatches != 1 || outcomes != 0 {
			t.Fatalf("UNKNOWN effects changed: parents=%d dispatches=%d outcomes=%d err=%v", parents, dispatches, outcomes, err)
		}
		if head := gitOutput(t, member.TargetPath, "rev-parse", "HEAD"); head != member.SourceHead {
			t.Fatalf("UNKNOWN recovery changed Git: %s", head)
		}
	}
}
