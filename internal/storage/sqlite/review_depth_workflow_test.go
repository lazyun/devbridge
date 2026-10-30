package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

const furtherReviewResult = `{"version":1,"reviewDisposition":"FURTHER_REVIEW_REQUIRED","summary":"Additional evidence required","workSpec":null,"verificationSpec":null}`

func assertReviewFloor(t *testing.T, f *reviewApprovalFixture, phase task.Phase, depth *task.ReviewDepth) {
	t.Helper()
	value := ownedTask(t, f)
	if value.Phase != phase || !reflect.DeepEqual(value.RequiredReviewDepth, depth) {
		t.Fatalf("Task progression: phase=%s depth=%v want %s/%v", value.Phase, value.RequiredReviewDepth, phase, depth)
	}
	guidance, err := f.store.ReadTask(context.Background(), f.project, f.area, value.ID)
	if err != nil || !reflect.DeepEqual(guidance.RequiredReviewDepth, depth) || !reflect.DeepEqual(guidance.AllowedReviewDepths, task.AllowedReviewDepths(depth)) {
		t.Fatalf("guidance %+v: %v", guidance, err)
	}
}

func TestReviewDepthStateMatrix(t *testing.T) {
	for _, depth := range []task.ReviewDepth{task.ReviewDepthFast, task.ReviewDepthDeep} {
		for _, disposition := range []review.Disposition{review.DispositionApprovalReady, review.DispositionFurtherReviewRequired} {
			t.Run(string(depth)+"/"+string(disposition), func(t *testing.T) {
				f, _ := newTaskOwnedFixture(t)
				assertReviewFloor(t, f, task.PhaseReviewRequired, task.ReviewDepthPointer(task.ReviewDepthFast))
				f.candidate.ReviewDepth = depth
				a := f.start(t)
				assertReviewFloor(t, f, task.PhaseReviewing, &depth)
				if a.Review.ReviewDepth != depth || a.Run.ReviewID != a.Review.ID || a.Review.RunID != a.Run.ID {
					t.Fatalf("1:1/depth lost: %+v", a)
				}
				raw := furtherReviewResult
				if disposition == review.DispositionApprovalReady {
					raw = approvedResult(t)
				}
				f.complete(t, a, raw)
				latest, err := f.reviews.Get(context.Background(), a.Review.ID)
				if err != nil || latest.ReviewDisposition != disposition || latest.TerminalStatus != "SUCCEEDED" {
					t.Fatalf("successful review: %+v %v", latest, err)
				}
				if ownedTask(t, f).CurrentReviewID != a.Review.ID {
					t.Fatal("latest pointer lost")
				}
				if disposition == review.DispositionApprovalReady {
					assertReviewFloor(t, f, task.PhaseReviewApprovalPending, nil)
					if ownedCount(t, f, "review_approval_decisions") != 1 {
						t.Fatal("approval missing")
					}
				} else {
					assertReviewFloor(t, f, task.PhaseReviewApprovalPending, nil)
					if ownedCount(t, f, "review_approval_decisions") != 1 || ownedCount(t, f, "reviews") != 1 || ownedCount(t, f, "runs") != 1 {
						t.Fatal("further review did not create only the confirmation authority")
					}
					_, err = f.store.Resolve(context.Background(), f.input(a, "APPROVE"))
					var stale *wp.Error
					if !errors.As(err, &stale) || stale.Code != "REVIEW_APPROVAL_NOT_AVAILABLE" {
						t.Fatalf("further approved: %v", err)
					}
					resolved, err := f.store.Resolve(context.Background(), f.input(a, "REVISE"))
					if err != nil {
						t.Fatalf("further revise: %v", err)
					}
					assertReviewFloor(t, f, task.PhaseReviewing, task.ReviewDepthPointer(task.ReviewDepthDeep))
					next, err := f.reviews.Get(context.Background(), resolved.SuccessorReviewID)
					if err != nil || next.Ordinal != 2 || next.RunID != resolved.SuccessorRunID {
						t.Fatal("new Review required")
					}
					f.complete(t, review.StartAdmission{Review: next, Run: run.Run{ID: next.RunID}}, furtherReviewResult)
					if err := f.store.Reconcile(context.Background()); err != nil {
						t.Fatal(err)
					}
					assertReviewFloor(t, f, task.PhaseReviewApprovalPending, nil)
					resolved, err = f.store.Resolve(context.Background(), f.input(review.StartAdmission{Review: next}, "REVISE"))
					if err != nil {
						t.Fatalf("second further revise: %v", err)
					}
					assertReviewFloor(t, f, task.PhaseReviewing, task.ReviewDepthPointer(task.ReviewDepthDeep))
					latest, err := f.reviews.Get(context.Background(), resolved.SuccessorReviewID)
					if err != nil {
						t.Fatal(err)
					}
					f.complete(t, review.StartAdmission{Review: latest, Run: run.Run{ID: latest.RunID}}, approvedResult(t))
					if err := f.store.Reconcile(context.Background()); err != nil {
						t.Fatal(err)
					}
					assertReviewFloor(t, f, task.PhaseReviewApprovalPending, nil)
					if _, err = f.store.Resolve(context.Background(), f.input(a, "APPROVE")); err == nil {
						t.Fatalf("old FAST Review approved: %v", err)
					}
					if _, err = f.store.Resolve(context.Background(), f.input(review.StartAdmission{Review: latest}, "APPROVE")); err != nil {
						t.Fatal(err)
					}
					assertReviewFloor(t, f, task.PhaseImplementationReady, nil)
				}
			})
		}
	}
}

