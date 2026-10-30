package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/repository"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workspace"
)

func TestLegacyDeliveryUsesExactTaskMemberWithUnrelatedPendingMembers(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-member-delivery")
	execution = terminalExecutionWithReport(t, f, execution)
	_, source, _ := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	ctx := context.Background()
	members := workspace.NewService(NewWorkspaceStore(f.db), nil, nil)
	paths := map[string]string{}
	heads := map[string]string{}
	for _, id := range []string{"sdk", "backend"} {
		path := registrationGitFixture(t, filepath.Join(t.TempDir(), id))
		head := commitTestGit(t, path, "rev-parse", "HEAD")
		paths[id] = path
		heads[id] = head
		if _, _, err := NewRepositoryStore(f.db).PutIfAbsent(ctx, repository.Repository{ID: id, ProjectID: f.project, Name: id, Path: path, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
		if _, err := members.EnsureForFeatureArea(ctx, f.project, id, f.area, head); err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "accept-exact-member"})
	if err != nil || result.Task.CommitSHA == "" {
		t.Fatalf("exact member commit: %+v %v", result, err)
	}
	if got := commitTestGit(t, source, "rev-parse", "HEAD"); got != result.Task.CommitSHA {
		t.Fatalf("commit used wrong member: %s", got)
	}
	if _, err = f.store.CommitFeatureArea(ctx, f.project, f.area, "merge-exact-member"); err != nil {
		t.Fatal(err)
	}
	for id, path := range paths {
		if got := commitTestGit(t, path, "rev-parse", "HEAD"); got != heads[id] {
			t.Fatalf("unrelated %s HEAD changed", id)
		}
	}
	value, err := members.GetByFeatureArea(ctx, f.area)
	if err != nil || len(value.Members) != 3 {
		t.Fatalf("members %+v %v", value, err)
	}
	for _, m := range value.Members {
		if m.RepositoryID == "sdk" || m.RepositoryID == "backend" {
			if m.WorktreeID != "" || m.Preparation != "PENDING" {
				t.Fatalf("unrelated member was prepared: %+v", m)
			}
		}
	}
}

func TestAggregateDeliveryCannotReportLegacySingleSHA(t *testing.T) {
	f, first := newTaskOwnedFixture(t)
	ctx := context.Background()
	if _, _, err := NewRepositoryStore(f.db).PutIfAbsent(ctx, repository.Repository{ID: "sdk", ProjectID: f.project, Name: "sdk", Path: filepath.Join(t.TempDir(), "sdk"), CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "sdk-task"
	second.RepositoryID = "sdk"
	second.BaseCommit = strings.Repeat("b", 40)
	if err := NewTaskStore(f.db).Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CommitFeatureArea(ctx, f.project, f.area, "aggregate-delivery"); !errors.Is(err, errMultiRepositoryDeliveryUnsupported) {
		t.Fatalf("aggregate admission: %v", err)
	}
	if got := ownedCount(t, f, "feature_area_commit_receipts"); got != 0 {
		t.Fatalf("unsupported delivery persisted %d receipts", got)
	}
}

func TestDeliveryBlocksFailedExactMemberUntilSameMemberRecovered(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-failed-member")
	execution = terminalExecutionWithReport(t, f, execution)
	repo, source, baseline := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	ctx := context.Background()
	members := workspace.NewService(NewWorkspaceStore(f.db), nil, nil)
	value, err := members.GetByFeatureArea(ctx, f.area)
	if err != nil {
		t.Fatal(err)
	}
	member, err := value.MemberForRepository(owner.RepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = members.RecordMemberFailure(ctx, value.ID, member.ID, "identity validation unavailable"); err != nil {
		t.Fatal(err)
	}
	result, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "accept-failed-member"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.CommitSHA != "" {
		t.Fatal("failed member was committed")
	}
	if _, err = f.store.RetryTaskCommit(ctx, f.project, owner.ID); !errors.Is(err, errDeliveryMemberUnavailable) {
		t.Fatalf("retry bypassed failed member: %v", err)
	}
	if _, err = f.store.CommitFeatureArea(ctx, f.project, f.area, "merge-failed-member"); !errors.Is(err, errDeliveryMemberUnavailable) {
		t.Fatalf("merge bypassed failed member: %v", err)
	}
	if got := commitTestGit(t, source, "rev-parse", "HEAD"); got != baseline {
		t.Fatal("failed member source HEAD changed")
	}
	if got := commitTestGit(t, repo, "rev-parse", "HEAD"); got != baseline {
		t.Fatal("failed member target HEAD changed")
	}
	// Recover the same immutable worktree, then resume the existing receipt.
	if _, err = members.AttachMemberWorktree(ctx, value.ID, member.ID, member.WorktreeID); err != nil {
		t.Fatal(err)
	}
	committed, err := f.store.RetryTaskCommit(ctx, f.project, owner.ID)
	if err != nil || committed.CommitSHA == "" {
		t.Fatalf("recovered original receipt: %+v %v", committed, err)
	}
}
