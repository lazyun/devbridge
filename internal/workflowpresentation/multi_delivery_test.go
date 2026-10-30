package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/featurearea"
	"testing"
)

func TestMultiDeliveryFactsCompletePartialAndNoChange(t *testing.T) {
	for _, tc := range []struct {
		name, state, integration, memberState string
		pending                               bool
		wantCommit, wantDev                   string
	}{
		{"complete", "COMPLETED", "MERGED", "COMPLETED", false, "代码：已完成逐仓交付", "Dev：已全部合入"},
		{"partial", "PENDING", "PARTIAL", "COMPLETED", true, "代码：部分仓库已提交", "Dev：部分仓库已合入"},
		{"no-change", "COMPLETED", "MERGED", "NO_CHANGE", false, "代码：无变更，交付完成", "Dev：无变更，无需合入"},
		{"abandoned", "ABANDONED", "UNMERGED", "COMPLETED", false, "代码：交付已终止", "Dev：等待合入"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := TaskView{ID: "task", Status: "SUCCEEDED", CommitStatus: "UNCOMMITTED"}
			f := featurearea.TaskCommitFact{TaskID: "task", Pending: tc.pending, IntegrationState: tc.integration, Delivery: delivery.Status{State: tc.state, Members: []delivery.Member{{State: tc.memberState}}}}
			p := Projection{TaskView: &v, Commit: &featurearea.CommitCapability{CommitStatus: "COMMITTED"}}
			ApplyCommitFacts(&p, featurearea.CommitFacts{Tasks: []featurearea.TaskCommitFact{f}})
			if v.CommitSummary != tc.wantCommit || v.DevSummary != tc.wantDev {
				t.Fatalf("%+v", v)
			}
			if v.CommitSHA != "" || v.MergedCommit != "" {
				t.Fatal("fabricated scalar commit")
			}
			if tc.name == "no-change" && p.CommitView.Summary != "提交：无代码变更，无需合入" {
				t.Fatal(p.CommitView.Summary)
			}
		})
	}
}
