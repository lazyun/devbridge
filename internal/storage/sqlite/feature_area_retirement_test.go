package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/retirement"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
)

func requestRetirement(t *testing.T, f agentStorageFixture) featurearea.FeatureArea {
	t.Helper()
	a, err := ownershipLifecycleService(t, f).Retire(context.Background(), f.session.ID, f.area.ID, "retire-once")
	if err != nil || a.Status != featurearea.StatusRetiring || a.Retirement == nil {
		t.Fatalf("intent=%+v %v", a, err)
	}
	return a
}
func convergeUnlaunchedRetirement(t *testing.T, db *DB, area string) {
	t.Helper()
	ctx := context.Background()
	s := NewFeatureAreaStore(db)
	targets, err := s.RetirementTargets(ctx, area)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range targets {
		if _, _, err = NewExecutionCancellationStore(db).Begin(ctx, in, false); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.FinishRetirement(ctx, area, ""); err != nil {
		t.Fatal(err)
	}
	a, err := s.Get(ctx, area)
	if err != nil || a.Status != featurearea.StatusRetired || a.Retirement.CompletedAt == nil {
		t.Fatalf("completion=%+v %v", a, err)
	}
}
func TestRetirementBasicAndPreRun(t *testing.T) {
	for _, mode := range []string{"DISCOVERING", "ACTIVE", "TASK", "CONFIRMED_EXECUTION"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newAgentStorageFixture(t, "retire-"+mode)
			if mode == "DISCOVERING" || mode == "ACTIVE" {
				f.area.ID += "-empty"
				f.area.Status = featurearea.Status(mode)
				if err := NewFeatureAreaStore(f.db).Create(ctx, f.area); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "PENDING_EXECUTION" || mode == "CONFIRMED_EXECUTION" {
				id := seedExecutionOwner(t, f.db, f.task, f.session, "retire-owner")
				_ = id // Task approval already authorizes the candidate.
			}
			before := requestRetirement(t, f)
			convergeUnlaunchedRetirement(t, f.db, f.area.ID)
			for _, request := range []string{"retire-once", "another-retry"} {
				a, err := ownershipLifecycleService(t, f).Retire(ctx, f.session.ID, f.area.ID, request)
				if err != nil || a.Status != featurearea.StatusRetired || a.Retirement.RequestID != before.Retirement.RequestID {
					t.Fatalf("replay=%+v %v", a, err)
				}
			}
			var n int
			want := 0
			if mode == "CONFIRMED_EXECUTION" {
				want = 1
			} // retained Review
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM runs WHERE feature_area_id=?`, f.area.ID).Scan(&n); err != nil || n != want {
				t.Fatalf("fake backend=%d %v", n, err)
			}
		})
	}
}
func TestRetirementOwnerKnownAndOrphanMatrix(t *testing.T) {
	for _, kind := range []agentconversation.OperationKind{agentconversation.Review, agentconversation.Execution} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unknown=%v", kind, unknown), func(t *testing.T) {
				ctx := context.Background()
				f, r, _ := unifiedOwnerFixture(t, kind)
				conversations := NewAgentConversationStore(f.db)
				original, err := conversations.Boundary(ctx, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				requestRetirement(t, f)
				next := f.task
				next.ID += "-forbidden"
				if err = NewTaskStore(f.db).Create(ctx, next); err == nil {
					t.Fatal("Task admitted after intent")
				}
				if _, err = f.db.sql.Exec(`UPDATE runs SET turn_id='late-turn' WHERE id=?`, r.ID); err == nil {
					t.Fatal("new Turn after intent")
				}
				if tx, e := f.db.beginWrite(runtimeepoch.WithHostToolFence(ctx, r.RuntimeEpoch, r.ID)); e == nil {
					tx.Rollback()
					t.Fatal("Host Tool write after intent")
				}
				if _, err = conversations.AuthorizeRun(ctx, r.ID); err == nil {
					t.Fatal("Host Tool capability retained")
				}
				if _, err = conversations.Reset(ctx, f.area.ID, "late-reset"); err == nil {
					t.Fatal("reset admitted after intent")
				}
				targets, err := NewFeatureAreaStore(f.db).RetirementTargets(ctx, f.area.ID)
				if err != nil {
					t.Fatal(err)
				}
				var in executioncancel.Input
				for _, v := range targets {
					if v.RunID == r.ID {
						in = v
					}
				}
				if in.OwnerType != string(kind) {
					t.Fatalf("targets=%+v", targets)
				}
				target := dispatchCancellation(t, f, in)
				if unknown {
					err = NewExecutionCancellationStore(f.db).FinishInterrupted(ctx, r.ID)
				} else {
					err = cancelWithSafeIntent(t, f, r, target)
				}
				if err != nil {
					t.Fatal(err)
				}
				convergeUnlaunchedRetirement(t, f.db, f.area.ID)
				if unknown {
					after, _ := conversations.Boundary(ctx, r.ID)
					if !reflect.DeepEqual(original, after) || after.Quiesced {
						t.Fatal("UNKNOWN rewritten")
					}
				}
				if _, err = NewRunStore(f.db).Update(runtimeepoch.WithFence(ctx, r.RuntimeEpoch, r.ID), r.ID, func(v *run.Run) error { v.Status = run.StatusSucceeded; return nil }); err == nil {
					t.Fatal("late success")
				}
				if _, err = conversations.GetClaim(ctx, f.area.ID); !errors.Is(err, agentconversation.ErrNotFound) {
					t.Fatal("claim retained")
				}
			})
		}
	}
}

// A restarted runtime has no locally owned backend. Canonical Begin consumes
// durable receipts/boundaries without redispatching an interrupt.
type retirementTestCanceler struct{ store *ExecutionCancellationStore }

func (c retirementTestCanceler) CancelExecution(ctx context.Context, in executioncancel.Input) (executioncancel.Receipt, error) {
	v, _, err := c.store.Begin(ctx, in, false)
	return v, err
}
func TestRetirementCrashResumeAndAtomicIntent(t *testing.T) {
	for _, window := range []string{"INTENT", "CANCELLATION", "DISPATCH", "LAST_TASK"} {
		t.Run(window, func(t *testing.T) {
			ctx := context.Background()
			f, r, _ := unifiedOwnerFixture(t, agentconversation.Review)
			requestRetirement(t, f)
			targets, err := NewFeatureAreaStore(f.db).RetirementTargets(ctx, f.area.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, in := range targets {
				if in.RunID != r.ID {
					continue
				}
				switch window {
				case "CANCELLATION":
					_, _, err = NewExecutionCancellationStore(f.db).Begin(ctx, in, true)
				case "DISPATCH", "LAST_TASK":
					dispatchCancellation(t, f, in)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if window == "LAST_TASK" {
				if err = NewExecutionCancellationStore(f.db).FinishInterrupted(ctx, r.ID); err != nil {
					t.Fatal(err)
				}
			}
			original, _ := NewAgentConversationStore(f.db).Boundary(ctx, r.ID)
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			db := openTestDB(t, f.root)
			defer db.Close()
			if err = NewRecoveryStore(db).RecoverAgentOperations(ctx, testNow); err != nil {
				t.Fatal(err)
			}
			coordinator := retirement.Start(NewFeatureAreaStore(db), retirementTestCanceler{NewExecutionCancellationStore(db)})
			defer coordinator.Close()
			deadline := time.Now().Add(5 * time.Second)
			for {
				a, err := NewFeatureAreaStore(db).Get(ctx, f.area.ID)
				if err != nil {
					t.Fatal(err)
				}
				if a.Status == featurearea.StatusRetired {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("startup stuck=%+v", a)
				}
				time.Sleep(10 * time.Millisecond)
			}
			after, _ := NewAgentConversationStore(db).Boundary(ctx, r.ID)
			if !reflect.DeepEqual(original, after) {
				t.Fatal("restart changed UNKNOWN")
			}
		})
	}
	f := newAgentStorageFixture(t, "retire-rollback")
	if _, err := f.db.sql.Exec(`CREATE TRIGGER fail_retirement BEFORE UPDATE OF status ON feature_areas WHEN NEW.status='RETIRING' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := ownershipLifecycleService(t, f).Retire(context.Background(), f.session.ID, f.area.ID, "retire"); err == nil {
		t.Fatal("fault ignored")
	}
	var n int
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM feature_area_retirements`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial intent=%d %v", n, err)
	}
}
func TestRetirementReviewAdmissionAndDuplicateIntentOrdering(t *testing.T) {
	for i := 0; i < 8; i++ {
		f := newAgentStorageFixture(t, fmt.Sprintf("retire-review-race-%d", i))
		ctx := context.Background()
		op := prepareAgentOperation(t, f, agentconversation.Review, "race")
		svc := ownershipLifecycleService(t, f)
		r, reserveErr, retireErr := raceReservationAndLifecycle(func() (run.Run, error) { return op.make(ctx, "race-run") }, func() error { _, err := svc.Retire(ctx, f.session.ID, f.area.ID, "first"); return err })
		if retireErr != nil {
			t.Fatal(retireErr)
		}
		if reserveErr == nil {
			targets, err := NewFeatureAreaStore(f.db).RetirementTargets(ctx, f.area.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, v := range targets {
				found = found || v.RunID == r.ID
			}
			if !found {
				t.Fatal("admission winner omitted")
			}
		}
		if _, err := op.make(ctx, "after-retire"); err == nil {
			t.Fatal("Review admitted after stop")
		}
		if _, err := svc.Retire(ctx, f.session.ID, f.area.ID, "second"); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := f.db.sql.QueryRow(`SELECT count(*) FROM feature_area_retirements WHERE feature_area_id=?`, f.area.ID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("duplicate intent=%d %v", n, err)
		}
	}
}
func TestRetirementLastTerminalRollbackAndOtherAreaIndependence(t *testing.T) {
	f := newAgentStorageFixture(t, "retire-final-rollback")
	ctx := context.Background()
	other := f.area
	other.ID += "-other"
	if err := NewFeatureAreaStore(f.db).Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	requestRetirement(t, f)
	next := f.task
	next.ID += "-independent"
	next.FeatureAreaID = other.ID
	if err := NewTaskStore(f.db).Create(ctx, next); err != nil {
		t.Fatal(err)
	}
	store := NewFeatureAreaStore(f.db)
	targets, err := store.RetirementTargets(ctx, f.area.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range targets {
		if _, _, err = NewExecutionCancellationStore(f.db).Begin(ctx, in, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.db.sql.Exec(`CREATE TRIGGER fail_retired BEFORE UPDATE OF completed_at ON feature_area_retirements BEGIN SELECT RAISE(ABORT,'injected final crash'); END`); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRetirement(ctx, f.area.ID, ""); err == nil {
		t.Fatal("fault ignored")
	}
	a, err := store.Get(ctx, f.area.ID)
	if err != nil || a.Status != featurearea.StatusRetiring || a.Retirement.CompletedAt != nil {
		t.Fatalf("partial finalization=%+v %v", a, err)
	}
	if _, err = f.db.sql.Exec(`DROP TRIGGER fail_retired`); err != nil {
		t.Fatal(err)
	}
	convergeUnlaunchedRetirement(t, f.db, f.area.ID)
	var n int
	if err = f.db.sql.QueryRow(`SELECT count(*) FROM events WHERE entity_id=? AND type='feature_area.status_changed'`, f.area.ID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("status events=%d %v", n, err)
	}
}
func TestRetirementVerificationExactCurrentRun(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			f, implementation, _, _ := cancellationStoreFixture(t, "retire-verification", true)
			ctx := context.Background()
			conversations := NewAgentConversationStore(f.db)
			if err := conversations.RecordFinalization(ctx, implementation.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: implementation.TurnID, Quiesced: true}); err != nil {
				t.Fatal(err)
			}
			runs := run.NewService(NewRunStore(f.db), nil, nil)
			if _, err := runs.Transition(ctx, implementation.ID, run.StatusCollecting); err != nil {
				t.Fatal(err)
			}
			if _, err := runs.Succeed(ctx, implementation.ID, run.Result{}); err != nil {
				t.Fatal(err)
			}
			_, verification, err := NewExecutionProposalStore(f.db).ReserveVerificationRun(ctx, implementation.ExecutionProposalID, "retire-verification-run", testNow.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			setAgentRunState(t, f.db, verification.ID, run.StatusPreparing, "", "")
			if _, err = conversations.BeginRun(ctx, verification.ID); err != nil {
				t.Fatal(err)
			}
			setAgentRunState(t, f.db, verification.ID, run.StatusRunning, implementation.ThreadID, "verification-turn")
			if err = conversations.MarkTurnPending(ctx, verification.ID); err != nil {
				t.Fatal(err)
			}
			verification, err = NewRunStore(f.db).Get(ctx, verification.ID)
			if err != nil {
				t.Fatal(err)
			}
			original, _ := conversations.Boundary(ctx, verification.ID)
			requestRetirement(t, f)
			targets, err := NewFeatureAreaStore(f.db).RetirementTargets(ctx, f.area.ID)
			if err != nil || len(targets) != 1 || targets[0].RunID != verification.ID {
				t.Fatalf("targets=%+v %v", targets, err)
			}
			target := dispatchCancellation(t, f, targets[0])
			if unknown {
				err = NewExecutionCancellationStore(f.db).FinishInterrupted(ctx, verification.ID)
			} else {
				err = cancelWithSafeIntent(t, f, verification, target)
			}
			if err != nil {
				t.Fatal(err)
			}
			convergeUnlaunchedRetirement(t, f.db, f.area.ID)
			if unknown {
				after, _ := conversations.Boundary(ctx, verification.ID)
				if !reflect.DeepEqual(original, after) || after.Quiesced {
					t.Fatal("UNKNOWN rewritten")
				}
			}
			old, err := NewRunStore(f.db).Get(ctx, implementation.ID)
			if err != nil || old.Status != run.StatusSucceeded {
				t.Fatalf("implementation history=%+v %v", old, err)
			}
		})
	}
}
func TestRetirementPendingReviewGateCannotAdvance(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	svc, _ := featurearea.NewService(NewFeatureAreaStore(f.db), NewAISessionStore(f.db), NewTaskStore(f.db).CountByFeatureArea, nil, nil)
	if _, err := svc.Retire(ctx, f.session, f.area, "stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Resolve(ctx, f.input(a, "APPROVE")); err == nil {
		t.Fatal("retiring area approved Task")
	}
	convergeUnlaunchedRetirement(t, f.db, f.area)
	value, err := NewTaskStore(f.db).Get(ctx, a.Review.TaskID)
	if err != nil || value.Status != task.StatusCanceled || value.ReviewApprovalID != "" || value.CurrentExecutionID != "" {
		t.Fatalf("retirement fabricated authority: %+v %v", value, err)
	}
	if _, err = f.store.Resolve(ctx, f.input(a, "APPROVE")); err == nil {
		t.Fatal("retired gate revived")
	}
}

func TestRetirementDoesNotCancelProjectInspection(t *testing.T) {
	f := newAgentStorageFixture(t, "retire-inspection")
	ctx := context.Background()
	probes := NewContextProbeStore(f.db)
	p := contextprobe.Probe{ID: "inspection-independent", AISessionID: f.session.ID, ProjectID: f.project.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopePaths, Paths: []string{"README.md"}}, CodexInstruction: "Inspect Project", Status: contextprobe.StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, CreatedAt: testNow, UpdatedAt: testNow}
	if err := probes.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	p, err := probes.Update(ctx, p.ID, func(v *contextprobe.Probe) error {
		v.Status = contextprobe.StatusRunning
		v.StartedAt = &testNow
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	requestRetirement(t, f)
	convergeUnlaunchedRetirement(t, f.db, f.area.ID)
	inspection, err := NewTaskStore(f.db).Get(ctx, p.TaskID)
	if err != nil || inspection.Type != task.TypeInspection || inspection.Scope != task.ScopeProject || inspection.Status != task.StatusRunning {
		t.Fatalf("inspection=%+v %v", inspection, err)
	}
	if _, err = probes.Update(runtimeepoch.WithFence(ctx, p.RuntimeEpoch, p.RunID), p.ID, func(v *contextprobe.Probe) error {
		v.Status = contextprobe.StatusSucceeded
		v.FinishedAt = &testNow
		v.Result = "Project facts retained"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestRetirementReviewCompletionAndExistingCancellation(t *testing.T) {
	for _, existingCancel := range []bool{false, true} {
		t.Run(fmt.Sprint(existingCancel), func(t *testing.T) {
			ctx := context.Background()
			f, r, _ := unifiedOwnerFixture(t, agentconversation.Review)
			store := NewExecutionCancellationStore(f.db)
			if existingCancel {
				input := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: "REVIEW", OwnerID: r.OperationID, RunID: r.ID, RequestID: "user-cancel-first", Reason: "User stop"}
				dispatchCancellation(t, f, input)
				requestRetirement(t, f)
				targets, err := NewFeatureAreaStore(f.db).RetirementTargets(ctx, f.area.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, target := range targets {
					if target.RunID == r.ID && (target.RequestID != input.RequestID || target.Reason != input.Reason) {
						t.Fatal("parallel cancellation identity")
					}
				}
				if err = store.FinishInterrupted(ctx, r.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				svc := ownershipLifecycleService(t, f)
				_, completionErr, retireErr := raceReservationAndLifecycle(func() (run.Run, error) {
					if err := NewAgentConversationStore(f.db).RecordFinalization(ctx, r.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: r.TurnID, Quiesced: true}); err != nil {
						return run.Run{}, err
					}
					runs := run.NewService(NewRunStore(f.db), nil, nil)
					if _, err := runs.Transition(ctx, r.ID, run.StatusCollecting); err != nil {
						return run.Run{}, err
					}
					return runs.Succeed(ctx, r.ID, run.Result{})
				}, func() error { _, err := svc.Retire(ctx, f.session.ID, f.area.ID, "retire-once"); return err })
				if completionErr != nil || retireErr != nil {
					t.Fatalf("completion=%v retire=%v", completionErr, retireErr)
				}
			}
			convergeUnlaunchedRetirement(t, f.db, f.area.ID)
			actual, err := NewRunStore(f.db).Get(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !existingCancel && actual.Status != run.StatusSucceeded {
				t.Fatal("completion history lost")
			}
		})
	}
}
