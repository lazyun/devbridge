package bridgehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
)

// The regression uses real persisted semantic Tasks and a separate Review
// operation. The browser must never expose an ID that its exact detail rejects.
func TestProductTaskListEveryClickableIDResolvesExactSemanticDetail(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	ctx := f.ctx
	verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "local fixture"}}
	first := createConsoleTask(t, f, f.moduleID, verification, "Semantic Task one")
	second := createConsoleTask(t, f, f.moduleID, verification, "Semantic Task two")
	area, err := f.plane.FeatureAreas.GetForSession(ctx, f.sessionID, first.FeatureAreaID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := f.plane.Reviews.Create(ctx, f.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: first.ID, ProjectID: f.projectID, FeatureAreaID: area.ID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: f.configID, BaseCommit: f.baseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Review instruction must not replace semantic Task title", EngineeringGoal: "Review fixture"}))
	if err != nil {
		t.Fatal(err)
	}
	reviewRun, err := f.plane.Runs.CreateFocusedScopeReviewForReview(ctx, operation.ID, operation.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.plane.Reviews.LinkRun(ctx, operation.ID, reviewRun.ID); err != nil {
		t.Fatal(err)
	}
	expected := map[string]task.Task{first.ID: first, second.ID: second}
	for _, route := range []string{"/ui/api/project-workspace", "/ui/api/work-tabs/tasks"} {
		status, _, body := getRaw(t, f.handler, route+"?project_id="+url.QueryEscape(f.projectID))
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", route, status, body)
		}
		var data struct {
			Tasks []struct {
				ID     string `json:"id"`
				Title  string `json:"title"`
				Phase  string `json:"phase"`
				Status string `json:"status"`
			} `json:"tasks"`
			Items []struct {
				ID     string `json:"id"`
				Title  string `json:"title"`
				Phase  string `json:"phase"`
				Status string `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(body), &data); err != nil {
			t.Fatal(err)
		}
		values := data.Tasks
		if values == nil {
			values = data.Items
		}
		var rawList struct {
			Tasks []map[string]any `json:"tasks"`
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal([]byte(body), &rawList); err != nil {
			t.Fatal(err)
		}
		rawValues := rawList.Tasks
		if rawValues == nil {
			rawValues = rawList.Items
		}
		factsByID := make(map[string]map[string]any, len(rawValues))
		for _, facts := range rawValues {
			factsByID[facts["id"].(string)] = facts
		}
		if len(values) != len(expected) {
			t.Fatalf("%s exposed %d Tasks, want %d: %s", route, len(values), len(expected), body)
		}
		for _, item := range values {
			semantic, ok := expected[item.ID]
			if !ok || item.ID == operation.ID {
				t.Fatalf("non-semantic Task exposed: %+v", item)
			}
			fresh, err := f.plane.Tasks.Get(ctx, semantic.ID)
			if err != nil {
				t.Fatal(err)
			}
			if item.Phase != string(fresh.Phase) || item.Status != string(fresh.Status) {
				t.Fatalf("Task phase/status must be direct facts: %+v vs %+v", item, fresh)
			}
			path := "/ui/api/tasks/" + url.PathEscape(item.ID) + "?project_id=" + url.QueryEscape(f.projectID) + "&feature_area_id=" + url.QueryEscape(semantic.FeatureAreaID)
			code, _, detail := getRaw(t, f.handler, path)
			if code != http.StatusOK {
				t.Fatalf("clickable Task %s detail: %d %s", item.ID, code, detail)
			}
			var result struct {
				ID     string `json:"id"`
				Title  string `json:"title"`
				Phase  string `json:"phase"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(detail), &result); err != nil {
				t.Fatal(err)
			}
			if result.ID != item.ID || result.Title != item.Title || result.Phase != item.Phase || result.Status != item.Status {
				t.Fatalf("list/detail facts diverged: %+v / %+v", item, result)
			}
			var detailFacts map[string]any
			if err := json.Unmarshal([]byte(detail), &detailFacts); err != nil {
				t.Fatal(err)
			}
			if _, ok := detailFacts["requiredReviewDepth"]; !ok {
				t.Fatal("Task depth fact omitted")
			}
			expectedDepths, _ := json.Marshal(task.AllowedReviewDepths(fresh.RequiredReviewDepth))
			projectedDepths, _ := json.Marshal(detailFacts["allowedReviewDepths"])
			if string(expectedDepths) != string(projectedDepths) {
				t.Fatalf("depth projection inferred: %s vs %s", projectedDepths, expectedDepths)
			}
			for _, field := range []string{"requiredReviewDepth", "allowedReviewDepths", "phase", "phaseLabel", "phaseDescription", "status", "commitStatus", "commitSha", "devIntegrationStatus", "mergedCommit", "commitActivity", "commitErrorSummary", "commitSummary", "devSummary", "currentAction", "recovery"} {
				if !reflect.DeepEqual(factsByID[item.ID][field], detailFacts[field]) {
					t.Errorf("%s Task %s %s differs between list/detail: %#v / %#v", route, item.ID, field, factsByID[item.ID][field], detailFacts[field])
				}
			}
		}
	}
	other, err := f.plane.Projects.Create(ctx, "Foreign project")
	if err != nil {
		t.Fatal(err)
	}
	foreignArea, err := f.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: f.sessionID, Title: "Other FeatureArea", ProblemStatement: "Other owner"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, id, project, area string }{
		{"wrong project", first.ID, other.ID, ""},
		{"wrong FeatureArea", first.ID, f.projectID, foreignArea.ID},
		{"Review operation", operation.ID, f.projectID, ""},
		{"Review pseudo Task", "work_review_" + operation.ID, f.projectID, ""},
		{"stale Task", "deleted-task", f.projectID, ""},
		{"Execution ID", "execution-not-a-task", f.projectID, ""},
		{"Inspection ID", "inspection-not-a-task", f.projectID, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := url.Values{"project_id": {test.project}}
			if test.area != "" {
				query.Set("feature_area_id", test.area)
			}
			status, _, body := getRaw(t, f.handler, "/ui/api/tasks/"+url.PathEscape(test.id)+"?"+query.Encode())
			if status != http.StatusNotFound {
				t.Fatalf("foreign/non-Task detail: %d %s", status, body)
			}
		})
	}
}

