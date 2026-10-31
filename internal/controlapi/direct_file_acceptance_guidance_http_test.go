package controlapi_test

// Regression for the assistant-authored local MCP run: a genuine v2 file-only
// completion has a formal result, not an invented Execution acceptance decision.
// Setup confirmations below are explicitly simulated fixture-user decisions.
import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func assertNoExecutionAcceptanceProjection(t *testing.T, value any) {
	t.Helper()
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "decisionId" {
				if id, ok := child.(string); ok && strings.HasPrefix(id, "TASK_ACCEPTANCE:") {
					t.Fatalf("file-only projection invented Execution acceptance: %s", id)
				}
			}
			// Workspace history legitimately contains virtual review:<id> run owners.
			// They are not a current Execution or an acceptance target.
			if key == "currentExecutionId" && child != nil && child != "" {
				t.Fatalf("file-only projection invented Execution: %v", child)
			}
			if key == "code" && child == "RESOLVE_TASK_ACCEPTANCE" {
				t.Fatal("file-only projection directs nonexistent Execution acceptance")
			}
			assertNoExecutionAcceptanceProjection(t, child)
		}
	case []any:
		for _, child := range v {
			assertNoExecutionAcceptanceProjection(t, child)
		}
	}
}

func assertExactFileResultGuidance(t *testing.T, envelope map[string]any, taskID, revisionID, workflowID string) {
	t.Helper()
	assertNoExecutionAcceptanceProjection(t, envelope)
	if context := envelope["context"].(map[string]any); context["executionId"] != nil && context["executionId"] != "" {
		t.Fatalf("file-only envelope acquired an Execution target: %+v", context)
	}
	workflow := envelope["workflow"].(map[string]any)
	action := workflow["nextAction"].(map[string]any)
	if action["code"] != "READ_DIRECT_FILE_RESULT" || action["required"] != true || workflow["refreshRequired"] != false || workflow["generation"] == "" {
		t.Fatalf("missing truthful file result guidance: %+v", workflow)
	}
	message, _ := action["message"].(string)
	for _, exact := range []string{"file_implementation_result_get", taskID, revisionID, workflowID, "NONE", "no current Execution is recorded", "separate explicit user decision"} {
		if !strings.Contains(message, exact) {
			t.Fatalf("guidance omitted exact result scope/acceptance boundary %q: %s", exact, message)
		}
	}
	if pending, ok := workflow["pendingDecisions"].([]any); ok && len(pending) != 0 {
		t.Fatalf("read guidance acquired an acceptance decision: %+v", pending)
	}
}

