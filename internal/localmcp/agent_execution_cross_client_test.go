package localmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestThreeKindUniversalExecutionCrossClientContract(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := f.ctx
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	current, proposal := f.admitScalarExecution(t, sid)
	backend := &unifiedBackend{stubCodexSession: &stubCodexSession{id: "three-kind"}, fixture: f, entered: make(chan struct{}), finish: make(chan struct{})}
	f.plane.Coordinator.Codex = backend
	started, err := f.control.StartTaskExecution(ctx, sid, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fake backend not started")
	}
	reserved := assertScalarActiveRun(t, f, proposal, started.Run.ID)
	probe, err := f.plane.ContextProbes.Create(ctx, contextprobe.CreateInput{AISessionID: sid, RepositoryID: f.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}, CodexInstruction: "Fixture inspection"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.ContextProbes.Start(ctx, probe.ID); err != nil {
		t.Fatal(err)
	}
	web, err := bridgehttp.New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	assertAllClients := func() {
		t.Helper()
		projections, err := f.control.ListAgentExecutions(ctx, f.projectID)
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ui/api/project-workspace?project_id="+f.projectID, nil)
		setLocalOperatorLoopback(req)
		web.Handler().ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatal(rr.Body.String())
		}
		var view consoleapi.ProductProjectWorkspace
		if err = json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		byID := map[string]agentexecution.Projection{}
		kinds := map[agentexecution.Kind]bool{}
		for _, p := range projections {
			byID[p.ExecutionID] = p
			kinds[p.Kind] = true
		}
		if len(kinds) != 3 {
			t.Fatalf("not three kinds: %+v", kinds)
		}
		for _, e := range view.Executions {
			p, ok := byID[e.ID]
			if !ok || e.Execution == nil || !reflect.DeepEqual(*e.Execution, p) || e.TaskID != p.TaskID || e.Phase != p.Stage || e.State != p.Status || e.SemanticOwnerID != p.SemanticOwnerID {
				t.Fatalf("Web/Core mismatch: %+v / %+v", e, p)
			}
		}
		if len(view.Executions) != len(projections) {
			t.Fatal("Web inventory incomplete")
		}
		for _, session := range []*sdkmcp.ClientSession{gpt, local} {
			inventory := callToolMap(t, ctx, session, "work_list", map[string]any{"aiSessionId": sid, "kind": "OPERATION", "limit": 100})
			raw, _ := json.Marshal(inventory["result"].(map[string]any)["operations"])
			var values []presentation.SelectedOperation
			if err = json.Unmarshal(raw, &values); err != nil {
				t.Fatal(err)
			}
			if len(values) != len(projections) {
				t.Fatal("MCP inventory incomplete", inventory)
			}
			listed := map[string]presentation.SelectedOperation{}
			for _, value := range values {
				listed[value.Kind+":"+value.OperationID] = value
			}
			for _, p := range projections {
				want := presentation.SelectedOperationFromAgentExecution(p)
				if want == nil || !reflect.DeepEqual(listed[want.Kind+":"+want.OperationID], *want) {
					t.Fatalf("MCP inventory diverged: %+v / %+v", values, projections)
				}
				readOperationSurfaceResult(t, f, session, sid, p)
			}
		}
	}
	assertAllClients()
	// Both MCP adapters advertise cancellation of the same exact active Run.
	for _, session := range []*sdkmcp.ClientSession{gpt, local} {
		p, err := f.control.GetAgentExecutionForSession(ctx, sid, proposal.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := readOperationSurfaceResult(t, f, session, sid, p)
		if out["cancellation"].(map[string]any)["available"] != true {
			t.Fatal(out)
		}
	}
	receipt := cancelHTTP(t, web.Handler(), f.projectID, proposal.ID, reserved.ID, "no-run")
	deadline := time.Now().Add(5 * time.Second)
	for receipt.ResolvedAt == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		receipt = cancelHTTP(t, web.Handler(), f.projectID, proposal.ID, reserved.ID, "no-run")
	}
	if receipt.State != "CANCELED" || receipt.BackendDisposition != "TERMINATED" {
		t.Fatal(receipt)
	}
	replay := cancelHTTP(t, web.Handler(), f.projectID, proposal.ID, reserved.ID, "no-run")
	if !reflect.DeepEqual(receipt, replay) {
		t.Fatal("replay changed")
	}
	assertAllClients()
}
func cancelHTTP(t *testing.T, handler http.Handler, project, id, runID, requestID string) executioncancel.Receipt {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"runId": runID, "requestId": requestID})
	req := httptest.NewRequest(http.MethodPost, "/ui/api/executions/"+id+"/cancel?project_id="+project, bytes.NewReader(data))
	setLocalOperatorLoopback(req)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("cancel HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var receipt executioncancel.Receipt
	if err := json.Unmarshal(rr.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func setLocalOperatorLoopback(req *http.Request) {
	req.RemoteAddr = "127.0.0.1:45678"
	req.Host = "127.0.0.1:48765"
	*req = *req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 48765}))
}
func TestActiveDevelopmentCancellationThroughProductHTTP(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	current, proposal := f.admitScalarExecution(t, sid)
	backend := &unifiedBackend{stubCodexSession: &stubCodexSession{id: "http-development"}, fixture: f, entered: make(chan struct{}), finish: make(chan struct{})}
	f.plane.Coordinator.Codex = backend
	started, err := f.control.StartTaskExecution(f.ctx, sid, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fake Turn did not start")
	}
	web, err := bridgehttp.New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	receipt := cancelHTTP(t, web.Handler(), f.projectID, proposal.ID, started.Run.ID, "active-http")
	if receipt.OwnerID != proposal.ID || receipt.RunID != started.Run.ID {
		t.Fatal(receipt)
	}
	deadline := time.Now().Add(5 * time.Second)
	for receipt.ResolvedAt == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		receipt = cancelHTTP(t, web.Handler(), f.projectID, proposal.ID, started.Run.ID, "active-http")
	}
	if receipt.State != "CANCELED" || backend.calls.Load() != 1 {
		t.Fatalf("canonical convergence: %+v calls=%d", receipt, backend.calls.Load())
	}
	replay := cancelHTTP(t, web.Handler(), f.projectID, proposal.ID, started.Run.ID, "active-http")
	if !reflect.DeepEqual(receipt, replay) {
		t.Fatal("lost response replay changed")
	}
}

