package controlapi_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// The Review is executed through the existing separate-process app-server
// fixture. Only its canonical materialized result is used for APPROVE.
func withDefinitionOnlyCodingReview(t *testing.T, check func(*upperLifecycleFixture, task.Task, review.Review)) {
	t.Helper()
	runCodingReviewFixture(t, func(f *upperLifecycleFixture, taskID, reviewID, runID string) bool {
		t := f.t
		if strings.HasSuffix(t.Name(), "/mutation-blocked") {
			return true
		}
		deadline := time.Now().Add(20 * time.Second)
		var raw string
		for {
			err := f.plane.DB.SQL().QueryRow(`SELECT m.formal_result_json FROM review_result_materializations m JOIN runs r ON r.id=m.run_id WHERE m.review_id=? AND r.id=? AND m.outcome='VALID' AND r.status='SUCCEEDED' AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=r.feature_area_id)`, reviewID, runID).Scan(&raw)
			if err == nil {
				break
			}
			if current, readErr := f.facade.GetRunForSession(f.ctx, f.session, runID); readErr == nil && current.Status == run.StatusSucceeded && current.AnalysisState == "REQUIRED" {
				submitNaturalFixtureAnalysis(t, f, runID, []byte(os.Getenv("DEVBRIDGE_CODING_V1_REVIEW_RESULT")))
				continue
			}
			if time.Now().After(deadline) {
				t.Fatalf("real Coding Review did not settle: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		var result review.ImplementationResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		original, err := f.plane.Reviews.Get(f.ctx, reviewID)
		if err != nil || original.BackendSelection == nil || original.RuntimeConfig.Model == "" {
			t.Fatalf("Review has no frozen runtime: %+v %v", original, err)
		}
		f.task, f.review = taskID, reviewID
		parent, err := f.facade.GetTask(f.ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "definition-only-approve", Purpose: c.Execution, Action: "APPROVE", Execution: &c.ExecutionBody{Action: "APPROVE", TaskID: taskID, FeatureAreaID: f.area, ReviewID: reviewID, DecisionID: wp.DecisionID(reviewID), TaskDefinitionRevisionID: parent.CurrentDefinitionRevisionID, ExecutionID: parent.CurrentExecutionID}, Variants: []c.Variant{{ID: "approve", Summary: "Approve the reviewed definition without runtime start", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: result.RepositoryTargets, PrimaryMemberID: result.PrimaryMemberID, WorkSpec: &result.WorkSpec, VerificationSpec: &result.VerificationSpec}}})
		if err != nil {
			t.Fatal(err)
		}
		approved, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "definition-only-approve-commit", ContentHash: prepared.ContentHash, VariantID: "approve"})
		if err != nil || approved.State != "COMPLETED" {
			t.Fatalf("NO_START approval=%+v %v", approved, err)
		}
		current, err := f.facade.GetTask(f.ctx, taskID)
		if err != nil || current.CurrentExecutionID != "" || current.Phase != task.PhaseImplementationReady {
			t.Fatalf("definition-only Task=%+v %v", current, err)
		}
		var inheritedRuntime, inheritedBackend string
		if err := f.plane.DB.SQL().QueryRow(`SELECT runtime_config_json,backend_selection_json FROM review_approval_materializations WHERE task_id=? AND initial_revision_id=?`, current.ID, current.CurrentDefinitionRevisionID).Scan(&inheritedRuntime, &inheritedBackend); err != nil {
			t.Fatal(err)
		}
		var frozenRuntime codexruntime.NextRunRuntimeConfig
		var frozenBackend codexconfig.BackendSelection
		if err := json.Unmarshal([]byte(inheritedRuntime), &frozenRuntime); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(inheritedBackend), &frozenBackend); err != nil {
			t.Fatal(err)
		}
		if frozenRuntime != original.RuntimeConfig || frozenBackend.ConfigID != original.BackendSelection.ConfigID || frozenBackend.ConfigFingerprint != original.BackendSelection.ConfigFingerprint {
			t.Fatalf("fixture did not preserve Review metadata: %s %s", inheritedRuntime, inheritedBackend)
		}
		// Stop background continuation so only the explicit command under test
		// may initiate implementation. The real Core/SQLite pipeline stays wired.
		f.plane.Retirement.Close()
		check(f, current, original)
		return false
	}, func(f *upperLifecycleFixture) {
		// The common fixture previously used a different Task in its original
		// area. A fresh canonical area keeps that unrelated workspace history
		// outside this Task's Review-to-Implementation preparation authority.
		t := f.t
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "definition-only-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Definition-only start", ProblemStatement: "Review and implement exact members"}, Variants: []c.Variant{{ID: "create", Summary: "Create isolated area"}}})
		if err != nil {
			t.Fatal(err)
		}
		created, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "definition-only-area-commit", ContentHash: prepared.ContentHash, VariantID: "create"})
		if err != nil || created.State != "COMPLETED" {
			t.Fatalf("new area=%s %v", created.State, err)
		}
		f.area = created.Steps[0].FeatureAreaID
		for i := range f.targets {
			f.targets[i].MemberID = ""
			f.targets[i].MemberVersion = 0
		}
		planned, err := f.plane.Workspaces.PlanMembers(f.ctx, f.project, f.area, f.targets)
		if err != nil {
			t.Fatal(err)
		}
		f.targets = planned.Targets
		if _, err := f.plane.Workspaces.EnsurePlannedMembers(f.ctx, f.project, f.area, planned.WorkspaceID, planned.Targets); err != nil {
			t.Fatal(err)
		}
	})
}