func TestReviewDepthApprovalNewCycleAndTerminal(t *testing.T) {
	for _, option := range []string{"APPROVE", "REVISE", "REJECT"} {
		t.Run(option, func(t *testing.T) {
			f, _ := newTaskOwnedFixture(t)
			f.candidate.ReviewDepth = task.ReviewDepthDeep
			a := f.start(t)
			f.complete(t, a, approvedResult(t))
			if _, err := f.store.Resolve(context.Background(), f.input(a, option)); err != nil {
				t.Fatal(err)
			}
			value := ownedTask(t, f)
			if option == "REVISE" {
				assertReviewFloor(t, f, task.PhaseReviewing, task.ReviewDepthPointer(task.ReviewDepthDeep))
			} else if value.RequiredReviewDepth != nil {
				t.Fatal("approval left depth authority")
			}
			if option == "REJECT" && value.Status != task.StatusCanceled {
				t.Fatal("reject did not cancel")
			}
		})
	}
}

func TestReviewDepthInfrastructureFailureKeepsFloor(t *testing.T) {
	for _, depth := range []task.ReviewDepth{task.ReviewDepthFast, task.ReviewDepthDeep} {
		t.Run(string(depth), func(t *testing.T) {
			f, _ := newTaskOwnedFixture(t)
			f.candidate.ReviewDepth = depth
			a := f.start(t)
			_, err := NewRunStore(f.db).Update(context.Background(), a.Run.ID, func(r *run.Run) error { r.Status = run.StatusInfrastructureFailed; r.UpdatedAt = f.now; return nil })
			if err != nil {
				t.Fatal(err)
			}
			assertReviewFloor(t, f, task.PhaseReviewing, &depth)
			r, err := f.reviews.Get(context.Background(), a.Review.ID)
			if err != nil || r.ReviewDisposition != "" || ownedTask(t, f).Status != task.StatusActive || ownedCount(t, f, "review_approval_decisions") != 0 {
				t.Fatalf("infra advanced: %+v %v", r, err)
			}
			if depth == task.ReviewDepthDeep {
				f.candidate.ReviewDepth = task.ReviewDepthFast
				_, err = f.reviews.AdmitStart(context.Background(), f.candidate, "infra-downgrade", strings.Repeat("b", 64), f.candidate.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository})
				var shallow *review.DepthTooShallowError
				if !errors.As(err, &shallow) {
					t.Fatalf("infra downgrade: %v", err)
				}
			}
		})
	}
}

func TestReviewDepthLatestOnlyAfterRevisedFast(t *testing.T) {
	f, _ := newTaskOwnedFixture(t)
	fast := f.start(t)
	f.complete(t, fast, approvedResult(t))
	resolved, err := f.store.Resolve(context.Background(), f.input(fast, "REVISE"))
	if err != nil {
		t.Fatal(err)
	}
	deepReview, err := f.reviews.Get(context.Background(), resolved.SuccessorReviewID)
	if err != nil {
		t.Fatal(err)
	}
	deep := review.StartAdmission{Review: deepReview, Run: run.Run{ID: resolved.SuccessorRunID}}
	f.complete(t, deep, approvedResult(t))
	staleInput := f.input(fast, "APPROVE")
	staleInput.RequestID = "stale-fast-new-request"
	if _, err := f.store.Resolve(context.Background(), staleInput); err == nil {
		t.Fatal("stale FAST approved newer DEEP")
	}
	latest, err := f.store.Resolve(context.Background(), f.input(deep, "APPROVE"))
	if err != nil {
		t.Fatal(err)
	}
	if latest.Task.CurrentReviewID != deep.Review.ID || latest.Task.ReviewApprovalID != wp.DecisionID(deep.Review.ID) || latest.Task.RequiredReviewDepth != nil {
		t.Fatalf("wrong approval source %+v", latest.Task)
	}
}

func TestReviewDepthHistoricalApprovalParserPreservesBytes(t *testing.T) {
	current := approvedResult(t)
	historical := strings.Replace(current, `"reviewDisposition":"APPROVAL_READY",`, "", 1)
	if _, err := review.ParseImplementationResult(historical); err == nil {
		t.Fatal("new protocol accepted missing disposition")
	}
	result, err := parsePersistedApprovalResult(historical)
	if err != nil || result.ReviewDisposition != review.DispositionApprovalReady || strings.Contains(historical, "reviewDisposition") {
		t.Fatalf("legacy reader: %+v %v", result, err)
	}
	if _, err := parsePersistedApprovalResult(furtherReviewResult); err == nil {
		t.Fatal("legacy reader converted further review")
	}
}
