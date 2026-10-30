package localmcp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func rejectedReadFixture(t *testing.T) (*integrationFixture, *sdkmcp.ClientSession, string, executionproposal.Proposal) {
	t.Helper()
	f := newIntegrationFixture(t)
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	f.plane.Retirement.Close()
	current, a := f.admitScalarExecution(t, sid)
	backend := &unifiedBackend{stubCodexSession: &stubCodexSession{id: "historical-cancel"}, fixture: f, entered: make(chan struct{}), finish: make(chan struct{})}
	f.plane.Coordinator.Codex = backend
	started, e := f.control.StartTaskExecution(f.ctx, sid, current.ID)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-backend.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fake backend not started")
	}
	web, e := bridgehttp.New(f.plane, false)
	if e != nil {
		t.Fatal(e)
	}
	receipt := cancelHTTP(t, web.Handler(), f.projectID, a.ID, started.Run.ID, "cancel-fixture")
	deadline := time.Now().Add(5 * time.Second)
	for receipt.ResolvedAt == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		receipt = cancelHTTP(t, web.Handler(), f.projectID, a.ID, started.Run.ID, "cancel-fixture")
	}
	if receipt.State != "CANCELED" || receipt.BackendDisposition != "TERMINATED" {
		t.Fatal(receipt)
	}
	a, e = f.plane.ExecutionProposals.Get(f.ctx, sid, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	return f, gpt, sid, a
}

func nextReadAuthorization(t *testing.T, f *integrationFixture, sid string, a executionproposal.Proposal) executionproposal.Proposal {
	t.Helper()
	work, verify := a.WorkSpec, a.VerificationSpec
	resolved, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).ResolveTaskAcceptance(f.ctx, wp.TaskAcceptanceInput{AISessionID: sid, ExecutionID: a.ID, DecisionID: "TASK_ACCEPTANCE:" + a.ID, Option: "REQUEST_CHANGES", RequestID: "next-" + a.ID, NextWorkSpec: &work, NextVerificationSpec: &verify})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.plane.ExecutionProposals.Get(f.ctx, sid, resolved.SuccessorExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.control.GetTask(f.ctx, a.TaskID)
	if err != nil || current.Work.ExecutionID != b.ID || b.ID == a.ID {
		t.Fatalf("successor not current: %+v %v", current, err)
	}
	return b
}

