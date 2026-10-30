package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/worktree"
)

type capabilityRunLookup struct{ value run.Run }

func (f capabilityRunLookup) GetByInteraction(_ context.Context, threadID, turnID string) (run.Run, error) {
	if threadID != f.value.ThreadID || turnID != f.value.TurnID {
		return run.Run{}, errors.New("interaction not found")
	}
	return f.value, nil
}

type eventuallyBoundRunLookup struct {
	value    run.Run
	attempts atomic.Int32
}

func (f *eventuallyBoundRunLookup) GetByInteraction(_ context.Context, threadID, turnID string) (run.Run, error) {
	if threadID != f.value.ThreadID || turnID != f.value.TurnID || f.attempts.Add(1) < 3 {
		return run.Run{}, errors.New("interaction not yet bound")
	}
	return f.value, nil
}

type capabilityReviewRuntime struct {
	value    review.Review
	evidence []review.AccessEvidence
}

func (f *capabilityReviewRuntime) Get(_ context.Context, id string) (review.Review, error) {
	if id != f.value.ID {
		return review.Review{}, review.ErrNotFound
	}
	return f.value, nil
}
func (*capabilityReviewRuntime) RecordPrompt(context.Context, string, string, time.Time) (review.Review, error) {
	return review.Review{}, nil
}
func (f *capabilityReviewRuntime) RecordAccess(_ context.Context, id, operation string, outcome review.AccessOutcome, code string) error {
	f.evidence = append(f.evidence, review.AccessEvidence{ReviewID: id, Operation: operation, Outcome: outcome, Code: code})
	return nil
}
func (f *capabilityReviewRuntime) SummarizeEvidence(context.Context, string) (review.EvidenceSummary, error) {
	return review.EvidenceSummary{}, nil
}

type capabilityRepositoryLookup struct{ value repository.Repository }

func (f capabilityRepositoryLookup) Get(context.Context, string) (repository.Repository, error) {
	return f.value, nil
}

type capabilityWorktreeLookup struct{ value worktree.Worktree }

func (f capabilityWorktreeLookup) Get(context.Context, string) (worktree.Worktree, error) {
	return f.value, nil
}

type capabilityInspector struct{ readPath string }

func (*capabilityInspector) Search(context.Context, string, string, []string, int) (reviewaccess.SearchResult, error) {
	return reviewaccess.SearchResult{}, nil
}
func (f *capabilityInspector) Read(_ context.Context, root, path string, _, _ int) (reviewaccess.ReadResult, error) {
	f.readPath = path
	return reviewaccess.ReadResult{Path: path, StartLine: 1, EndLine: 1, Content: "repository content"}, nil
}
func (*capabilityInspector) List(context.Context, string, string, int) (reviewaccess.ListResult, error) {
	return reviewaccess.ListResult{}, nil
}

func newCapabilityDispatcher() (*ReviewCapabilityDispatcher, *capabilityReviewRuntime, *capabilityInspector) {
	runValue := run.Run{TaskID: "semantic-task", ID: "run-internal", ReviewID: "review-product", ProjectID: "project-one", FeatureAreaID: "feature-one", OperationKind: run.OperationReview, OperationID: "review-product", Purpose: run.PurposeScopeReview, Status: run.StatusRunning, WorktreeID: "worktree-internal", ThreadID: "thread-trusted", TurnID: "turn-trusted"}
	reviews := &capabilityReviewRuntime{value: review.Review{ReviewDepth: "DEEP", TaskID: "semantic-task", ID: "review-product", ProjectID: "project-one", FeatureAreaID: "feature-one", RepositoryID: "repository-one", CodexConfigID: "config-1", BaseCommit: strings.Repeat("a", 40), RunID: "run-internal"}}
	inspector := &capabilityInspector{}
	return &ReviewCapabilityDispatcher{
		Runs: capabilityRunLookup{value: runValue}, Reviews: reviews, Grants: capabilityGrantLookup{value: reviewgrant.Grant{ReviewID: "review-product", ProjectID: "project-one", FeatureAreaID: "feature-one", RepositoryID: "repository-one", Mode: reviewgrant.ModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}}},
		Conversations: &architectureClaimRuntime{claim: agentconversation.Claim{RunID: runValue.ID, Kind: agentconversation.Review, OperationID: runValue.ReviewID, FeatureAreaID: runValue.FeatureAreaID}},
		Repositories:  capabilityRepositoryLookup{value: repository.Repository{ID: "repository-one", ProjectID: "project-one"}},
		Worktrees:     capabilityWorktreeLookup{value: worktree.Worktree{ID: "worktree-internal", RunID: "run-that-allocated-worktree", RepositoryID: "repository-one", Path: "/bounded/worktree", Status: worktree.StatusReady}},
		Inspector:     inspector,
	}, reviews, inspector
}

