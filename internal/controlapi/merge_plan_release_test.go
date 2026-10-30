package controlapi_test

import (
	"context"
	"errors"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
)

func TestMergeTargetReservationSerializesAreas(t *testing.T) {
	f := completeFollowupTask(t)
	other := createDirectAreaOnFixture(t, f, "reservation-other")
	current := approveDirectOnFixture(t, other, "reservation-other")
	acceptMatchingMergeContent(t, other, current, "reservation-other")
	legacyCopy := *f
	legacy := mixedLegacyTaskFromFixture(t, &legacyCopy)
	calls := 0
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, repo string) error {
		if stage != "AREA_MERGE_BEFORE_GIT" || calls != 0 {
			return nil
		}
		calls++
		for _, area := range []string{other.area, legacy.area} {
			for _, inventory := range []bool{false, true} {
				var cap featurearea.CommitCapability
				var e error
				if inventory {
					cap, e = f.plane.WorkflowProgression.CommitInventoryCapability(f.ctx, f.project, area)
				} else {
					cap, e = f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, area)
				}
				if e != nil || cap.CanCommit || cap.Reason != "GIT_FINALIZATION_PENDING" {
					t.Fatalf("area=%s inventory=%v busy target advertised: %+v %v", area, inventory, cap, e)
				}
			}
		}
		cap, e := other.plane.WorkflowProgression.CommitCapability(other.ctx, other.project, other.area)
		if e != nil || cap.CanCommit || cap.Reason != "GIT_FINALIZATION_PENDING" {
			t.Fatalf("busy target advertised as available: %+v %v", cap, e)
		}
		_, e = other.facade.CommitFeatureArea(other.ctx, other.session, other.area, "competing-reservation")
		if !errors.Is(e, developmentcycle.ErrFinalizationPending) {
			t.Fatalf("competing merge must fail before reserving: %v", e)
		}
		var n int
		if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_commit_receipts WHERE feature_area_id=?`, other.area).Scan(&n); e != nil || n != 0 {
			t.Fatalf("competing request left receipt: %d %v", n, e)
		}
		return nil
	})
	if _, e := f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "reservation-first"); e != nil {
		t.Fatal(e)
	}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
	if calls != 1 {
		t.Fatal("missing overlap")
	}
	if _, e := other.facade.CommitFeatureArea(other.ctx, other.session, other.area, "competing-reservation"); e != nil {
		t.Fatalf("fresh plan after first settles: %v", e)
	}
}

func TestRejectedMergePlanReleaseAndFreshConfirmation(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "unapplied"
		if partial {
			name = "partial"
		}
		t.Run(name, func(t *testing.T) {
			f := completeFollowupTask(t)
			prepared, input := prepareFollowupMerge(t, f)
			plan := prepared.Content.FeatureArea.DeliveryPlan
			index := 0
			if partial {
				index = 1
			}
			m := plan.Members[index]
			drifted := false
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, repo string) error {
				if stage == "AREA_MERGE_BEFORE_GIT" && repo == m.RepositoryID && !drifted {
					drifted = true
					gitOutput(t, m.TargetPath, "commit", "--allow-empty", "-m", "external advancement")
				}
				return nil
			})
			failed, e := f.facade.CommitConfirmation(f.ctx, input)
			f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
			if e == nil || !drifted {
				t.Fatalf("expected rejection: %+v %v", failed, e)
			}
			request := failed.Steps[0].RequestID
			candidates, e := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
			if e != nil || len(candidates) != 1 || !candidates[0].CanRelease {
				t.Fatalf("release not available: %+v %v", candidates, e)
			}
			in := delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: request, RepositoryID: m.RepositoryID, PredecessorOperationID: candidates[0].OperationID, RequestID: "release-obsolete", ReleasePlan: true}
			before := gitOutput(t, m.TargetPath, "rev-parse", "HEAD")
			type attempt struct {
				receipt delivery.MergeRetryReceipt
				err     error
			}
			replies := make(chan attempt, 2)
			for range 2 {
				go func() { v, e := f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in); replies <- attempt{v, e} }()
			}
			firstReply, secondReply := <-replies, <-replies
			released, e := firstReply.receipt, firstReply.err
			if secondReply.err != nil || secondReply.receipt != released {
				t.Fatalf("concurrent release: %+v %+v", firstReply, secondReply)
			}
			if e != nil || !released.PlanReleased || released.OperationID != "" {
				t.Fatalf("release: %+v %v", released, e)
			}
			var releasedAt, completedAt any
			if e = f.plane.DB.SQL().QueryRow(`SELECT released_at,completed_at FROM feature_area_commit_receipts WHERE request_id=?`, request).Scan(&releasedAt, &completedAt); e != nil || releasedAt == nil || completedAt != nil {
				t.Fatalf("release fabricated completion: %v %v %v", releasedAt, completedAt, e)
			}

			for _, query := range []string{
				`UPDATE feature_area_commit_receipts SET released_at=NULL WHERE request_id=?`,
				`UPDATE feature_area_commit_receipts SET completed_at='2026-10-02T00:00:00Z' WHERE request_id=?`,
				`DELETE FROM merge_plan_releases WHERE merge_request_id=?`,
			} {
				if _, e = f.plane.DB.SQL().Exec(query, request); e == nil {
					t.Fatalf("mutable release: %s", query)
				}
			}
			for _, cold := range []bool{false, true} {
				if cold {
					reopenDirectDeliveryFixture(t, f)
					f.plane.Retirement.Close()
				}
				assertDeliveryReplayReadOnly(t, f, func(ctx context.Context) {
					replay, err := f.facade.ReprepareFeatureAreaMerge(ctx, f.session, in)
					if err != nil || replay != released {
						t.Fatalf("read-only release replay: %+v %v", replay, err)
					}
					changed := in
					changed.ReleasePlan = false
					if _, err = f.facade.ReprepareFeatureAreaMerge(ctx, f.session, changed); !errors.Is(err, delivery.ErrRequestConflict) {
						t.Fatalf("release/retry request conflict: %v", err)
					}
					changed = in
					changed.RepositoryID = "wrong-repository"
					if _, err = f.facade.ReprepareFeatureAreaMerge(ctx, f.session, changed); !errors.Is(err, delivery.ErrRequestConflict) {
						t.Fatalf("changed release request: %v", err)
					}
					fresh := in
					fresh.RequestID = "new-request-without-writer"
					if _, err = f.facade.ReprepareFeatureAreaMerge(ctx, f.session, fresh); err == nil {
						t.Fatal("fresh delivery command bypassed writer fence")
					}

				})
				replay, e := f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in)
				if e != nil || replay != released {
					t.Fatalf("cold=%v replay: %+v %v", cold, replay, e)
				}
				changed := in
				changed.ReleasePlan = false
				if _, e = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, changed); !errors.Is(e, delivery.ErrRequestConflict) {
					t.Fatalf("changed mode: %v", e)
				}
				changed = in
				changed.RepositoryID = "foreign"
				if _, e = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, changed); !errors.Is(e, delivery.ErrRequestConflict) {
					t.Fatalf("changed payload: %v", e)
				}
				if _, e = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, request); !errors.Is(e, delivery.ErrPlanReleased) {
					t.Fatalf("old plan replay: %v", e)
				}
				cap, e := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
				if e != nil || !cap.CanCommit || cap.MergeRetryOperation != nil {
					t.Fatalf("released plan still blocks: %+v %v", cap, e)
				}
				facts, e := f.plane.WorkflowProgression.CommitPresentationFacts(f.ctx, f.project, f.area)
				if e != nil || facts.Pending || facts.Delivery.State != "RELEASED" {
					t.Fatalf("released presentation: %+v %v", facts, e)
				}
				old, e := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash})
				if e != nil || old.CanResume || old.State == "COMPLETED" {
					t.Fatalf("old confirmation: %+v %v", old, e)
				}
			}
			if gitOutput(t, m.TargetPath, "rev-parse", "HEAD") != before {
				t.Fatal("release/replay modified Git")
			}
			if _, e = f.facade.SelectDevelopmentBranchForSession(f.ctx, developmentcycle.SelectInput{AISessionID: f.session, RepositoryID: m.RepositoryID, DevelopmentRef: m.DevelopmentRef, RequestID: "released-cycle-reselect"}); e != nil {
				t.Fatalf("released plan blocks context: %v", e)
			}
			fresh, e := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "new-plan", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area}, Variants: []c.Variant{{ID: "merge", Summary: "Confirm current remaining members"}}})
			if e != nil {
				t.Fatal(e)
			}
			next := fresh.Content.FeatureArea.DeliveryPlan
			if len(next.Members) != 2-index || next.SHA256 == plan.SHA256 {
				t.Fatalf("new plan did not preserve completed history: %+v", next)
			}
			done, e := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: fresh.IntentID, Version: fresh.Version, Purpose: fresh.Purpose, Action: fresh.Action, RequestID: "new-plan-confirm", ContentHash: fresh.ContentHash, VariantID: "merge"})
			if e != nil || done.State != "COMPLETED" {
				t.Fatalf("fresh merge: %+v %v", done, e)
			}
			replay, e := f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in)
			if e != nil || replay != released {
				t.Fatalf("release replay after later completion: %+v %v", replay, e)
			}
		})
	}
}

func TestMergePlanReleaseRefusesUnknown(t *testing.T) {
	f := completeFollowupTask(t)
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, repo string) error {
		if stage == "AREA_MERGE_BEFORE_RESULT" {
			return errors.New("lost outcome")
		}
		return nil
	})
	if _, e := f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "unknown-plan"); e == nil {
		t.Fatal("expected unknown")
	}
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
	var op, repo string
	if e := f.plane.DB.SQL().QueryRow(`SELECT operation_id,repository_id FROM merge_delivery_dispatches JOIN merge_delivery_operations USING(operation_id) WHERE merge_request_id='unknown-plan'`).Scan(&op, &repo); e != nil {
		t.Fatal(e)
	}
	in := delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: "unknown-plan", RepositoryID: repo, PredecessorOperationID: op, RequestID: "release-unknown", ReleasePlan: true}
	if _, e := f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in); e == nil {
		t.Fatal("unknown released")
	}
	var n int
	if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM merge_plan_releases`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("release facts: %d %v", n, e)
	}
}

