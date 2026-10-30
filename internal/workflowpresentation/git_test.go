package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
	"strings"
	"testing"
	"time"
)

func TestCompletedAreaDeliveryDoesNotDisplayStaleTaskCommitWait(t *testing.T) {
	for _, tc := range []struct {
		name, state, lastError, recovery string
		pending                          bool
		wantEmpty                        bool
	}{
		{name: "settled", state: "COMPLETED", wantEmpty: true},
		{name: "still pending", state: "COMPLETED", pending: true},
		{name: "error remains", state: "COMPLETED", lastError: "MERGE_CONFLICT"},
		{name: "unknown outcome", state: "COMPLETED", recovery: "UNKNOWN"},
		{name: "incomplete", state: "PENDING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Projection{Commit: &featurearea.CommitCapability{CommitStatus: "COMMITTED", Reason: "TASK_COMMIT_PENDING", MergeRecoveryState: tc.recovery}}
			facts := featurearea.CommitFacts{Pending: tc.pending, LastError: tc.lastError, Delivery: delivery.Status{State: tc.state, Members: []delivery.Member{{State: "COMPLETED"}}}}
			ApplyCommitFacts(&p, facts)
			if (p.CommitView.Reason == "") != tc.wantEmpty {
				t.Fatalf("reason contradicts delivery facts: %+v", p.CommitView)
			}
			if p.Commit.Reason != "TASK_COMMIT_PENDING" {
				t.Fatal("presentation changed canonical admission facts")
			}
		})
	}
}

func TestCompletedAreaDeliveryKeepsWaitForNewActiveTask(t *testing.T) {
	p := Projection{TaskView: &TaskView{Status: "ACTIVE"}, Commit: &featurearea.CommitCapability{CommitStatus: "COMMITTED", Reason: "TASK_COMMIT_PENDING"}}
	ApplyCommitFacts(&p, featurearea.CommitFacts{Delivery: delivery.Status{State: "COMPLETED", Members: []delivery.Member{{State: "COMPLETED"}}}})
	if p.CommitView.Reason != CommitReason("TASK_COMMIT_PENDING") {
		t.Fatal("completed history hid active Task wait", p.CommitView)
	}
	p.TaskView = nil
	p.Commit.Reason = "FEATURE_AREA_UNAVAILABLE"
	ApplyCommitFacts(&p, featurearea.CommitFacts{Delivery: delivery.Status{State: "COMPLETED"}})
	if p.CommitView.Reason == "" {
		t.Fatal("completed history hid unrelated admission reason")
	}
}

func TestGitProductStatesAndTextParity(t *testing.T) {
	sha := strings.Repeat("a", 40)
	merge := strings.Repeat("b", 40)
	for _, tc := range []struct {
		name, status, commit, merged, error, activity, summary string
		pending, retry                                         bool
	}{
		{name: "not accepted", status: "RUNNING", summary: "代码：未提交"},
		{name: "accepted pending", status: "SUCCEEDED", pending: true, activity: "RUNNING", summary: "代码：正在提交…"},
		{name: "accepted no failure evidence", status: "SUCCEEDED", summary: "代码：未提交"},
		{name: "committed", status: "SUCCEEDED", commit: sha, summary: "代码：已提交 · aaaaaaa"},
		{name: "failed", status: "SUCCEEDED", pending: true, error: "TASK_COMMIT_SNAPSHOT_DRIFT", activity: "FAILED", retry: true, summary: "Task 已验收 · 代码提交失败"},
		{name: "merged", status: "SUCCEEDED", commit: sha, merged: merge, summary: "代码：已提交 · aaaaaaa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := BuildTask(task.Task{ID: "t", Type: task.TypeDevelopment, Status: task.Status(tc.status), CommitSHA: tc.commit, MergedCommit: tc.merged}, nil, nil, time.Now())
			p := Projection{TaskView: &v, Commit: &featurearea.CommitCapability{CommitStatus: "UNCOMMITTED", CanRetry: true, RetryTaskID: "t", DevelopmentBranch: "dev_test"}}
			ApplyCommitFacts(&p, featurearea.CommitFacts{Tasks: []featurearea.TaskCommitFact{{TaskID: "t", Pending: tc.pending, LastError: tc.error}}})
			if v.CommitSummary != tc.summary || v.CommitActivity != tc.activity || p.Commit.CanRetry != tc.retry {
				t.Fatalf("%+v %+v", v, p.Commit)
			}
			if !strings.Contains(RenderText(p), tc.summary) {
				t.Fatal("text diverged")
			}
			if strings.Contains(v.CommitSummary, sha) || strings.Contains(v.DevSummary, merge) {
				t.Fatal("full SHA in primary text")
			}
			if v.Details.CommitSHA != tc.commit || v.Details.MergedCommit != tc.merged {
				t.Fatal("details lost full SHA")
			}
			for _, step := range v.Progress {
				if strings.Contains(strings.ToLower(step.Phase), "commit") || strings.Contains(strings.ToLower(step.Phase), "merge") {
					t.Fatal("extra progress step")
				}
			}
		})
	}
}
func TestAreaSubmissionStatesPreserveLifecycleAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, reason, error, want, activity string
		pending                             bool
	}{
		{name: "blocked", reason: "TASK_COMMIT_PENDING", want: "等待 Task 完成验证、验收和代码提交"},
		{name: "dirty", reason: "worktree is not clean", want: "请先处理开发工作区中尚未提交的改动"},
		{name: "running", pending: true, want: "正在提交…", activity: "RUNNING"},
		{name: "conflict", error: "MERGE_CONFLICT", want: "无法提交到 dev：存在代码冲突", activity: "FAILED"},
		{name: "recovery", pending: true, error: "RECOVERY_REQUIRED", want: "提交状态需要恢复", activity: "RECOVERY_REQUIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Projection{LifecycleStatus: "ACTIVE", Commit: &featurearea.CommitCapability{CommitStatus: "UNCOMMITTED", CanCommit: true, Reason: tc.reason, RequestID: "original", DevelopmentBranch: "dev_exact"}}
			ApplyCommitFacts(&p, featurearea.CommitFacts{RequestID: "original", Pending: tc.pending, LastError: tc.error})
			if p.CommitView.Reason != tc.want || p.CommitView.Activity != tc.activity || p.LifecycleStatus != "ACTIVE" || p.Commit.RequestID != "original" || p.Commit.CommitStatus != "UNCOMMITTED" {
				t.Fatalf("%+v", p)
			}
			if !strings.Contains(RenderText(p), tc.want) {
				t.Fatal("fallback mismatch")
			}
		})
	}
	p := Projection{Commit: &featurearea.CommitCapability{CommitStatus: "UNCOMMITTED", CanCommit: true, DevelopmentBranch: "dev_exact"}}
	ApplyCommitFacts(&p, featurearea.CommitFacts{})
	if p.CommitView.ActionLabel != "提交到 dev_exact" {
		t.Fatal(p.CommitView)
	}
}