func TestReviewDynamicToolSchemasContainNoBridgeBusinessIdentity(t *testing.T) {
	encoded, err := json.Marshal(ReviewDynamicToolSpecs())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"aiSessionId", "reviewId", "runId", "taskId", "repositoryId", "threadId", "turnId"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Codex Review capability schema leaks %s: %s", forbidden, encoded)
		}
	}
}

func TestFeatureAreaHostToolV1IsExactStableUnion(t *testing.T) {
	specs := FeatureAreaHostToolV1Specs()
	if len(specs) != 4 {
		t.Fatalf("Feature Area host tools=%d, want 4", len(specs))
	}
	want := []string{ReviewRepositorySearchTool, ReviewRepositoryReadTool, ReviewRepositoryListTool, GoalProgressTool}
	for i, name := range want {
		if specs[i].Name != name || specs[i].Type != "function" {
			t.Fatalf("tool[%d]=%+v, want function %q", i, specs[i], name)
		}
	}
}

func TestTrustedInteractionMapsToReviewAndReadsRelevantProductionOutsideFocus(t *testing.T) {
	dispatcher, reviews, inspector := newCapabilityDispatcher()
	response, err := dispatcher.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: "thread-trusted", TurnID: "turn-trusted", CallID: "call-1", Tool: ReviewRepositoryReadTool, Arguments: json.RawMessage(`{"path":"module-b/production.go","startLine":1,"endLine":20}`)})
	if err != nil || !response.Success {
		t.Fatalf("trusted Review read response=%+v err=%v", response, err)
	}
	if len(response.ContentItems) != 1 || !strings.Contains(response.ContentItems[0].Text, "repository content") {
		t.Fatalf("successful repository capability result=%+v", response)
	}
	if inspector.readPath != "module-b/production.go" {
		t.Fatalf("read path=%q; focus was incorrectly treated as authorization", inspector.readPath)
	}
	if len(reviews.evidence) != 1 || reviews.evidence[0].ReviewID != "review-product" || reviews.evidence[0].Outcome != review.AccessSuccess {
		t.Fatalf("authoritative Review evidence=%+v", reviews.evidence)
	}
}

func TestTrustedInteractionAcceptsTaskWorkspaceWorktreeAllocatedByPriorRun(t *testing.T) {
	dispatcher, reviews, _ := newCapabilityDispatcher()
	worktreeValue := dispatcher.Worktrees.(capabilityWorktreeLookup).value
	if worktreeValue.RunID == dispatcher.Runs.(capabilityRunLookup).value.ID {
		t.Fatal("fixture must model a Worktree allocated by a prior Run")
	}
	response, err := dispatcher.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: "thread-trusted", TurnID: "turn-trusted", CallID: "call-reused-worktree", Tool: ReviewRepositoryReadTool, Arguments: json.RawMessage(`{"path":"module-b/production.go"}`)})
	if err != nil || !response.Success || len(reviews.evidence) != 1 || reviews.evidence[0].Outcome != review.AccessSuccess {
		t.Fatalf("reused Worktree response=%+v evidence=%+v err=%v", response, reviews.evidence, err)
	}
}

