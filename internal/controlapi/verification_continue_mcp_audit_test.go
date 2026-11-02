package controlapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The real GPT HTTP registration intentionally returns a direct start output,
// not a business envelope. Runtime execution uses only the explicitly trusted
// TEST_ONLY protocol fixture; this is not a native/model qualification.
func TestVerificationContinueExactHistoricalTargetThroughGPTMCP(t *testing.T) {
	for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_ASK_USER", "DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION"} {
		t.Setenv(key, "")
	}
	t.Setenv("DEVBRIDGE_TEST_UNIQUE_TURNS", "1")
	runCodingDeliveryFixtureFrom(t, newUpperLifecycleFixtureScoped(t, "data-instance-local"), func(f *upperLifecycleFixture, original string) bool {
		current := awaitAcceptanceRecoveryFixture(t, f, original)
		prepare := func(current task.Task, execution, request string) string {
			t.Helper()
			p, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "prepare-" + request, Purpose: c.Execution, Action: "REQUEST_REVERIFICATION", Execution: &c.ExecutionBody{Action: "REQUEST_REVERIFICATION", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: execution, DecisionID: wp.TaskAcceptanceDecisionID(execution)}, Variants: []c.Variant{{ID: "reverify", Summary: "Explicit fixture user requests exact reverification"}}})
			if err != nil {
				t.Fatal(err)
			}
			out, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "commit-" + request, ContentHash: p.ContentHash, VariantID: "reverify"})
			if err != nil || out.State != "COMPLETED" {
				t.Fatalf("reverification admission: %+v %v", out, err)
			}
			next, err := f.facade.GetTask(f.ctx, f.task)
			if err != nil || next.CurrentExecutionID == execution || next.Phase != task.PhaseVerificationReady {
				t.Fatalf("not exact NO_START successor: %+v %v", next, err)
			}
			return next.CurrentExecutionID
		}
		second := prepare(current, original, "first-reverification")
		target, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
		if err != nil || target.ExecutionID != second {
			t.Fatalf("current target: %+v %v", target, err)
		}
		client, closeClient := pendingCancelHTTPClient(t, f, true)
		call := func(args map[string]any) (*sdkmcp.CallToolResult, mcpserver.SurfaceExecutionStartOutput, map[string]any) {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "verification_continue", Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var out mcpserver.SurfaceExecutionStartOutput
			var wire map[string]any
			if err = json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			return result, out, wire
		}
		args := map[string]any{"aiSessionId": f.session, "taskId": f.task, "expectedTarget": target}
		result, started, wire := call(args)
		if result.IsError || !started.ReservationCommitted || started.Proposal.ID != second || started.Run.ID == "" || wire["result"] != nil || wire["workflow"] != nil {
			t.Fatalf("verification direct contract: %+v %+v", result, wire)
		}
		verificationRun := started.Run.ID
		current = awaitAcceptanceRecoveryFixture(t, f, second)
		third := prepare(current, second, "second-reverification")
		tables := []string{"tasks", "execution_proposals", "runs", "task_definition_revisions", "decision_resolution_receipts", "runtime_selection_sources", "runtime_selection_bindings", "runtime_scope_plans", "runtime_workset_bindings", "runtime_start_authorities"}
		for _, cold := range []bool{false, true} {
			if cold {
				closeClient()
				reopenDirectDeliveryFixture(t, f)
				f.plane.Retirement.Close()
				client, closeClient = pendingCancelHTTPClient(t, f, true)
			}
			before := definitionFormalSnapshot(t, f, tables)
			result, replayed, wire := call(args)
			if result.IsError {
				content, _ := json.Marshal(result.Content)
				if !strings.Contains(string(content), "CONFLICT") {
					t.Fatalf("historical target failed for unrelated reason: %s", content)
				}
			}
			if wire["workflow"] != nil || wire["context"] != nil {
				t.Errorf("direct verification unexpectedly borrowed envelope: %+v", wire)
			}
			if !result.IsError && (replayed.Proposal.ID != second || replayed.Run.ID != verificationRun) {
				t.Errorf("cold=%v original start substituted successor: %+v", cold, wire)
			}
			if replayed.Proposal.ID == third {
				t.Errorf("cold=%v exact old target started third execution", cold)
			}
			t.Logf("cold=%v old-target isError=%v originalExecution=%s returnedExecution=%s originalRun=%s returnedRun=%s", cold, result.IsError, second, replayed.Proposal.ID, verificationRun, replayed.Run.ID)
			changed := map[string]any{"aiSessionId": f.session, "taskId": f.task, "expectedTarget": target, "runtimeConfig": map[string]any{"model": "forbidden-new-model"}}
			denied, _, _ := call(changed)
			if !denied.IsError {
				t.Error("continuation accepted new configuration")
			}
			assertDefinitionFormalSnapshot(t, f, tables, before, "historical verification continuation and configuration refusal")
			after, err := f.facade.GetTask(f.ctx, f.task)
			if err != nil || after.CurrentExecutionID != third {
				t.Fatalf("current successor changed: %+v %v", after, err)
			}
		}
		// A genuinely reserved Run can fail its subsequent handoff. Stop the real
		// supervisor before the exact C command, without replacing any service or
		// business rows. No backend is allowed to execute during this fault case.
		thirdTarget, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
		if err != nil || thirdTarget.ExecutionID != third {
			t.Fatalf("third target: %+v %v", thirdTarget, err)
		}
		if err = f.plane.Supervisor.Shutdown(); err != nil {
			t.Fatal(err)
		}
		failed, committed, direct := call(map[string]any{"aiSessionId": f.session, "taskId": f.task, "expectedTarget": thirdTarget})
		if !failed.IsError || !committed.ReservationCommitted || !committed.RefreshRequired || !strings.Contains(committed.DispatchError, "SUPERVISOR_SHUTTING_DOWN") || committed.Handoff.Code != "SUPERVISOR_SHUTTING_DOWN" || committed.Proposal.ID != third || committed.Run.ID == "" || direct["result"] != nil || direct["workflow"] != nil {
			t.Fatalf("committed handoff failure lost direct receipt: %+v %+v", failed, direct)
		}
		var reservedCount, startedCount int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*),count(*) FILTER(WHERE turn_id IS NOT NULL AND turn_id<>'') FROM runs WHERE execution_proposal_id=?`, third).Scan(&reservedCount, &startedCount); err != nil || reservedCount != 1 || startedCount != 0 {
			t.Fatalf("shutdown handoff unexpectedly executed: runs=%d turns=%d %v", reservedCount, startedCount, err)
		}
		t.Logf("committed-error direct output retained execution=%s run=%s reservationCommitted=%v refreshRequired=%v dispatchError=%s", committed.Proposal.ID, committed.Run.ID, committed.ReservationCommitted, committed.RefreshRequired, committed.DispatchError)
		return false
	}, nil)
}
