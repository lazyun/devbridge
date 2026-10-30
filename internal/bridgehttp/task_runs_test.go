package bridgehttp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowtest"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTaskRunHTTPPagesAndExactOwnerFence(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	ctx := f.ctx
	spec := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "isolated fixture"}}
	owner := createConsoleTask(t, f, f.moduleID, spec, "Run owner")
	foreign := createConsoleTask(t, f, f.moduleID, spec, "Other Task")
	var last run.Run
	for i := 0; i < 23; i++ {
		operation, err := f.plane.Reviews.Create(ctx, f.codingReview(t, review.Review{Model: "gpt-6-astra", ReasoningEffort: "high", ReviewDepth: review.ReviewDepthDeep, TaskID: owner.ID, ProjectID: f.projectID, FeatureAreaID: owner.FeatureAreaID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: f.configID, BaseCommit: f.baseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: fmt.Sprintf("Historical review %d", i), EngineeringGoal: "Bounded history"}))
		if err != nil {
			t.Fatal(err)
		}
		value, err := f.plane.Runs.CreateFocusedScopeReviewForReview(ctx, operation.ID, operation.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
		if err != nil {
			t.Fatal(err)
		}
		if err = f.plane.Reviews.LinkRun(ctx, operation.ID, value.ID); err != nil {
			t.Fatal(err)
		}

		for _, state := range []run.Status{run.StatusPreparing, run.StatusRunning, run.StatusCollecting, run.StatusSucceeded} {
			if state == run.StatusCollecting {
				if _, err = f.plane.Runs.RecordPrompt(ctx, value.ID, "exact persisted historical prompt", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if value, err = f.plane.Runs.Transition(ctx, value.ID, state); err != nil {
				t.Fatal(err)
			}
		}
		last = value
	}

	base := "/ui/api/tasks/" + owner.ID + "/runs?project_id=" + f.projectID
	status, _, body := getRaw(t, f.handler, base)
	firstBytes := len(body)
	var first consoleapi.TaskRunPage
	if status != http.StatusOK || json.Unmarshal([]byte(body), &first) != nil || len(first.Items) != 20 || first.Total != 23 || first.Limit != 20 {
		t.Fatalf("default page: %d %s", status, body)
	}
	status, _, body = getRaw(t, f.handler, base+"&offset=20&limit=20")
	var second consoleapi.TaskRunPage
	if status != http.StatusOK || json.Unmarshal([]byte(body), &second) != nil || len(second.Items) != 3 || second.Total != 23 {
		t.Fatalf("second page: %d %s", status, body)
	}
	for _, item := range first.Items {
		for _, other := range second.Items {
			if item.ID == other.ID {
				t.Fatal("pagination duplicated Run")
			}
		}
		if item.RunPurpose != "SCOPE_REVIEW" || item.ReviewID == "" || item.TaskExecutionID != "" || item.AgentExecutionID != item.ExecutionID {
			t.Fatalf("identity drift: %+v", item)
		}
	}
	for _, suffix := range []string{"&limit=101", "&offset=-1", "&owner=wrong", "&purpose=REVIEW"} {
		status, _, body = getRaw(t, f.handler, base+suffix)
		if status != http.StatusBadRequest {
			t.Fatalf("invalid page accepted: %s %d %s", suffix, status, body)
		}
	}
	status, _, body = getRaw(t, f.handler, base+"&purpose=VERIFICATION")
	var filtered consoleapi.TaskRunPage
	if status != http.StatusOK || json.Unmarshal([]byte(body), &filtered) != nil || filtered.Total != 0 || len(filtered.Items) != 0 {
		t.Fatalf("purpose filter did not precede pagination: %d %s", status, body)
	}
	for _, suffix := range []string{"", "/prompt"} {
		path := "/v1/runs/" + last.ID + suffix + "?project_id=" + f.projectID + "&task_id="
		status, _, body = getRaw(t, f.handler, path+owner.ID)
		if status != http.StatusOK {
			t.Fatalf("exact owner refused: %d %s", status, body)
		}
		if suffix == "" {
			t.Logf("Run Detail bytes=%d", len(body))
			var detail consoleapi.RunDetail
			if json.Unmarshal([]byte(body), &detail) != nil || detail.Run.ID != last.ID || detail.Run.TaskID != owner.ID || detail.FrozenConfig.Source != "REVIEW" {
				t.Fatalf("detail owner/config missing: %s", body)
			}
		} else if !strings.Contains(body, "exact persisted historical prompt") {
			t.Fatal("historical prompt reconstructed or missing")
		}
		status, _, body = getRaw(t, f.handler, path+foreign.ID)
		if status != http.StatusNotFound {
			t.Fatalf("wrong Task owner accepted: %d %s", status, body)
		}
		status, _, body = getRaw(t, f.handler, "/v1/runs/missing"+suffix+"?project_id="+f.projectID+"&task_id="+owner.ID)
		if status != http.StatusNotFound {
			t.Fatalf("missing Run status: %d %s", status, body)
		}
	}
	status, _, body = getRaw(t, f.handler, "/ui/api/tasks/"+owner.ID+"?project_id="+f.projectID)
	var detail consoleapi.ProductTaskSummary
	if status != http.StatusOK || json.Unmarshal([]byte(body), &detail) != nil || detail.Presentation == nil || detail.Presentation.TaskView == nil {
		t.Fatalf("Task detail: %d %s", status, body)
	}
	t.Logf("23-Run fixture bytes: default Task=%d; 20-row page=%d", len(body), firstBytes)
	rows := detail.Presentation.TaskView.Details
	if len(rows.Runs) != 20 || rows.RunsTotal != 23 || !rows.RunsTruncated {
		t.Fatalf("default Task unbounded: %+v", rows)
	}
	for i, item := range rows.Runs {
		if item.ID != first.Items[i].ID {
			t.Fatal("default Task and Run page diverged")
		}
	}
}

func TestTaskDefinitionRevisionHTTPIsExactAndReadOnly(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	ctx := context.Background()
	spec := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "isolated fixture"}}
	owner := createConsoleTask(t, f, f.moduleID, spec, "Frozen intent")
	foreign := createConsoleTask(t, f, f.moduleID, spec, "Foreign owner")
	owner, _, _ = workflowtest.ApproveAndReserve(t, f.plane.DB, owner, f.sessionID, owner.WorkSpec, owner.VerificationSpec)
	path := "/ui/api/tasks/" + owner.ID + "/definition-revisions/" + owner.CurrentDefinitionRevisionID + "?project_id=" + f.projectID
	status, _, body := getRaw(t, f.handler, path)
	var result consoleapi.TaskDefinitionDetail
	if status != http.StatusOK || json.Unmarshal([]byte(body), &result) != nil || result.ID != owner.CurrentDefinitionRevisionID || result.TaskID != owner.ID || result.Definition.WorkSpec.Goal != owner.WorkSpec.Goal || !result.Current {
		t.Fatalf("definition mismatch: %d %s", status, body)
	}
	status, _, body = getRaw(t, f.handler, strings.Replace(path, owner.ID, foreign.ID, 1))
	if status != http.StatusNotFound {
		t.Fatalf("revision accepted foreign Task: %d %s", status, body)
	}
	fresh, err := f.plane.Tasks.Get(ctx, owner.ID)
	if err != nil || fresh.CurrentExecutionID != owner.CurrentExecutionID || fresh.CurrentDefinitionRevisionID != owner.CurrentDefinitionRevisionID || fresh.Phase != owner.Phase {
		t.Fatal("read changed Task pointers")
	}
}
