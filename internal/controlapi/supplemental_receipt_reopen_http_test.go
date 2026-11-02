package controlapi_test

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	sqlitedriver "modernc.org/sqlite"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Real Core Close/Open, not a killed OS process; TEST_ONLY runtime and real MCP.
func TestSupplementalCommittedRequestReceiptLossReopenRealHTTP(t *testing.T) {
	observer := fmt.Sprintf("supplemental_receipt_observe_%d", fileHandoffObserverID.Add(1))
	var hits atomic.Uint64
	var live atomic.Bool
	live.Store(true)
	t.Cleanup(func() { live.Store(false) })
	const token = "TEST_ONLY_SUPPLEMENTAL_STEP_RECEIPT_LOSS"
	if err := sqlitedriver.RegisterScalarFunction(observer, 1, func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
		if live.Load() && len(args) == 1 && args[0] == token {
			hits.Add(1)
		}
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	runCodingDeliveryFixtureFrom(t, f, func(f *upperLifecycleFixture, executionID string) bool {
		// Local bounded phase and pipeline join, allowing race instrumentation.
		f.plane.Retirement.Close()
		deadline := time.Now().Add(2 * time.Minute)
		var current task.Task
		for {
			submitCompletedFixtureVerificationAnalysis(t, f, executionID)
			var readErr error
			current, readErr = f.facade.GetTask(f.ctx, f.task)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if current.Phase == task.PhaseAcceptancePending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("real terminal execution did not reach acceptance %+v", current)
			}
			time.Sleep(25 * time.Millisecond)
		}
		terminal, readErr := f.facade.GetExecutionProposal(f.ctx, f.session, executionID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, id := range []string{terminal.ImplementationRunID, terminal.VerificationRunID} {
			if runtime, ok := f.plane.Supervisor.GetSession(id); ok {
				select {
				case <-runtime.Done():
				case <-time.After(time.Until(deadline)):
					t.Fatal("real pipeline join timed out")
				}
			}
		}
		var err error
		f.facade, err = controlapi.NewFromControlPlane(f.plane)
		if err != nil {
			t.Fatal(err)
		}
		gctx, gpt := compositionGPTClient(t, f)
		decode := func(v any, out any) {
			t.Helper()
			raw, e := json.Marshal(v)
			if e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(raw, out); e != nil {
				t.Fatal(e)
			}
		}
		gptCall := func(name string, in any) map[string]any {
			t.Helper()
			return callUpperMCP(t, gctx, gpt, name, mcpObject(t, in))
		}
		intent := func(v map[string]any) c.Intent {
			t.Helper()
			var out c.Result
			decode(v, &out)
			if out.Intent == nil {
				t.Fatalf("missing intent %+v", v)
			}
			return *out.Intent
		}
		// Copy the capability's observed five-field target from the real GPT projection.
		projection := gptCall("workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
		var observed *wp.TaskReviewRequestTarget
		var find func(any)
		find = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				if x["action"] == "REQUEST_TASK_REVIEW" && x["available"] == true {
					if target, ok := x["target"].(map[string]any); ok && target["expectedReviewRequest"] != nil {
						var exact wp.TaskReviewRequestTarget
						decode(target["expectedReviewRequest"], &exact)
						observed = &exact
					}
				}
				for _, child := range x {
					find(child)
				}
			case []any:
				for _, child := range x {
					find(child)
				}
			}
		}
		find(projection)
		if observed == nil {
			t.Fatalf("missing available supplemental capability: %+v", projection)
		}
		supplemental := c.PrepareInput{AISessionID: f.session, RequestID: "successor-supplemental-prepare", Purpose: c.Review, Action: "REQUEST_SUPPLEMENTAL_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: workset.CloneTargets(f.targets), ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}, SupplementalPurpose: "Review preserved partial worker evidence after explicit authority cancellation; author a separately approved completion plan", ExpectedTarget: observed}, Variants: []c.Variant{{ID: "upper", Label: "Review partial evidence", Summary: "Review existing partial files without resetting", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt in both existing members. Preserve and assess the partial worker evidence. Propose a separately approved Direct File completion; do not modify files."}}}
		supplementalArgs := mcpObject(t, supplemental)
		// The public schema requires explicit empty scalar repositoryId for v2.
		supplementalArgs["review"].(map[string]any)["repositoryId"] = ""
		supplementalArgs["review"].(map[string]any)["reviewPlan"] = map[string]any{"mode": "REPOSITORY"}
		prepared := intent(gptCall("review_prepare", supplementalArgs))

		input := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, ContentHash: prepared.ContentHash, RequestID: "supplemental-receipt-original", VariantID: "upper"}
		read := c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: input.RequestID}
		quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
		ddl := fmt.Sprintf(`CREATE TRIGGER supplemental_step_receipt_loss BEFORE UPDATE OF intent_json ON confirmation_intents WHEN NEW.intent_id=%s AND json_extract(NEW.intent_json,'$.steps[0].kind')='REQUEST_REVIEW' AND json_extract(NEW.intent_json,'$.steps[0].state')='COMPLETED' AND json_extract(NEW.intent_json,'$.steps[1].kind')='START_REVIEW' AND json_extract(NEW.intent_json,'$.steps[1].state')='PENDING' BEGIN SELECT %s('%s'); SELECT RAISE(ABORT,'%s'); END`, quote(prepared.IntentID), observer, token, token)
		if _, err = f.plane.DB.SQL().Exec(ddl); err != nil {
			t.Fatal(err)
		}
		beforeRuns := fileClientCount(t, f, "runs")
		failed, callErr := gpt.CallTool(gctx, &sdkmcp.CallToolParams{Name: "confirmation_commit", Arguments: mcpObject(t, input)})
		if callErr != nil || failed == nil || hits.Load() != 1 {
			t.Fatalf("fault not observed reply=%+v transport=%v hits=%d", failed, callErr, hits.Load())
		}
		var raw, oldEpoch string
		if err = f.plane.DB.SQL().QueryRow(`SELECT intent_json,worker_epoch FROM confirmation_intents WHERE intent_id=?`, prepared.IntentID).Scan(&raw, &oldEpoch); err != nil {
			t.Fatal(err)
		}
		var durable c.Intent
		if err = json.Unmarshal([]byte(raw), &durable); err != nil {
			t.Fatal(err)
		}
		if durable.State != "CONFIRMING" || len(durable.Steps) != 2 || durable.Steps[0].State != "UNKNOWN" || durable.Steps[1].State != "PENDING" {
			t.Fatalf("wrong durable window %+v", durable)
		}
		var n int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_supplemental_review_requests WHERE task_id=? AND request_id=? AND source_execution_id=? AND source_decision_id=?`, f.task, durable.Steps[0].RequestID, executionID, wp.TaskAcceptanceDecisionID(executionID)).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing committed original request %d %v", n, err)
		}
		if fileClientCount(t, f, "runs") != beforeRuns {
			t.Fatal("START_REVIEW ran before receipt")
		}
		owner, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil || owner.Phase != task.PhaseReviewRequired || owner.CurrentExecutionID != "" || owner.CurrentDefinitionRevisionID != current.CurrentDefinitionRevisionID {
			t.Fatalf("request not committed %+v %v", owner, err)
		}
		if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER supplemental_step_receipt_loss`); err != nil {
			t.Fatal(err)
		}
		facts := []string{"task_supplemental_review_requests", "task_supplemental_review_current", "task_review_superseded_executions", "decision_resolution_receipts", "execution_proposals"}
		stable := definitionFormalSnapshot(t, f, facts)
		check := func(value c.Intent, wantResume bool) {
			t.Helper()
			if value.State != "CONFIRMING" || value.CanResume != wantResume || len(value.Steps) != 2 || value.Steps[0].State != "COMPLETED" || value.Steps[0].TaskID != f.task || value.Steps[0].RequestID != durable.Steps[0].RequestID || value.Steps[1].State != "PENDING" || value.Steps[1].RequestID != durable.Steps[1].RequestID || value.Steps[1].RunID != "" || value.Steps[1].ReviewID != "" {
				t.Fatalf("wrong recovered projection wantResume=%v %+v", wantResume, value)
			}
		}
		sameEpochTables := definitionFormalEveryTable(t, f)
		sameEpochBefore := definitionFormalSnapshot(t, f, sameEpochTables)
		sameEpoch := intent(gptCall("confirmation_get", read))
		check(sameEpoch, false)
		if sameEpoch.Submission == nil || sameEpoch.Submission.NextAction == c.ContinueOriginalSteps {
			t.Fatal("same epoch guidance steals live worker")
		}

		check(intent(gptCall("confirmation_commit", input)), false)
		var same string
		if err = f.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, prepared.IntentID).Scan(&same); err != nil || same != raw {
			t.Fatalf("same epoch stole worker/wrote receipt %v", err)
		}
		if fileClientCount(t, f, "runs") != beforeRuns || !reflect.DeepEqual(stable, definitionFormalSnapshot(t, f, facts)) {
			t.Fatal("same epoch replay redispatched")
		}
		assertDefinitionFormalSnapshot(t, f, sameEpochTables, sameEpochBefore, "same epoch read and original replay cannot claim pending work")
		if err = gpt.Close(); err != nil {
			t.Fatal(err)
		}
		reopenDirectDeliveryFixture(t, f)
		f.plane.Retirement.Close()
		var newEpoch string
		if err = f.plane.DB.SQL().QueryRow(`SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&newEpoch); err != nil || newEpoch == oldEpoch || newEpoch == "" {
			t.Fatalf("epoch did not change %q %q %v", oldEpoch, newEpoch, err)
		}
		gctx, gpt = compositionGPTClient(t, f)
		all := definitionFormalEveryTable(t, f)
		beforeRead := definitionFormalSnapshot(t, f, all)
		noRequest := read
		noRequest.RequestID = ""
		withoutRequest := intent(gptCall("confirmation_get", noRequest))
		check(withoutRequest, true)
		if withoutRequest.Submission == nil || withoutRequest.Submission.NextAction != c.ContinueOriginalSteps {
			t.Fatalf("no-request read missing resume %+v", withoutRequest.Submission)
		}
		recovered := intent(gptCall("confirmation_get", read))
		check(recovered, true)
		if recovered.Submission == nil || recovered.Submission.NextAction != c.ContinueOriginalSteps {
			t.Fatalf("exact-request read loses resume guidance; no-request=%+v exact=%+v", withoutRequest.Submission, recovered.Submission)
		}
		assertDefinitionFormalSnapshot(t, f, all, beforeRead, "cold read only recovers projection")
		completed := intent(gptCall("confirmation_commit", input))
		if completed.State != "COMPLETED" || completed.CanResume || len(completed.Steps) != 2 || completed.Steps[0].RequestID != durable.Steps[0].RequestID || completed.Steps[1].RequestID != durable.Steps[1].RequestID || completed.Steps[1].State != "COMPLETED" || completed.Steps[1].ReviewID == "" || completed.Steps[1].RunID == "" {
			t.Fatalf("original resume failed %+v", completed)
		}
		if fileClientCount(t, f, "runs") != beforeRuns+1 || !reflect.DeepEqual(stable, definitionFormalSnapshot(t, f, facts)) || hits.Load() != 1 {
			t.Fatal("resume duplicated old business or execution")
		}
		ctx, local, _ := workerHTTPClient(t, f)
		value := callUpperMCP(t, ctx, local, "review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": completed.Steps[1].ReviewID})
		var access review.UpperAccess
		decode(value["result"], &access)
		if access.State != "ACTIVE" || access.Target.RunID != completed.Steps[1].RunID || access.Target.RuntimeEpoch != newEpoch {
			t.Fatalf("resumed Review not active in new epoch %+v", access)
		}
		finalTables := definitionFormalEveryTable(t, f)
		finalBefore := definitionFormalSnapshot(t, f, finalTables)
		replay := intent(gptCall("confirmation_commit", input))
		if replay.State != "COMPLETED" || !reflect.DeepEqual(replay.Steps, completed.Steps) {
			t.Fatalf("exact completed replay changed result %+v", replay)
		}
		assertDefinitionFormalSnapshot(t, f, finalTables, finalBefore, "completed original replay has no effects")
		t.Logf("exact committed first request recovered; same epoch fenced; %s -> %s permits unique pending Review %s", oldEpoch, newEpoch, completed.Steps[1].ReviewID)
		return false
	}, nil)
}
