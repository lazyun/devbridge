package sqlite

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

type taskFenceStore interface {
	Update(ctx context.Context, id string, update func(*task.Task) error) (task.Task, error)
	Get(ctx context.Context, id string) (task.Task, error)
}

// TestTaskUpdateFenceParityAcrossStores drives the identical set of illegal
// semantic callbacks through BOTH the SQLite TaskStore and the task MemoryStore
// and requires each to conflict with the same fence classification while the
// persisted Task stays field-by-field identical.
func TestTaskUpdateFenceParityAcrossStores(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	approved, _ := readyExecutionTask(t, db, "fence-parity")
	sqliteStore := NewTaskStore(db)
	sqliteBaseline, err := sqliteStore.Get(ctx, approved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sqliteBaseline.ReviewApprovalID == "" || sqliteBaseline.CurrentDefinitionRevisionID == "" {
		t.Fatalf("fixture must carry durable approval identity: %+v", sqliteBaseline)
	}
	memoryStore := task.NewMemoryStore()
	if err := memoryStore.Create(ctx, sqliteBaseline); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		mutate   func(*task.Task)
		expected error
	}{
		{"status", func(v *task.Task) { v.Status = task.StatusSucceeded }, task.ErrConflict},
		{"phase", func(v *task.Task) { v.Phase = task.PhaseDelivering }, task.ErrConflict},
		{"title", func(v *task.Task) { v.Title = "parity title" }, task.ErrConflict},
		{"goal", func(v *task.Task) { v.Goal = "parity goal" }, task.ErrConflict},
		{"problemStatement", func(v *task.Task) { v.ProblemStatement = "parity problem" }, task.ErrConflict},
		{"constraints", func(v *task.Task) { v.Constraints = append(slices.Clone(v.Constraints), "parity constraint") }, task.ErrConflict},
		{"workSpec", func(v *task.Task) { v.WorkSpec.CodexInstruction = "parity instruction" }, task.ErrConflict},
		{"verificationSpec", func(v *task.Task) { v.VerificationSpec.Scope = "parity scope" }, task.ErrConflict},
		{"repositoryTargets", func(v *task.Task) {
			v.RepositoryTargets = append(slices.Clone(v.RepositoryTargets), workset.Target{RepositoryID: "parity-repo", MemberID: "parity-member", MemberVersion: 1, OriginCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"})
		}, task.ErrConflict},
		{"requiredReviewDepth", func(v *task.Task) {
			if v.RequiredReviewDepth != nil && *v.RequiredReviewDepth == task.ReviewDepthFast {
				v.RequiredReviewDepth = task.ReviewDepthPointer(task.ReviewDepthDeep)
			} else {
				v.RequiredReviewDepth = task.ReviewDepthPointer(task.ReviewDepthFast)
			}
		}, task.ErrConflict},
		{"projectId", func(v *task.Task) { v.ProjectID = "parity-project" }, task.ErrConflict},
		{"repositoryId", func(v *task.Task) { v.RepositoryID = "parity-repository" }, task.ErrConflict},
		{"currentReviewId", func(v *task.Task) { v.CurrentReviewID = "parity-review" }, task.ErrConflict},
		{"currentExecutionId", func(v *task.Task) { v.CurrentExecutionID = "parity-execution" }, task.ErrConflict},
		{"currentDefinitionRevisionId", func(v *task.Task) { v.CurrentDefinitionRevisionID = "parity-revision" }, task.ErrConflict},
		{"reviewApprovalId", func(v *task.Task) { v.ReviewApprovalID = "parity-approval" }, task.ErrConflict},
		{"model", func(v *task.Task) { v.Model = "parity-model" }, task.ErrConflict},
		{"reasoningEffort", func(v *task.Task) { v.ReasoningEffort = "parity-effort" }, task.ErrConflict},
		{"featureAreaId", func(v *task.Task) { v.FeatureAreaID = "parity-feature" }, task.ErrTopicImmutable},
		{"codexConfigId", func(v *task.Task) { v.CodexConfigID = "parity-config" }, task.ErrCodexConfigImmutable},
		{"baseCommit", func(v *task.Task) { v.BaseCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }, task.ErrBaseCommitImmutable},
	}

	fields := []struct {
		name string
		read func(task.Task) any
	}{
		{"status", func(v task.Task) any { return v.Status }},
		{"phase", func(v task.Task) any { return v.Phase }},
		{"title", func(v task.Task) any { return v.Title }},
		{"goal", func(v task.Task) any { return v.Goal }},
		{"problemStatement", func(v task.Task) any { return v.ProblemStatement }},
		{"constraints", func(v task.Task) any { return v.Constraints }},
		{"workSpec", func(v task.Task) any { return v.WorkSpec }},
		{"verificationSpec", func(v task.Task) any { return v.VerificationSpec }},
		{"repositoryTargets", func(v task.Task) any { return v.RepositoryTargets }},
		{"requiredReviewDepth", func(v task.Task) any { return v.RequiredReviewDepth }},
		{"projectId", func(v task.Task) any { return v.ProjectID }},
		{"repositoryId", func(v task.Task) any { return v.RepositoryID }},
		{"featureAreaId", func(v task.Task) any { return v.FeatureAreaID }},
		{"codexConfigId", func(v task.Task) any { return v.CodexConfigID }},
		{"baseCommit", func(v task.Task) any { return v.BaseCommit }},
		{"currentReviewId", func(v task.Task) any { return v.CurrentReviewID }},
		{"currentExecutionId", func(v task.Task) any { return v.CurrentExecutionID }},
		{"currentDefinitionRevisionId", func(v task.Task) any { return v.CurrentDefinitionRevisionID }},
		{"reviewApprovalId", func(v task.Task) any { return v.ReviewApprovalID }},
		{"model", func(v task.Task) any { return v.Model }},
		{"reasoningEffort", func(v task.Task) any { return v.ReasoningEffort }},
	}

	for _, runner := range []struct {
		name         string
		store        taskFenceStore
		baseline     task.Task
		conflictWith error
	}{
		{name: "memory", store: memoryStore, baseline: func() task.Task {
			v, e := memoryStore.Get(ctx, approved.ID)
			if e != nil {
				t.Fatal(e)
			}
			return v
		}(), conflictWith: task.ErrConflict},
		{name: "sqlite", store: sqliteStore, baseline: sqliteBaseline, conflictWith: ErrConflict},
	} {
		for _, c := range cases {
			mutation := c.mutate
			_, err := runner.store.Update(ctx, approved.ID, func(v *task.Task) error { mutation(v); return nil })
			fence := c.expected
			if c.expected == task.ErrConflict {
				fence = runner.conflictWith
			}
			if !errors.Is(err, fence) {
				t.Fatalf("%s store %s: err=%v want=%v", runner.name, c.name, err, fence)
			}
			after, getErr := runner.store.Get(ctx, approved.ID)
			if getErr != nil {
				t.Fatalf("%s store %s: read after rejection: %v", runner.name, c.name, getErr)
			}
			for _, field := range fields {
				if !reflect.DeepEqual(field.read(after), field.read(runner.baseline)) {
					t.Fatalf("%s store rejected %s but mutated %s: before=%v after=%v", runner.name, c.name, field.name, field.read(runner.baseline), field.read(after))
				}
			}
		}
	}
}
