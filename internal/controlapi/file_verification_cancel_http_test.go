package controlapi_test

// Real Core, GPT MCP and TEST_ONLY subprocess protocol on both sides. FILE work
// is already durably recorded before its separate verification Run is canceled.
import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/fileworker"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFileVerificationPublicCancellationRealHTTP(t *testing.T) {
	for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_DROP_RESOLVED", "DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "DEVBRIDGE_CODING_V1_TURN_SUFFIX", "DEVBRIDGE_TEST_UNIQUE_TURNS", "DEVBRIDGE_CODING_V1_WITH_APPROVAL", "DEVBRIDGE_CODING_V1_MULTI_QUESTION"} {
		t.Setenv(key, "")
	}
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-verification")
	f, current, _, request := fileClientFixture(t, task.VerificationModeRun)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	session, closeHTTP := pendingCancelHTTPClient(t, f, true)
	defer closeHTTP()
	wire := fileClientTransport(t, session, request, current)
	callbacks := 0
	client, e := fileworker.New(wire, request, func(c context.Context, d fileworker.Delivery, files fileworker.Files) (fileworker.Completion, error) {
		callbacks++
		fileClientReplaceBoth(t, f, current, c, d, files)
		return fileworker.Completion{Summary: "Recorded file work; cancel only its separately running verification"}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	out, e := client.Run(ctx)
	if e != nil || out.State != fileworker.ResultRecorded || out.ResultReceipt == nil || out.ResultReceipt.ExecutionID == "" || out.ResultReceipt.VerificationRunID == "" || out.ResultReceipt.VerificationStartError != "" {
		t.Fatalf("formal file result/handoff failed: %+v %v", out, e)
	}
	original := *out.ResultReceipt
	question, live := waitUserRequest(t, f, f.task, "turn-verification")
	if live.ID != original.VerificationRunID || live.ExecutionProposalID != original.ExecutionID || live.Purpose != run.PurposeVerification || live.Status != run.StatusRunning || question.RunID != live.ID {
		t.Fatalf("not exact live verification: %+v question=%+v", live, question)
	}
	pipeline, ok := f.plane.Supervisor.GetSession(live.ID)
	if !ok {
		t.Fatal("live verification lacks owned supervisor pipeline")
	}
	tables := []string{"direct_file_task_authorizations", "direct_file_task_members", "direct_file_worker_claims", "direct_file_operation_receipts", "direct_file_implementation_results", "direct_file_verifications", "direct_file_verification_selections", "task_definition_revisions", "confirmation_intents"}
	frozen := definitionFormalSnapshot(t, f, tables)
	trees := upperClientTrees(t, f)
	// The command target MUST come from the public projection, not private SQL.
	workspace := callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
	raw, e := json.Marshal(workspace["result"])
	if e != nil {
		t.Fatal(e)
	}
	var view struct {
		TaskView struct {
			Actions []p.Capability `json:"actions"`
		} `json:"taskView"`
	}
	if e = json.Unmarshal(raw, &view); e != nil {
		t.Fatal(e)
	}
	var observed *p.ActionTarget
	for _, cap := range view.TaskView.Actions {
		if cap.Action == "CANCEL_EXECUTION" && cap.Available {
			if observed != nil {
				t.Fatalf("ambiguous public cancel targets: %s", raw)
			}
			observed = cap.Target
		}
	}
	if observed == nil || observed.OperationKind != "EXECUTION" || observed.OperationID != original.ExecutionID || observed.RunID != live.ID || observed.FeatureAreaID != f.area || observed.TaskID != f.task {
		t.Fatalf("public FILE verification cancel target absent/wrong: %s", raw)
	}
	t.Logf("public exact cancellation target execution=%s verificationRun=%s; real pending question=%s", observed.OperationID, observed.RunID, question.RequestID)
	command := map[string]any{"aiSessionId": f.session, "ownerType": observed.OperationKind, "ownerId": observed.OperationID, "runId": observed.RunID, "requestId": "cancel-recorded-file-verification", "reason": "Explicit simulated user cancels the exact separate verification, not completed FILE work"}
	reply, e := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "operation_cancel", Arguments: command})
	if e != nil || reply == nil || reply.IsError {
		t.Fatalf("public cancellation failed: %+v %v", reply, e)
	}
	raw, e = json.Marshal(reply.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var response struct {
		Result executioncancel.Receipt `json:"result"`
	}
	if e = json.Unmarshal(raw, &response); e != nil || response.Result.OwnerID != observed.OperationID || response.Result.RunID != observed.RunID || response.Result.BackendDisposition == "NO_BACKEND" || (response.Result.State != "CANCEL_REQUESTED" && response.Result.State != "CANCELED") {
		t.Fatalf("wrong actual cancellation receipt: %s %v", raw, e)
	}
	// Receipt/interrupt ACK is not proof of termination. Join actual finalization.
	select {
	case <-pipeline.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("verification pipeline did not terminate after cancellation")
	}
	var settled executioncancel.Receipt
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		receipts, err := f.facade.ListExecutionCancellationsForSession(f.ctx, f.session, original.ExecutionID)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range receipts {
			if r.RequestID == command["requestId"] && r.State == "CANCELED" {
				settled = r
			}
		}
		if settled.State == "CANCELED" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if settled.State != "CANCELED" || settled.RunID != live.ID || settled.OwnerID != original.ExecutionID || settled.BackendDisposition == "NO_BACKEND" {
		t.Fatalf("cancellation did not really settle %+v", settled)
	}
	var terminalObserved bool
	if e = f.plane.DB.SQL().QueryRow(`SELECT terminal_observed FROM execution_cancellations WHERE owner_id=? AND request_id=?`, original.ExecutionID, command["requestId"]).Scan(&terminalObserved); e != nil || !terminalObserved {
		t.Fatalf("missing actual terminal observation: %t %v", terminalObserved, e)
	}
	ended, e := f.facade.GetRunForSession(f.ctx, f.session, live.ID)
	if e != nil || ended.Status != run.StatusCanceled || ended.Purpose != run.PurposeVerification {
		t.Fatalf("Run terminal mismatch %+v %v", ended, e)
	}
	execution, e := f.facade.GetExecutionProposal(f.ctx, f.session, original.ExecutionID)
	if e != nil || execution.Outcome != "CANCELED" || execution.VerificationRunID != live.ID || execution.ImplementationRunID != "" {
		t.Fatalf("Execution terminal mismatch %+v %v", execution, e)
	}
	observedResult, e := client.Observe(ctx)
	if e != nil || observedResult.State != fileworker.ResultRecorded || observedResult.ResultReceipt == nil || observedResult.ResultReceipt.ID != original.ID || observedResult.ResultReceipt.ExecutionID != original.ExecutionID || observedResult.ResultReceipt.VerificationRunID != live.ID || observedResult.ResultReceipt.VerificationStatus != "CANCELED" || observedResult.ResultReceipt.Accepted || !reflect.DeepEqual(observedResult.ResultReceipt.Members, original.Members) || !reflect.DeepEqual(observedResult.ResultInput, out.ResultInput) {
		t.Fatalf("cancellation lost/rewrote formal file result %+v %v", observedResult, e)
	}
	grant, e := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if e != nil || grant.State != "CLOSED" || grant.Enabled {
		t.Fatalf("completed file authority reopened %+v %v", grant, e)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if e := f.plane.DB.SQL().QueryRow(q, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	if count(`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, original.ExecutionID) != 1 || count(`SELECT count(*) FROM runs WHERE task_id=? AND purpose='IMPLEMENTATION'`, f.task) != 0 || count(`SELECT count(*) FROM execution_cancellations WHERE owner_id=? AND request_id=?`, original.ExecutionID, command["requestId"]) != 1 {
		t.Fatal("extra Run or cancellation receipt")
	}
	if callbacks != 1 || wire.count("file_worker_claim") != 1 || wire.count("file_write") != 2 || wire.count("file_implementation_result_submit") != 1 || frozen != definitionFormalSnapshot(t, f, tables) || !reflect.DeepEqual(trees, upperClientTrees(t, f)) {
		t.Fatal("cancellation repeated/changed original file work or authority")
	}
	wireLog, e := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
	if e != nil {
		t.Fatal(e)
	}
	interrupts := 0
	for _, line := range strings.Split(string(wireLog), "\n") {
		if strings.Contains(line, `"method":"turn/interrupt"`) {
			interrupts++
		}
	}
	if interrupts != 1 {
		t.Fatalf("actual interrupt dispatch count=%d wire=%s", interrupts, wireLog)
	}
	finalWorkspace := callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
	finalRaw, err := json.Marshal(finalWorkspace["result"])
	if err != nil {
		t.Fatal(err)
	}

	var finalView struct {
		TaskView struct {
			Phase        task.Phase                                  `json:"phase"`
			Activity     struct{ Status, OperationID, RunID string } `json:"activity"`
			Actions      []p.Capability                              `json:"actions"`
			Verification struct{ Status string }                     `json:"verification"`
		} `json:"taskView"`
	}
	if e = json.Unmarshal(finalRaw, &finalView); e != nil {
		t.Fatal(e)
	}
	v := finalView.TaskView
	if v.Phase != task.PhaseVerifying || v.Activity.Status != "CANCELED" || v.Activity.OperationID != original.ExecutionID || v.Activity.RunID != live.ID || v.Verification.Status != "CANCELED" {
		t.Fatalf("terminal verification displayed as active/wrong owner: %s", finalRaw)
	}
	reviewAvailable := false
	for _, action := range v.Actions {
		if action.Action == "COMPLETE_TASK" && action.Available {
			t.Fatal("canceled verification falsely permits delivery")
		}
		if action.Action == "REQUEST_TASK_REVIEW" && action.Available {
			a := action.Target
			if a == nil || a.TaskID != f.task || a.FeatureAreaID != f.area || a.ExpectedReviewRequest == nil || a.ExpectedReviewRequest.CurrentExecutionID != original.ExecutionID || a.ExpectedReviewRequest.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID || a.ExpectedReviewRequest.Phase != task.PhaseVerifying || a.ExpectedReviewRequest.PendingDecisionID != "TASK_ACCEPTANCE:"+original.ExecutionID {
				t.Fatalf("supplemental review target lost exact lineage %+v", a)
			}
			reviewAvailable = true
		}
	}
	if !reviewAvailable {
		t.Fatalf("no public supplemental review exit after cancellation: %s", finalRaw)
	}
	t.Logf("post-cancel phase=%s activity=%s exactExecution=%s; REQUEST_TASK_REVIEW available with original frozen lineage, COMPLETE_TASK blocked", v.Phase, v.Activity.Status, v.Activity.OperationID)

	fileClientNoAcceptance(t, f)
	if fileClientCount(t, f, "task_commit_receipts") != 0 {
		t.Fatal("implicit delivery")
	}
	t.Logf("joined actual interrupted verification: Run=%s Execution=%s cancellation=%s; original FILE ResultRecorded+CANCELED, CLOSED grant, unchanged members, no acceptance/delivery", ended.Status, execution.Outcome, settled.State)
}

// A second legitimate verification-only lineage: formally authorized Coding
// reverification. Its missing ImplementationRunID must not remove cancellation.
func TestReverificationOnlyPublicCancellationRealHTTP(t *testing.T) {
	for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_ASK_USER", "DEVBRIDGE_CODING_V1_DROP_RESOLVED", "DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "DEVBRIDGE_CODING_V1_TURN_SUFFIX", "DEVBRIDGE_TEST_UNIQUE_TURNS"} {
		t.Setenv(key, "")
	}
	runCodingDeliveryFixtureFrom(t, newUpperLifecycleFixtureScoped(t, "data-instance-local"), func(f *upperLifecycleFixture, original string) bool {
		current := awaitAcceptanceRecoveryFixture(t, f, original)
		prepared, e := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "prepare-cancellable-reverification", Purpose: c.Execution, Action: "REQUEST_REVERIFICATION", Execution: &c.ExecutionBody{Action: "REQUEST_REVERIFICATION", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: original, DecisionID: wp.TaskAcceptanceDecisionID(original)}, Variants: []c.Variant{{ID: "reverify", Summary: "Explicit simulated user requests exact reverification"}}})
		if e != nil {
			t.Fatal(e)
		}
		committed, e := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "commit-cancellable-reverification", ContentHash: prepared.ContentHash, VariantID: "reverify"})
		if e != nil || committed.State != "COMPLETED" {
			t.Fatalf("formal reverification %+v %v", committed, e)
		}
		t.Setenv("DEVBRIDGE_CODING_V1_TURN_SUFFIX", "-cancel-successor")
		t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-verification-cancel-successor")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		session, closeHTTP := pendingCancelHTTPClient(t, f, true)
		defer closeHTTP()
		workspace := callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
		raw, e := json.Marshal(workspace["result"])
		if e != nil {
			t.Fatal(e)
		}
		var view struct {
			TaskView struct {
				ExecutionStartTarget *wp.TaskExecutionExpectation `json:"executionStartTarget"`
				Actions              []p.Capability               `json:"actions"`
			} `json:"taskView"`
		}
		if e = json.Unmarshal(raw, &view); e != nil || view.TaskView.ExecutionStartTarget == nil {
			t.Fatalf("public successor target %s %v", raw, e)
		}
		target := *view.TaskView.ExecutionStartTarget
		if target.ExecutionID == original || target.ExecutionID == "" || target.Phase != task.PhaseVerificationReady {
			t.Fatalf("wrong NO_START successor %+v", target)
		}
		reply, e := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "verification_continue", Arguments: map[string]any{"aiSessionId": f.session, "taskId": f.task, "expectedTarget": target}})
		if e != nil || reply == nil || reply.IsError {
			t.Fatalf("start exact successor %+v %v", reply, e)
		}
		_, live := waitUserRequest(t, f, f.task, "turn-verification-cancel-successor")
		proposal, e := f.facade.GetExecutionProposal(f.ctx, f.session, target.ExecutionID)
		if e != nil || proposal.ImplementationRunID != "" || proposal.VerificationRunID != live.ID || live.ExecutionProposalID != target.ExecutionID || live.Purpose != run.PurposeVerification {
			t.Fatalf("not verification-only successor %+v %+v %v", proposal, live, e)
		}
		pipeline, ok := f.plane.Supervisor.GetSession(live.ID)
		if !ok {
			t.Fatal("no live successor pipeline")
		}
		workspace = callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
		raw, e = json.Marshal(workspace["result"])
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(raw, &view); e != nil {
			t.Fatal(e)
		}
		var owner *p.ActionTarget
		for _, a := range view.TaskView.Actions {
			if a.Action == "CANCEL_EXECUTION" && a.Available {
				owner = a.Target
			}
		}
		if owner == nil || owner.OperationKind != "EXECUTION" || owner.OperationID != target.ExecutionID || owner.RunID != live.ID {
			t.Fatalf("public verification-only successor cancellation missing %s", raw)
		}
		callUpperMCP(t, ctx, session, "operation_cancel", map[string]any{"aiSessionId": f.session, "ownerType": owner.OperationKind, "ownerId": owner.OperationID, "runId": owner.RunID, "requestId": "cancel-reverification-only", "reason": "Explicit simulated user cancels exact verification-only successor"})
		select {
		case <-pipeline.Done():
		case <-time.After(20 * time.Second):
			t.Fatal("reverification cancellation did not join")
		}
		deadline := time.Now().Add(10 * time.Second)
		var terminal bool
		for time.Now().Before(deadline) {
			if e = f.plane.DB.SQL().QueryRow(`SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE owner_id=? AND request_id='cancel-reverification-only' AND state='CANCELED' AND terminal_observed=1)`, target.ExecutionID).Scan(&terminal); e != nil {
				t.Fatal(e)
			}
			if terminal {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		ended, e := f.facade.GetRunForSession(f.ctx, f.session, live.ID)
		if e != nil || !terminal || ended.Status != run.StatusCanceled {
			t.Fatalf("successor terminal missing %+v %v", ended, e)
		}
		proposal, e = f.facade.GetExecutionProposal(f.ctx, f.session, target.ExecutionID)
		if e != nil || proposal.Outcome != "CANCELED" || proposal.ImplementationRunID != "" || proposal.VerificationRunID != live.ID {
			t.Fatalf("wrong successor outcome %+v %v", proposal, e)
		}
		var n int
		if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, target.ExecutionID).Scan(&n); e != nil || n != 1 {
			t.Fatalf("extra successor Run n=%d %v", n, e)
		}
		t.Logf("formally confirmed reverification-only successor %s had zero ImplementationRunID; exact public cancellation joined %s", target.ExecutionID, live.ID)
		return false
	}, nil)
}