func definitionOnlyStart(t *testing.T, f *upperLifecycleFixture, current task.Task) (c.Intent, c.Options) {
	t.Helper()
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "definition-explicit-start", Purpose: c.Execution, Action: "START_APPROVED", Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: current.ID, FeatureAreaID: f.area, ReviewID: f.review, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: expected.ExecutionID, ExpectedTarget: &expected}, Variants: []c.Variant{{ID: "start", Summary: "Start with the explicitly selected implementation runtime", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	if err != nil || len(opts.BackendCandidates) == 0 || opts.LockedRuntimeConfig != nil {
		t.Fatalf("definition-only options=%+v %v", opts, err)
	}
	return prepared, opts
}

func differentDefinitionRuntime(t *testing.T, opts c.Options, old codexruntime.NextRunRuntimeConfig) codexruntime.NextRunRuntimeConfig {
	t.Helper()
	for _, model := range opts.RuntimeOptions {
		for _, effort := range model.SupportedReasoningEfforts {
			if model.ModelID != old.Model || effort != old.ReasoningEffort {
				return codexruntime.NextRunRuntimeConfig{Model: model.ModelID, ReasoningEffort: effort}
			}
		}
	}
	t.Fatal("fixture has no distinct runtime")
	return codexruntime.NextRunRuntimeConfig{}
}

func TestDefinitionOnlyCodingReviewUsesExplicitStartSelection(t *testing.T) {
	withDefinitionOnlyCodingReview(t, func(f *upperLifecycleFixture, current task.Task, prior review.Review) {
		t := f.t
		alternate, wire := definitionOnlyAlternateBackend(t, f)
		prepared, opts := definitionOnlyStart(t, f, current)
		selectedRuntime := differentDefinitionRuntime(t, opts, prior.RuntimeConfig)
		backend := alternate.Selection("SESSION_INVENTORY")
		found := false
		for _, option := range opts.BackendCandidates {
			if option.ConfigID == backend.ConfigID && option.ConfigFingerprint == backend.ConfigFingerprint {
				found = true
			}
		}
		if !found || opts.RuntimeSelectionMode != "EXPLICIT_SELECTION" || opts.LockedBackend != nil || backend.ConfigID == prior.BackendSelection.ConfigID {
			t.Fatal("reset did not expose a distinct explicit backend")
		}
		commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "definition-explicit-start-commit", ContentHash: prepared.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &selectedRuntime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}}
		startInput := controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: current.ID, RequestID: "definition-explicit-start-command", ExpectedTarget: *prepared.Content.Execution.ExpectedTarget, RuntimeConfig: selectedRuntime, Backend: *commit.Selection.Backend}
		if _, err := f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(prepared)); err != nil {
			t.Fatal(err)
		}
		launched, err := f.facade.StartApprovedTaskExecution(f.ctx, startInput)
		startErr := err
		key := c.ValueHash([]string{"devbridge.approved-task-start.v1", f.session, startInput.RequestID})
		started, readErr := f.plane.Confirmations.ReplayPrepare(f.ctx, f.session, "approved_task_start_prepare_"+key, c.ValueHash(startInput))
		if readErr != nil {
			t.Fatal(readErr)
		}
		commit.IntentID, commit.Version, commit.ContentHash, commit.RequestID = started.IntentID, started.Version, started.ContentHash, "approved_task_start_commit_"+key
		if len(started.Steps) != 1 || started.Steps[0].ExecutionID == "" {
			t.Fatalf("explicit start=%+v %v", started, err)
		}
		proposal, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, started.Steps[0].ExecutionID)
		if err != nil || proposal.RuntimeConfig != selectedRuntime || proposal.BackendSelection == nil || proposal.BackendSelection.ConfigID != backend.ConfigID || proposal.BackendSelection.ConfigFingerprint != backend.ConfigFingerprint {
			t.Fatalf("explicit selection ignored: Execution runtime=%+v backend=%+v; selected runtime=%+v backend=%+v; inherited Review=%+v err=%v", proposal.RuntimeConfig, proposal.BackendSelection, selectedRuntime, backend, prior.RuntimeConfig, err)
		}
		if startErr != nil || started.State != "COMPLETED" || started.Steps[0].RunID != "" || started.Steps[0].Effect != "PREPARATION_READY" || launched.Run.ID == "" {
			var detail string
			_ = f.plane.DB.SQL().QueryRow(`SELECT COALESCE(failure_message,'') FROM runs WHERE id=?`, started.Steps[0].RunID).Scan(&detail)
			t.Fatalf("explicit start=%s %v run failure=%s", started.State, startErr, detail)
		}
		runID := launched.Run.ID
		var model, effort, configID, fingerprint string
		if err = f.plane.DB.SQL().QueryRow(`SELECT p.model,p.reasoning_effort,p.backend_identity,p.backend_config_fingerprint FROM runtime_scope_plans p JOIN runtime_preparation_attempts a ON a.plan_id=p.plan_id WHERE a.run_id=?`, runID).Scan(&model, &effort, &configID, &fingerprint); err != nil {
			t.Fatal(err)
		}
		if model != selectedRuntime.Model || effort != selectedRuntime.ReasoningEffort || configID != backend.ConfigID || fingerprint != backend.ConfigFingerprint {
			t.Fatalf("Run plan ignored explicit selection: %s/%s %s/%s", model, effort, configID, fingerprint)
		}
		waitDefinitionRuntimeSession(t, f, runID, proposal.ID)
		data, err := os.ReadFile(wire)
		if err != nil {
			t.Fatal(err)
		}
		turns := 0
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var entry struct {
				Method string `json:"method"`
				Params struct {
					Model  string `json:"model"`
					Effort string `json:"effort"`
				} `json:"params"`
			}
			if err = json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Method == "turn/start" {
				turns++
				if entry.Params.Model != selectedRuntime.Model || entry.Params.Effort != selectedRuntime.ReasoningEffort {
					t.Fatalf("runtime wire selection=%+v", entry.Params)
				}
			}
		}
		if turns != 2 {
			t.Fatalf("explicit Implementation and Verification runtime turns=%d", turns)
		}
		settled, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, proposal.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, phase := range []string{"same-process", "cold-reopen"} {
			if phase == "cold-reopen" {
				reopenDirectDeliveryFixture(t, f)
				f.plane.Retirement.Close()
				waitDefinitionRuntimeSession(t, f, runID, proposal.ID)
			}
			// Both runtime phases have joined before either replay baseline.
			// Restart must retain the exact frozen start identity.
			baseline, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, proposal.ID)
			if err != nil || baseline.ID != settled.ID || baseline.RunID != settled.RunID || baseline.TaskDefinitionRevisionID != settled.TaskDefinitionRevisionID || baseline.RuntimeConfig != settled.RuntimeConfig || !reflect.DeepEqual(baseline.BackendSelection, settled.BackendSelection) {
				t.Fatalf("%s restart lost immutable start identity: %v", phase, err)
			}
			before := definitionRuntimeSnapshot(t, f, true)
			replay, err := f.facade.CommitConfirmation(f.ctx, commit)
			if err != nil || replay.State != "COMPLETED" || !reflect.DeepEqual(replay.Steps, started.Steps) {
				t.Fatalf("%s original request replay=%s %+v %v", phase, replay.State, replay.Steps, err)
			}
			if after := definitionRuntimeSnapshot(t, f, true); after != before {
				t.Fatalf("%s completed replay wrote durable facts:\n%s", phase, definitionRuntimeSnapshotDiff(t, f, before, after))
			}
			currentProposal, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, proposal.ID)
			if err != nil || !reflect.DeepEqual(currentProposal, baseline) {
				t.Fatalf("%s replay rewrote Execution: %v", phase, err)
			}
			conflict := commit
			conflict.Selection = commit.Selection
			conflict.Selection.RuntimeConfig = &prior.RuntimeConfig
			if _, err = f.facade.CommitConfirmation(f.ctx, conflict); err == nil {
				t.Fatalf("%s original request accepted changed selection", phase)
			}
			if after := definitionRuntimeSnapshot(t, f, true); after != before {
				t.Fatalf("%s conflicting original request wrote durable facts:\n%s", phase, definitionRuntimeSnapshotDiff(t, f, before, after))
			}

			conflict.RequestID = "definition-different-conflicting-" + phase
			if _, err = f.facade.CommitConfirmation(f.ctx, conflict); err == nil {
				t.Fatalf("%s different request accepted conflicting selection", phase)
			}
			if after := definitionRuntimeSnapshot(t, f, true); after != before {
				t.Fatalf("%s conflicting new request wrote durable facts:\n%s", phase, definitionRuntimeSnapshotDiff(t, f, before, after))
			}
		}
	})
}