func assertExactExecutionCard(t *testing.T, f *integrationFixture, gpt *sdkmcp.ClientSession, sid string, p agentexecution.Projection) {
	t.Helper()
	card := callToolMap(t, f.ctx, gpt, "ui_workflow_state_get", map[string]any{"aiSessionId": sid, "taskId": p.TaskID, "executionProposalId": p.ExecutionProposalID})
	raw, _ := json.Marshal(card["agentExecution"])
	var actual agentexecution.Projection
	if err := json.Unmarshal(raw, &actual); err != nil || !reflect.DeepEqual(actual, p) || card["executionId"] != p.ExecutionID || card["executionProposalId"] != p.ExecutionProposalID {
		t.Fatalf("Card borrowed current identity/status: %+v want %+v (%v)", card, p, err)
	}
	owner, e := f.plane.Tasks.Get(f.ctx, p.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	if card["phase"] != string(owner.Phase) || card["terminal"] != (owner.Status != task.StatusActive) {
		t.Fatalf("child overwrote Task phase: %+v", card)
	}
	if p.RunID != "" && card["currentRun"].(map[string]any)["id"] != p.RunID {
		t.Fatal("wrong historical Run", card)
	}
	if card["executionProposal"] != nil || card["taskPlan"] != nil {
		t.Fatal("obsolete authorization Card", card)
	}

}

func assertHistoricalRejectedClients(t *testing.T, f *integrationFixture, gpt *sdkmcp.ClientSession, sid string, a executionproposal.Proposal, disposition, cancellation string) agentexecution.Projection {
	t.Helper()
	p, err := f.control.GetAgentExecutionForSession(f.ctx, sid, a.ID)
	if err != nil || p.ExecutionID != a.ID || p.TaskID != a.TaskID || p.Kind != agentexecution.KindDevelopment || p.Status != "CANCELED" || p.DomainStatus != "CONSUMED" || p.DomainResult != "CANCELED" || p.BackendDisposition != disposition || p.CancellationState != cancellation || p.RunID != a.RunID || len(p.RunIDs) != 1 || p.UserAuthorizationRequired {
		t.Fatalf("historical Core facts: %+v %v", p, err)
	}
	for _, session := range []*sdkmcp.ClientSession{gpt, connectLocalMCP(t, f.libreURL)} {
		out := readOperationSurfaceResult(t, f, session, sid, p)
		cap := out["cancellation"].(map[string]any)
		if cap["available"] != false || len(cap["availableActions"].([]any)) != 0 {
			t.Fatal("historical rejection advertises cancel", out)
		}
	}
	reader, err := consoleapi.New(f.plane, f.control)
	if err != nil {
		t.Fatal(err)
	}
	view, err := reader.GetProductProjectWorkspace(f.operatorContext(t), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	assertView := func(view consoleapi.ProductProjectWorkspace) {
		t.Helper()
		found := false
		for _, e := range view.Executions {
			if e.ID == a.ID {
				found = true
				if e.Execution == nil || !reflect.DeepEqual(*e.Execution, p) || e.CancellationCapability == nil || e.CancellationCapability.Available {
					t.Fatal("historical Console/Web/Core mismatch", e)
				}
			}
		}
		if !found {
			t.Fatal("historical Execution disappeared from inventory")
		}
	}
	assertView(view)
	web, err := bridgehttp.New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui/api/project-workspace?project_id="+f.projectID, nil)
	setLocalOperatorLoopback(req)
	web.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	assertView(view)
	assertExactExecutionCard(t, f, gpt, sid, p)
	return p
}

func TestHistoricalRejectedCardUsesSelectedExecutionAfterSuccessor(t *testing.T) {
	f, gpt, sid, a := rejectedReadFixture(t)
	b := nextReadAuthorization(t, f, sid, a)
	assertHistoricalRejectedClients(t, f, gpt, sid, a, "TERMINATED", "CANCELED")
	p := assertDevelopmentProjectionClients(t, f, sid, b.ID, "PENDING", "NOT_STARTED", "", true)
	assertExactExecutionCard(t, f, gpt, sid, p)
	if _, err := f.control.AuthorizeAndStartExecution(f.ctx, sid, a.ID); err == nil {
		t.Fatal("historical rejection acquired start authority")
	}
	if receipts, err := f.control.ListExecutionCancellationsForSession(f.ctx, sid, a.ID); err != nil || len(receipts) != 1 {
		t.Fatalf("history read synthesized cancellation: %+v %v", receipts, err)
	}
	other, _ := f.pendingReviewGate(t, sid)
	callToolErrorText(t, f.ctx, gpt, "ui_workflow_state_get", map[string]any{"aiSessionId": sid, "taskId": other.ID, "executionProposalId": a.ID})
	foreign, err := f.plane.Projects.Create(f.ctx, "Other project")
	if err != nil {
		t.Fatal(err)
	}
	foreignSID := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": foreign.ID})["aiSessionId"].(string)
	callToolErrorText(t, f.ctx, gpt, "ui_workflow_state_get", map[string]any{"aiSessionId": foreignSID, "taskId": a.TaskID, "executionProposalId": a.ID})
}

func TestTaskHTTPExactExecutionOwnerIsolationAndReplay(t *testing.T) {
	f, gpt, sid, a := rejectedReadFixture(t)
	web, err := bridgehttp.New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	older := cancelHTTP(t, web.Handler(), f.projectID, a.ID, a.RunID, "read-older")
	if older.State != "ALREADY_TERMINAL" || older.BackendDisposition != "TERMINATED" {
		t.Fatal(older)
	}
	b := nextReadAuthorization(t, f, sid, a)
	readHTTP := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		setLocalOperatorLoopback(req)
		web.Handler().ServeHTTP(rr, req)
		return rr
	}
	assertReads := func(status, disposition, cancellation string, available bool) map[string]string {
		t.Helper()
		p := assertDevelopmentProjectionClients(t, f, sid, b.ID, status, disposition, cancellation, available)
		assertExactExecutionCard(t, f, gpt, sid, p)
		current, err := f.control.GetTask(f.ctx, b.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		bodies := map[string]string{}
		for _, scope := range []string{"ai_session_id=" + sid, "project_id=" + f.projectID} {
			rr := readHTTP("/v1/tasks/" + b.TaskID + "?" + scope)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", scope, rr.Code, rr.Body.String())
			}
			var work task.Work
			if scope == "ai_session_id="+sid {
				var value task.Task
				if err := json.Unmarshal(rr.Body.Bytes(), &value); err != nil {
					t.Fatal(err)
				}
				work = value.Work
			} else {
				var value consoleapi.TaskDetail
				if err := json.Unmarshal(rr.Body.Bytes(), &value); err != nil {
					t.Fatal(err)
				}
				work = value.Task.Work
			}
			if !reflect.DeepEqual(work, current.Work) || work.ExecutionID != b.ID || work.ExecutionStatus != status || work.BackendDisposition != disposition || work.CancellationState != cancellation || work.RunID != "" {
				t.Errorf("%s returned raw/foreign Task.Work: %+v want %+v", scope, work, current.Work)
			}
			bodies[scope] = rr.Body.String()
		}
		// Console's list and detail share the same authorized Task summary path.
		rr := readHTTP("/v1/tasks?project_id=" + f.projectID)
		var list consoleapi.Page[consoleapi.TaskSummary]
		if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil || rr.Code != 200 {
			t.Fatalf("Task list: %s %v", rr.Body.String(), err)
		}
		found := false
		for _, value := range list.Items {
			if value.ID == b.TaskID {
				found = true
				if !reflect.DeepEqual(value.Work, current.Work) {
					t.Errorf("list returned raw Work: %+v want %+v", value.Work, current.Work)
				}
			}
		}
		if !found {
			t.Fatal("current Task absent from list")
		}
		return bodies
	}
	assertReads("PENDING", "NOT_STARTED", "", true)
	beforeA := assertHistoricalRejectedClients(t, f, gpt, sid, a, "TERMINATED", "ALREADY_TERMINAL")
	own := cancelHTTP(t, web.Handler(), f.projectID, b.ID, "", "read-current")
	if own.OwnerID != b.ID || own.ExecutionID != b.ID || own.TaskID != b.TaskID || own.RunID != "" || own.State != "CANCELED" || own.BackendDisposition != "NO_BACKEND" || own.BackendInterruptState != "NOT_SENT" || own.RequestID == older.RequestID {
		t.Fatal("incorrect canonical receipt", own)
	}
	first := assertReads("CANCELED", "NO_BACKEND", "CANCELED", false)
	replay := cancelHTTP(t, web.Handler(), f.projectID, b.ID, "", "read-current")
	if !reflect.DeepEqual(own, replay) || !reflect.DeepEqual(first, assertReads("CANCELED", "NO_BACKEND", "CANCELED", false)) {
		t.Fatal("response-loss replay changed receipt or HTTP detail")
	}
	afterA := assertHistoricalRejectedClients(t, f, gpt, sid, a, "TERMINATED", "ALREADY_TERMINAL")
	if !reflect.DeepEqual(beforeA, afterA) {
		t.Fatal("successor cancellation changed historical projection")
	}
	oldReceipts, err := f.control.ListExecutionCancellationsForSession(f.ctx, sid, a.ID)
	if err != nil || len(oldReceipts) != 2 || !reflect.DeepEqual(oldReceipts[1], older) {
		t.Fatalf("historical receipt changed: %+v %v", oldReceipts, err)
	}
	foreign, err := f.plane.Projects.Create(f.ctx, "HTTP other project")
	if err != nil {
		t.Fatal(err)
	}
	foreignSID := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": foreign.ID})["aiSessionId"].(string)
	// Preserve the existing route error contracts: the raw AIContext error maps
	// to INTERNAL_ERROR, while Console maps a foreign Task to NOT_FOUND.
	for _, target := range []struct {
		scope string
		code  int
	}{{"ai_session_id=" + foreignSID, http.StatusInternalServerError}, {"project_id=" + foreign.ID, http.StatusNotFound}} {
		if rr := readHTTP("/v1/tasks/" + b.TaskID + "?" + target.scope); rr.Code != target.code {
			t.Fatalf("foreign Task read %s: %d %s", target.scope, rr.Code, rr.Body.String())
		}
	}
}
