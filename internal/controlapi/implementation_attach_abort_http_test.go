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

	"github.com/lazyun/devbridge/internal/codexconfig"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	sqlitedriver "modernc.org/sqlite"
)

// Controlled pre-runtime SQL failure, not an explanation of any prior intermittent
// infrastructure failure. Real Core and protocol subprocess; explicit simulated user.
func TestImplementationAttachAbortThenPublicRetryRealHTTP(t *testing.T) {
	var mu sync.Mutex
	var observed [][2]string
	observer := fmt.Sprintf("implementation_attach_observer_%d", fileHandoffObserverID.Add(1))
	if err := sqlitedriver.RegisterScalarFunction(observer, 2, func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
		mu.Lock()
		defer mu.Unlock()
		observed = append(observed, [2]string{fmt.Sprint(args[0]), fmt.Sprint(args[1])})
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	var launch controlapi.ApprovedTaskStartInput
	runCodingDeliveryFixtureFrom(t, f, func(f *upperLifecycleFixture, oldID string) bool {
		f.plane.Retirement.Close()
		deadline := time.Now().Add(2 * time.Minute)
		var old run.Run
		for {
			proposal, err := f.facade.GetExecutionProposal(f.ctx, f.session, oldID)
			if err != nil {
				t.Fatal(err)
			}
			old, err = f.plane.Runs.Get(f.ctx, proposal.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var claims int
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, old.ID).Scan(&claims); err != nil {
				t.Fatal(err)
			}
			joined := true
			if s, ok := f.plane.Supervisor.GetSession(old.ID); ok {
				select {
				case <-s.Done():
				default:
					joined = false
				}
			}
			if old.Status == run.StatusInfrastructureFailed && claims == 0 && joined {
				break
			}
			if old.Status == run.StatusSucceeded || old.Status == run.StatusFailed || old.Status == run.StatusCanceled || time.Now().After(deadline) {
				// Preserve actual fixture rows before t.TempDir cleanup; no recovery writes.
				for _, table := range []string{"feature_area_agent_claims", "execution_proposals", "runtime_handoffs", "agent_operation_boundaries", "agent_finalization_intents"} {
					rows, queryErr := f.plane.DB.SQL().Query("SELECT * FROM " + table)
					if queryErr != nil {
						t.Logf("diagnostic %s queryErr=%v", table, queryErr)
						continue
					}
					columns, _ := rows.Columns()
					var records []map[string]any
					for rows.Next() {
						values := make([]any, len(columns))
						dest := make([]any, len(columns))
						for i := range values {
							dest[i] = &values[i]
						}
						if scanErr := rows.Scan(dest...); scanErr != nil {
							t.Fatal(scanErr)
						}
						record := map[string]any{}
						for i, key := range columns {
							if b, ok := values[i].([]byte); ok {
								record[key] = string(b)
							} else {
								record[key] = values[i]
							}
						}
						records = append(records, record)
					}
					rowErr := rows.Err()
					_ = rows.Close()
					raw, _ := json.Marshal(records)
					t.Logf("diagnostic %s=%s error=%v", table, raw, rowErr)
				}
				workspace, workspaceErr := f.facade.DevelopmentWorkspaceForSession(f.ctx, f.session, f.area, f.task)
				publicJSON, _ := json.Marshal(workspace.Presentation)
				_, sessionPresent := f.plane.Supervisor.GetSession(old.ID)
				t.Logf("diagnostic publicPresentation=%s readErr=%v supervisorSessionPresent=%t elapsedSinceRunCreated=%s", publicJSON, workspaceErr, sessionPresent, time.Since(old.CreatedAt))
				t.Fatalf("expected exact pre-runtime failure: run=%+v failure=%+v claims=%d joined=%t", old, old.Failure, claims, joined)
			}
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		hits := append([][2]string(nil), observed...)
		mu.Unlock()
		if len(hits) != 1 || hits[0] != [2]string{old.ID, oldID} || old.Failure == nil || old.Failure.Code != "RUNTIME_WORKSET_PREPARATION_FAILED" || old.Failure.Stage != "exact repository workset" || !strings.Contains(old.Failure.Message, "TEST_ONLY_IMPLEMENTATION_ATTACH_ABORT") {
			t.Fatalf("wrong causal failure hits=%v run=%+v failure=%+v", hits, old, old.Failure)
		}
		if old.StartedAt != nil || old.PromptSentAt != nil || old.FinalCodexPrompt != "" || old.ThreadID != "" || old.TurnID != "" || old.RuntimeWorkset != nil || old.WorkspaceID != "" {
			t.Fatalf("failed attachment dispatched runtime: %+v", old)
		}
		current, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		predecessor, err := f.facade.GetExecutionProposal(f.ctx, f.session, oldID)
		if err != nil {
			t.Fatal(err)
		}
		if current.CurrentExecutionID != oldID || predecessor.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID || predecessor.RunID != old.ID || predecessor.Outcome != executionproposal.OutcomeFailed || predecessor.FailureCode != old.Failure.Code {
			t.Fatal("original admitted owner lost")
		}
		var persistedState, persistedCode string
		var acceptedAt any
		if err = f.plane.DB.SQL().QueryRow(`SELECT state,code,accepted_at FROM runtime_handoffs WHERE run_id=?`, old.ID).Scan(&persistedState, &persistedCode, &acceptedAt); err != nil || persistedState != "REJECTED" || persistedCode != old.Failure.Code || acceptedAt != nil {
			t.Fatalf("raw rejected handoff %s/%s/%v err=%v", persistedState, persistedCode, acceptedAt, err)
		}
		var runtimeEffects int
		if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM agent_operation_boundaries WHERE run_id=?)+(SELECT count(*) FROM agent_finalization_intents WHERE run_id=?)+(SELECT count(*) FROM run_execution_records WHERE run_id=?)+(SELECT count(*) FROM run_repository_bindings WHERE run_id=?)`, old.ID, old.ID, old.ID, old.ID).Scan(&runtimeEffects); err != nil || runtimeEffects != 0 {
			t.Fatalf("unexpected runtime effects %d %v", runtimeEffects, err)
		}
		if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER implementation_attach_abort`); err != nil {
			t.Fatal(err)
		}
		// Exact original durable composed confirmation replay must retain old identity.
		replay, err := f.facade.StartApprovedTaskExecution(f.ctx, launch)
		if replay.Proposal.ID != oldID || replay.Run.ID != old.ID {
			t.Fatalf("old admission replay substituted owner: %+v %v", replay, err)
		}
		before := definitionFormalSnapshot(t, f, []string{"runs", "execution_proposals", "run_execution_records", "run_analyses"})
		replay, err = f.facade.StartApprovedTaskExecution(f.ctx, launch)
		if replay.Proposal.ID != oldID || replay.Run.ID != old.ID || before != definitionFormalSnapshot(t, f, []string{"runs", "execution_proposals", "run_execution_records", "run_analyses"}) {
			t.Fatalf("old replay redispatched: %+v %v", replay, err)
		}
		ctx, cancel := context.WithTimeout(f.ctx, 2*time.Minute)
		defer cancel()
		client, closeHTTP := pendingCancelHTTPClient(t, f, true)
		defer closeHTTP()
		call := func(name string, args any) *sdkmcp.CallToolResult {
			t.Helper()
			r, e := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
			if e != nil || r == nil {
				t.Fatalf("%s transport %v %+v", name, e, r)
			}
			return r
		}
		decode := func(src, dst any) {
			t.Helper()
			b, e := json.Marshal(src)
			if e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(b, dst); e != nil {
				t.Fatal(e)
			}
		}
		conf := func(r *sdkmcp.CallToolResult) c.Result {
			t.Helper()
			var out c.Result
			decode(r.StructuredContent, &out)
			if r.IsError {
				b, _ := json.Marshal(r)
				t.Fatalf("unexpected protocol failure %s", b)
			}
			return out
		}
		wr := call("workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
		if wr.IsError {
			t.Fatalf("workspace %+v", wr)
		}
		var obj any
		decode(wr.StructuredContent, &obj)
		var target *presentation.ActionTarget
		var find func(any)
		find = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				if x["action"] == "RETRY_IMPLEMENTATION" && x["available"] == true && x["target"] != nil {
					var value presentation.ActionTarget
					decode(x["target"], &value)
					target = &value
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
		find(obj)
		if target == nil || target.TaskID != f.task || target.ExecutionID != oldID || target.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID || target.DecisionID == "" {
			b, _ := json.Marshal(wr)
			t.Fatalf("missing exact public retry target %+v workspace=%s", target, b)
		}
		prepare := c.PrepareInput{AISessionID: f.session, RequestID: "attach-failure-retry-prepare", Purpose: c.Execution, Action: "RETRY_IMPLEMENTATION", Execution: &c.ExecutionBody{Action: "RETRY_IMPLEMENTATION", TaskID: target.TaskID, FeatureAreaID: target.FeatureAreaID, ExecutionID: target.ExecutionID, TaskDefinitionRevisionID: target.TaskDefinitionRevisionID, DecisionID: target.DecisionID, ReviewID: current.CurrentReviewID}, Variants: []c.Variant{{ID: "retry", Label: "Retry unchanged implementation", Summary: "Explicit simulated retry of unchanged definition", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}}
		prepared := conf(call("execution_prepare", mcpObject(t, prepare)))
		if prepared.Intent == nil {
			t.Fatalf("no retry intent %+v", prepared)
		}
		p := *prepared.Intent
		options := conf(call("confirmation_options_get", c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, ContentHash: p.ContentHash}}))
		if options.Options == nil || options.Options.LockedRuntimeConfig == nil || options.Options.LockedBackend == nil {
			t.Fatalf("missing frozen selection %+v", options)
		}
		commit := c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, ContentHash: p.ContentHash, RequestID: "attach-failure-retry-confirm", VariantID: "retry", Selection: c.Selection{RuntimeConfig: options.Options.LockedRuntimeConfig, Backend: &codexconfig.BackendSelectionInput{ConfigID: options.Options.LockedBackend.ConfigID, ConfigFingerprint: options.Options.LockedBackend.ConfigFingerprint}}}
		commitArgs := mcpObject(t, commit)
		// Public selection schema omits the internal empty service-tier field.
		delete(commitArgs["selection"].(map[string]any)["runtimeConfig"].(map[string]any), "serviceTier")
		confirmed := conf(call("confirmation_commit", commitArgs))
		if confirmed.Intent == nil || confirmed.Intent.State != "COMPLETED" {
			t.Fatalf("retry not completed %+v", confirmed)
		}
		next, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil || next.CurrentExecutionID == "" || next.CurrentExecutionID == oldID || next.CurrentDefinitionRevisionID != current.CurrentDefinitionRevisionID {
			t.Fatalf("no exact successor %+v %v", next, err)
		}
		nextID := next.CurrentExecutionID
		deadline = time.Now().Add(2 * time.Minute)
		for {
			proposal, e := f.facade.GetExecutionProposal(f.ctx, f.session, nextID)
			if e != nil {
				t.Fatal(e)
			}
			ready := proposal.RunID != "" && proposal.VerificationRunID != ""
			for _, id := range []string{proposal.RunID, proposal.VerificationRunID} {
				if id == "" {
					continue
				}
				value, e := f.plane.Runs.Get(f.ctx, id)
				if e != nil {
					t.Fatal(e)
				}
				if value.Status == run.StatusFailed || value.Status == run.StatusInfrastructureFailed || value.Status == run.StatusCanceled {
					t.Fatalf("successor failed %+v failure=%+v", value, value.Failure)
				}
				if value.Status != run.StatusSucceeded {
					ready = false
				}
				if s, ok := f.plane.Supervisor.GetSession(id); ok {
					select {
					case <-s.Done():
					default:
						ready = false
					}
				}
				var claims int
				if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, id).Scan(&claims); e != nil {
					t.Fatal(e)
				}
				if claims != 0 {
					ready = false
				}
			}
			if ready {
				submitCompletedFixtureVerificationAnalysis(t, f, nextID)
			}
			next, err = f.facade.GetTask(f.ctx, f.task)
			if err != nil {
				t.Fatal(err)
			}
			if ready && next.Phase == task.PhaseAcceptancePending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("successor did not reach real acceptance: %+v proposal=%+v", next, proposal)
			}
			time.Sleep(20 * time.Millisecond)
		}
		var implementations, verifications int
		if err = f.plane.DB.SQL().QueryRow(`SELECT sum(purpose='IMPLEMENTATION'),sum(purpose='VERIFICATION') FROM runs WHERE task_id=?`, f.task).Scan(&implementations, &verifications); err != nil || implementations != 2 || verifications != 1 {
			t.Fatalf("unexpected attempts impl=%d verify=%d %v", implementations, verifications, err)
		}
		stable := definitionFormalSnapshot(t, f, []string{"runs", "execution_proposals", "run_execution_records", "run_analyses"})
		_ = conf(call("confirmation_commit", commitArgs))
		replay, err = f.facade.StartApprovedTaskExecution(f.ctx, launch)
		if replay.Proposal.ID != oldID || replay.Run.ID != old.ID || stable != definitionFormalSnapshot(t, f, []string{"runs", "execution_proposals", "run_execution_records", "run_analyses"}) {
			t.Fatalf("late replay redispatched %+v %v", replay, err)
		}
		retained, err := f.plane.Runs.Get(f.ctx, old.ID)
		if err != nil || !reflect.DeepEqual(old, retained) {
			t.Fatalf("original failure changed: %+v %v", retained, err)
		}
		mu.Lock()
		hitCount := len(observed)
		mu.Unlock()
		if hitCount != 1 {
			t.Fatalf("fault hit count %d", hitCount)
		}
		t.Logf("exact attachment abort Run=%s; no runtime started; public confirmed retry Execution=%s reached ACCEPTANCE_PENDING with unique implementation+verification; original failure preserved", old.ID, nextID)
		return false
	}, func(f *upperLifecycleFixture, inventory c.Intent, in controlapi.ApprovedTaskStartInput) (executionproposal.StartResult, error) {
		launch = in
		if _, err := f.facade.DiscardConfirmationProposal(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: inventory.IntentID, Version: inventory.Version, Purpose: inventory.Purpose, ContentHash: inventory.ContentHash}); err != nil {
			t.Fatal(err)
		}
		admitted, err := f.facade.StartApprovedTaskExecution(f.ctx, in)
		if err != nil && admitted.Run.ID != "" && admitted.Proposal.ID != "" {
			actual, readErr := f.plane.Runs.Get(f.ctx, admitted.Run.ID)
			if readErr == nil && actual.Status == run.StatusInfrastructureFailed && actual.ExecutionProposalID == admitted.Proposal.ID && actual.Failure != nil && actual.Failure.Code == "RUNTIME_WORKSET_PREPARATION_FAILED" && strings.Contains(actual.Failure.Message, "TEST_ONLY_IMPLEMENTATION_ATTACH_ABORT") {
				t.Logf("initial start surfaced exact controlled preparation failure: %v; durable failure=%+v", err, actual.Failure)
				return admitted, nil // Fixture expects success; the callback verifies this exact intentional failure.
			}
		}
		return admitted, err
	}, func(f *upperLifecycleFixture) {
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
		ddl := fmt.Sprintf(`CREATE TRIGGER implementation_attach_abort BEFORE UPDATE OF workspace_id ON runs WHEN NEW.task_id=%s AND NEW.purpose='IMPLEMENTATION' AND OLD.workspace_id IS NULL AND NEW.workspace_id IS NOT NULL BEGIN SELECT %s(NEW.id,NEW.execution_proposal_id); SELECT RAISE(ABORT,'TEST_ONLY_IMPLEMENTATION_ATTACH_ABORT'); END`, quote(f.task), observer)
		if _, err := f.plane.DB.SQL().Exec(ddl); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER IF EXISTS implementation_attach_abort`); err != nil {
				t.Error(err)
			}
		})
	})
}
