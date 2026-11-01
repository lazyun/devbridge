package controlapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Faults affect only this disposable fixture's exact cancellation. No trigger
// repairs business state: recovery must use the real canonical receipt, if any.
func TestFileWorkerCancellationCanonicalReceiptRecoveryHTTP(t *testing.T) {
	for _, receiptExists := range []bool{true, false} {
		name := "missing_business_receipt"
		if receiptExists {
			name = "lost_confirmation_completion"
		}
		t.Run(name, func(t *testing.T) {
			f, current, state, claim := fileWorkerHTTPFixture(t)
			local, closeLocal := pendingCancelHTTPClient(t, f, false)
			gpt, closeGPT := pendingCancelHTTPClient(t, f, true)
			evidence := os.Getenv("DEVBRIDGE_CANCEL_RECEIPT_EVIDENCE_DIR")
			if evidence != "" {
				if e := os.MkdirAll(evidence, 0700); e != nil {
					t.Fatal(e)
				}
				var e error
				evidence, e = os.MkdirTemp(evidence, name+"-")
				if e != nil {
					t.Fatal(e)
				}
				t.Log("evidence:", evidence)
			}
			save := func(name string, v any) {
				t.Helper()
				if evidence == "" {
					return
				}
				b, e := json.MarshalIndent(v, "", "  ")
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(evidence, name+".json"), b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			seq := 0
			rawCall := func(s *sdkmcp.ClientSession, name string, args any) *sdkmcp.CallToolResult {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				r, e := s.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
				seq++
				save(fmt.Sprintf("%02d-%s", seq, name), map[string]any{"input": args, "output": r, "transportError": fmt.Sprint(e)})
				if e != nil {
					t.Fatalf("%s transport: %v", name, e)
				}
				return r
			}
			call := func(s *sdkmcp.ClientSession, name string, args any) map[string]any {
				t.Helper()
				r := rawCall(s, name, args)
				if r.IsError {
					b, _ := json.Marshal(r)
					t.Fatalf("%s business error: %s", name, b)
				}
				return mcpObject(t, r.StructuredContent)
			}
			decode := func(v any, out any) {
				t.Helper()
				b, e := json.Marshal(v)
				if e != nil {
					t.Fatal(e)
				}
				if e = json.Unmarshal(b, out); e != nil {
					t.Fatal(e)
				}
			}
			intent := func(v any) c.Intent {
				t.Helper()
				var r c.Result
				decode(v, &r)
				if r.Intent == nil {
					t.Fatalf("missing intent: %+v", v)
				}
				return *r.Intent
			}
			claimed := call(local, "file_worker_claim", claim)["result"].(map[string]any)
			args := mcpObject(t, directInput(f, state, current.RepositoryTargets[0]))
			read := call(local, "file_read", args)["result"].(map[string]any)
			args["protocolVersion"], args["action"], args["requestId"], args["expectedDigest"] = 2, "REPLACE", "completed-before-cancel", read["digest"]
			args["workerClaim"], args["content"] = mcpObject(t, claim.WorkerClaimIdentity), "completed worker bytes before cancellation\n"
			written := call(local, "file_write", args)["result"].(map[string]any)
			var e error
			current, e = f.facade.GetTask(f.ctx, f.task)
			if e != nil {
				t.Fatal(e)
			}
			prepare := c.PrepareInput{AISessionID: f.session, RequestID: "prepare-exact-cancel", Purpose: c.Execution, Action: "CANCEL_DIRECT_FILE", Execution: &c.ExecutionBody{Action: "CANCEL_DIRECT_FILE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: state.DecisionID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, Variants: []c.Variant{{ID: "exact", Label: "Explicit isolated cancellation", Summary: "Cancel isolated worker authority", ImplementationMode: current.WorkSpec.ImplementationMode, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}}
			p := intent(call(gpt, "execution_prepare", prepare))
			commit := c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "commit-exact-cancel", ContentHash: p.ContentHash, VariantID: "exact", Selection: c.Selection{}}
			subrequest := "confirmation_" + c.ValueHash([]string{p.IntentID, "CANCEL_DIRECT_FILE"})[:32]
			var root string
			if e = f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, state.WorkflowID, current.RepositoryTargets[0].MemberID).Scan(&root); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(root, "same.txt")
			physical, e := snapshotTCPMutation(path)
			if e != nil {
				t.Fatal(e)
			}
			trigger := `CREATE TRIGGER isolated_cancel_fault BEFORE INSERT ON direct_file_start_receipts WHEN NEW.ai_session_id='` + f.session + `' AND NEW.request_id='` + subrequest + `' AND NEW.action='CANCEL_DIRECT_FILE' BEGIN SELECT RAISE(ABORT,'injected exact cancellation receipt rollback'); END`
			stage := "BUSINESS_STEP"
			if receiptExists {
				stage = "RECEIPT_READBACK"
				trigger = `CREATE TRIGGER isolated_cancel_fault BEFORE UPDATE OF intent_json ON confirmation_intents WHEN NEW.intent_id='` + p.IntentID + `' AND json_extract(NEW.intent_json,'$.state')='COMPLETED' BEGIN SELECT RAISE(ABORT,'injected exact cancellation completion loss'); END`
			}
			if _, e = f.plane.DB.SQL().Exec(trigger); e != nil {
				t.Fatal(e)
			}
			failed := rawCall(gpt, "confirmation_commit", commit)
			failureJSON, _ := json.Marshal(failed)
			save("fault-trigger", map[string]any{"sql": trigger, "expectedPublicStage": stage})
			failure := mcpObject(t, failed.StructuredContent)["failure"].(map[string]any)
			if !failed.IsError || failure["code"] != "CONFIRMATION_FACTS_UNAVAILABLE" || failure["stage"] != stage || strings.Contains(string(failureJSON), "INVALID_ARGUMENT") {
				t.Fatalf("fault was not exact storage business failure: %s", failureJSON)
			}
			durable := func() c.Intent {
				t.Helper()
				var raw string
				if e := f.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, p.IntentID).Scan(&raw); e != nil {
					t.Fatal(e)
				}
				var out c.Intent
				decode(json.RawMessage(raw), &out)
				return out
			}
			d := durable()
			wantState := "UNKNOWN"
			if receiptExists {
				wantState = "CONFIRMING"
			}
			if d.State != wantState || len(d.Steps) != 1 || d.Steps[0].State != "UNKNOWN" || d.Steps[0].RequestID != subrequest {
				t.Fatalf("wrong durable cancellation: %+v", d)
			}
			var grant, mutation string
			var receipts int
			if e = f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_task_authorizations WHERE workflow_id=?`, state.WorkflowID).Scan(&grant); e != nil {
				t.Fatal(e)
			}
			if e = f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, state.WorkflowID, "completed-before-cancel").Scan(&mutation); e != nil {
				t.Fatal(e)
			}
			if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_start_receipts WHERE ai_session_id=? AND request_id=? AND action='CANCEL_DIRECT_FILE' AND workflow_id=? AND json_extract(receipt_json,'$.state')='CLOSED'`, f.session, subrequest, state.WorkflowID).Scan(&receipts); e != nil {
				t.Fatal(e)
			}
			if mutation != "COMPLETED" || (receiptExists && (grant != "CLOSED" || receipts != 1)) || (!receiptExists && (grant == "CLOSED" || receipts != 0)) {
				t.Fatalf("grant=%s mutation=%s receipts=%d", grant, mutation, receipts)
			}
			save("pre-restart-durable-witness", map[string]any{"intent": d, "grant": grant, "mutation": mutation, "cancellationReceipts": receipts, "physicalDigest": fmt.Sprintf("%x", physical.Digest), "physicalMtime": physical.Info.ModTime()})
			if _, e = f.plane.DB.SQL().Exec(`DROP TRIGGER isolated_cancel_fault`); e != nil {
				t.Fatal(e)
			}
			closeLocal()
			closeGPT()
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
			local, closeLocal = pendingCancelHTTPClient(t, f, false)
			gpt, closeGPT = pendingCancelHTTPClient(t, f, true)
			tables := definitionFormalEveryTable(t, f)
			before := definitionFormalSnapshot(t, f, tables)
			coldDurable := durable()
			readInput := c.ReadInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, ContentHash: p.ContentHash, RequestID: commit.RequestID}
			observed := intent(call(gpt, "confirmation_get", readInput))
			wantObserved := "UNKNOWN"
			if receiptExists {
				wantObserved = "COMPLETED"
			}
			if observed.State != wantObserved || observed.CanResume || observed.Submission == nil || observed.Submission.Outcome != c.Consumed || !observed.Submission.Recorded || observed.Submission.Consumed == nil || !*observed.Submission.Consumed || observed.Submission.RedispatchSameIntent {
				t.Fatalf("wrong cold recovery: %+v", observed)
			}
			if receiptExists {
				if len(observed.Steps) != 1 || observed.Steps[0].Kind != "CANCEL_DIRECT_FILE" || observed.Steps[0].RequestID != subrequest || observed.Steps[0].WorkflowID != state.WorkflowID || observed.Steps[0].RevisionID != current.CurrentDefinitionRevisionID || observed.Steps[0].Failure != nil || observed.Content.Execution == nil || observed.Content.Execution.TaskID != f.task {
					t.Fatalf("recovered step lost exact identity: %+v", observed)
				}
			}
			if !receiptExists && (observed.Submission.ReprepareAllowed || string(observed.Submission.NextAction) != "READ_ORIGINAL_RECEIPT") {
				t.Fatalf("missing receipt permits retry: %+v", observed.Submission)
			}
			worker := call(local, "file_worker_get", claim)["result"].(map[string]any)
			if worker["state"] != directfile.WorkerOutcomeUnknown || worker["executionPayload"] != nil || worker["resultReceipt"] != nil || !reflect.DeepEqual(worker["receipt"], claimed["receipt"]) {
				t.Fatalf("old worker regained payload: %+v", worker)
			}
			if !receiptExists {
				projection := call(gpt, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
				view := projection["result"].(map[string]any)["taskView"].(map[string]any)
				node := view["actionNode"].(map[string]any)
				if node["taskId"] != f.task || node["intentId"] != p.IntentID || node["state"] != "UNKNOWN" || node["blocking"] != true {
					t.Fatalf("wrong blocking cancellation owner: %+v", node)
				}
				workflow := projection["workflow"].(map[string]any)
				next := workflow["nextAction"].(map[string]any)
				currentAction := view["currentAction"].(map[string]any)
				if next["code"] != "RESOLVE_CURRENT_TASK_ACTION" || currentAction["code"] != "RESOLVE_CURRENT_TASK_ACTION" {
					t.Fatalf("unknown cancellation suggests a new write: next=%+v current=%+v", next, currentAction)
				}
				blockedCancel := false
				for _, raw := range workflow["capabilities"].([]any) {
					cap := raw.(map[string]any)
					if cap["action"] == "CANCEL_EXECUTION" {
						blockedCancel = true
						if cap["available"] != false {
							t.Fatalf("blocked cancellation capability became available: %+v", cap)
						}
					}
				}
				if !blockedCancel {
					t.Fatal("missing blocked cancellation capability")
				}

			}
			assertDefinitionFormalSnapshot(t, f, tables, before, "cold dual-MCP readback is read-only")
			if !reflect.DeepEqual(coldDurable, durable()) {
				t.Fatal("read-only recovery rewrote durable intent")
			}
			save("cold-read-witness", map[string]any{"persistedIntent": coldDurable, "observedIntent": observed, "allTablesUnchanged": true})
			if receiptExists {
				replay := intent(call(gpt, "confirmation_commit", commit))
				if replay.State != "COMPLETED" || replay.CanResume || !reflect.DeepEqual(replay.Steps, observed.Steps) {
					t.Fatalf("proven historical replay changed result: %+v", replay)
				}
				assertDefinitionFormalSnapshot(t, f, tables, before, "proven original replay must not dispatch business effects")
				args["requestId"], args["expectedDigest"], args["content"] = "forbidden-fresh-write", written["digest"], "must not write\n"
				denied := rawCall(local, "file_write", args)
				if !denied.IsError || mcpObject(t, denied.StructuredContent)["code"] != string(directfile.NotEnabled) {
					t.Fatal("closed authority admitted fresh write")
				}
				assertDefinitionFormalSnapshot(t, f, tables, before, "denied fresh write must not create receipt or effects")
			} else {
				// Never replay an UNKNOWN commit. This is a distinct, explicitly attempted
				// preparation after all read-only invariants have already been verified.
				prepare.RequestID = "separate-cancel-prepare-stays-blocked"
				blocked := rawCall(gpt, "execution_prepare", prepare)
				b, _ := json.Marshal(blocked)
				if !blocked.IsError || !strings.Contains(string(b), "TASK_ACTION_PENDING") {
					t.Fatalf("missing receipt lost task owner: %s", b)
				}
			}
			now, e := snapshotTCPMutation(path)
			if e != nil || !sameTCPMutation(physical, now) {
				t.Fatalf("cancellation recovery changed file: %v", e)
			}
			save("final-physical-witness", map[string]any{"beforeDigest": fmt.Sprintf("%x", physical.Digest), "afterDigest": fmt.Sprintf("%x", now.Digest), "sameFile": os.SameFile(physical.Info, now.Info), "sameMtime": physical.Info.ModTime() == now.Info.ModTime(), "receiptExists": receiptExists})
			closeLocal()
			closeGPT()
			t.Logf("canonical receipt=%v: durable=%s cold read=%s; reads preserve all tables and physical bytes", receiptExists, coldDurable.State, observed.State)
		})
	}
}
