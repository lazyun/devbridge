package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type surfaceCommandSpy struct {
	BridgeService
	fileAction string
	fileInput  directfile.Input
	starts     int
	startTask  string
	lifecycle  string
}

func (s *surfaceCommandSpy) InitializeSession(_ context.Context, _ aiprojectselection.ClientBindingReference, _ string, id string) (aicontext.InitializationResult, error) {
	projectID := "project"
	return aicontext.InitializationResult{AISessionID: id, State: "PROJECT_BOUND", BoundProjectID: &projectID, AvailableProjects: []aicontext.ProjectIdentity{}, AvailableProjectsStatus: "NOT_APPLICABLE", AvailableProjectsComplete: true}, nil
}
func (s *surfaceCommandSpy) BindProject(context.Context, aiprojectselection.ClientBindingReference, string, string, string) (aicontext.ProjectBindingResult, error) {
	return aicontext.ProjectBindingResult{}, errors.New("fixture binding is already established")
}

func (s *surfaceCommandSpy) DirectFileEnable(context.Context, directfile.Scope) (directfile.State, error) {
	return directfile.State{}, nil
}
func (s *surfaceCommandSpy) DirectFileDisable(context.Context, directfile.Scope) (directfile.State, error) {
	return directfile.State{}, nil
}
func (s *surfaceCommandSpy) DirectFileFileList(context.Context, directfile.Input) (directfile.Result, error) {
	return directfile.Result{}, nil
}
func (s *surfaceCommandSpy) DirectFileFileRead(context.Context, directfile.Input) (directfile.Result, error) {
	return directfile.Result{}, nil
}
func (s *surfaceCommandSpy) file(action string, in directfile.Input) (directfile.Result, error) {
	s.fileAction, s.fileInput = action, in
	return directfile.Result{Path: in.Path}, nil
}
func (s *surfaceCommandSpy) DirectFileFileCreate(_ context.Context, in directfile.Input) (directfile.Result, error) {
	return s.file("CREATE", in)
}
func (s *surfaceCommandSpy) DirectFileFileReplace(_ context.Context, in directfile.Input) (directfile.Result, error) {
	return s.file("REPLACE", in)
}
func (s *surfaceCommandSpy) DirectFileFileDelete(_ context.Context, in directfile.Input) (directfile.Result, error) {
	return s.file("DELETE", in)
}
func (s *surfaceCommandSpy) DirectFileFileMove(_ context.Context, in directfile.Input) (directfile.Result, error) {
	return s.file("MOVE", in)
}
func (s *surfaceCommandSpy) StartTaskExecution(_ context.Context, _ string, id string) (executionproposal.StartResult, error) {
	s.starts++
	s.startTask = id
	return executionproposal.StartResult{}, nil
}
func (s *surfaceCommandSpy) ActivateFeatureArea(context.Context, string, string, string) (featurearea.FeatureArea, error) {
	s.lifecycle = "ACTIVATE"
	return featurearea.FeatureArea{}, nil
}
func (s *surfaceCommandSpy) RetireFeatureArea(context.Context, string, string, string) (featurearea.FeatureArea, error) {
	s.lifecycle = "RETIRE"
	return featurearea.FeatureArea{}, nil
}
func (s *surfaceCommandSpy) DecomposeFeatureArea(context.Context, featurearea.DecomposeInput) (featurearea.LifecycleResult, error) {
	s.lifecycle = "DECOMPOSE"
	return featurearea.LifecycleResult{}, nil
}
func (s *surfaceCommandSpy) WorkflowGuidanceForResource(context.Context, wp.ResourceScope) (wp.Guidance, error) {
	return wp.Guidance{}, errors.New("isolated readback failure")
}