// This second real app-server fixture gets a distinct backend thread after the
// canonical context reset; the prior Review helper keeps its original thread.
func TestDefinitionOnlyRuntimeAppServerHelper(t *testing.T) {
	if os.Getenv("DEVBRIDGE_DEFINITION_START_HELPER") != "1" {
		return
	}
	write := func(v any) { raw, _ := json.Marshal(v); fmt.Fprintln(os.Stdout, string(raw)) }
	var roots []string
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(51)
		}
		file, err := os.OpenFile(os.Getenv("DEVBRIDGE_DEFINITION_START_WIRE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(52)
		}
		_, _ = file.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
		_ = file.Close()
		switch req.Method {
		case "config/read":
			write(map[string]any{"id": req.ID, "result": map[string]any{"config": map[string]any{}}})
		case "mcpServerStatus/list":
			write(map[string]any{"id": req.ID, "result": map[string]any{"data": []any{}, "nextCursor": nil}})
		case "initialize":
			write(map[string]any{"id": req.ID, "result": map[string]any{"userAgent": "definition-only runtime fixture"}})
		case "initialized":
		case "thread/name/set", "thread/inject_items":
			write(map[string]any{"id": req.ID, "result": map[string]any{}})
		case "thread/start", "thread/resume":
			var in struct {
				CWD                   string   `json:"cwd"`
				Roots                 []string `json:"runtimeWorkspaceRoots"`
				Permissions           string   `json:"permissions"`
				DeveloperInstructions string   `json:"developerInstructions"`
			}
			if json.Unmarshal(req.Params, &in) != nil {
				os.Exit(53)
			}
			roots = in.Roots
			if in.Permissions != "" {
				info := codingFixturePermissionInfo(in.DeveloperInstructions)
				roots = nil
				if len(info.Copies) > 0 {
					for _, copy := range info.Copies {
						roots = append(roots, copy.Root)
					}
				} else {
					for _, root := range in.Roots {
						if root != info.Scratch {
							roots = append(roots, root)
						}
					}
				}
			}
			if len(roots) != 2 {
				os.Exit(53)
			}
			result := map[string]any{"thread": map[string]any{"id": "thread-definition-start"}, "cwd": in.CWD}
			if in.Permissions != "" {
				result["activePermissionProfile"] = map[string]any{"id": in.Permissions}
			}
			write(map[string]any{"id": req.ID, "result": result})
		case "turn/start":
			var params struct {
				OutputSchema json.RawMessage `json:"outputSchema"`
				Input        []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if json.Unmarshal(req.Params, &params) != nil || len(roots) != 2 {
				os.Exit(54)
			}
			implementation := bytes.Contains(params.OutputSchema, []byte("resultDisposition"))
			if len(params.OutputSchema) == 0 {
				// The fixture recognizes its two authored prompts. Native turns
				// no longer carry the old final-message schema discriminator.
				if len(params.Input) != 1 {
					os.Exit(60)
				}
				implementation = strings.Contains(params.Input[0].Text, "Update same.txt")
				if !implementation && !strings.Contains(params.Input[0].Text, "Verify both exact member contracts") {
					os.Exit(61)
				}
			}
			turnID := "turn-definition-verification"
			if implementation {
				turnID = "turn-definition-start"
			}
			write(map[string]any{"id": req.ID, "result": map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}}})
			for i, root := range roots {
				if implementation {
					before, err := os.ReadFile(filepath.Join(root, "same.txt"))
					if err != nil {
						os.Exit(55)
					}
					name := ""
					if strings.Contains(string(before), "app source") {
						name = "app"
					} else if strings.Contains(string(before), "sdk source") {
						name = "sdk"
					} else {
						os.Exit(56)
					}
					if os.WriteFile(filepath.Join(root, "same.txt"), []byte("coded "+name+"\n"), 0600) != nil {
						os.Exit(57)
					}
				} else {
					process := osexec.Command("/bin/sh", "-c", "test -f same.txt")
					process.Dir = root
					output, err := process.CombinedOutput()
					if err != nil {
						os.Exit(59)
					}
					item := map[string]any{"id": fmt.Sprintf("definition-check-%d", i), "type": "commandExecution", "command": "test -f same.txt", "cwd": root, "status": "completed", "commandActions": []any{map[string]any{"type": "unknown", "command": "test -f same.txt"}}, "exitCode": 0, "aggregatedOutput": string(output)}
					write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-definition-start", "turnId": turnID, "item": item}})
				}
			}
			message := `{"resultDisposition":"COMPLETED","summary":"updated both exact members"}`
			if !implementation {
				message = `{"version":2,"summary":"both exact checks passed","checks":[{"index":0,"status":"PASSED","summary":"first member file exists","evidence":"test -f same.txt exited 0","reason":""},{"index":1,"status":"PASSED","summary":"second member file exists","evidence":"test -f same.txt exited 0","reason":""}],"acceptanceCriteria":[{"index":0,"status":"PASSED","summary":"both contracts remain present","evidence":"two commands exited 0","reason":""}]}`
			}
			if len(params.OutputSchema) == 0 {
				message = "Completed the exact fixture operation."
			}
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-definition-start", "turnId": turnID, "item": map[string]any{"id": "result-" + turnID, "type": "agentMessage", "text": message, "phase": "final_answer"}}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-definition-start", "turn": map[string]any{"id": turnID, "status": "completed", "items": []any{}}}})
		default:
			os.Exit(58)
		}
	}
	os.Exit(0)
}

