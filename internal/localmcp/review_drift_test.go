package localmcp_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func waitForReviewSucceeded(t *testing.T, fixture *integrationFixture, reviewID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	lastStatus := ""
	for time.Now().Before(deadline) {
		reviewValue, err := fixture.plane.Reviews.Get(fixture.ctx, reviewID)
		if err == nil && reviewValue.RunID != "" {
			runValue, runErr := fixture.plane.Runs.Get(fixture.ctx, reviewValue.RunID)
			if runErr == nil {
				lastStatus = string(runValue.Status)
				if runValue.Status == run.StatusSucceeded {
					return
				}
				if runValue.Status != run.StatusCreated && runValue.Status != run.StatusPreparing && runValue.Status != run.StatusRunning && runValue.Status != run.StatusCollecting {
					t.Fatalf("review run ended in %s: failure=%+v", runValue.Status, runValue.Failure)
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("review run did not succeed before the deadline (last status %q)", lastStatus)
}

func (f *integrationFixture) startReview(t *testing.T, aiSessionID, reviewID string) run.Run {
	t.Helper()
	value, err := f.control.StartScopeReview(f.ctx, scopereview.StartInput{AISessionID: aiSessionID, ReviewID: reviewID})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestReviewGetExposesSharedRepositoryDriftThroughBothAdapters(t *testing.T) {
	fixture := newIntegrationFixture(t)
	// Review Runs must complete without Codex: install the stub Codex port
	// before the Review starts.
	fixture.plane.Coordinator.Codex = &stubCodexFactory{}
	gptSession := connectGPT(t, fixture.gptURL)
	libreSession := connectLocalMCP(t, fixture.libreURL)
	sessionGPT := callToolMap(t, fixture.ctx, gptSession, "project_open", map[string]any{"projectId": fixture.projectID})["aiSessionId"].(string)
	callToolMap(t, fixture.ctx, libreSession, "project_open", map[string]any{"projectId": fixture.projectID})

	featureAreaID := callToolMap(t, fixture.ctx, gptSession, "featureArea_create", map[string]any{"aiSessionId": sessionGPT, "title": "Interface review", "problemStatement": "Review the interface."})["id"].(string)
	head, err := fixture.plane.Repositories.CurrentHEAD(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}

	intentID := closureTaskReviewArgs(t, fixture, sessionGPT, featureAreaID)["taskId"].(string)
	reviewOne, err := fixture.plane.Reviews.Create(fixture.ctx, fixture.frozenCodingReview(t, productreview.Review{ReviewDepth: "DEEP", TaskID: intentID,
		ProjectID: fixture.projectID, FeatureAreaID: featureAreaID, CreatedByAISessionID: sessionGPT, RepositoryID: fixture.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: head,
		Focus:           productreview.Focus{Kind: productreview.FocusFocused, Summary: "Interface focus", Paths: []string{"tracked.txt"}},
		EngineeringGoal: "Review the interface.",
		Instruction:     "Inspect the interface implementation without modifying it.",
		Model:           "gpt-5.6-terra", ReasoningEffort: "high",
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.startReview(t, sessionGPT, reviewOne.ID)
	waitForReviewSucceeded(t, fixture, reviewOne.ID)
	reviewCommit, err := fixture.plane.Repositories.CurrentHEAD(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}

	// A teammate changes the explicitly focused file; HEAD advances.
	if err := os.WriteFile(filepath.Join(fixture.repoPath, "tracked.txt"), []byte("onboarding v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := commitAll(t, fixture.repoPath, "team changes the focused interface")
	if next == reviewCommit {
		t.Fatal("fixture HEAD did not advance")
	}

	for name, session := range map[string]*sdkmcp.ClientSession{"gpt": gptSession, "local-mcp": libreSession} {
		read := callToolMap(t, fixture.ctx, session, "operation_get", map[string]any{"aiSessionId": sessionGPT, "target": map[string]any{"kind": "REVIEW", "operationId": reviewOne.ID, "ownerTaskId": reviewOne.TaskID}, "include": []string{"EVIDENCE"}})
		drift, ok := read["result"].(map[string]any)["repositoryDrift"].(map[string]any)
		if !ok {
			t.Fatalf("%s operation_get EVIDENCE lost repositoryDrift: %+v", name, read)
		}
		if drift["available"] != true || drift["reviewCommit"] != reviewCommit || drift["currentCommit"] != next {
			t.Fatalf("%s drift identity = %+v", name, drift)
		}
		if drift["repositoryChanged"] != true || drift["changedFileCount"] != float64(1) {
			t.Fatalf("%s drift facts = %+v", name, drift)
		}
		changed, ok := drift["focusPathsChanged"].([]any)
		if !ok || len(changed) != 1 {
			t.Fatalf("%s focusPathsChanged = %+v", name, drift["focusPathsChanged"])
		}
		change := changed[0].(map[string]any)
		if change["path"] != "tracked.txt" || change["change"] != "MODIFIED" {
			t.Fatalf("%s focus path change = %+v", name, change)
		}
		if drift["changedFocusPathCount"] != float64(1) || drift["otherChangedFileCount"] != float64(0) || drift["truncated"] != false {
			t.Fatalf("%s drift counts = %+v", name, drift)
		}
		if _, hasRun := drift["runId"]; hasRun {
			t.Fatalf("%s drift leaks run identity", name)
		}
	}
}
