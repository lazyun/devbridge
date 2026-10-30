package workflowpresentation

import (
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/review"
)

func TestUpperReviewActivityUsesEvidenceNotRefreshTime(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	value := review.Review{ExecutorKind: review.ExecutorUpperLLM, CreatedAt: now.Add(-time.Hour), UpperAccess: &review.UpperAccess{State: "ACTIVE"}}
	activity := PresentReviewActivity(value, now)
	if !activity.Inactive || activity.LastActivityAt != nil || activity.ExecutionMethod != "GPT 审查" || !strings.Contains(activity.Summary, "原 GPT 对话") {
		t.Fatalf("idle review: %+v", activity)
	}
	last := now.Add(-time.Minute)
	value.UpperAccess.LastObservedAt = &last
	if next := PresentReviewActivity(value, now); next.Inactive || next.LastActivityAt == nil || !next.LastActivityAt.Equal(last) {
		t.Fatalf("recent evidence: %+v", next)
	}
	value.UpperAccess.State = "CANCELED"
	value.TerminalStatus = "CANCELED"
	if next := PresentReviewActivity(value, now.Add(time.Hour)); next.Inactive || next.Notice != "" {
		t.Fatalf("terminal review warned: %+v", next)
	}
}

func TestUpperReviewActivityBoundaryAndClosedStates(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{5*time.Minute - time.Nanosecond, 5 * time.Minute, 6 * time.Minute, -time.Minute} {
		r := review.Review{ExecutorKind: review.ExecutorUpperLLM, CreatedAt: now.Add(-age), UpperAccess: &review.UpperAccess{State: "ACTIVE"}}
		a := PresentReviewActivity(r, now)
		if a.Inactive != (age >= 5*time.Minute) {
			t.Fatalf("age=%v: %+v", age, a)
		}
		again := PresentReviewActivity(r, now.Add(time.Minute))
		if a.LastActivityAt != nil || again.LastActivityAt != nil || r.UpperAccess.LastObservedAt != nil {
			t.Fatal("status read manufactured activity")
		}
	}
	for _, state := range []string{"PREPARING", "FINALIZING", "RECOVERY_REQUIRED", "CANCELED"} {
		r := review.Review{ExecutorKind: review.ExecutorUpperLLM, CreatedAt: now.Add(-time.Hour), UpperAccess: &review.UpperAccess{State: state}}
		if a := PresentReviewActivity(r, now); a.Inactive || a.Notice != "" {
			t.Fatalf("state=%s: %+v", state, a)
		}
	}
	r := review.Review{ExecutorKind: review.ExecutorUpperLLM, CreatedAt: now.Add(-time.Hour), ReportID: "report", UpperAccess: &review.UpperAccess{State: "AWAITING_REPORT"}}
	if a := PresentReviewActivity(r, now); a.Inactive || a.Stage != "COMPLETED" {
		t.Fatalf("completed: %+v", a)
	}
	if a := PresentReviewActivity(review.Review{ExecutorKind: review.ExecutorPiAgent}, now); strings.Contains(a.ExecutionMethod, "Codex") {
		t.Fatal("Pi mislabeled as Codex")
	}
}