func definitionOnlyAlternateBackend(t *testing.T, f *upperLifecycleFixture) (codexconfig.Config, string) {
	t.Helper()
	if _, err := f.facade.ResetFeatureAreaAgentContext(f.ctx, f.session, f.area, "definition-start-reset"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	launcher, wire := filepath.Join(root, "alternate-codex"), filepath.Join(root, "wire.jsonl")
	executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestDefinitionOnlyRuntimeAppServerHelper$\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVBRIDGE_DEFINITION_START_HELPER", "1")
	t.Setenv("DEVBRIDGE_DEFINITION_START_WIRE", wire)
	now := time.Now().UTC()
	config := codexconfig.Config{ID: "definition_alternate_backend", Name: "Explicit implementation backend", CodexPath: launcher, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewCodexConfigStore(f.plane.DB).Create(f.ctx, config); err != nil {
		t.Fatal(err)
	}
	installProtocolFixturePlatform(t, f)
	return config, wire
}

// Take values, not just counts: a rejected request must not rewrite pointers,
// receipts or preparation identities while leaving row counts unchanged.
func definitionRuntimeSnapshot(t *testing.T, f *upperLifecycleFixture, confirmations bool) string {
	t.Helper()
	tables := []string{"tasks", "task_definition_revisions", "task_plans", "execution_proposals", "execution_start_effects", "execution_proposal_admissions", "operator_task_execution_start_receipts", "runs", "run_repository_bindings", "runtime_scope_plans", "runtime_preparation_authorities", "runtime_preparation_attempts", "runtime_workspace_preparation_claims", "runtime_workspace_observations", "runtime_workset_bindings", "runtime_start_authorities", "runtime_preparation_receipts", "runtime_handoffs", "review_approval_materializations", "review_approval_decisions", "formal_execution_t1", "formal_execution_t2", "formal_execution_receipts", "formal_adoption_business", "formal_adoption_receipts", "runtime_selection_sources", "runtime_selection_bindings"}
	if confirmations {
		tables = append(tables, "confirmation_intents", "confirmation_commit_requests")
	}
	all := map[string][][]any{}
	for _, table := range tables {
		rows, err := f.plane.DB.SQL().Query(`SELECT * FROM ` + table + ` ORDER BY 1,2`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, v := range values {
				if raw, ok := v.([]byte); ok {
					values[i] = string(raw)
				}
			}
			all[table] = append(all[table], values)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	raw, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDefinitionOnlyInheritedReviewRuntimeCannotAuthorizeRawStart(t *testing.T) {
	withDefinitionOnlyCodingReview(t, func(f *upperLifecycleFixture, current task.Task, _ review.Review) {
		t := f.t
		store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
		revision, err := f.plane.Tasks.GetDefinitionRevision(f.ctx, current.CurrentDefinitionRevisionID)
		if err != nil {
			t.Fatal(err)
		}
		targetsJSON, err := json.Marshal(revision.Definition.RepositoryTargets)
		if err != nil {
			t.Fatal(err)
		}
		expected := &wp.TaskExecutionExpectation{FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, Phase: task.PhaseImplementationReady, PrimaryMemberID: revision.Definition.PrimaryMemberID, ApprovedWorksetHash: fmt.Sprintf("%x", sha256.Sum256(targetsJSON))}
		for _, stage := range []string{"without-intent", "prepared-but-uncommitted"} {
			if stage == "prepared-but-uncommitted" {
				definitionOnlyStart(t, f, current)
			}
			before := definitionRuntimeSnapshot(t, f, true)
			if _, _, err := store.ReserveTaskExecutionExpectedCommand(f.ctx, f.session, current.ID, "START_EXECUTION", "raw-"+stage, expected); !errors.Is(err, runtimeselection.ErrBindingMissing) {
				t.Fatalf("%s raw start=%v", stage, err)
			}
			if after := definitionRuntimeSnapshot(t, f, true); after != before {
				t.Fatalf("%s rejected raw start changed durable facts", stage)
			}
		}
	})
}

func TestRuntimeAuthorizedApprovalKeepsStartSelectionLocked(t *testing.T) {
	f, id, ref := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	prior, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitApproveAndStart(f.ctx, id, ref)
	if err != nil {
		t.Fatal(err)
	}
	selections := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	source, err := selections.ReadSource(f.ctx, prior.T1.Identity.SourceOriginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = selections.MaterializeBinding(f.ctx, runtimeselection.BindingInput{RequestID: "runtime_binding_" + source.SourceID, Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: ref.LineageVersion}); err != nil {
		t.Fatal(err)
	}
	backend, frozen := prior.Archive.Backend, prior.Archive.Runtime
	config, err := sqlite.NewCodexConfigStore(f.plane.DB).Get(f.ctx, backend.ConfigID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, current.CurrentExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "locked-start", Purpose: c.Execution, Action: "START_APPROVED", Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: current.ID, FeatureAreaID: f.area, ReviewID: f.review, ExecutionID: current.CurrentExecutionID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExpectedTarget: &wp.TaskExecutionExpectation{FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: current.CurrentExecutionID, Phase: task.PhaseImplementationReady, PrimaryMemberID: current.PrimaryMemberID, ApprovedWorksetHash: proposal.ApprovedWorksetHash}}, Variants: []c.Variant{{ID: "start", Summary: "Start authorized runtime", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	if err != nil || opts.RuntimeSelectionMode != "FROZEN_EXECUTION" || opts.LockedRuntimeConfig == nil || *opts.LockedRuntimeConfig != frozen || opts.LockedBackend == nil || opts.LockedBackend.ConfigID != backend.ConfigID || opts.LockedBackend.ConfigFingerprint != backend.ConfigFingerprint {
		t.Fatalf("authorized runtime lost lock: %+v %v", opts, err)
	}
	changed := differentDefinitionRuntime(t, opts, frozen)
	before := definitionRuntimeSnapshot(t, f, false)
	_, err = f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "locked-start-change", ContentHash: prepared.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &changed, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}})
	if err == nil || !strings.Contains(err.Error(), "FROZEN_RUNTIME_SELECTION_CONFLICT") {
		t.Fatalf("locked runtime override=%v", err)
	}
	if after := definitionRuntimeSnapshot(t, f, false); after != before {
		t.Fatal("rejected locked-runtime override changed runtime or approval facts")
	}
	alternate := config
	alternate.ID = "locked_alternate_backend"
	alternate.Name = "Another enabled backend"
	if err = sqlite.NewCodexConfigStore(f.plane.DB).Create(f.ctx, alternate); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(prepared)); err != nil {
		t.Fatal(err)
	}
	next := prepared.Content
	next.RequestID = "locked-start-backend"
	next.SubmittedHash = ""
	prepared, err = f.facade.PrepareConfirmation(f.ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	before = definitionRuntimeSnapshot(t, f, false)
	_, err = f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "locked-start-backend-change", ContentHash: prepared.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &frozen, Backend: &codexconfig.BackendSelectionInput{ConfigID: alternate.ID, ConfigFingerprint: alternate.Fingerprint()}}})
	if err == nil || !strings.Contains(err.Error(), "FROZEN_BACKEND_SELECTION_CONFLICT") {
		t.Fatalf("locked backend override=%v", err)
	}
	if after := definitionRuntimeSnapshot(t, f, false); after != before {
		t.Fatal("rejected locked-backend override changed runtime or approval facts")
	}
}

func TestDefinitionOnlyConsumedSelectionIsBoundToExactActorAndStep(t *testing.T) {
	withDefinitionOnlyCodingReview(t, func(f *upperLifecycleFixture, current task.Task, prior review.Review) {
		t := f.t
		prepared, opts := definitionOnlyStart(t, f, current)
		runtime := differentDefinitionRuntime(t, opts, prior.RuntimeConfig)
		backend := opts.BackendCandidates[0]
		commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "selection-before-effect", ContentHash: prepared.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}}
		commit.SubmittedHash = c.ValueHash(commit)
		commit.BackendSelection = &backend
		step := c.Step{Kind: "START_IMPLEMENTATION", RequestID: "confirmation_" + c.ValueHash([]string{prepared.IntentID, "START_IMPLEMENTATION"})[:32], State: "PENDING"}
		// Stop at the real Core unique-consumption transaction, before any
		// business effect. Consumption records this exact selection but cannot
		// replace the final formal source and revision binding.
		consumed, fresh, err := sqlite.NewConfirmationStore(f.plane.DB).BeginCommit(f.ctx, commit, []c.Step{step})
		if err != nil || !fresh || consumed.State != "CONFIRMING" || consumed.Submission == nil || consumed.Submission.Outcome != c.Consumed {
			t.Fatalf("selection consumption=%+v fresh=%t %v", consumed.Submission, fresh, err)
		}
		expected := prepared.Content.Execution.ExpectedTarget
		store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
		before := definitionRuntimeSnapshot(t, f, true)
		if _, _, err = store.ReserveTaskExecutionExpectedCommand(f.ctx, f.session, current.ID, "START_EXECUTION", "different-step-request", expected); !errors.Is(err, runtimeselection.ErrBindingMissing) {
			t.Fatalf("different step borrowed consumed selection: %v", err)
		}
		if after := definitionRuntimeSnapshot(t, f, true); after != before {
			t.Fatal("different step request wrote durable facts")
		}
		ingress, err := localoperator.NewIngress("definition-selection-test")
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "http://127.0.0.1:4777/operator", nil)
		req.RemoteAddr = "127.0.0.1:52222"
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4777}))
		operatorCtx, err := ingress.AdmitHTTP(req)
		if err != nil {
			t.Fatal(err)
		}
		operatorCtx, err = localoperator.BindTask(operatorCtx, f.project, current.ID, "START_EXECUTION")
		if err != nil {
			t.Fatal(err)
		}
		access, _ := localoperator.FromContext(operatorCtx)
		if _, _, err = store.ReserveTaskExecutionExpectedForOperator(operatorCtx, f.project, current.ID, "START_EXECUTION", access.PrincipalID(), step.RequestID, expected); !errors.Is(err, runtimeselection.ErrBindingMissing) {
			t.Fatalf("operator borrowed AI selection: %v", err)
		}
		if after := definitionRuntimeSnapshot(t, f, true); after != before {
			t.Fatal("operator request wrote durable facts")
		}
		admitted, reserved, err := store.ReserveTaskExecutionExpectedCommand(f.ctx, f.session, current.ID, "START_EXECUTION", step.RequestID, expected)
		if !errors.Is(err, runtimeselection.ErrBindingMissing) || admitted.ID != "" || reserved.ID != "" {
			t.Fatalf("consumed selection bypassed formal source and binding: execution=%s run=%s %v", admitted.ID, reserved.ID, err)
		}
		if after := definitionRuntimeSnapshot(t, f, true); after != before {
			t.Fatal("consumed-only start created authority or runtime work")
		}
	})
}

