package bridgehttp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestCodexHistoryHTTPUsesExactOwnerAndDoesNotStartWork(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	spec := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "fixture"}}
	owner := createConsoleTask(t, f, f.moduleID, spec, "history owner")
	other := createConsoleTask(t, f, f.moduleID, spec, "foreign owner")
	reviewValue, e := f.plane.Reviews.Create(f.ctx, f.codingReview(t, review.Review{Model: "gpt-6-astra", ReasoningEffort: "high", ReviewDepth: review.ReviewDepthDeep, TaskID: owner.ID, ProjectID: f.projectID, FeatureAreaID: owner.FeatureAreaID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: f.configID, BaseCommit: f.baseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Read history fixture", EngineeringGoal: "Bounded history"}))
	if e != nil {
		t.Fatal(e)
	}
	value, e := f.plane.Runs.CreateFocusedScopeReviewForReview(f.ctx, reviewValue.ID, reviewValue.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.plane.Reviews.LinkRun(f.ctx, reviewValue.ID, value.ID); e != nil {
		t.Fatal(e)
	}
	path := "/ui/api/runs/" + value.ID + "/session/native-history?project_id=" + f.projectID + "&task_id="
	code, _, body := getRaw(t, f.handler, path+owner.ID)
	var result consoleapi.CodexHistoryPage
	if code != http.StatusOK || json.Unmarshal([]byte(body), &result) != nil || result.Availability != "UNAVAILABLE" || result.RunID != value.ID {
		t.Fatalf("missing history fallback: %d %s", code, body)
	}
	for _, suffix := range []string{other.ID, owner.ID + "&limit=51", owner.ID + "&unknown=true"} {
		code, _, body = getRaw(t, f.handler, path+suffix)
		if code < 400 {
			t.Fatalf("accepted unscoped/invalid read: %s", body)
		}
	}
	after, e := f.plane.Runs.Get(f.ctx, value.ID)
	if e != nil || after.Status != value.Status || after.ThreadID != "" || after.TurnID != "" {
		t.Fatalf("history changed run: %+v %v", after, e)
	}
}