func surfaceTestSession(t *testing.T, service BridgeService) (*sdkmcp.ClientSession, map[string]*sdkmcp.Tool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "isolated-surface", Version: "1"}, nil)
	server.AddReceivingMiddleware(WorkflowGuidanceMiddleware(service))
	RegisterSurfaceBusinessTools(server, service)
	a, b := sdkmcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "isolated-contract", Version: "1"}, nil)
	session, err := client.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*sdkmcp.Tool{}
	for _, v := range listed.Tools {
		if byName[v.Name] != nil {
			t.Fatalf("duplicate %s", v.Name)
		}
		byName[v.Name] = v
	}
	return session, byName
}
func TestSurfaceActualSharedCatalogAndClosedBranches(t *testing.T) {
	session, listed := surfaceTestSession(t, &surfaceCommandSpy{})
	got := []string{}
	for name, v := range listed {
		got = append(got, name)
		if v.Meta["openai/outputTemplate"] != nil || v.Meta["openai/toolInvocation/invoking"] != nil || v.Meta["openai/toolInvocation/invoked"] != nil {
			t.Fatalf("shared Local catalog exposes Card invocation metadata: %s %#v", name, v.Meta)
		}
	}
	sort.Strings(got)
	want := []string{}
	for _, name := range SurfaceBusinessToolNames {
		if !strings.HasPrefix(name, "repository_unlink_") {
			want = append(want, name)
		}
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shared catalog %v", got)
	}
	for _, name := range []string{"workflow_get", "review_get", "execution_get", "featureArea_create", "featureArea_retire", "review_approval_respond", "task_acceptance_respond", "execution_cancel", "file_create", "file_replace", "file_delete", "file_move", "codex_request_resolve"} {
		if listed[name] != nil {
			t.Fatalf("legacy alias remains %s", name)
		}
	}
	tests := []struct {
		name    string
		valid   []string
		invalid []string
	}{
		{"feature_area_lifecycle", []string{`{"action":"ACTIVATE","aiSessionId":"s","featureAreaId":"a","requestId":"r"}`, `{"action":"RETIRE","aiSessionId":"s","featureAreaId":"a","requestId":"r","confirmed":true}`, `{"action":"DECOMPOSE","aiSessionId":"s","featureAreaId":"a","requestId":"r","children":[{"title":"child","problemStatement":"intent"}]}`}, []string{`{"action":"RETIRE","aiSessionId":"s","featureAreaId":"a","requestId":"r"}`, `{"action":"ACTIVATE","aiSessionId":"s","featureAreaId":"a","requestId":"r","children":[]}`, `{"action":"DECOMPOSE","aiSessionId":"s","featureAreaId":"a","requestId":"r","children":[],"confirmed":true}`}},
		{"decision_respond", []string{`{"kind":"REVIEW_APPROVAL","aiSessionId":"s","taskId":"t","decisionId":"REVIEW_APPROVAL:r","option":"APPROVE","requestId":"r"}`, `{"kind":"TASK_ACCEPTANCE","aiSessionId":"s","taskId":"t","featureAreaId":"a","executionId":"e","taskDefinitionRevisionId":"v","decisionId":"TASK_ACCEPTANCE:e","option":"REQUEST_REVERIFICATION","requestId":"r"}`}, []string{`{"kind":"RUNTIME_APPROVAL","aiSessionId":"s","taskId":"t","decisionId":"RUNTIME_APPROVAL:a","option":"APPROVE","requestId":"r"}`, `{"kind":"REVIEW_APPROVAL","aiSessionId":"s","taskId":"t","decisionId":"REVIEW_APPROVAL:r","option":"APPROVE","requestId":"r","executionId":"e"}`, `{"kind":"TASK_ACCEPTANCE","aiSessionId":"s","taskId":"t","featureAreaId":"a","executionId":"e","taskDefinitionRevisionId":"v","decisionId":"TASK_ACCEPTANCE:e","option":"REQUEST_CHANGES","requestId":"r"}`}},
		{"file_write", []string{`{"action":"CREATE","aiSessionId":"s","featureAreaId":"a","workflowId":"w","path":"x","content":""}`, `{"action":"REPLACE","aiSessionId":"s","featureAreaId":"a","workflowId":"w","path":"x","content":"data","workflowComplete":true}`, `{"action":"DELETE","aiSessionId":"s","featureAreaId":"a","workflowId":"w","path":"x"}`, `{"action":"MOVE","aiSessionId":"s","featureAreaId":"a","workflowId":"w","path":"x","destination":"y"}`}, []string{`{"action":"DELETE","aiSessionId":"s","featureAreaId":"a","workflowId":"w","path":"x","content":""}`, `{"action":"CREATE","aiSessionId":"s","featureAreaId":"a","workflowId":"w","path":"x","content":"data","destination":"y"}`, `{"action":"MOVE","aiSessionId":"s","featureAreaId":"a","workflowId":"w","path":"x"}`, `{"action":"UPSERT","aiSessionId":"s","featureAreaId":"a","workflowId":"w","path":"x","content":"data"}`}},
		{"operation_cancel", []string{`{"aiSessionId":"s","ownerType":"TASK","ownerId":"t","requestId":"r"}`, `{"aiSessionId":"s","ownerType":"INSPECTION","ownerId":"t","requestId":"r"}`, `{"aiSessionId":"s","executionId":"e","runId":"run","requestId":"r"}`}, []string{`{"aiSessionId":"s","executionId":"e","ownerType":"TASK","ownerId":"t","requestId":"r"}`}},
		{"runtime_request_respond", []string{`{"kind":"RUNTIME_APPROVAL","aiSessionId":"s","approvalId":"a","decision":"DECLINE"}`}, []string{`{"kind":"RUNTIME_APPROVAL","aiSessionId":"s","approvalId":"a","decision":"DECLINE","requestId":"r"}`, `{"kind":"RUNTIME_APPROVAL","aiSessionId":"s","approvalId":"a","decision":"DECLINE","permissions":{}}`, `{"kind":"CODEX_REQUEST","aiSessionId":"s","resolution":{}}`, `{"kind":"CODEX_REQUEST","aiSessionId":"s","resolution":{"projectId":"p","featureAreaId":"a","taskId":"t","runId":"r","threadId":"th","turnId":"tu","connectionId":"c","itemId":"i","requestId":1,"response":{"decision":"decline"}}}`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			schema := resolveSchema(t, listed[tc.name].InputSchema)
			for _, raw := range tc.valid {
				var input any
				_ = json.Unmarshal([]byte(raw), &input)
				if err := schema.Validate(input); err != nil {
					t.Fatalf("valid branch %s: %v", raw, err)
				}
			}
			for _, raw := range tc.invalid {
				var input map[string]any
				_ = json.Unmarshal([]byte(raw), &input)
				if err := schema.Validate(input); err == nil {
					t.Fatalf("invalid branch accepted %s", raw)
				}
				result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: tc.name, Arguments: input})
				if err == nil && !result.IsError {
					t.Fatalf("actual handler accepted invalid branch %s", raw)
				}
			}
		})
	}
}