func TestProductionWebAssetsContainNoObsoleteWorkflowAuthority(t *testing.T) {
	entries, err := embeddedUI.ReadDir("ui")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"featureArea_review_start", "review_task_id", "work_review_", "ui_decision_get", "ui_task_decide", "ui_execution_decide", "task_plan_status", "confirmed_task_plan_id", "TaskPlan", "Preparation"}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		value, err := embeddedUI.ReadFile("ui/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range forbidden {
			if strings.Contains(string(value), marker) {
				t.Errorf("production asset %s retains %s", entry.Name(), marker)
			}
		}
	}
}

func TestFeatureAreaCreateTaskHTTPKeepsSemanticIntentAndReplaysWithoutReview(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	ctx := f.ctx
	// The fixture adds a custom configuration beside the bundled default; this product command requires one enabled choice.
	if _, err := f.plane.DB.SQL().ExecContext(ctx, "UPDATE codex_configs SET enabled=0 WHERE id<>?", f.configID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.ProjectContexts.EnsureRequired(ctx, f.projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.ProjectContexts.MarkBootstrapped(ctx, f.projectID, projectcontext.StatusReady, "webui-create-fixture", "", "", nil); err != nil {
		t.Fatal(err)
	}
	areaID := newActiveConsoleFeature(t, f, "Feature accepting semantic Tasks")
	route := "/ui/api/featureAreas/" + areaID + "/action?project_id=" + url.QueryEscape(f.projectID)
	input := map[string]any{"action": "CREATE_TASK", "requestId": "create-semantic-http", "task": map[string]any{"title": "修复 Card 布局", "problemStatement": "用户提交了一段独立的工程意图；尚未提供任何 Review instruction。", "repositoryId": f.repositoryID, "constraints": []string{"保持标题语义"}}}
	// The product read surface has no independently persisted operator actor;
	// its historical writable action must fail closed rather than borrowing the
	// selected AI Session.
	postJSON(t, f.handler, route, input, http.StatusInternalServerError, nil)
	checkCounts := func() {
		t.Helper()
		values, err := f.plane.Tasks.List(ctx)
		if err != nil || len(values) != 0 {
			t.Fatalf("Task count=%d err=%v", len(values), err)
		}
		reviews, err := f.plane.Reviews.ListByFeatureArea(ctx, areaID)
		if err != nil || len(reviews) != 0 {
			t.Fatalf("create fabricated Reviews: %d err=%v", len(reviews), err)
		}
		runs, err := f.plane.Runs.List(ctx)
		if err != nil || len(runs) != 0 {
			t.Fatalf("create fabricated Runs: %d err=%v", len(runs), err)
		}
	}
	checkCounts()
	// Losing the usable context disables fresh create capability without changing
	// the persisted Task. A committed request still has to replay its receipt.
	if _, err := f.plane.DB.SQL().ExecContext(ctx, "UPDATE project_contexts SET status='REQUIRED',context_fingerprint='' WHERE project_id=?", f.projectID); err != nil {
		t.Fatal(err)
	}
	status, _, body := getRaw(t, f.handler, "/ui/api/featureAreas/"+areaID+"/presentation?project_id="+url.QueryEscape(f.projectID))
	if status != http.StatusOK {
		t.Fatalf("FeatureArea presentation: %d %s", status, body)
	}
	var presentation struct {
		Capabilities []struct {
			Action    string `json:"action"`
			Available bool   `json:"available"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(body), &presentation); err != nil {
		t.Fatal(err)
	}
	for _, cap := range presentation.Capabilities {
		if cap.Action == "CREATE_TASK" && cap.Available {
			t.Fatal("fresh Task command stayed available after its context prerequisite was invalidated")
		}
	}
	postJSON(t, f.handler, route, input, http.StatusInternalServerError, nil)
	checkCounts()
	other, err := f.plane.Projects.Create(ctx, "Foreign Task creation project")
	if err != nil {
		t.Fatal(err)
	}
	postJSON(t, f.handler, "/ui/api/featureAreas/"+areaID+"/action?project_id="+url.QueryEscape(other.ID), input, http.StatusNotFound, nil)
	checkCounts()
}

func TestTaskActionHTTPRejectsPreviouslyObservedCapabilityAfterPhaseChanges(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	ctx := f.ctx
	owner := createConsoleTask(t, f, f.moduleID, task.VerificationSpec{}, "Task with an observed Review action")
	status, _, body := getRaw(t, f.handler, "/ui/api/tasks/"+owner.ID+"?project_id="+f.projectID)
	if status != http.StatusOK {
		t.Fatalf("Task detail: %d %s", status, body)
	}
	var detail consoleapi.ProductTaskSummary
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatal(err)
	}
	var observed *workflowpresentation.ActionTarget
	for _, cap := range detail.Presentation.TaskView.Actions {
		if cap.Action == "START_TASK_REVIEW" && cap.Available {
			observed = cap.Target
		}
	}
	if observed == nil {
		t.Fatal("initial Review capability missing")
	}
	operation, err := f.plane.Reviews.Create(ctx, f.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: owner.ID, ProjectID: f.projectID, FeatureAreaID: owner.FeatureAreaID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: f.configID, BaseCommit: f.baseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Read-only local fixture", EngineeringGoal: "Local review admission"}))
	if err != nil {
		t.Fatal(err)
	}
	admittedRun, err := f.plane.Runs.CreateFocusedScopeReviewForReview(ctx, operation.ID, operation.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.plane.Reviews.LinkRun(ctx, operation.ID, admittedRun.ID); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(consoleapi.TaskActionInput{ReviewDepth: task.ReviewDepthDeep, Action: "START_TASK_REVIEW", Target: observed, RequestID: "stale-observed-review", ReviewInstruction: "Must not start another Review"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/ui/api/tasks/"+owner.ID+"/action?project_id="+f.projectID, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code < 400 || response.Code >= 500 {
		t.Fatalf("stale action needs client rejection: %d %s", response.Code, response.Body.String())
	}
	reviews, err := f.plane.Reviews.ListByFeatureArea(ctx, owner.FeatureAreaID)
	if err != nil || len(reviews) != 1 {
		t.Fatalf("stale action admitted Review: %d %v", len(reviews), err)
	}
	runs, err := f.plane.Runs.List(ctx)
	if err != nil || len(runs) != 1 || runs[0].ID != admittedRun.ID {
		t.Fatalf("stale action admitted Run: %+v %v", runs, err)
	}
	fresh, err := f.plane.Tasks.Get(ctx, owner.ID)
	if err != nil || fresh.Phase != task.PhaseReviewing || fresh.CurrentReviewID != operation.ID {
		t.Fatalf("stale action changed Task: %+v %v", fresh, err)
	}
}

// Two explicit authored specifications can each use the documented instruction
// bound. JSON transport must not reject them before the exact action authority.
func TestTaskActionTransportSupportsAuthoredSpecsAndRejectsTrailingJSON(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	owner := createPlanningTaskForArea(t, f, newActiveConsoleFeature(t, f, "Transport"), "Semantic transport Task")
	payload, err := json.Marshal(map[string]any{"action": "REQUEST_CHANGES", "requestId": "large-spec-request", "nextWorkSpec": map[string]any{"codexInstruction": strings.Repeat("x", 64<<10)}, "nextVerificationSpec": map[string]any{"codexInstruction": strings.Repeat("y", 64<<10)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		body   []byte
		status int
	}{
		{"complete specifications reject without current feature-area guidance", payload, http.StatusBadRequest},
		{"second JSON value rejected", append(append([]byte(nil), payload...), []byte(" {}")...), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/ui/api/tasks/"+owner.ID+"/action?project_id="+f.projectID, bytes.NewReader(tc.body))
			req.RemoteAddr = "127.0.0.1:45678"
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			f.handler.ServeHTTP(res, req)
			if res.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", res.Code, tc.status, res.Body.String())
			}
		})
	}
	runs, err := f.plane.Runs.List(context.Background())
	if err != nil || len(runs) != 0 {
		t.Fatalf("invalid actions created Runs: %+v %v", runs, err)
	}
}
