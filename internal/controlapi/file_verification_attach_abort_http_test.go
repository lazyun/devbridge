package controlapi_test

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/fileworker"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	sqlitedriver "modernc.org/sqlite"
)

// The callback writes real fixture files and Core owns verification. This tests
// preparation after admission, not the existing pre-admission FILE SQL cut.
// It neither authorizes a FILE reverification nor claims native Dot coverage.
func TestFileVerificationAttachAbortRealHTTP(t *testing.T) {
	for _, name := range []string{"healthy_verification_only", "reserved_verification_attach_abort", "rejected_receipt_write_abort"} {
		fault := name != "healthy_verification_only"
		receiptAbort := name == "rejected_receipt_write_abort"
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var hits [][3]string
			observer := fmt.Sprintf("file_verification_attach_observer_%d", fileHandoffObserverID.Add(1))
			// Registered before connections; observer only records arguments from SQL.
			if err := sqlitedriver.RegisterScalarFunction(observer, 3, func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
				mu.Lock()
				defer mu.Unlock()
				hits = append(hits, [3]string{fmt.Sprint(args[0]), fmt.Sprint(args[1]), fmt.Sprint(args[2])})
				return int64(1), nil
			}); err != nil {
				t.Fatal(err)
			}
			var receiptHits []string
			if err := sqlitedriver.RegisterScalarFunction(observer+"_receipt", 1, func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
				mu.Lock()
				defer mu.Unlock()
				receiptHits = append(receiptHits, fmt.Sprint(args[0]))
				return int64(1), nil
			}); err != nil {
				t.Fatal(err)
			}
			f, current, _, request := fileClientFixture(t, task.VerificationModeRun)
			ctx, cancel := context.WithTimeout(f.ctx, 3*time.Minute)
			defer cancel()
			session, closeHTTP := pendingCancelHTTPClient(t, f, true)
			defer closeHTTP()
			wire := fileClientTransport(t, session, request, current)
			beforeTrees := upperClientTrees(t, f)
			var callbackTrees map[string]upperClientTree
			callbacks := 0
			if fault {
				quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
				ddl := fmt.Sprintf(`CREATE TRIGGER file_verification_attach_abort BEFORE UPDATE OF workspace_id ON runs
 WHEN NEW.task_id=%s AND NEW.purpose='VERIFICATION' AND OLD.workspace_id IS NULL AND NEW.workspace_id IS NOT NULL
 BEGIN SELECT %s(NEW.id,NEW.execution_proposal_id,(SELECT result_json FROM direct_file_implementation_results WHERE task_id=NEW.task_id AND revision_id=(SELECT task_definition_revision_id FROM execution_proposals WHERE id=NEW.execution_proposal_id)));
 SELECT RAISE(ABORT,'TEST_ONLY_FILE_VERIFICATION_ATTACH_ABORT'); END`, quote(f.task), observer)
				if _, err := f.plane.DB.SQL().Exec(ddl); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER IF EXISTS file_verification_attach_abort`); err != nil {
						t.Error(err)
					}
				})
			}
			if receiptAbort {
				ddl := fmt.Sprintf(`CREATE TRIGGER file_verification_receipt_abort BEFORE UPDATE ON runtime_handoffs WHEN NEW.state='REJECTED' AND EXISTS(SELECT 1 FROM runs r WHERE r.id=NEW.run_id AND r.task_id='%s' AND r.purpose='VERIFICATION') BEGIN SELECT %s_receipt(NEW.run_id); SELECT RAISE(ABORT,'TEST_ONLY_FILE_REJECTED_RECEIPT_ABORT'); END`, strings.ReplaceAll(f.task, "'", "''"), observer)
				if _, err := f.plane.DB.SQL().Exec(ddl); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER IF EXISTS file_verification_receipt_abort`); err != nil {
						t.Error(err)
					}
				})
			}
			client, err := fileworker.New(wire, request, func(callCtx context.Context, d fileworker.Delivery, files fileworker.Files) (fileworker.Completion, error) {
				callbacks++
				fileClientReplaceBoth(t, f, current, callCtx, d, files)
				callbackTrees = upperClientTrees(t, f)
				return fileworker.Completion{Summary: "Real FILE result before verification preparation", VerificationNotes: "Only Core's separately authorized TEST_ONLY verification runs commands"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := client.Run(ctx)
			if err != nil || out.State != fileworker.ResultRecorded || out.ResultReceipt == nil || out.ResultInput == nil || callbacks != 1 {
				raw, _ := json.Marshal(out)
				t.Fatalf("formal FILE result not recorded: %s err=%v callbacks=%d", raw, err, callbacks)
			}
			receipt := out.ResultReceipt
			if receipt.ID == "" || receipt.ExecutionID == "" || receipt.Accepted || len(out.Mutations) != 2 {
				t.Fatalf("wrong formal receipt %+v", receipt)
			}
			fileClientSubmissionProof(t, f, request, out.ResultInput)
			for key, old := range beforeTrees {
				now := callbackTrees[key]
				if now.HEAD != old.HEAD || strings.HasPrefix(key, "source:") && now != old {
					t.Fatalf("callback changed source/HEAD %s", key)
				}
			}
			var runID string
			if err = f.plane.DB.SQL().QueryRow(`SELECT id FROM runs WHERE execution_proposal_id=? AND purpose='VERIFICATION'`, receipt.ExecutionID).Scan(&runID); err != nil {
				t.Fatalf("no actual admitted verification Run: %v receipt=%+v", err, receipt)
			}
			if receipt.VerificationRunID != "" && receipt.VerificationRunID != runID {
				t.Fatalf("receipt substituted verification identity %+v actual=%s", receipt, runID)
			}
			if n := fileClientCount(t, f, "execution_proposals"); n != 1 {
				t.Fatalf("expected unique verification-only Execution, got%d", n)
			}
			var implementations, verifications int
			if err = f.plane.DB.SQL().QueryRow(`SELECT sum(purpose='IMPLEMENTATION'),sum(purpose='VERIFICATION') FROM runs WHERE task_id=?`, f.task).Scan(&implementations, &verifications); err != nil || implementations != 0 || verifications != 1 {
				t.Fatalf("unexpected Run counts impl=%d verify=%d err=%v", implementations, verifications, err)
			}
			frozenTables := []string{"direct_file_implementation_results", "direct_file_operation_receipts", "direct_file_worker_claims", "direct_file_verifications", "task_definition_revisions", "direct_file_verification_selections"}
			frozen := definitionFormalSnapshot(t, f, frozenTables)
			deadline := time.Now().Add(2 * time.Minute)
			var terminal run.Run
			var claims int
			joined := false
			for {
				terminal, err = f.plane.Runs.Get(f.ctx, runID)
				if err != nil {
					t.Fatal(err)
				}
				joined = true
				if active, ok := f.plane.Supervisor.GetSession(runID); ok {
					select {
					case <-active.Done():
					default:
						joined = false
					}
				}
				if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, runID).Scan(&claims); err != nil {
					t.Fatal(err)
				}
				done := terminal.Status == run.StatusSucceeded || terminal.Status == run.StatusFailed || terminal.Status == run.StatusInfrastructureFailed || terminal.Status == run.StatusCanceled
				// Observe the real Run and session first; product completion is separately checked.
				if done && joined {
					break
				}
				if time.Now().After(deadline) {
					raw, _ := json.Marshal(terminal)
					t.Fatalf("verification did not reach actual joined terminal: %s claims=%d joined=%t", raw, claims, joined)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if fault {
				if receipt.VerificationStartError == "" {
					t.Fatalf("recorded FILE result hid verification start failure: %+v", receipt)
				}
				mu.Lock()
				observed := append([][3]string(nil), hits...)
				mu.Unlock()
				if len(observed) != 1 || observed[0][0] != runID || observed[0][1] != receipt.ExecutionID || !json.Valid([]byte(observed[0][2])) {
					t.Fatalf("wrong exact attach SQL cut: %+v", observed)
				}
				var persistedResult string
				if err = f.plane.DB.SQL().QueryRow(`SELECT result_json FROM direct_file_implementation_results WHERE id=?`, receipt.ID).Scan(&persistedResult); err != nil || persistedResult != observed[0][2] {
					t.Fatalf("formal pre-cut result changed: %v", err)
				}
				if terminal.Status != run.StatusInfrastructureFailed || terminal.Failure == nil || terminal.Failure.Code != "RUNTIME_WORKSET_PREPARATION_FAILED" || terminal.Failure.Stage != "exact repository workset" || !strings.Contains(terminal.Failure.Message, "TEST_ONLY_FILE_VERIFICATION_ATTACH_ABORT") {
					t.Fatalf("not exact preparation failure: %+v failure=%+v", terminal, terminal.Failure)
				}
				if terminal.StartedAt != nil || terminal.PromptSentAt != nil || terminal.FinalCodexPrompt != "" || terminal.ThreadID != "" || terminal.TurnID != "" || terminal.WorkspaceID != "" || terminal.RuntimeWorkset != nil {
					t.Fatalf("failed attachment entered runtime: %+v", terminal)
				}
				var rawState, rawCode string
				var accepted any
				wantState, wantCode := "REJECTED", terminal.Failure.Code
				if receiptAbort {
					wantState, wantCode = "PREPARING", "HANDOFF_IN_PROGRESS"
				}
				if err = f.plane.DB.SQL().QueryRow(`SELECT state,code,accepted_at FROM runtime_handoffs WHERE run_id=?`, runID).Scan(&rawState, &rawCode, &accepted); err != nil || rawState != wantState || rawCode != wantCode || accepted != nil {
					t.Fatalf("raw handoff %s/%s/%v err=%v", rawState, rawCode, accepted, err)
				}
				var effects int
				if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM agent_operation_boundaries WHERE run_id=?)+(SELECT count(*) FROM agent_finalization_intents WHERE run_id=?)+(SELECT count(*) FROM run_execution_records WHERE run_id=?)+(SELECT count(*) FROM run_repository_bindings WHERE run_id=?)`, runID, runID, runID, runID).Scan(&effects); err != nil || effects != 0 {
					t.Fatalf("unexpected runtime effects %d %v", effects, err)
				}
				if receiptAbort {
					mu.Lock()
					writes := append([]string(nil), receiptHits...)
					mu.Unlock()
					if len(writes) != 1 || writes[0] != runID {
						t.Fatalf("receipt rollback not exact %+v", writes)
					}
					if _, e := f.plane.DB.SQL().Exec(`DROP TRIGGER file_verification_receipt_abort`); e != nil {
						t.Fatal(e)
					}
					effectTables := []string{"execution_proposals", "runs", "runtime_handoffs", "feature_area_agent_claims", "agent_operation_boundaries", "agent_finalization_intents"}
					before := definitionFormalSnapshot(t, f, effectTables)
					if store, ok := any(sqlite.NewExecutionProposalStore(f.plane.DB)).(interface {
						SettleRejectedFileVerification(context.Context, string, string, time.Time) (executionproposal.Proposal, bool, error)
					}); ok {
						t.Log("candidate exact settlement capability present; probing missing receipt fail-closed")
						if _, changed, e := store.SettleRejectedFileVerification(f.ctx, receipt.ExecutionID, runID, time.Now()); e != nil || changed {
							t.Fatalf("missing rejected receipt authorized settlement changed=%t err=%v", changed, e)
						}
					} else {
						t.Log("baseline lacks optional settlement capability: only the additional direct-store probe is inapplicable; all public SQL-fault assertions remain active")
					}
					if before != definitionFormalSnapshot(t, f, effectTables) {
						t.Fatal("receipt rollback guard changed business")
					}
				}
				// Permit ordinary asynchronous bookkeeping, never synthesize completion.
				for {
					proposal, e := f.facade.GetExecutionProposal(f.ctx, f.session, receipt.ExecutionID)
					if e != nil {
						t.Fatal(e)
					}
					if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, runID).Scan(&claims); e != nil {
						t.Fatal(e)
					}
					if proposal.ImplementationRunID != "" || proposal.VerificationRunID != runID {
						t.Fatalf("not verification-only lineage %+v", proposal)
					}
					if receiptAbort {
						if proposal.Outcome != executionproposal.OutcomeRunning || claims != 1 {
							t.Fatalf("unknown receipt released owner: %+v claim=%d", proposal, claims)
						}
						break
					}
					if proposal.Outcome == executionproposal.OutcomeFailed && claims == 0 {
						if proposal.FailureCode != terminal.Failure.Code || proposal.FailureMessage != terminal.Failure.Message {
							t.Fatalf("completion changed precise failure %+v run=%+v", proposal, terminal.Failure)
						}
						break
					}
					if time.Now().After(deadline) {
						// Capture actual facts before fixture cleanup, not guessed recovery actions.
						for _, table := range []string{"execution_proposals", "feature_area_agent_claims", "runtime_handoffs", "agent_operation_boundaries", "agent_finalization_intents"} {
							rows, e := f.plane.DB.SQL().Query("SELECT * FROM " + table)
							if e != nil {
								t.Fatal(e)
							}
							cols, _ := rows.Columns()
							var records []map[string]any
							for rows.Next() {
								values := make([]any, len(cols))
								dest := make([]any, len(cols))
								for i := range values {
									dest[i] = &values[i]
								}
								if e = rows.Scan(dest...); e != nil {
									t.Fatal(e)
								}
								record := map[string]any{}
								for i, k := range cols {
									if b, ok := values[i].([]byte); ok {
										record[k] = string(b)
									} else {
										record[k] = values[i]
									}
								}
								records = append(records, record)
							}
							e = rows.Err()
							_ = rows.Close()
							raw, _ := json.Marshal(records)
							t.Logf("actual %s=%s err=%v", table, raw, e)
						}
						public, e := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_get", Arguments: map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}}})
						raw, _ := json.Marshal(public)
						t.Logf("actual public workspace=%s err=%v", raw, e)
						observed, observeErr := client.Observe(ctx)
						observedJSON, _ := json.Marshal(observed)
						t.Logf("actual FILE Observe=%s err=%v", observedJSON, observeErr)
						if frozen != definitionFormalSnapshot(t, f, frozenTables) || !reflect.DeepEqual(callbackTrees, upperClientTrees(t, f)) {
							t.Fatal("failed verification changed formal FILE facts/files")
						}
						t.Fatalf("safe rejected verification-only Run did not complete product: Execution=%+v claim=%d joined=%t elapsed=%s failure=%+v", proposal, claims, joined, time.Since(terminal.CreatedAt), terminal.Failure)
					}
					time.Sleep(20 * time.Millisecond)
				}
			} else {
				if terminal.Status != run.StatusSucceeded {
					t.Fatalf("healthy verification failed %+v failure=%+v", terminal, terminal.Failure)
				}
				if terminal.ResultProtocol == run.NaturalResultProtocol && terminal.VerificationReport == nil {
					submitNaturalFixtureAnalysis(t, f, runID, nil)
					terminal, err = f.plane.Runs.Get(f.ctx, runID)
					if err != nil {
						t.Fatal(err)
					}
				}
				if terminal.VerificationReport == nil || len(terminal.VerificationReport.Checks) != 2 {
					t.Fatalf("healthy missing real commands %+v", terminal)
				}
				seen := map[string]bool{}
				for _, check := range terminal.VerificationReport.Checks {
					if check.Observed == nil || check.Observed.ExitCode == nil || *check.Observed.ExitCode != 0 || check.Observed.Command != "test -f same.txt" || check.Observed.Source == nil {
						t.Fatalf("missing actual command evidence %+v", check)
					}
					seen[check.Observed.Source.RepositoryID] = true
				}
				if len(seen) != 2 || !seen[f.repos["app"]] || !seen[f.repos["sdk"]] {
					t.Fatalf("wrong member coverage %v", seen)
				}
			}
			if !fault {
				proposal, e := f.facade.GetExecutionProposal(f.ctx, f.session, receipt.ExecutionID)
				if e != nil || proposal.Outcome != executionproposal.OutcomeSucceeded || proposal.ImplementationRunID != "" || proposal.VerificationRunID != runID {
					t.Fatalf("healthy verification-only product %+v %v", proposal, e)
				}
				if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, runID).Scan(&claims); e != nil || claims != 0 {
					t.Fatalf("healthy claim not released %d %v", claims, e)
				}
			}
			observed, e := client.Observe(ctx)
			exact, readErr := f.facade.GetFileImplementationResult(f.ctx, directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: request.Claim.WorkflowID})
			if e != nil || readErr != nil || observed.ResultReceipt == nil || observed.ResultReceipt.ID != receipt.ID || exact.ID != receipt.ID || exact.ExecutionID != receipt.ExecutionID || !reflect.DeepEqual(exact.Members, receipt.Members) || exact.Accepted {
				t.Fatalf("FILE history changed observed=%+v exact=%+v errs=%v/%v", observed, exact, e, readErr)
			}
			if !fault && (exact.VerificationStatus != "SUCCEEDED" || observed.ResultReceipt.VerificationStatus != "SUCCEEDED") {
				t.Fatalf("healthy verification projection exact=%+v observed=%+v", exact, observed)
			}
			if fault {
				want := "FAILED"
				if receiptAbort {
					want = "RUNNING"
				}
				if exact.VerificationStatus != want || observed.ResultReceipt.VerificationStatus != want {
					t.Fatalf("public failure/unknown projection wrong exact=%+v observed=%+v", exact, observed)
				}
				tables := []string{"execution_proposals", "runs", "runtime_handoffs", "feature_area_agent_claims", "agent_operation_boundaries", "agent_finalization_intents", "run_execution_records", "run_repository_bindings"}
				before := definitionFormalSnapshot(t, f, tables)
				reply, e := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "file_implementation_result_submit", Arguments: mcpObject(t, *out.ResultInput)})
				if e != nil || reply == nil || reply.IsError {
					raw, _ := json.Marshal(reply)
					t.Fatalf("exact result replay %s err=%v", raw, e)
				}
				again, e := client.Observe(ctx)
				if e != nil || again.ResultReceipt == nil || again.ResultReceipt.ID != receipt.ID || again.ResultReceipt.VerificationStatus != want {
					t.Fatalf("observe after replay %+v %v", again, e)
				}
				if before != definitionFormalSnapshot(t, f, tables) {
					t.Fatal("read/exact result replay redispatched or changed runtime facts")
				}
				if !receiptAbort {
					public := callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
					raw, _ := json.Marshal(public)
					var obj any
					_ = json.Unmarshal(raw, &obj)
					available := false
					var walk func(any)
					walk = func(v any) {
						switch x := v.(type) {
						case map[string]any:
							if x["action"] == "REQUEST_TASK_REVIEW" && x["available"] == true {
								target, ok := x["target"].(map[string]any)
								if ok && target["taskId"] == f.task && target["featureAreaId"] == f.area {
									expected, ok := target["expectedReviewRequest"].(map[string]any)
									if ok && expected["currentExecutionId"] == receipt.ExecutionID && expected["taskDefinitionRevisionId"] == current.CurrentDefinitionRevisionID {
										available = true
									}
								}
							}
							for _, child := range x {
								walk(child)
							}
						case []any:
							for _, child := range x {
								walk(child)
							}
						}
					}
					walk(obj)
					if !available {
						t.Fatalf("safe failed FILE product still blocks supplemental review %s", raw)
					}
				}
			}
			if frozen != definitionFormalSnapshot(t, f, frozenTables) || !reflect.DeepEqual(callbackTrees, upperClientTrees(t, f)) {
				t.Fatal("verification changed original formal FILE facts/files")
			}
			if wire.count("file_worker_claim") != 1 || wire.count("file_write") != 2 || wire.count("file_implementation_result_submit") != 1 || callbacks != 1 {
				t.Fatal("observer resent FILE work")
			}
			mu.Lock()
			hitCount := len(hits)
			mu.Unlock()
			wantHits := 0
			if fault {
				wantHits = 1
			}
			if hitCount != wantHits {
				t.Fatalf("unexpected trigger count%d", hitCount)
			}
			fileClientNoAcceptance(t, f)
			if fileClientCount(t, f, "task_commit_receipts") != 0 {
				t.Fatal("unexpected delivery")
			}
			if fault && !receiptAbort {
				// The TEST_ONLY runtime fixture omits the UPPER worker facade
				// dependency. Restore the same real plane's canonical facade;
				// no grant, approval, runtime selection or business rows change.
				canonical, e := controlapi.NewFromControlPlane(f.plane)
				if e != nil {
					t.Fatal(e)
				}
				f.facade = canonical
				nextClient, closeNext := pendingCancelHTTPClient(t, f, true)
				defer closeNext()
				call := func(tool string, input any) *sdkmcp.CallToolResult {
					t.Helper()
					out, e := nextClient.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: input})
					if e != nil || out == nil || out.IsError {
						raw, _ := json.Marshal(out)
						t.Fatalf("supplemental %s: %s err=%v", tool, raw, e)
					}
					return out
				}
				decode := func(src, dst any) {
					t.Helper()
					raw, e := json.Marshal(src)
					if e != nil {
						t.Fatal(e)
					}
					if e = json.Unmarshal(raw, dst); e != nil {
						t.Fatal(e)
					}
				}
				public := call("workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
				var root any
				decode(public.StructuredContent, &root)
				var target *wp.TaskReviewRequestTarget
				var walk func(any)
				walk = func(v any) {
					switch x := v.(type) {
					case map[string]any:
						if x["action"] == "REQUEST_TASK_REVIEW" && x["available"] == true {
							if actual, ok := x["target"].(map[string]any); ok && actual["taskId"] == f.task && actual["featureAreaId"] == f.area && actual["expectedReviewRequest"] != nil {
								var value wp.TaskReviewRequestTarget
								decode(actual["expectedReviewRequest"], &value)
								if value.CurrentExecutionID == receipt.ExecutionID && value.TaskDefinitionRevisionID == current.CurrentDefinitionRevisionID {
									target = &value
								}
							}
						}
						for _, child := range x {
							walk(child)
						}
					case []any:
						for _, child := range x {
							walk(child)
						}
					}
				}
				walk(root)
				if target == nil {
					t.Fatal("canonical public source omitted exact supplemental target")
				}
				counts := map[string]int{}
				for _, table := range []string{"reviews", "runs", "review_grants"} {
					counts[table] = fileClientCount(t, f, table)
				}
				readTargets := workset.CloneTargets(current.RepositoryTargets)
				for i := range readTargets {
					readTargets[i].Write = workset.WriteScope{Kind: "NONE"}
				}
				input := c.PrepareInput{AISessionID: f.session, RequestID: "file-failure-supplemental-prepare", Purpose: c.Review, Action: "REQUEST_SUPPLEMENTAL_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: readTargets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}, SupplementalPurpose: "Review the existing FILE changes and revise the failed verification plan", ExpectedTarget: target}, Variants: []c.Variant{{ID: "upper", Label: "Review existing FILE work", Summary: "Explicit simulated same-Task supplemental review", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt in both exact members and assess the failed verification plan against the existing FILE changes. Do not edit files."}}}
				args := mcpObject(t, input)
				args["review"].(map[string]any)["repositoryId"] = ""
				args["review"].(map[string]any)["reviewPlan"] = map[string]any{"mode": "REPOSITORY"}
				var prepared c.Result
				decode(call("review_prepare", args).StructuredContent, &prepared)
				if prepared.Intent == nil {
					t.Fatalf("no supplemental intent %+v", prepared)
				}
				intent := *prepared.Intent
				var opts c.Result
				decode(call("confirmation_options_get", c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, ContentHash: intent.ContentHash}}).StructuredContent, &opts)
				if opts.Options == nil {
					t.Fatal("supplemental options missing")
				}
				for table, n := range counts {
					if fileClientCount(t, f, table) != n {
						t.Fatalf("prepare/options caused %s effect", table)
					}
				}
				commit := c.CommitInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, ContentHash: intent.ContentHash, RequestID: "file-failure-supplemental-confirm", VariantID: "upper"}
				var confirmed c.Result
				decode(call("confirmation_commit", commit).StructuredContent, &confirmed)
				if confirmed.Intent == nil || confirmed.Intent.State != "COMPLETED" || len(confirmed.Intent.Steps) != 2 || confirmed.Intent.Steps[0].State != "COMPLETED" || confirmed.Intent.Steps[1].State != "COMPLETED" || confirmed.Intent.Steps[1].ReviewID == "" || confirmed.Intent.Steps[1].RunID == "" {
					t.Fatalf("supplemental writer did not complete %+v", confirmed)
				}
				for table, n := range counts {
					if fileClientCount(t, f, table) != n+1 {
						t.Fatalf("supplemental did not create unique %s", table)
					}
				}
				owner, e := f.facade.GetTask(f.ctx, f.task)
				if e != nil || owner.CurrentExecutionID != "" || owner.CurrentReviewID != confirmed.Intent.Steps[1].ReviewID || owner.Phase != task.PhaseReviewing || owner.CurrentDefinitionRevisionID != current.CurrentDefinitionRevisionID {
					t.Fatalf("new review is not Task owner %+v %v", owner, e)
				}
				access, e := f.facade.UpperReviewAccessForSession(f.ctx, f.session, owner.CurrentReviewID)
				if e != nil || access.State != "ACTIVE" || access.Target.RunID != confirmed.Intent.Steps[1].RunID {
					t.Fatalf("missing actual supplemental grant %+v %v", access, e)
				}
				old, e := f.plane.Runs.Get(f.ctx, runID)
				if e != nil || !reflect.DeepEqual(old, terminal) {
					t.Fatalf("supplemental changed failure history %+v %v", old, e)
				}
				if frozen != definitionFormalSnapshot(t, f, frozenTables) || !reflect.DeepEqual(callbackTrees, upperClientTrees(t, f)) {
					t.Fatal("supplemental admission changed original FILE evidence")
				}
				fileClientNoAcceptance(t, f)
				if fileClientCount(t, f, "task_commit_receipts") != 0 {
					t.Fatal("supplemental unexpectedly delivered")
				}
				t.Logf("actual same-Task supplemental review admitted Review=%s Run=%s; no worker/read chain or delivery executed", owner.CurrentReviewID, access.Target.RunID)
			}
			t.Logf("fault=%t exact verification-only Run=%s FILEresult=%s preserved; actual terminal=%s no implementation Run", fault, runID, receipt.ID, terminal.Status)
		})
	}
}
