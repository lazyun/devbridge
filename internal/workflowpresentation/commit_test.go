package workflowpresentation

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/task"
	"strings"
	"testing"
	"time"
)

func TestCommitProjectionIsDerivedAndDevelopmentOnly(t *testing.T) {
	now := time.Now()
	owner := task.Task{Type: task.TypeDevelopment, Status: task.StatusSucceeded}
	v := BuildTask(owner, nil, nil, now)
	if v.CommitStatus != "UNCOMMITTED" || v.CommitSummary != "代码：未提交" {
		t.Fatalf("%+v", v)
	}
	owner.CommitSHA = strings.Repeat("a", 40)
	owner.CommittedAt = &now
	v = BuildTask(owner, nil, nil, now)
	if v.CommitStatus != "COMMITTED" || v.DevIntegrationStatus != "UNMERGED" {
		t.Fatalf("%+v", v)
	}
	if strings.Contains(v.CommitSummary, owner.CommitSHA) {
		t.Fatal("full SHA in main view")
	}
	owner.MergedCommit = strings.Repeat("b", 40)
	owner.MergedAt = &now
	v = BuildTask(owner, nil, nil, now)
	if v.DevIntegrationStatus != "MERGED" || v.Details.MergedCommit != owner.MergedCommit || v.Details.CommittedAt == nil {
		t.Fatalf("%+v", v)
	}
	for _, kind := range []task.Type{task.Type("REVIEW"), task.TypeInspection} {
		owner.Type = kind
		v = BuildTask(owner, nil, nil, now)
		if v.CommitStatus != "" || v.CommitSHA != "" {
			t.Fatal("non-development commit semantics")
		}
	}
	data, _ := json.Marshal(v)
	if strings.Contains(string(data), "LEGACY_UNRECORDED") {
		t.Fatal("historical commit fallback")
	}
	for _, action := range v.Actions {
		if action.Action == "COMMIT_TASK" || action.Action == "TASK_COMMIT" {
			t.Fatal("manual Task commit")
		}
	}
}