func TestReviewCapabilityWaitsForExactTrustedTurnBinding(t *testing.T) {
	dispatcher, reviews, _ := newCapabilityDispatcher()
	runValue := dispatcher.Runs.(capabilityRunLookup).value
	lookup := &eventuallyBoundRunLookup{value: runValue}
	dispatcher.Runs = lookup
	response, err := dispatcher.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: runValue.ThreadID, TurnID: runValue.TurnID, CallID: "call-racing", Tool: ReviewRepositoryReadTool, Arguments: json.RawMessage(`{"path":"module-b/production.go"}`)})
	if err != nil || !response.Success || lookup.attempts.Load() != 3 || len(reviews.evidence) != 1 || reviews.evidence[0].Outcome != review.AccessSuccess {
		t.Fatalf("racing binding response=%+v attempts=%d evidence=%+v err=%v", response, lookup.attempts.Load(), reviews.evidence, err)
	}
}

func TestReviewCapabilityRejectsStaleInteractionButAllowsTestLikePathText(t *testing.T) {
	dispatcher, reviews, _ := newCapabilityDispatcher()
	stale, err := dispatcher.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: "thread-trusted", TurnID: "turn-stale", CallID: "call-stale", Tool: ReviewRepositoryReadTool, Arguments: json.RawMessage(`{"path":"module-b/production.go"}`)})
	if err != nil || stale.Success || len(reviews.evidence) != 0 {
		t.Fatalf("stale interaction response=%+v evidence=%+v err=%v", stale, reviews.evidence, err)
	}
	allowed, err := dispatcher.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: "thread-trusted", TurnID: "turn-trusted", CallID: "call-test", Tool: ReviewRepositoryReadTool, Arguments: json.RawMessage(`{"path":"module-b/production_test.go"}`)})
	if err != nil || !allowed.Success {
		t.Fatalf("test-like path received semantic rejection: response=%+v err=%v", allowed, err)
	}
	if len(reviews.evidence) != 1 || reviews.evidence[0].Outcome != review.AccessSuccess || reviews.evidence[0].Code != "" {
		t.Fatalf("authorized evidence=%+v", reviews.evidence)
	}
}

type capabilityGrantLookup struct{ value reviewgrant.Grant }

func (s capabilityGrantLookup) Get(_ context.Context, id string) (reviewgrant.Grant, error) {
	if s.value.ReviewID != id {
		return reviewgrant.Grant{}, reviewgrant.ErrNotFound
	}
	return s.value, nil
}

func TestReviewCapabilityRejectsEveryMismatchedGrantOwner(t *testing.T) {
	for _, field := range []string{"review", "project", "feature", "repository"} {
		t.Run(field, func(t *testing.T) {
			d, reviews, inspector := newCapabilityDispatcher()
			lookup := d.Grants.(capabilityGrantLookup)
			switch field {
			case "review":
				lookup.value.ReviewID = "another-review"
			case "project":
				lookup.value.ProjectID = "another-project"
			case "feature":
				lookup.value.FeatureAreaID = "another-feature"
			case "repository":
				lookup.value.RepositoryID = "another-repository"
			}
			d.Grants = lookup
			response, err := d.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: "thread-trusted", TurnID: "turn-trusted", Tool: ReviewRepositoryReadTool, Arguments: json.RawMessage(`{"path":"source.go"}`)})
			if err != nil || response.Success || inspector.readPath != "" || len(reviews.evidence) > 0 {
				t.Fatalf("mismatched grant authorized: %+v %v", response, err)
			}
		})
	}
}

func TestRepositoryCapabilityRequiresCurrentClaimAuthority(t *testing.T) {
	for _, missing := range []bool{true, false} {
		dispatcher, reviews, inspector := newCapabilityDispatcher()
		if missing {
			dispatcher.Conversations = nil
		} else {
			dispatcher.Conversations.(*architectureClaimRuntime).claim.RuntimeEpoch = "stale-epoch"
		}
		response, err := dispatcher.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: "thread-trusted", TurnID: "turn-trusted", CallID: "unowned-call", Tool: ReviewRepositoryReadTool, Arguments: json.RawMessage(`{"path":"source.go"}`)})
		if err != nil {
			t.Fatal(err)
		}
		if got := architectureToolErrorCode(t, response); got != "OPERATION_CONTEXT_UNAVAILABLE" {
			t.Fatalf("missing=%v code=%s", missing, got)
		}
		if inspector.readPath != "" || len(reviews.evidence) != 0 {
			t.Fatal("unowned request performed repository access")
		}
	}
}