// A field-level diff distinguishes a writer finishing in the background from
// an accidental mutation of the replayed receipt without omitting either.
func definitionRuntimeSnapshotDiff(t *testing.T, f *upperLifecycleFixture, before, after string) string {
	t.Helper()
	var old, new map[string][][]any
	if err := json.Unmarshal([]byte(before), &old); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(after), &new); err != nil {
		t.Fatal(err)
	}
	tables := make([]string, 0, len(old))
	for table := range old {
		tables = append(tables, table)
	}
	for table := range new {
		if _, exists := old[table]; !exists {
			tables = append(tables, table)
		}
	}
	sort.Strings(tables)
	var out strings.Builder
	short := func(value any) string {
		raw, _ := json.Marshal(value)
		if len(raw) > 220 {
			raw = append(raw[:220], []byte("...")...)
		}
		return string(raw)
	}
	for _, table := range tables {
		a, b := old[table], new[table]
		if reflect.DeepEqual(a, b) {
			continue
		}
		fmt.Fprintf(&out, "%s rows %d -> %d\n", table, len(a), len(b))
		rows, err := f.plane.DB.SQL().Query(`SELECT * FROM ` + table + ` LIMIT 0`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		limit := len(a)
		if len(b) < limit {
			limit = len(b)
		}
		changed := 0
		for row := 0; row < limit && changed < 8; row++ {
			for col := range a[row] {
				if !reflect.DeepEqual(a[row][col], b[row][col]) {
					fmt.Fprintf(&out, "  row %d %s: %s -> %s\n", row, columns[col], short(a[row][col]), short(b[row][col]))
					changed++
					if changed >= 8 {
						break
					}
				}
			}
		}
	}
	return out.String()
}