func TestSurfaceReadDescriptionsAreSilentAndReuseExactTargets(t *testing.T) {
	_, listed := surfaceTestSession(t, &surfaceCommandSpy{})
	for _, name := range []string{"workspace_get", "work_list", "operation_get"} {
		description := listed[name].Description
		for _, want := range []string{"SILENT", "internal analysis", "component refresh", "never inserts a Card", "never inserts a Card or changes workflow state"} {
			if !strings.Contains(description, want) {
				t.Fatalf("%s description omits %q: %s", name, want, description)
			}
		}
	}
	operation := listed["operation_get"].Description
	for _, want := range []string{"workspace_show result", "do not mechanically call workspace_get first"} {
		if !strings.Contains(operation, want) {
			t.Fatalf("operation_get description omits %q: %s", want, operation)
		}
	}
}
func TestSurfaceFileBranchesDelegateExactCoreAndCompletion(t *testing.T) {
	spy := &surfaceCommandSpy{}
	session, listed := surfaceTestSession(t, spy)
	for _, action := range []string{"CREATE", "REPLACE", "DELETE", "MOVE"} {
		args := map[string]any{"action": action, "aiSessionId": "s", "featureAreaId": "a", "workflowId": "exact-workflow", "path": "source", "workflowComplete": true}
		if action == "CREATE" || action == "REPLACE" {
			args["content"] = "exact bytes"
		}
		if action == "MOVE" {
			args["destination"] = "target"
		}
		out, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "file_write", Arguments: args})
		if err != nil || out.IsError {
			t.Fatalf("%s failed %v %+v", action, err, out)
		}
		if err := resolveSchema(t, listed["file_write"].OutputSchema).Validate(jsonValue(t, out.StructuredContent)); err != nil {
			t.Fatalf("generated file_write output schema mismatch: %v", err)
		}
		if spy.fileAction != action || spy.fileInput.WorkflowID != "exact-workflow" || !spy.fileInput.WorkflowComplete {
			t.Fatalf("dispatch changed authority or completion: %+v", spy)
		}
	}
}
func TestSurfaceExecutionStartAndFailedRefreshPreserveCommand(t *testing.T) {
	spy := &surfaceCommandSpy{}
	session, listed := surfaceTestSession(t, spy)
	out, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "execution_start", Arguments: map[string]any{"aiSessionId": "s", "taskId": "exact-current-task"}})
	if err != nil || out.IsError {
		t.Fatalf("committed start lost to readback %v %+v", err, out)
	}
	if err := resolveSchema(t, listed["execution_start"].OutputSchema).Validate(jsonValue(t, out.StructuredContent)); err != nil {
		t.Fatalf("generated execution_start output schema mismatch: %v", err)
	}
	if spy.starts != 1 || spy.startTask != "exact-current-task" {
		t.Fatalf("wrong canonical start %+v", spy)
	}
	v := jsonValue(t, out.StructuredContent)
	if v["result"] == nil || v["refreshRequired"] != true || v["workflow"].(map[string]any)["stale"] != true {
		t.Fatalf("success/refresh separation missing %#v", v)
	}
}