// Assert fresh reads against explicit expected facts, not just adapter equality.
func assertDevelopmentProjectionClients(t *testing.T, f *integrationFixture, sid, id, status, disposition, cancellation string, available bool) agentexecution.Projection {
	t.Helper()
	p, err := f.control.GetAgentExecutionForSession(f.ctx, sid, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.ExecutionID != id || p.Kind != agentexecution.KindDevelopment || p.Status != status || p.BackendDisposition != disposition || p.CancellationState != cancellation || p.RunID != "" || len(p.RunIDs) != 0 {
		t.Fatalf("wrong Core facts: %+v", p)
	}
	if status == "REJECTED" && (p.DomainStatus != "REJECTED" || p.DomainResult != "PENDING" || p.UserAuthorizationRequired) {
		t.Fatalf("rejection conflated: %+v", p)
	}
	values, err := f.control.ListAgentExecutions(f.ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range values {
		if v.ExecutionID == id {
			found = reflect.DeepEqual(v, p)
		}
	}
	if !found {
		t.Fatal("Core inventory missing exact projection")
	}
	taskValue, err := f.control.GetTask(f.ctx, p.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if taskValue.Work.ExecutionID != id || taskValue.Work.ExecutionStatus != status || taskValue.Work.CancellationState != cancellation || taskValue.Work.BackendDisposition != disposition {
		t.Fatalf("Task.Work mismatch: %+v", taskValue.Work)
	}
	current, err := f.control.CurrentWorkflowForSession(f.ctx, sid, p.FeatureAreaID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Guidance != nil && current.Guidance.CurrentState == "FEATURE_AREA_IDLE" {
		if current.Task != nil || current.AgentExecution != nil || current.LatestTask == nil || current.LatestTask.ID != p.TaskID {
			t.Fatalf("idle current=%+v", current)
		}
	} else if current.AgentExecution == nil || !reflect.DeepEqual(*current.AgentExecution, p) {
		t.Fatalf("current mismatch: %+v want %+v", current.AgentExecution, p)
	}
	cap, _, err := f.control.ExecutionCancellationPresentationForSession(f.ctx, sid, id)
	if err != nil || cap.Available != available {
		t.Fatalf("Core capability: %+v %v", cap, err)
	}
	reader, err := consoleapi.New(f.plane, f.control)
	if err != nil {
		t.Fatal(err)
	}
	view, err := reader.GetProductProjectWorkspace(f.operatorContext(t), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	assertView := func(v consoleapi.ProductProjectWorkspace) {
		t.Helper()
		taskFound := false
		for _, task := range v.Tasks {
			if task.ID != p.TaskID {
				continue
			}
			taskFound = true
			if task.Work.ExecutionID != id || task.Work.ExecutionStatus != status || task.Work.CancellationState != cancellation || task.Work.BackendDisposition != disposition {
				t.Fatalf("Console/Web Task.Work mismatch: %+v", task.Work)
			}
		}
		if !taskFound {
			t.Fatal("missing Console/Web task")
		}

		found := false
		for _, e := range v.Executions {
			if e.ID != id {
				continue
			}
			found = true
			if e.Execution == nil || !reflect.DeepEqual(*e.Execution, p) || e.State != status || e.BackendDisposition != disposition {
				t.Fatalf("Console/Web mismatch: %+v", e)
			}
			if e.CancellationCapability == nil || e.CancellationCapability.Available != available {
				t.Fatalf("Console/Web capability: %+v", e)
			}
		}
		if !found {
			t.Fatal("missing Console/Web execution")
		}
	}
	assertView(view)
	web, err := bridgehttp.New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ui/api/project-workspace?project_id="+f.projectID, nil)
	setLocalOperatorLoopback(req)
	rr := httptest.NewRecorder()
	web.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	if err = json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	assertView(view)
	for _, session := range []*sdkmcp.ClientSession{connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)} {
		out := readOperationSurfaceResult(t, f, session, sid, p)
		c := out["cancellation"].(map[string]any)
		if c["available"] != available {
			t.Fatal(c)
		}
		if !available && len(c["availableActions"].([]any)) != 0 {
			t.Fatal("terminal action advertised", c)
		}
		if cancellation == "" && len(c["receipts"].([]any)) != 0 {
			t.Fatal("borrowed or invented receipt", c)
		}
		if cancellation == "CANCELED" {
			if len(c["receipts"].([]any)) != 1 {
				t.Fatal("missing own canonical receipt", c)
			}
			for _, rawReceipt := range c["receipts"].([]any) {
				receipt := rawReceipt.(map[string]any)
				if receipt["ownerId"] != p.SemanticOwnerID || receipt["executionId"] != id || receipt["taskId"] != p.TaskID || receipt["backendDisposition"] != "NO_BACKEND" || receipt["backendInterruptState"] != "NOT_SENT" {
					t.Fatal("wrong receipt correlation", receipt)
				}
			}
		}
		workflow := callToolMap(t, f.ctx, session, "workspace_get", map[string]any{"aiSessionId": sid, "featureAreaId": p.FeatureAreaID, "taskId": p.TaskID})
		result := workflow["result"].(map[string]any)
		if result["selectedTask"].(map[string]any)["id"] != p.TaskID {
			t.Fatal("workspace changed exact Task", workflow)
		}
		if status == "REJECTED" {
			for _, value := range workflow["workflow"].(map[string]any)["capabilities"].([]any) {
				c := value.(map[string]any)
				if c["action"] == "START_EXECUTION" && c["available"] == true {
					t.Fatal("rejected workflow advertises start", workflow)
				}
			}
		}
		inv := callToolMap(t, f.ctx, session, "work_list", map[string]any{"aiSessionId": sid, "kind": "OPERATION", "taskId": p.TaskID, "limit": 100})
		raw, _ := json.Marshal(inv["result"].(map[string]any)["operations"])
		var listed []presentation.SelectedOperation
		if err = json.Unmarshal(raw, &listed); err != nil {
			t.Fatal(err)
		}
		want := presentation.SelectedOperationFromAgentExecution(p)
		found := false
		for _, v := range listed {
			if v.OperationID == want.OperationID && v.Kind == want.Kind {
				found = reflect.DeepEqual(v, *want)
			}
		}
		if !found {
			t.Fatal("MCP inventory missing exact projection", inv)
		}
	}
	runs, err := sqlite.NewAgentExecutionStore(f.plane.DB).ExactRuns(f.ctx, id)
	if err != nil || len(runs) != 0 {
		t.Fatalf("synthetic Run: %+v %v", runs, err)
	}
	return p
}

func TestCanceledDevelopmentPreservesTaskPhaseAcrossClients(t *testing.T) {
	f, gpt, sid, a := rejectedReadFixture(t)
	assertHistoricalRejectedClients(t, f, gpt, sid, a, "TERMINATED", "CANCELED")
	owner, e := f.plane.Tasks.Get(f.ctx, a.TaskID)
	if e != nil || owner.Status != "ACTIVE" || owner.Phase != "IMPLEMENTING" {
		t.Fatalf("operation cancellation changed Task: %+v %v", owner, e)
	}
}

func TestNoRunSuccessorCancellationExactOwnerReplayAcrossClients(t *testing.T) {
	f, _, sid, a := rejectedReadFixture(t)
	next := nextReadAuthorization(t, f, sid, a)
	assertDevelopmentProjectionClients(t, f, sid, next.ID, "PENDING", "NOT_STARTED", "", true)
	web, e := bridgehttp.New(f.plane, false)
	if e != nil {
		t.Fatal(e)
	}
	own := cancelHTTP(t, web.Handler(), f.projectID, next.ID, "", "current-owner")
	if own.OwnerID != next.ID || own.RunID != "" || own.BackendDisposition != "NO_BACKEND" {
		t.Fatal(own)
	}
	first := assertDevelopmentProjectionClients(t, f, sid, next.ID, "CANCELED", "NO_BACKEND", "CANCELED", false)
	replay := cancelHTTP(t, web.Handler(), f.projectID, next.ID, "", "current-owner")
	again := assertDevelopmentProjectionClients(t, f, sid, next.ID, "CANCELED", "NO_BACKEND", "CANCELED", false)
	if !reflect.DeepEqual(own, replay) || !reflect.DeepEqual(first, again) {
		t.Fatal("replay changed exact ownership")
	}
}

// Read the actual RESULT branch and compare canonical facts without rebuilding
// the removed execution_get response shape.
func readOperationSurfaceResult(t *testing.T, f *integrationFixture, session *sdkmcp.ClientSession, sid string, expected agentexecution.Projection) map[string]any {
	t.Helper()
	selected := presentation.SelectedOperationFromAgentExecution(expected)
	if selected == nil {
		t.Fatal("Core projection has no exact semantic target")
	}
	envelope := callToolMap(t, f.ctx, session, "operation_get", map[string]any{"aiSessionId": sid, "target": selected.Target, "include": []string{"RESULT"}})
	result := envelope["result"].(map[string]any)
	raw, _ := json.Marshal(result["coreOperation"])
	var actual agentexecution.Projection
	if err := json.Unmarshal(raw, &actual); err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("MCP exact Core facts diverged: %+v / %+v (%v)", actual, expected, err)
	}
	var operation presentation.SelectedOperation
	raw, _ = json.Marshal(result["operation"])
	if err := json.Unmarshal(raw, &operation); err != nil || !reflect.DeepEqual(operation.Target, selected.Target) || operation.Status != expected.Status || operation.BackendDisposition != expected.BackendDisposition {
		t.Fatalf("compact operation diverged: %+v / %+v (%v)", operation, expected, err)
	}
	if result["coreOperationSource"] != "CORE_OPERATION" || result["cancellationSource"] != "CORE_CANCELLATION" {
		t.Fatal("source provenance absent", result)
	}
	return result
}