// Implementation SUCCEEDED and claim release precede the same executor's
// Verification reservation and completion callback. Join the supervisor
// session, including its rekeyed Verification owner, before freezing a baseline.
func waitDefinitionRuntimeSession(t *testing.T, f *upperLifecycleFixture, runID, executionID string) {
	t.Helper()
	live, ok := f.plane.Supervisor.GetSession(runID)
	if !ok {
		proposal, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, executionID)
		if err != nil {
			t.Fatal(err)
		}
		if proposal.VerificationRunID != "" {
			live, ok = f.plane.Supervisor.GetSession(proposal.VerificationRunID)
		}
	}
	if ok {
		waitCtx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
		defer cancel()
		terminal, err := live.Wait(waitCtx)
		if err != nil || terminal.Status != "SUCCEEDED" || terminal.Purpose != "VERIFICATION" {
			t.Fatalf("joined runtime failed: run=%s purpose=%s status=%s failure=%+v err=%v", terminal.ID, terminal.Purpose, terminal.Status, terminal.Failure, err)
		}
	}
	// Removal follows all execute-defer persistence. A very fast completed
	// session still must have the same successful durable terminal evidence.
	implemented, err := f.plane.Runs.Get(f.ctx, runID)
	if err != nil || implemented.Status != "SUCCEEDED" || implemented.Purpose != "IMPLEMENTATION" {
		t.Fatalf("settled Implementation=%s/%s err=%v", implemented.Status, implemented.Purpose, err)
	}
	proposal, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, executionID)
	if err != nil || proposal.VerificationRunID == "" || proposal.Outcome != executionproposal.OutcomeSucceeded {
		t.Fatalf("settled Execution outcome=%s verification=%s err=%v", proposal.Outcome, proposal.VerificationRunID, err)
	}
	verified, err := f.plane.Runs.Get(f.ctx, proposal.VerificationRunID)
	if err != nil || verified.Status != "SUCCEEDED" || verified.Purpose != "VERIFICATION" {
		t.Fatalf("settled Verification=%s/%s err=%v", verified.Status, verified.Purpose, err)
	}
}