func TestDirectFileNoneAcceptanceGuidanceThroughBothMCPAdapters(t *testing.T) {
	for _, adapter := range []string{"GPT", "LOCAL"} {
		t.Run(adapter, func(t *testing.T) {
			f, current, state, claim := fileWorkerHTTPFixture(t)
			beforeTrees := upperClientTrees(t, f)
			var ctx context.Context
			var session *sdkmcp.ClientSession
			if adapter == "GPT" {
				ctx, session = compositionGPTClient(t, f)
			} else {
				ctx, session, _ = workerHTTPClient(t, f)
			}
			listed, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			tools := map[string]*sdkmcp.Tool{}
			for _, tool := range listed.Tools {
				tools[tool.Name] = tool
			}
			historicalArgs := map[string]any{"aiSessionId": f.session, "reviewId": f.review}
			historicalBefore := callUpperMCP(t, ctx, session, "review_result_get", historicalArgs)["result"]
			callUpperMCP(t, ctx, session, "file_worker_claim", mcpObject(t, claim))
			for _, target := range current.RepositoryTargets {
				args := mcpObject(t, directInput(f, state, target))
				before := callUpperMCP(t, ctx, session, "file_read", args)["result"].(map[string]any)
				args["action"], args["protocolVersion"] = "REPLACE", 2
				args["requestId"], args["expectedDigest"] = "guidance-write-"+target.RepositoryID, before["digest"]
				args["content"] = "guidance regression change " + target.RepositoryID + "\n"
				args["workerClaim"] = mcpObject(t, claim.WorkerClaimIdentity)
				callUpperMCP(t, ctx, session, "file_write", args)
			}
			input := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "guidance-result", Summary: "Two actual scoped file writes", VerificationNotes: "Explicit fixture-user NONE waiver; no Core verification or acceptance claimed", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration, WorkerClaim: &claim.WorkerClaimIdentity}
			submitted := callUpperMCP(t, ctx, session, "file_implementation_result_submit", mcpObject(t, input))
			validateFileWorkerHTTPOutput(t, tools["file_implementation_result_submit"], submitted)
			assertExactFileResultGuidance(t, submitted, f.task, current.CurrentDefinitionRevisionID, state.WorkflowID)
			result := submitted["result"].(map[string]any)
			resultID, _ := result["id"].(string)
			if resultID == "" || result["workflowId"] != state.WorkflowID || result["taskDefinitionRevisionId"] != current.CurrentDefinitionRevisionID || result["executionId"] != nil || result["verificationRunId"] != nil || result["accepted"] != false || result["verificationStatus"] != "NOT_RUN" || len(result["members"].([]any)) != 2 {
				t.Fatalf("formal result identity/status changed: %+v", result)
			}
			tables := []string{"tasks", "task_definition_revisions", "review_reports", "runs", "execution_proposals", "direct_file_task_authorizations", "direct_file_worker_claims", "direct_file_operation_receipts", "direct_file_implementation_results", "direct_file_implementation_acceptances", "task_commit_receipts", "confirmation_commit_requests", "task_action_heads"}
			ledger := definitionFormalSnapshot(t, f, tables)
			read := callUpperMCP(t, ctx, session, "file_implementation_result_get", mcpObject(t, directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID}))
			validateFileWorkerHTTPOutput(t, tools["file_implementation_result_get"], read)
			assertExactFileResultGuidance(t, read, f.task, current.CurrentDefinitionRevisionID, state.WorkflowID)
			if !reflect.DeepEqual(read["result"], result) {
				t.Fatal("projection altered formal result")
			}
			workspace := callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
			assertExactFileResultGuidance(t, workspace, f.task, current.CurrentDefinitionRevisionID, state.WorkflowID)
			body := workspace["result"].(map[string]any)
			view := body["taskView"].(map[string]any)
			if view["currentAction"].(map[string]any)["code"] != "READ_DIRECT_FILE_RESULT" || len(view["executionHistory"].([]any)) != 0 || body["featureAreaProduct"].(map[string]any)["requiredAction"].(map[string]any)["code"] != "READ_DIRECT_FILE_RESULT" {
				t.Fatalf("workspace/product projection disagrees: %+v", view)
			}
			historical := callUpperMCP(t, ctx, session, "review_result_get", historicalArgs)
			assertExactFileResultGuidance(t, historical, f.task, current.CurrentDefinitionRevisionID, state.WorkflowID)
			if !reflect.DeepEqual(historical["result"], historicalBefore) {
				t.Fatal("projection rewrote historical review report")
			}
			if definitionFormalSnapshot(t, f, tables) != ledger {
				t.Fatal("read guidance changed durable workflow facts")
			}
			compositionEffects(t, f, 1, 1, 2, 1)
			closed, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
			if err != nil || closed.State != "CLOSED" || closed.Enabled {
				t.Fatalf("projection reopened file authority: %+v %v", closed, err)
			}
			owner, err := f.facade.GetTask(f.ctx, f.task)
			if err != nil || owner.Status != task.StatusActive || owner.Phase != task.PhaseAcceptancePending {
				t.Fatalf("projection accepted Task: %+v %v", owner, err)
			}
			for key, now := range upperClientTrees(t, f) {
				old := beforeTrees[key]
				if (strings.HasPrefix(key, "source:") && now != old) || now.HEAD != old.HEAD {
					t.Fatalf("projection delivered source or committed %s", key)
				}
			}
		})
	}
}
