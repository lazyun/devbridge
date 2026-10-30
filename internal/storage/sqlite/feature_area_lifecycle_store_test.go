package sqlite

import (
	"context"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/task"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
)

func lifecycleFixture(t *testing.T, label string) (agentStorageFixture, *featurearea.Service) {
	t.Helper()
	f := newAgentStorageFixture(t, label)
	f.area.ID = "lifecycle-" + label
	f.area.Status = featurearea.StatusDiscovering
	if err := NewFeatureAreaStore(f.db).Create(context.Background(), f.area); err != nil {
		t.Fatal(err)
	}
	service, err := featurearea.NewService(NewFeatureAreaStore(f.db), NewAISessionStore(f.db), NewTaskStore(f.db).CountByFeatureArea, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f, service
}
func decompositionInput(f agentStorageFixture) featurearea.DecomposeInput {
	return featurearea.DecomposeInput{AISessionID: f.session.ID, FeatureAreaID: f.area.ID, RequestID: "decompose-1", Children: []featurearea.ChildDefinition{{Title: "First", ProblemStatement: "First independent capability"}, {Title: "Second", ProblemStatement: "Second independent capability"}}}
}
func TestLifecycleDecomposeRollsBackEveryInternalWrite(t *testing.T) {
	failures := []string{
		`BEFORE INSERT ON feature_areas WHEN NEW.title='First'`,
		`BEFORE INSERT ON feature_area_relations WHEN (SELECT title FROM feature_areas WHERE id=NEW.target_feature_area_id)='First'`,
		`BEFORE INSERT ON events WHEN NEW.type='feature_area.created'`,
		`BEFORE INSERT ON feature_areas WHEN NEW.title='Second'`,
		`BEFORE INSERT ON feature_area_relations WHEN (SELECT title FROM feature_areas WHERE id=NEW.target_feature_area_id)='Second'`,
		`BEFORE UPDATE OF status ON feature_areas WHEN NEW.status='DECOMPOSED'`,
		`BEFORE UPDATE OF ended_at ON agent_conversations`,
		`BEFORE INSERT ON events WHEN NEW.type='feature_area.status_changed'`,
		`BEFORE INSERT ON feature_area_lifecycle_requests`,
	}
	for index, failure := range failures {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			f, service := lifecycleFixture(t, fmt.Sprintf("rollback-%d", index))
			conversation, err := NewAgentConversationStore(f.db).Reset(context.Background(), f.area.ID, "initial")
			if err != nil {
				t.Fatal(err)
			}
			var initialAreas, initialEvents int
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM feature_areas`).Scan(&initialAreas); err != nil {
				t.Fatal(err)
			}
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM events`).Scan(&initialEvents); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.sql.Exec(`CREATE TRIGGER fail_lifecycle ` + failure + ` BEGIN SELECT RAISE(ABORT,'injected lifecycle write failure'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Decompose(context.Background(), decompositionInput(f)); err == nil {
				t.Fatal("decomposition ignored failure injection")
			}
			var areas, events, relations, receipts int
			_ = f.db.sql.QueryRow(`SELECT count(*) FROM feature_areas`).Scan(&areas)
			_ = f.db.sql.QueryRow(`SELECT count(*) FROM events`).Scan(&events)
			_ = f.db.sql.QueryRow(`SELECT count(*) FROM feature_area_relations WHERE source_feature_area_id=?`, f.area.ID).Scan(&relations)
			_ = f.db.sql.QueryRow(`SELECT count(*) FROM feature_area_lifecycle_requests WHERE feature_area_id=?`, f.area.ID).Scan(&receipts)
			parent, _ := NewFeatureAreaStore(f.db).Get(context.Background(), f.area.ID)
			current, err := NewAgentConversationStore(f.db).Current(context.Background(), f.area.ID)
			if areas != initialAreas || events != initialEvents || relations != 0 || receipts != 0 || parent.Status != featurearea.StatusDiscovering || err != nil || current.ID != conversation.ID {
				t.Fatalf("partial decomposition: areas=%d events=%d relations=%d receipts=%d parent=%+v current=%+v err=%v", areas, events, relations, receipts, parent, current, err)
			}
		})
	}
}

func TestLifecycleTaskReviewHistoryPreservedAndEmptyAreaDecomposeReplay(t *testing.T) {
	f, service := lifecycleFixture(t, "reviews")
	f.task.FeatureAreaID = f.area.ID
	f.task = f.addTask(t, "semantic-intent")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		v := review.Review{ReviewDepth: task.ReviewDepthFast, ID: fmt.Sprintf("review_lifecycle_%d", i), TaskID: f.task.ID, ProjectID: f.project.ID, FeatureAreaID: f.area.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, f.db, f.task.CodexConfigID), CreatedByAISessionID: f.session.ID, AuthorKind: string(review.AuthorAISession), AuthorID: f.session.ID, BaseCommit: f.task.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "goal", Instruction: "read-only", CreatedAt: testNow}
		if err := NewReviewStore(f.db).Create(ctx, v); err != nil {
			t.Fatal(err)
		}
		r, err := NewRunStore(f.db).CreateProductScopeReviewAttempt(ctx, v.ID, fmt.Sprintf("lifecycle-run-%d", i), v.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{}, testNow)
		if err != nil {
			t.Fatal(err)
		}
		if r.TaskID != f.task.ID {
			t.Fatal("Review created another Task")
		}
		if _, err := service.Activate(ctx, f.session.ID, f.area.ID, "busy"); !errors.Is(err, agentconversation.ErrBusy) {
			t.Fatalf("activation crossed claim: %v", err)
		}
		if _, err := run.NewService(NewRunStore(f.db), nil, nil).Fail(ctx, r.ID, run.Failure{Kind: run.FailureInfrastructure, Code: "fixture-no-dispatch"}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := NewTaskStore(f.db).CountByFeatureArea(ctx, f.area.ID); err != nil || n != 1 {
		t.Fatalf("semantic Task count %d %v", n, err)
	}
	if _, err := service.Decompose(ctx, decompositionInput(f)); err == nil {
		t.Fatal("decomposition discarded semantic Task")
	}
	// The existing empty-domain decomposition contract and replay remain intact.
	f.area.ID = "empty-decomposition-area"
	if err := NewFeatureAreaStore(f.db).Create(ctx, f.area); err != nil {
		t.Fatal(err)
	}
	conversation, err := NewAgentConversationStore(f.db).Reset(context.Background(), f.area.ID, "before-decompose")
	if err != nil {
		t.Fatal(err)
	}
	input := decompositionInput(f)
	result, err := service.Decompose(context.Background(), input)
	if err != nil || len(result.Children) != 2 || len(result.Relations) != 2 || result.Parent.Status != featurearea.StatusDecomposed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	old, err := NewAgentConversationStore(f.db).Get(context.Background(), conversation.ID)
	if err != nil || old.EndedAt == nil {
		t.Fatalf("conversation=%+v err=%v", old, err)
	}
	replay, err := service.Decompose(context.Background(), input)
	if err != nil || replay.Children[0].ID != result.Children[0].ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	input.Children[0].Title = "different"
	if _, err := service.Decompose(context.Background(), input); !errors.Is(err, featurearea.ErrConflict) {
		t.Fatalf("changed payload=%v", err)
	}
	for _, child := range result.Children {
		if child.Status != featurearea.StatusDiscovering {
			t.Fatalf("child=%+v", child)
		}
		if _, err := NewAgentConversationStore(f.db).Current(context.Background(), child.ID); !errors.Is(err, agentconversation.ErrNotFound) {
			t.Fatalf("inherited context=%v", err)
		}
	}
	if _, err := NewAgentConversationStore(f.db).Reset(context.Background(), f.area.ID, "terminal-reset"); !errors.Is(err, agentconversation.ErrUnavailable) {
		t.Fatalf("terminal reset=%v", err)
	}
}

func TestLifecycleTaskCreateRetireWriteOrdering(t *testing.T) {
	for i := 0; i < 12; i++ {
		f, service := lifecycleFixture(t, fmt.Sprintf("race-task-%d", i))
		if _, err := service.Activate(context.Background(), f.session.ID, f.area.ID, "activate"); err != nil {
			t.Fatal(err)
		}
		value := f.task
		value.ID = fmt.Sprintf("racing-task-%d", i)
		value.FeatureAreaID = f.area.ID
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var createErr, retireErr error
		go func() { defer wg.Done(); <-start; createErr = NewTaskStore(f.db).Create(context.Background(), value) }()
		go func() {
			defer wg.Done()
			<-start
			_, retireErr = service.Retire(context.Background(), f.session.ID, f.area.ID, "retire")
		}()
		close(start)
		wg.Wait()
		if retireErr != nil {
			t.Fatal(retireErr)
		}
		parent, err := NewFeatureAreaStore(f.db).Get(context.Background(), f.area.ID)
		if err != nil || parent.Status != featurearea.StatusRetiring {
			t.Fatalf("parent=%+v err=%v", parent, err)
		}
		// A second creation after retirement must fail regardless of the race order.
		value.ID += "-after"
		if err := NewTaskStore(f.db).Create(context.Background(), value); err == nil {
			t.Fatal("Task committed after retirement")
		}
		if createErr != nil && !errors.Is(createErr, ErrConflict) {
			t.Fatalf("unexpected create error=%v", createErr)
		}
	}
}
