package workflowpresentation

import (
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/review"
)

func TestUpperReviewStageUsesDurableFacts(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		state, report, terminal, blocker, want, actor string
		observed                                      bool
	}{
		{state: "PREPARING", want: "PREPARING", actor: "CORE"},
		{state: "PREPARING", blocker: "worktree unavailable", want: "PREPARATION_FAILED", actor: "UPPER_LLM"},
		{state: "ACTIVE", want: "AWAITING_FILE_ACCESS", actor: "UPPER_LLM"},
		{state: "ACTIVE", observed: true, want: "READING", actor: "UPPER_LLM"},
		{state: "AWAITING_REPORT", want: "AWAITING_REPORT", actor: "UPPER_LLM"},
		{state: "FINALIZING", want: "FINALIZING", actor: "CORE"},
		{state: "RECOVERY_REQUIRED", want: "RECOVERY_REQUIRED", actor: "USER"},
		{state: "CLOSED", report: "report-1", terminal: "SUCCEEDED", want: "COMPLETED", actor: "USER"},
		{state: "CANCELED", terminal: "CANCELED", want: "CANCELED", actor: "USER"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			v := review.Review{ExecutorKind: review.ExecutorUpperLLM, ReportID: tc.report, TerminalStatus: tc.terminal, ReviewDisposition: review.DispositionBlocked, UpperAccess: &review.UpperAccess{State: tc.state, Blocker: tc.blocker}}
			if tc.observed {
				v.UpperAccess.LastObservedAt = &now
			}
			stage, label, actor := UpperReviewStage(v)
			if stage != tc.want || actor != tc.actor || label == "" {
				t.Fatalf("stage=%s actor=%s label=%s", stage, actor, label)
			}
		})
	}
	// Transport success without the formal report cannot be presented as completion.
	v := review.Review{ExecutorKind: review.ExecutorUpperLLM, TerminalStatus: "SUCCEEDED", UpperAccess: &review.UpperAccess{State: "AWAITING_REPORT"}}
	if stage, _, _ := UpperReviewStage(v); stage == "COMPLETED" {
		t.Fatal("transport completion replaced formal result")
	}
}
