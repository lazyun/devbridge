package execution

import (
	"context"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
	"reflect"
	"strings"
	"testing"
)

func TestExactWorksetRequiresApprovedExecutionBeforeAnyRuntimeStarts(t *testing.T) {
	c, runs, tasks, trees, session, _ := successfulCoordinator()
	tasks.value.RepositoryTargets = []workset.Target{{RepositoryID: "app", MemberID: "app-member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}}, {RepositoryID: "sdk", MemberID: "sdk-member", MemberVersion: 1, OriginCommit: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}}
	if _, err := c.PrepareRun(context.Background(), tasks.value.ID); err == nil || !strings.Contains(err.Error(), "approved Execution reservation") {
		t.Fatalf("prepare=%v", err)
	}
	if len(runs.sequence) != 0 || trees.creates != 0 || session.startCalls != 0 || session.turn != 0 {
		t.Fatal("unsupported workset caused external side effects")
	}
	if _, err := c.ExecuteRun(context.Background(), PreparedRun{task: tasks.value}, nil); err == nil {
		t.Fatalf("handoff bypass=%v", err)
	}
	if trees.creates != 0 || session.startCalls != 0 {
		t.Fatal("handoff started unsupported runtime")
	}
}

func TestExactReviewWorksetRequiresFrozenLayoutBeforeBackendHandoff(t *testing.T) {
	c, runs, _, trees, session, _ := successfulCoordinator()
	reserved := reserveCoordinatorReview(t, c, runs, "inspect the exact frozen members")
	reviews := c.Reviews.(*coordinatorReviewRuntime)
	reviews.value.RepositoryTargets = []workset.Target{
		{RepositoryID: "app", MemberID: "app-member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}},
		{RepositoryID: "sdk", MemberID: "sdk-member", MemberVersion: 1, OriginCommit: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}},
	}
	frozen := workset.CloneTargets(reviews.value.RepositoryTargets)

	prepared, err := c.PrepareScopeReview(context.Background(), reserved.ID)
	if err == nil {
		t.Fatalf("prepare review=%v", err)
	}
	if prepared.Run.Status != run.StatusInfrastructureFailed || prepared.Run.Failure == nil || prepared.Run.Failure.Code != "RUNTIME_WORKSET_INCOMPLETE" {
		t.Fatalf("review failure=%+v", prepared.Run)
	}
	if !reflect.DeepEqual(reviews.value.RepositoryTargets, frozen) {
		t.Fatalf("admission mutated the frozen Review definition: got=%+v want=%+v", reviews.value.RepositoryTargets, frozen)
	}
	if trees.creates != 0 || session.startCalls != 0 || session.resumeCalls != 0 || session.turn != 0 {
		t.Fatal("unsupported Review workset claimed or handed off runtime")
	}
}

func TestRecoveredPreparedRunRequiresFrozenRuntimeWorksetBeforeConversation(t *testing.T) {
	c, runs, tasks, trees, session, _ := successfulCoordinator()
	runtime := &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "canonical", FeatureAreaID: tasks.value.FeatureAreaID, ContinuationSafe: true}}
	c.Conversations = runtime
	service := c.Workspaces.(*workspace.Service)
	targets := []workset.Target{
		{RepositoryID: "repo-1", OriginCommit: tasks.value.BaseCommit, Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}},
		{RepositoryID: "sdk", OriginCommit: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}},
	}
	planned, err := service.PlanMembers(context.Background(), tasks.value.ProjectID, tasks.value.FeatureAreaID, targets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.EnsurePlannedMembers(context.Background(), tasks.value.ProjectID, tasks.value.FeatureAreaID, planned.WorkspaceID, planned.Targets); err != nil {
		t.Fatal(err)
	}
	reserved, err := runs.service.Create(context.Background(), tasks.value.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepared := PreparedRun{Run: reserved, task: tasks.value, repository: repository.Repository{ID: tasks.value.RepositoryID, ProjectID: tasks.value.ProjectID}}
	if _, err = c.ExecuteRun(context.Background(), prepared, nil); err == nil {
		t.Fatalf("recovered handoff=%v", err)
	}
	if runtime.begin != 0 || trees.creates != 0 || session.startCalls != 0 || session.resumeCalls != 0 || session.turn != 0 {
		t.Fatalf("multi-member workspace touched conversation/runtime: conversation=%+v trees=%d session=%+v", runtime, trees.creates, session)
	}
}