func acceptMatchingMergeContent(t *testing.T, f *upperLifecycleFixture, current task.Task, tag string) {
	t.Helper()
	started, err := directConfirmationTagged(t, f, current, "START_DIRECT_FILE", tag, directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("start %s intent=%+v err=%v", tag, started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.State != "ACTIVE" {
		t.Fatalf("state %s=%+v err=%v", tag, state, err)
	}
	for _, target := range current.RepositoryTargets {
		in := directInput(f, state, target)
		before, readErr := f.facade.FileRead(f.ctx, in)
		if readErr != nil {
			t.Fatal(readErr)
		}
		in.RequestID, in.ExpectedDigest, in.Content = tag+"-replace-"+target.RepositoryID, before.Digest, "fault delivery "+target.RepositoryID+"\n"
		if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: tag + "-submit", Summary: "Both exact members changed", VerificationNotes: "Explicit user waiver", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: tag + "-accept", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil || !accepted.Accepted || accepted.CommitError != "" {
		t.Fatalf("accept %s result=%+v err=%v", tag, accepted, err)
	}
}

// A known rejection in one repository must not hide an unknown dispatch in
// another. Seed only the interruption boundary, retaining all SQL guards.
func TestMergePlanReleaseRefusesMixedUnknown(t *testing.T) {
	assertMergeReleaseUnsafeSibling(t, "unknown")
}

func TestMergePlanReleaseRequiresCompleteProjectedEvidence(t *testing.T) {
	for _, boundary := range []string{"missing-operation", "missing-member", "unprojected-applied"} {
		t.Run(boundary, func(t *testing.T) { assertMergeReleaseUnsafeSibling(t, boundary) })
	}
}

func assertMergeReleaseUnsafeSibling(t *testing.T, boundary string) {
	f := completeFollowupTask(t)
	prepared, input := prepareFollowupMerge(t, f)
	plan := prepared.Content.FeatureArea.DeliveryPlan
	first, second := plan.Members[0], plan.Members[1]
	fired := false
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, repo string) error {
		if stage != "AREA_MERGE_BEFORE_GIT" || fired {
			return nil
		}
		fired = true
		gitOutput(t, first.TargetPath, "commit", "--allow-empty", "-m", "known target drift")
		// Model incomplete historical authority only in this isolated fixture.
		// Keep release admission and its SQL proof guard intact.
		if boundary == "missing-operation" || boundary == "missing-member" {
			if _, e := f.plane.DB.SQL().Exec(`DROP TRIGGER merge_current_durable`); e != nil {
				return e
			}
			if _, e := f.plane.DB.SQL().Exec(`DELETE FROM merge_delivery_current_operations WHERE repository_id=?`, second.RepositoryID); e != nil {
				return e
			}
			if boundary == "missing-member" {
				if _, e := f.plane.DB.SQL().Exec(`DROP TRIGGER merge_operation_durable`); e != nil {
					return e
				}
				if _, e := f.plane.DB.SQL().Exec(`DELETE FROM merge_delivery_operations WHERE repository_id=?`, second.RepositoryID); e != nil {
					return e
				}
				if _, e := f.plane.DB.SQL().Exec(`DROP TRIGGER area_commit_member_durable`); e != nil {
					return e
				}
				if _, e := f.plane.DB.SQL().Exec(`DELETE FROM feature_area_commit_members WHERE repository_id=?`, second.RepositoryID); e != nil {
					return e
				}
			}
			return nil
		}
		_, e := f.plane.DB.SQL().Exec(`INSERT INTO merge_delivery_dispatches(operation_id,runtime_epoch,created_at) SELECT c.operation_id,a.epoch_id,'2026-10-02T00:00:00Z' FROM merge_delivery_current_operations c CROSS JOIN runtime_authority a WHERE c.repository_id=? AND c.merge_request_id IN (SELECT request_id FROM feature_area_commit_receipts WHERE feature_area_id=?)`, second.RepositoryID, f.area)
		if e != nil {
			return e
		}
		if boundary == "unprojected-applied" {
			gitOutput(t, second.TargetPath, "merge", "--ff-only", second.SourceHead)
			result := gitOutput(t, second.TargetPath, "rev-parse", "HEAD")
			_, e = f.plane.DB.SQL().Exec(`INSERT INTO merge_delivery_outcomes(operation_id,outcome,before_external_effect,commit_oid,merge_oid,error,created_at) SELECT operation_id,'APPLIED',0,?,?,?,'2026-10-02T00:00:00Z' FROM merge_delivery_current_operations WHERE repository_id=?`, second.SourceHead, result, "", second.RepositoryID)
		}
		return e
	})
	failed, e := f.facade.CommitConfirmation(f.ctx, input)
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(nil)
	if e == nil {
		t.Fatal("expected rejected plus unknown")
	}
	request := failed.Steps[0].RequestID
	for _, cold := range []bool{false, true} {
		if cold {
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
		}
		candidates, e := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
		if e != nil || len(candidates) != 1 || candidates[0].CanRelease {
			t.Fatalf("unsafe sibling offered release: %+v %v", candidates, e)
		}
		in := delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: request, RepositoryID: first.RepositoryID, PredecessorOperationID: candidates[0].OperationID, RequestID: "mixed-release", ReleasePlan: true}
		if _, e = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, in); e == nil {
			t.Fatal("unsafe sibling released")
		}
		_, e = f.plane.DB.SQL().Exec(`INSERT INTO merge_plan_releases(project_id,request_id,merge_request_id,feature_area_id,repository_id,predecessor_operation_id,created_at) VALUES(?,?,?,?,?,?,?)`, f.project, in.RequestID, request, f.area, first.RepositoryID, in.PredecessorOperationID, "2026-10-02T00:00:00Z")
		if e == nil {
			t.Fatal("SQL bypass released unsafe sibling")
		}
	}
}
