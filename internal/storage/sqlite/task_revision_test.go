package sqlite

import (
	"context"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
)

func revisionTaskFixture(t *testing.T, db *DB, suffix string) (task.Task, aisession.Session) {
	t.Helper()
	f := seed(t, db, suffix)
	current := f.task
	current.ID = "revision-task-" + suffix
	current.FeatureAreaID = "revision-feature-" + suffix
	current.Status, current.Phase = task.StatusActive, task.PhaseReviewRequired
	current.CurrentReviewID, current.CurrentExecutionID, current.CurrentDefinitionRevisionID, current.ReviewApprovalID, current.ConfirmedTaskPlanID = "", "", "", "", ""
	current.Model, current.ReasoningEffort = "gpt-5.6-luna", "low"
	area := featurearea.FeatureArea{ID: current.FeatureAreaID, ProjectID: current.ProjectID, CreatedByAISessionID: "fixture-session-" + suffix, Title: "Revision intent", ProblemStatement: "Prepare intent", Status: featurearea.StatusActive, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(db).Create(context.Background(), area); err != nil {
		t.Fatal(err)
	}
	if err := NewTaskStore(db).Create(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	session, err := NewAISessionStore(db).Get(context.Background(), "fixture-session-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	return current, session
}

func TestInitialTaskAndGoalRevisionTransactionsAreImmutable(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	current, session := revisionTaskFixture(t, db, "revision-transactions")
	store := NewTaskStore(db)
	before, _ := store.Get(ctx, current.ID)
	if before.CurrentDefinitionRevisionID == "" || before.ReviewApprovalID != "" || before.CurrentExecutionID != "" {
		t.Fatal("intent created implementation revision")
	}
	goals := []task.Goal{{ID: "goal_first", TaskID: current.ID, Position: 0, Title: "First goal", CreatedAt: testNow}, {ID: "goal_second", TaskID: current.ID, Position: 1, Title: "Second goal", CreatedAt: testNow}}
	if err := store.DefineGoals(ctx, current.ID, goals); err != nil {
		t.Fatal(err)
	}
	before, _ = store.Get(ctx, current.ID)
	if before.CurrentDefinitionRevisionID == "" || before.ReviewApprovalID != "" || before.CurrentExecutionID != "" {
		t.Fatal("pre-review goals authorized implementation")
	}
	current = approveFixtureTask(t, db, current, session)
	initial, err := store.GetDefinitionRevision(ctx, current.CurrentDefinitionRevisionID)
	if err != nil || initial.Ordinal != 3 || len(initial.Definition.Goals) != 2 {
		t.Fatalf("initial approval snapshot: %+v %v", initial, err)
	}
	for _, query := range []string{`UPDATE task_definition_revisions SET payload_json='{}' WHERE id=?`, `DELETE FROM task_definition_revisions WHERE id=?`} {
		if _, err := db.sql.ExecContext(ctx, query, initial.ID); err == nil {
			t.Fatal("immutable revision writable")
		}
	}
	if err := store.DefineGoals(ctx, current.ID, goals); err == nil {
		t.Fatal("approved goals mutable")
	}
}