type surfaceReservedDispatchSpy struct{ *surfaceCommandSpy }

func (s *surfaceReservedDispatchSpy) StartTaskExecution(_ context.Context, _ string, id string) (executionproposal.StartResult, error) {
	s.starts++
	s.startTask = id
	return executionproposal.StartResult{Proposal: executionproposal.Proposal{ID: "reserved-candidate", TaskID: id}, Run: run.Run{ID: "reserved-run", TaskID: id}}, errors.New("isolated dispatch unavailable")
}
func TestSurfaceReservedExecutionIdentitySurvivesDispatchFailure(t *testing.T) {
	spy := &surfaceReservedDispatchSpy{&surfaceCommandSpy{}}
	session, listed := surfaceTestSession(t, spy)
	out, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "execution_start", Arguments: map[string]any{"aiSessionId": "s", "taskId": "exact-task"}})
	if err != nil || !out.IsError {
		t.Fatalf("dispatch failure lost: %v %+v", err, out)
	}
	envelope := jsonValue(t, out.StructuredContent)
	value := envelope["result"].(map[string]any)
	if value["reservationCommitted"] != true || value["dispatchError"] == "" || envelope["refreshRequired"] != true || value["Proposal"].(map[string]any)["ID"] != "reserved-candidate" || value["Run"].(map[string]any)["ID"] != "reserved-run" {
		t.Fatalf("reserved identity/failure lost %#v", envelope)
	}
	if err := resolveSchema(t, listed["execution_start"].OutputSchema).Validate(envelope); err != nil {
		t.Fatalf("reserved failure violates output schema: %v", err)
	}
}

type surfaceTaskReadbackSpy struct{ BridgeService }

func (*surfaceTaskReadbackSpy) ReplayPlanningTask(context.Context, controlapi.CreatePlanningTaskInput) (task.Task, error) {
	return task.Task{ID: "committed-task", FeatureAreaID: "area", Title: "intent", Status: task.StatusActive, Phase: task.PhaseReviewRequired}, nil
}
func (*surfaceTaskReadbackSpy) WorkflowGuidanceForResource(context.Context, wp.ResourceScope) (wp.Guidance, error) {
	return wp.Guidance{}, errors.New("isolated post-commit readback unavailable")
}
func TestSurfaceCommittedTaskSurvivesReadbackFailure(t *testing.T) {
	out, err := TaskCreateCore(context.Background(), &surfaceTaskReadbackSpy{}, ExternalTaskCreateInput{AISessionID: "session", RequestID: "create-request", Title: "intent", ProblemStatement: "save intent"})
	if err != nil || out.ID != "committed-task" || !out.RefreshRequired || out.NextAction.Code != "" {
		t.Fatalf("committed Task lost or stale action authorized: %+v %v", out, err)
	}
}
