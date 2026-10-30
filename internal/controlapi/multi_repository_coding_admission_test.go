package controlapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

// TestMultiRepositoryCodingV1AppServerHelper is a separate-process fake for
// the production Codex Client. It speaks the pinned JSON-RPC wire, performs
// the approved fixture edits/checks in managed worktrees, and records every
// request for exact-wire assertions. It never runs during ordinary test
// discovery.
func TestMultiRepositoryCodingV1AppServerHelper(t *testing.T) {
	if os.Getenv("DEVBRIDGE_CODING_V1_HELPER") != "1" {
		return
	}
	logPath := os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG")
	appendLog := func(method string, params json.RawMessage) {
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(41)
		}
		defer file.Close()
		encoded, _ := json.Marshal(map[string]any{"method": method, "params": json.RawMessage(params)})
		_, _ = file.Write(append(encoded, '\n'))
	}
	write := func(value any) {
		encoded, err := json.Marshal(value)
		if err != nil {
			os.Exit(42)
		}
		written, writeErr := fmt.Fprintln(os.Stdout, string(encoded))
		// Optional test-only observation after the exact write; no payload or
		// ordering changes and no inference that Core received the notification.
		if responseLog := os.Getenv("DEVBRIDGE_CODING_V1_RESPONSE_LOG"); responseLog != "" {
			file, err := os.OpenFile(responseLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				os.Exit(58)
			}
			record := map[string]any{"at": time.Now().UTC(), "pid": os.Getpid(), "message": json.RawMessage(encoded), "writtenBytes": written}
			if writeErr != nil {
				record["writeError"] = writeErr.Error()
			}
			err = json.NewEncoder(file).Encode(record)
			_ = file.Close()
			if err != nil {
				os.Exit(59)
			}
		}
	}
	type request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	type binding struct {
		CWD                   string   `json:"cwd"`
		RuntimeWorkspaceRoots []string `json:"runtimeWorkspaceRoots"`
		Permissions           string   `json:"permissions"`
		DeveloperInstructions string   `json:"developerInstructions"`
	}
	var roots, sourceRoots []string
	var cwd string
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var req request
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(43)
		}
		appendLog(req.Method, req.Params)
		switch req.Method {
		case "config/read":
			write(map[string]any{"id": req.ID, "result": map[string]any{"config": map[string]any{}}})
		case "mcpServerStatus/list":
			write(map[string]any{"id": req.ID, "result": map[string]any{"data": []any{}, "nextCursor": nil}})
		case "initialize":
			write(map[string]any{"id": req.ID, "result": map[string]any{"userAgent": "devbridge coding v1 fixture"}})
		case "initialized":
		case "thread/name/set", "thread/inject_items":
			write(map[string]any{"id": req.ID, "result": map[string]any{}})
		case "thread/start", "thread/resume":
			var value binding
			if json.Unmarshal(req.Params, &value) != nil {
				os.Exit(44)
			}
			roots, cwd = value.RuntimeWorkspaceRoots, value.CWD
			sourceRoots = roots
			info := codingFixturePermissionInfo(value.DeveloperInstructions)
			if value.Permissions != "" {
				roots, sourceRoots = nil, nil
				if len(info.Copies) > 0 {
					for _, copy := range info.Copies {
						roots = append(roots, copy.Root)
						sourceRoots = append(sourceRoots, copy.SourceRoot)
					}
				} else {
					for _, root := range value.RuntimeWorkspaceRoots {
						if root != info.Scratch {
							roots = append(roots, root)
						}
					}
					sourceRoots = roots
				}
			}
			if len(roots) != 2 {
				os.Exit(44)
			}
			result := map[string]any{"thread": map[string]any{"id": "thread-coding-v1"}, "cwd": cwd}
			if value.Permissions != "" {
				result["activePermissionProfile"] = map[string]any{"id": value.Permissions}
			}
			write(map[string]any{"id": req.ID, "result": result})
		case "turn/start":
			var value struct {
				OutputSchema json.RawMessage `json:"outputSchema"`
				Input        []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if json.Unmarshal(req.Params, &value) != nil || len(roots) != 2 {
				os.Exit(45)
			}
			implementation := bytes.Contains(value.OutputSchema, []byte("resultDisposition"))
			codingReview := bytes.Contains(value.OutputSchema, []byte("reviewDisposition"))
			if len(value.OutputSchema) == 0 && len(value.Input) == 1 {
				prompt := value.Input[0].Text
				codingReview = strings.Contains(prompt, "Review both exact") || strings.Contains(prompt, "Review the revised question")
				implementation = strings.Contains(prompt, "Change both exact") || strings.Contains(prompt, "Before editing,")
			}
			turnID := "turn-verification"
			if codingReview {
				turnID = "turn-review"
			} else if implementation {
				turnID = "turn-implementation"
			}
			turnID += os.Getenv("DEVBRIDGE_CODING_V1_TURN_SUFFIX")
			if os.Getenv("DEVBRIDGE_TEST_UNIQUE_TURNS") == "1" {
				turnID += fmt.Sprintf("-%d", time.Now().UnixNano())
			}
			write(map[string]any{"id": req.ID, "result": map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}}})
			if strings.Contains(os.Getenv("DEVBRIDGE_CODING_V1_ASK_USER"), turnID) {
				if !codingFixtureAskUser(scanner, turnID, write, appendLog) {
					continue
				}
			}
			if codingReview {
				if os.Getenv("DEVBRIDGE_CODING_V1_REVIEW_MUTATE") == "1" {
					if err := os.WriteFile(filepath.Join(sourceRoots[0], "same.txt"), []byte("mutated during review\n"), 0o600); err != nil {
						os.Exit(51)
					}
				}
			} else if implementation {
				for _, root := range roots {
					before, err := os.ReadFile(filepath.Join(root, "same.txt"))
					if err != nil {
						os.Exit(46)
					}
					name := ""
					if strings.Contains(string(before), "app source") {
						name = "app"
					} else if strings.Contains(string(before), "sdk source") {
						name = "sdk"
					} else if os.Getenv("DEVBRIDGE_CODING_V1_INTERRUPT_ONCE") != "" && (string(before) == "coded app\n" || string(before) == "coded sdk\n") {
						// A continuation must accept the prior partial work. Leave
						// these bytes intact instead of requiring a clean checkout.
						continue
					} else {
						os.Exit(47)
					}
					if err = os.WriteFile(filepath.Join(root, "same.txt"), []byte("coded "+name+"\n"), 0o600); err != nil {
						os.Exit(48)
					}
				}
			}
			if !implementation && !codingReview {
				byName := map[string]string{}
				for _, root := range roots {
					content, err := os.ReadFile(filepath.Join(root, "same.txt"))
					if err != nil {
						os.Exit(49)
					}
					if strings.Contains(string(content), "coded app") {
						byName["app"] = root
					} else if strings.Contains(string(content), "coded sdk") {
						byName["sdk"] = root
					}
				}
				sourceByName := map[string]string{}
				for _, root := range sourceRoots {
					content, err := os.ReadFile(filepath.Join(root, "same.txt"))
					if err != nil {
						os.Exit(49)
					}
					if strings.Contains(string(content), "coded app") {
						sourceByName["app"] = root
					} else if strings.Contains(string(content), "coded sdk") {
						sourceByName["sdk"] = root
					}
				}
				commands := []struct{ command, dir string }{
					{`test "$(cat same.txt)" = "coded app"`, byName["app"]},
					{`test "$(cat same.txt)" = "coded sdk"`, byName["sdk"]},
					{fmt.Sprintf(`test "$(cat %q)" = "coded app" && test "$(cat %q)" = "coded sdk"`, filepath.Join(sourceByName["app"], "same.txt"), filepath.Join(sourceByName["sdk"], "same.txt")), byName["app"]},
				}
				if os.Getenv("DEVBRIDGE_TEST_FILE_VERIFICATION") == "1" {
					commands = commands[:2]
					commands[0].command = "test -f same.txt"
					commands[1].command = "test -f same.txt"
				}
				if os.Getenv("DEVBRIDGE_TEST_VERIFICATION_FAILURE") == "observed" {
					commands[0].command = "test 1 -eq 2"
				}
				if os.Getenv("DEVBRIDGE_CODING_DUPLICATE_VERIFICATION_ROOT") == "1" {
					// Both successful observations belong to app. The agent's
					// claimed sdk success below must not replace its missing proof.
					commands[0].command, commands[1].command = "test -f same.txt", "test -f same.txt"
					commands[1].dir = commands[0].dir
				}
				for i, command := range commands {
					process := osexec.Command("/bin/sh", "-c", command.command)
					process.Dir = command.dir
					output, err := process.CombinedOutput()
					exitCode := 0
					if err != nil {
						exitCode = 1
					}
					item := map[string]any{"id": fmt.Sprintf("command-%d", i), "type": "commandExecution", "command": command.command, "cwd": command.dir, "status": "completed", "commandActions": []any{map[string]any{"type": "unknown", "command": command.command}}, "exitCode": exitCode, "aggregatedOutput": string(output)}
					write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-coding-v1", "turnId": turnID, "item": item}})
				}
			}
			message := `{"resultDisposition":"COMPLETED","summary":"updated both exact members"}`
			if codingReview {
				message = os.Getenv("DEVBRIDGE_CODING_V1_REVIEW_RESULT")
				if !json.Valid([]byte(message)) {
					os.Exit(52)
				}
			} else if !implementation {
				message = `{"version":2,"summary":"all exact checks passed","checks":[{"index":0,"status":"PASSED","summary":"app passed","evidence":"exit 0","reason":""},{"index":1,"status":"PASSED","summary":"sdk passed","evidence":"exit 0","reason":""},{"index":2,"status":"PASSED","summary":"cross passed","evidence":"exit 0","reason":""}],"acceptanceCriteria":[{"index":0,"status":"PASSED","summary":"both members verified","evidence":"three commands","reason":""}]}`
			}
			if !implementation && !codingReview && os.Getenv("DEVBRIDGE_TEST_FILE_VERIFICATION") == "1" {
				message = `{"version":2,"summary":"Codex verification of GPT file work","checks":[{"index":0,"status":"PASSED","summary":"app checked","evidence":"command","reason":""},{"index":1,"status":"PASSED","summary":"sdk checked","evidence":"command","reason":""}],"acceptanceCriteria":[{"index":0,"status":"PASSED","summary":"both members checked","evidence":"commands","reason":""}]}`
			}
			if !implementation && !codingReview && os.Getenv("DEVBRIDGE_TEST_VERIFICATION_FAILURE") == "criterion" {
				message = strings.Replace(message, `"acceptanceCriteria":[{"index":0,"status":"PASSED"`, `"acceptanceCriteria":[{"index":0,"status":"FAILED"`, 1)
			}
			if len(value.OutputSchema) == 0 {
				message = "Codex 已完成本次工作。请根据命令记录与文件变化分析结果。"
			}
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-coding-v1", "turnId": turnID, "item": map[string]any{"id": "message-" + turnID, "type": "agentMessage", "text": message, "phase": "final_answer"}}})
			status := "completed"
			if marker := os.Getenv("DEVBRIDGE_CODING_V1_INTERRUPT_ONCE"); implementation && marker != "" {
				file, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err == nil {
					_ = file.Close()
					status = "interrupted"
				} else if !os.IsExist(err) {
					os.Exit(57)
				}
			}
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-coding-v1", "turn": map[string]any{"id": turnID, "status": status, "items": []any{}}}})
		default:
			os.Exit(50)
		}
	}
	os.Exit(0)
}

func TestMultiRepositoryCodingReviewProductionFacadeV2(t *testing.T) {
	runCodingReviewFixture(t, nil)
}

func runCodingReviewFixture(t *testing.T, onStarted func(*upperLifecycleFixture, string, string, string) bool, setup ...func(*upperLifecycleFixture)) {
	for _, tc := range []struct {
		name   string
		mutate bool
	}{
		{name: "approval-ready"},
		{name: "mutation-blocked", mutate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpperLifecycleFixture(t)
			if _, err := f.facade.SubmitReviewResult(f.ctx, f.findingsOnly("release-upper-review", f.access.Target)); err != nil {
				t.Fatal(err)
			}

			helperRoot := t.TempDir()
			wireLog := filepath.Join(helperRoot, "review-wire.jsonl")
			t.Setenv("DEVBRIDGE_CODING_V1_HELPER", "1")
			t.Setenv("DEVBRIDGE_CODING_V1_WIRE_LOG", wireLog)
			if tc.mutate {
				t.Setenv("DEVBRIDGE_CODING_V1_REVIEW_MUTATE", "1")
			}
			launcher := filepath.Join(helperRoot, "fake-codex")
			executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
			if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestMultiRepositoryCodingV1AppServerHelper$\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: launcher, AgentRuntime: f.plane.Runs.AgentRuntime}, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
				return codex.DynamicToolCallResponse{Success: false}, nil
			}}
			for _, configure := range setup {
				configure(f)
			}

			preparedTask, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "coding-review-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: f.area, Title: "Coding review intent", ProblemStatement: "Review the exact app and sdk contracts", Constraints: []string{"read only review"}}, Variants: []c.Variant{{ID: "create", Summary: "Create Coding Review Task"}}})
			if err != nil {
				t.Fatal(err)
			}
			createdTask, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: preparedTask.IntentID, Version: preparedTask.Version, Purpose: preparedTask.Purpose, Action: preparedTask.Action, RequestID: "coding-review-task-commit", ContentHash: preparedTask.ContentHash, VariantID: "create"})
			if err != nil || createdTask.State != "COMPLETED" {
				t.Fatalf("create Coding Review Task=%+v err=%v", createdTask, err)
			}
			taskID := createdTask.Steps[0].TaskID
			proposalTargets := workset.CloneTargets(f.targets)
			for i := range proposalTargets {
				proposalTargets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
			}
			work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update both reviewed contracts", Constraints: []string{"preserve exact members"}, CodexInstruction: "Update same.txt in both exact members.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
			verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "both exact members", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: f.targets[0].RepositoryID, RepositoryIDs: []string{f.targets[0].RepositoryID}, WorkingDirectory: "."}, {Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: f.targets[1].RepositoryID, RepositoryIDs: []string{f.targets[1].RepositoryID}, WorkingDirectory: "."}}, AcceptanceCriteria: []string{"Both exact contracts remain present"}, CodexInstruction: "Verify both exact member contracts."}
			modelChecks := make([]map[string]any, len(verification.Checks))
			for i, check := range verification.Checks {
				modelChecks[i] = map[string]any{"targetAlias": review.TargetAlias(i), "targetAliases": []string{review.TargetAlias(i)}, "workingDirectory": check.WorkingDirectory, "kind": check.Kind, "command": check.Command, "instruction": check.Instruction, "paths": append([]string{}, check.Paths...), "packages": append([]string{}, check.Packages...), "required": check.Required}
			}
			modelWork := map[string]any{"implementationMode": work.ImplementationMode, "goal": work.Goal, "constraints": work.Constraints, "codexInstruction": work.CodexInstruction, "executionScope": map[string]any{"kind": work.ExecutionScope.Kind, "paths": []string{}}}
			reviewResult, err := json.Marshal(map[string]any{"version": 2, "reviewDisposition": review.DispositionApprovalReady, "repositoryTargets": []map[string]any{{"targetAlias": review.TargetAlias(0), "proposedWriteScope": proposalTargets[0].Write}, {"targetAlias": review.TargetAlias(1), "proposedWriteScope": proposalTargets[1].Write}}, "primaryTargetAlias": review.TargetAlias(0), "summary": "Both exact members reviewed and ready", "workSpec": modelWork, "verificationSpec": map[string]any{"mode": verification.Mode, "scope": verification.Scope, "checks": modelChecks, "acceptanceCriteria": verification.AcceptanceCriteria, "codexInstruction": verification.CodexInstruction}})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("DEVBRIDGE_CODING_V1_REVIEW_RESULT", string(reviewResult))
			instruction := "  Review both exact same.txt contracts and return the v2 proposal byte-for-byte.\n"
			start, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "coding-review-start", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: taskID, FeatureAreaID: f.area, RepositoryTargets: workset.CloneTargets(f.targets), ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "coding", Summary: "Review both exact members", ExecutorKind: string(review.ExecutorCodingAgent), ReviewInstruction: instruction, PrimaryMemberID: f.targets[0].MemberID}}})
			if err != nil {
				t.Fatal(err)
			}
			options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, ContentHash: start.ContentHash}})
			if err != nil || len(options.BackendCandidates) == 0 {
				t.Fatalf("Coding Review options=%+v err=%v", options, err)
			}
			backend := options.BackendCandidates[0]
			runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
			started, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, Action: start.Action, RequestID: "coding-review-start-commit", ContentHash: start.ContentHash, VariantID: "coding", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}})
			if err != nil || started.State != "COMPLETED" || len(started.Steps) != 1 || started.Steps[0].ReviewID == "" || started.Steps[0].RunID == "" {
				for cause := err; cause != nil; cause = errors.Unwrap(cause) {
					t.Logf("Coding Review start cause: %T %v", cause, cause)
				}
				t.Fatalf("start Coding Review=%+v err=%v", started, err)
			}
			reviewID, runID := started.Steps[0].ReviewID, started.Steps[0].RunID
			if onStarted != nil && !onStarted(f, taskID, reviewID, runID) {
				return
			}
			deadline := time.Now().Add(15 * time.Second)
			var runStatus, failureCode string
			for {
				err = f.plane.DB.SQL().QueryRow(`SELECT status,COALESCE(failure_code,'') FROM runs WHERE id=?`, runID).Scan(&runStatus, &failureCode)
				if err == nil && (runStatus == "SUCCEEDED" || runStatus == "FAILED" || runStatus == "INFRASTRUCTURE_FAILED") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("Coding Review did not finish: status=%s code=%s err=%v", runStatus, failureCode, err)
				}
				time.Sleep(20 * time.Millisecond)
			}

			if !tc.mutate && runStatus == "SUCCEEDED" {
				var premature int
				if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM review_approval_decisions WHERE review_id=?`, reviewID).Scan(&premature); err != nil || premature != 0 {
					t.Fatalf("native completion created approval: %d %v", premature, err)
				}
				submitNaturalFixtureAnalysis(t, f, runID, reviewResult)
			}

			// Wait for materialization to create the approval decision (if expected).
			var bindings, observed, approvals int
			materializationDeadline := time.Now().Add(5 * time.Second)
			for {
				if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM run_repository_bindings WHERE run_id=?),(SELECT count(*) FROM run_repository_bindings WHERE run_id=? AND observation_state IS NOT NULL),(SELECT count(*) FROM review_approval_decisions WHERE review_id=?)`, runID, runID, reviewID).Scan(&bindings, &observed, &approvals); err != nil {
					t.Fatal(err)
				}
				if bindings != 2 || observed != 2 {
					t.Fatalf("Coding Review bindings/observations=%d/%d", bindings, observed)
				}
				// For non-mutate cases, wait for approval to appear.
				if !tc.mutate && approvals >= 1 {
					break
				}
				// For mutate cases or if we've waited long enough, break.
				if tc.mutate || time.Now().After(materializationDeadline) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if tc.mutate {
				if runStatus != "FAILED" || failureCode != "SCOPE_CONFLICT" || approvals != 0 {
					t.Fatalf("mutating Coding Review status/code/approvals=%s/%s/%d", runStatus, failureCode, approvals)
				}
				assertCodingReviewWire(t, wireLog, instruction, runtime, f.reposPath("backend"))
				return
			}
			if runStatus != "SUCCEEDED" || approvals != 1 {
				t.Fatalf("Coding Review status/code/approvals=%s/%s/%d", runStatus, failureCode, approvals)
			}
			stored, err := f.plane.Reviews.Get(f.ctx, reviewID)
			if err != nil || stored.ReviewDisposition != review.DispositionApprovalReady || stored.PrimaryMemberID != f.targets[0].MemberID || len(stored.RepositoryTargets) != 2 {
				t.Fatalf("stored Coding Review=%+v err=%v", stored, err)
			}
			approval, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "coding-review-approve", Purpose: c.Execution, Action: "APPROVE", Execution: &c.ExecutionBody{Action: "APPROVE", TaskID: taskID, FeatureAreaID: f.area, ReviewID: reviewID, DecisionID: wp.DecisionID(reviewID), TaskDefinitionRevisionID: codingReviewCurrentRevision(t, f, taskID)}, Variants: []c.Variant{{ID: "approve", Summary: "Approve exact Coding Review proposal without start", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: proposalTargets, PrimaryMemberID: f.targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}}})
			if err != nil {
				t.Fatal(err)
			}
			approved, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: approval.IntentID, Version: approval.Version, Purpose: approval.Purpose, Action: approval.Action, RequestID: "coding-review-approve-commit", ContentHash: approval.ContentHash, VariantID: "approve"})
			if err != nil || approved.State != "COMPLETED" {
				t.Fatalf("approve Coding Review=%+v err=%v", approved, err)
			}
			current, err := f.facade.GetTask(f.ctx, taskID)
			if err != nil || current.Phase != task.PhaseImplementationReady || current.CurrentExecutionID != "" || current.CurrentDefinitionRevisionID == "" || len(current.RepositoryTargets) != 2 {
				t.Fatalf("Coding Review NO_START Task=%+v err=%v", current, err)
			}
			assertCodingReviewWire(t, wireLog, instruction, runtime, f.reposPath("backend"))
		})
	}
}

func assertCodingReviewWire(t *testing.T, logPath, instruction string, runtime codexruntime.NextRunRuntimeConfig, excluded string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	var threads, turns []entry
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var value entry
		if err = json.Unmarshal(line, &value); err != nil {
			t.Fatal(err)
		}
		if value.Method == "thread/start" || value.Method == "thread/resume" {
			threads = append(threads, value)
		}
		if value.Method == "turn/start" {
			turns = append(turns, value)
		}
	}
	if len(threads) != 1 || len(turns) != 1 || bytes.Contains(data, []byte(excluded)) {
		t.Fatalf("Coding Review wire threads/turns=%d/%d excluded=%t log=%s", len(threads), len(turns), bytes.Contains(data, []byte(excluded)), data)
	}
	var thread struct {
		CWD                                                string   `json:"cwd"`
		Model                                              string   `json:"model"`
		RuntimeWorkspaceRoots                              []string `json:"runtimeWorkspaceRoots"`
		Permissions, ApprovalPolicy, DeveloperInstructions string
	}
	if err = json.Unmarshal(threads[0].Params, &thread); err != nil || thread.CWD == "" || thread.Model != runtime.Model || len(thread.RuntimeWorkspaceRoots) != 5 || !strings.HasPrefix(thread.Permissions, "devbridge_task_") || thread.ApprovalPolicy != "never" {
		t.Fatalf("Coding Review thread=%s err=%v", threads[0].Params, err)
	}
	var turn struct {
		Input []struct {
			Text string `json:"text"`
		} `json:"input"`
		Model, Effort, Permissions, ApprovalPolicy string
		OutputSchema                               json.RawMessage `json:"outputSchema"`
		RuntimeWorkspaceRoots                      []string        `json:"runtimeWorkspaceRoots"`
		SandboxPolicy                              struct {
			Type          string   `json:"type"`
			WritableRoots []string `json:"writableRoots"`
		} `json:"sandboxPolicy"`
	}
	if err = json.Unmarshal(turns[0].Params, &turn); err != nil || len(turn.Input) != 1 || turn.Input[0].Text != instruction || turn.Model != runtime.Model || turn.Effort != runtime.ReasoningEffort || len(turn.OutputSchema) != 0 || len(turn.RuntimeWorkspaceRoots) != 5 || turn.Permissions != thread.Permissions || turn.ApprovalPolicy != "never" || turn.SandboxPolicy.Type != "" || len(turn.SandboxPolicy.WritableRoots) != 0 {
		t.Fatalf("Coding Review turn=%s err=%v", turns[0].Params, err)
	}

	assertCodingFixtureEnvironment(t, thread.CWD, thread.RuntimeWorkspaceRoots, codingFixturePermissionInfo(thread.DeveloperInstructions), nil, true)
}

func TestMultiRepositoryCodingExplicitStartFreezesProposalAfterNoStartApproval(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	installProtocolFixturePlatform(t, f)
	f.plane.Coordinator.Codex = preparationOnlyCodexFactory{}
	targets, work, verification, _ := submitLifecycleApprovalReady(t, f, "coding-explicit-start")
	approval, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.session, RequestID: "coding-explicit-approve", Purpose: c.Execution, Action: "APPROVE",
		Execution: &c.ExecutionBody{Action: "APPROVE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: codingReviewCurrentRevision(t, f, f.task)},
		Variants:  []c.Variant{{ID: "approve", Summary: "Approve exact Coding definition without starting", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: approval.IntentID, Version: approval.Version, Purpose: approval.Purpose, Action: approval.Action, RequestID: "coding-explicit-approve-commit", ContentHash: approval.ContentHash, VariantID: "approve"}); err != nil {
		t.Fatal(err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	start, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.session, RequestID: "coding-explicit-start", Purpose: c.Execution, Action: "START_APPROVED",
		Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExpectedTarget: codingReviewCurrentTarget(t, f, f.task)},
		Variants:  []c.Variant{{ID: "start", Summary: "Start exact approved Coding definition", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}},
	})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, ContentHash: start.ContentHash}})
	if err != nil || len(opts.BackendCandidates) == 0 {
		t.Fatalf("options=%+v err=%v", opts, err)
	}
	backend := opts.BackendCandidates[0]
	runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	committed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, Action: start.Action, RequestID: "coding-explicit-start-commit", ContentHash: start.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}})
	if err != nil || committed.State != "COMPLETED" || len(committed.Steps) != 1 || committed.Steps[0].ExecutionID == "" || committed.Steps[0].RunID != "" || committed.Steps[0].Effect != "PREPARATION_READY" {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("explicit start cause: %T %v", cause, cause)
		}
		t.Fatalf("explicit start=%+v err=%v", committed, err)
	}
	var beforeRuns int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id IS NOT NULL`).Scan(&beforeRuns); err != nil || beforeRuns != 0 {
		t.Fatalf("confirmation created Run: count=%d err=%v", beforeRuns, err)
	}
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	launched, err := f.facade.StartApprovedTaskExecution(f.ctx, controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: f.task, RequestID: "coding-explicit-launch", ExpectedTarget: expected, RuntimeConfig: runtime, Backend: backend.Input()})
	if err != nil || launched.Proposal.ID != committed.Steps[0].ExecutionID || launched.Run.ID == "" || !launched.Handoff.Accepted() {
		t.Fatalf("explicit launch=%+v err=%v", launched, err)
	}
	var proposals, runs, threads int
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT COUNT(*) FROM execution_proposals),(SELECT COUNT(*) FROM runs WHERE execution_proposal_id IS NOT NULL),(SELECT COUNT(*) FROM runs WHERE thread_id IS NOT NULL)`).Scan(&proposals, &runs, &threads); err != nil {
		t.Fatal(err)
	}
	if proposals != 1 || runs != 1 || threads != 0 {
		t.Fatalf("proposal/run/thread=%d/%d/%d", proposals, runs, threads)
	}
	var primary, adapter, protocol, model, effort string
	if err = f.plane.DB.SQL().QueryRow(`SELECT primary_member_id,runtime_adapter_identity,runtime_protocol_identity,json_extract(runtime_config_json,'$.model'),json_extract(runtime_config_json,'$.reasoningEffort') FROM execution_proposals`).Scan(&primary, &adapter, &protocol, &model, &effort); err != nil {
		t.Fatal(err)
	}
	if primary != current.PrimaryMemberID || !runtimeplatform.QualifiedExecutorIdentity(adapter, protocol) || model != runtime.Model || effort != runtime.ReasoningEffort {
		t.Fatalf("frozen proposal=%q %q %q %q %q", primary, adapter, protocol, model, effort)
	}
	var bindings, worktrees int
	var runtimeHash string
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT COUNT(*) FROM run_repository_bindings WHERE execution_id IS NOT NULL),(SELECT COUNT(*) FROM worktrees),(SELECT runtime_workset_hash FROM runs WHERE execution_proposal_id IS NOT NULL)`).Scan(&bindings, &worktrees, &runtimeHash); err != nil {
		t.Fatal(err)
	}
	if bindings != 2 || worktrees != 2 || len(runtimeHash) != 64 {
		t.Fatalf("runtime bindings/worktrees/hash=%d/%d/%q", bindings, worktrees, runtimeHash)
	}
	var plans, authorities, attempts, observations, worksetBindings, startAuthorities int
	if err = f.plane.DB.SQL().QueryRow(`SELECT
 (SELECT count(*) FROM runtime_scope_plans WHERE owner_kind='EXECUTION'),
 (SELECT count(*) FROM runtime_preparation_authorities WHERE owner_kind='EXECUTION'),
 (SELECT count(*) FROM runtime_preparation_attempts),
 (SELECT count(*) FROM runtime_workspace_observations),
 (SELECT count(*) FROM runtime_workset_bindings WHERE owner_kind='EXECUTION'),
 (SELECT count(*) FROM runtime_start_authorities WHERE owner_kind='EXECUTION')`).Scan(&plans, &authorities, &attempts, &observations, &worksetBindings, &startAuthorities); err != nil {
		t.Fatal(err)
	}
	if plans != 1 || authorities != 1 || attempts != 1 || observations != 2 || worksetBindings != 1 || startAuthorities != 1 {
		t.Fatalf("canonical preparation facts plan/authority/attempt/observation/binding/start=%d/%d/%d/%d/%d/%d", plans, authorities, attempts, observations, worksetBindings, startAuthorities)
	}
}

func TestMultiRepositoryCodingProductionFacadeImplementationVerificationAcceptanceDelivery(t *testing.T) {
	runCodingDeliveryFixture(t, nil)
}

func runCodingDeliveryFixture(t *testing.T, onStarted func(*upperLifecycleFixture, string) bool, setup ...func(*upperLifecycleFixture)) {
	runCodingDeliveryFixtureWithStart(t, onStarted, nil, setup...)
}

func runCodingDeliveryFixtureWithStart(t *testing.T, onStarted func(*upperLifecycleFixture, string) bool, startExecution func(*upperLifecycleFixture, c.Intent, controlapi.ApprovedTaskStartInput) (executionproposal.StartResult, error), setup ...func(*upperLifecycleFixture)) {
	f := newUpperLifecycleFixture(t)
	installProtocolFixturePlatform(t, f)
	helperRoot := t.TempDir()
	wireLog := filepath.Join(helperRoot, "wire.jsonl")
	t.Setenv("DEVBRIDGE_CODING_V1_HELPER", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_WIRE_LOG", wireLog)
	launcher := filepath.Join(helperRoot, "fake-codex")
	executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestMultiRepositoryCodingV1AppServerHelper$\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DEVBRIDGE_USER_DECISION_REAL_FIXTURE") == "1" {
		launcher = os.Getenv("DEVBRIDGE_REAL_CODEX")
	}
	f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: launcher, AgentRuntime: f.plane.Runs.AgentRuntime}, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
		return codex.DynamicToolCallResponse{Success: false}, nil
	}}
	for _, configure := range setup {
		configure(f)
	}

	paths := map[string]string{}
	rows, err := f.plane.DB.SQL().Query(`SELECT b.repository_id,wt.path FROM run_repository_bindings b JOIN worktrees wt ON wt.id=b.worktree_id WHERE b.run_id=?`, f.run)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var repositoryID, root string
		if err = rows.Scan(&repositoryID, &root); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		paths[repositoryID] = root
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if paths[f.repos["app"]] == "" || paths[f.repos["sdk"]] == "" {
		t.Fatalf("managed roots=%+v", paths)
	}
	targets := workset.CloneTargets(f.targets)
	refs := make([]review.EvidenceRef, 0, len(targets))
	for i, target := range targets {
		observed, readErr := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: observed.EvidenceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: observed.Path, Digest: observed.Digest})
		targets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
	}
	appCommand := `test "$(cat same.txt)" = "coded app"`
	sdkCommand := `test "$(cat same.txt)" = "coded sdk"`
	if os.Getenv("DEVBRIDGE_TEST_VERIFICATION_FAILURE") == "observed" {
		appCommand = "test 1 -eq 2"
	}
	if os.Getenv("DEVBRIDGE_CODING_DUPLICATE_VERIFICATION_ROOT") == "1" {
		appCommand, sdkCommand = "test -f same.txt", "test -f same.txt"
	}
	crossCommand := fmt.Sprintf(`test "$(cat %q)" = "coded app" && test "$(cat %q)" = "coded sdk"`, filepath.Join(paths[f.repos["app"]], "same.txt"), filepath.Join(paths[f.repos["sdk"]], "same.txt"))
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update both exact contracts", Constraints: []string{"preserve repository identity"}, CodexInstruction: "  Change both exact same.txt files and preserve this prompt byte-for-byte.\n", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	if os.Getenv("DEVBRIDGE_USER_DECISION_REAL_FIXTURE") == "1" {
		work.CodexInstruction = "Before editing, use request_user_input to ask the user whether to proceed. Wait for the actual answer. Then update app/same.txt to coded app followed by a newline and sdk/same.txt to coded sdk followed by a newline within the two managed roots. Do not commit."
	}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "app, sdk and their integration", Checks: []task.VerificationCheck{
		{Kind: task.VerificationCheckTestCommand, Command: appCommand, Required: true, RepositoryID: f.repos["app"], RepositoryIDs: []string{f.repos["app"]}, WorkingDirectory: "."},
		{Kind: task.VerificationCheckTestCommand, Command: sdkCommand, Required: true, RepositoryID: f.repos["sdk"], RepositoryIDs: []string{f.repos["sdk"]}, WorkingDirectory: "."},
		{Kind: task.VerificationCheckTestCommand, Command: crossCommand, Required: true, RepositoryID: f.repos["app"], RepositoryIDs: []string{f.repos["app"], f.repos["sdk"]}, WorkingDirectory: "."},
	}, AcceptanceCriteria: []string{"Both members and the cross-repository contract pass"}, CodexInstruction: "Run the three exact checks without modifying either member."}
	if os.Getenv("DEVBRIDGE_TEST_REQUIRED_CUSTOM") != "" {
		for _, instruction := range []string{"Exercise node migration, CAS and restart recovery", "Exercise real Codex isolation and resume; report BLOCKED when unavailable"} {
			verification.Checks = append(verification.Checks, task.VerificationCheck{Kind: task.VerificationCheckCustom, Instruction: instruction, Required: true, RepositoryID: f.repos["app"], RepositoryIDs: []string{f.repos["app"], f.repos["sdk"]}, WorkingDirectory: "."})
		}
		verification.CodexInstruction += " Also execute the two authored CUSTOM scenarios and report their actual results or limitations."
	}
	if _, err = f.facade.SubmitReviewResult(f.ctx, review.SubmitInput{UpperTarget: f.access.Target, RequestID: "coding-v1-production-proposal", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, Summary: "Exact two-member Coding proposal", Disposition: review.DispositionApprovalReady, Coverage: []string{"app/same.txt", "sdk/same.txt"}, Findings: []review.Finding{}, Limitations: []string{"fixture repositories"}, RecommendedNextSteps: []string{"approve then start"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}}); err != nil {
		t.Fatal(err)
	}
	approval, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "coding-v1-production-approve", Purpose: c.Execution, Action: "APPROVE", Execution: &c.ExecutionBody{Action: "APPROVE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: codingReviewCurrentRevision(t, f, f.task)}, Variants: []c.Variant{{ID: "approve", Summary: "Approve exact Coding definition without start", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: approval.IntentID, Version: approval.Version, Purpose: approval.Purpose, Action: approval.Action, RequestID: "coding-v1-production-approve-commit", ContentHash: approval.ContentHash, VariantID: "approve"}); err != nil {
		t.Fatal(err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	start, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "coding-v1-production-start", Purpose: c.Execution, Action: "START_APPROVED", Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExpectedTarget: codingReviewCurrentTarget(t, f, f.task)}, Variants: []c.Variant{{ID: "start", Summary: "Start exact Coding execution", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
	if err != nil {
		t.Fatal(err)
	}
	options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, ContentHash: start.ContentHash}})
	if err != nil || len(options.BackendCandidates) == 0 {
		t.Fatalf("start options=%+v err=%v", options, err)
	}
	backend := options.BackendCandidates[0]
	runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	// This fixture promises a running pipeline. Confirmation itself seals only
	// admission, so use the explicit command to consume it and dispatch the Run.
	launch := controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: f.task, RequestID: "coding-v1-production-dispatch", ExpectedTarget: *start.Content.Execution.ExpectedTarget, RuntimeConfig: runtime, Backend: codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}
	var started executionproposal.StartResult
	if startExecution != nil {
		started, err = startExecution(f, start, launch)
	} else {
		// The explicit command prepares its own durable parent. Retire this
		// inventory-only proposal first; a Task cannot own two live parents.
		if _, err = f.facade.DiscardConfirmationProposal(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, ContentHash: start.ContentHash}); err != nil {
			t.Fatal(err)
		}
		started, err = f.facade.StartApprovedTaskExecution(f.ctx, launch)
	}
	// Dispatch is asynchronous. Keep the exact admitted Run and let the
	// downstream waits observe it; never resubmit merely because preparation
	// has not yet acknowledged its handoff.
	var handoffErr *executionproposal.HandoffError
	if errors.As(err, &handoffErr) && handoffErr.Handoff.State == executionproposal.HandoffPreparing && handoffErr.Handoff.RunID == started.Run.ID && handoffErr.Handoff.ExecutionID == started.Proposal.ID {
		err = nil
	}
	if err != nil || started.Proposal.ID == "" || started.Run.ID == "" {
		t.Fatalf("explicit delivery start=%+v err=%v", started, err)
	}
	executionID := started.Proposal.ID
	if onStarted != nil && !onStarted(f, executionID) {
		return
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var verifyID string
		if e := f.plane.DB.SQL().QueryRow(`SELECT id FROM runs WHERE execution_proposal_id=? AND purpose='VERIFICATION' AND status='SUCCEEDED' AND verification_result_json IS NULL`, executionID).Scan(&verifyID); e == nil {
			submitNaturalFixtureAnalysis(t, f, verifyID, nil)
		}
		current, err = f.facade.GetTask(f.ctx, f.task)
		if err == nil && current.Phase == task.PhaseAcceptancePending {
			break
		}
		if time.Now().After(deadline) {
			var facts string
			_ = f.plane.DB.SQL().QueryRow(`SELECT group_concat(purpose||':'||status||':'||COALESCE(failure_code,'')||':'||COALESCE(failure_message,''),'|') FROM runs WHERE execution_proposal_id=?`, executionID).Scan(&facts)
			t.Fatalf("execution did not reach acceptance: task=%+v err=%v runs=%s", current, err, facts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var successfulRuns, bindings int
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM runs WHERE execution_proposal_id=? AND status='SUCCEEDED'),(SELECT count(*) FROM run_repository_bindings WHERE execution_id=?)`, executionID, executionID).Scan(&successfulRuns, &bindings); err != nil || successfulRuns != 2 || bindings != 4 {
		t.Fatalf("successful runs/bindings=%d/%d err=%v", successfulRuns, bindings, err)
	}
	accept, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "coding-v1-production-accept", Purpose: c.Execution, Action: "ACCEPT", Execution: &c.ExecutionBody{Action: "ACCEPT", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: executionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID)}, Variants: []c.Variant{{ID: "accept", Summary: "Accept verified exact Coding result"}}})
	if err != nil {
		t.Fatal(err)
	}
	deliveryEffects := 0
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, _ string) error {
		if kind == "TASK_COMMIT_AFTER_GIT" {
			deliveryEffects++
			if deliveryEffects == 2 {
				return errors.New("injected Coding Task commit response loss")
			}
		}
		return nil
	})
	accepted, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: accept.IntentID, Version: accept.Version, Purpose: accept.Purpose, Action: accept.Action, RequestID: "coding-v1-production-accept-commit", ContentHash: accept.ContentHash, VariantID: "accept"})
	if err != nil || accepted.State != "COMPLETED" {
		t.Fatalf("acceptance=%+v err=%v", accepted, err)
	}
	type deliveryMember struct{ repositoryID, path, expectedHead, state string }
	memberRows, err := f.plane.DB.SQL().Query(`SELECT repository_id,workspace_path,expected_head,state FROM task_commit_members WHERE task_id=? ORDER BY repository_id`, f.task)
	if err != nil {
		t.Fatal(err)
	}
	var deliveryMembers []deliveryMember
	for memberRows.Next() {
		var value deliveryMember
		if err = memberRows.Scan(&value.repositoryID, &value.path, &value.expectedHead, &value.state); err != nil {
			memberRows.Close()
			t.Fatal(err)
		}
		deliveryMembers = append(deliveryMembers, value)
	}
	if err = memberRows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(deliveryMembers) != 2 || deliveryMembers[0].state != "COMPLETED" || deliveryMembers[1].state != "PENDING" {
		t.Fatalf("partial Coding delivery=%+v", deliveryMembers)
	}
	preRecoveryHeads := map[string]string{}
	for _, member := range deliveryMembers {
		preRecoveryHeads[member.repositoryID] = gitOutput(t, member.path, "rev-parse", "HEAD")
		if got := gitOutput(t, member.path, "rev-list", "--count", member.expectedHead+"..HEAD"); got != "1" {
			t.Fatalf("Coding member %s pre-recovery effect count=%s", member.repositoryID, got)
		}
	}
	reopenDirectDeliveryFixture(t, f)
	if _, err = f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task); err != nil {
		t.Fatalf("recover Coding Task delivery: %v", err)
	}
	for _, member := range deliveryMembers {
		if head := gitOutput(t, member.path, "rev-parse", "HEAD"); head != preRecoveryHeads[member.repositoryID] {
			t.Fatalf("Coding member %s committed twice: before=%s after=%s", member.repositoryID, preRecoveryHeads[member.repositoryID], head)
		}
		if got := gitOutput(t, member.path, "rev-list", "--count", member.expectedHead+"..HEAD"); got != "1" {
			t.Fatalf("Coding member %s recovered effect count=%s", member.repositoryID, got)
		}
	}
	var completedMembers, codingReceipts, directResults int
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM task_commit_members WHERE task_id=? AND state='COMPLETED' AND evidence_kind='CODING'),(SELECT count(*) FROM task_commit_receipts WHERE task_id=? AND protocol_version=2 AND evidence_kind='CODING' AND completed_at IS NOT NULL),(SELECT count(*) FROM direct_file_implementation_results WHERE task_id=?)`, f.task, f.task, f.task).Scan(&completedMembers, &codingReceipts, &directResults); err != nil || completedMembers != 2 || codingReceipts != 1 || directResults != 0 {
		current, _ = f.facade.GetTask(f.ctx, f.task)
		var parents, decisions int
		var receiptFacts, memberFacts string
		_ = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM task_commit_receipts WHERE task_id=?),(SELECT count(*) FROM decision_resolution_receipts WHERE subject_id=?),COALESCE((SELECT evidence_kind||':'||COALESCE(last_error,'')||':'||COALESCE(completed_at,'') FROM task_commit_receipts WHERE task_id=?),''),COALESCE((SELECT group_concat(repository_id||':'||state||':'||last_error,'|') FROM task_commit_members WHERE task_id=?),'')`, f.task, executionID, f.task, f.task).Scan(&parents, &decisions, &receiptFacts, &memberFacts)
		t.Fatalf("delivery members/receipt/direct results=%d/%d/%d parents=%d decisions=%d receipt=%q members=%q task=%s/%s intent=%+v err=%v", completedMembers, codingReceipts, directResults, parents, decisions, receiptFacts, memberFacts, current.Status, current.Phase, accepted, err)
	}
	merge, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "coding-v1-production-merge", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area}, Variants: []c.Variant{{ID: "merge", Summary: "Merge exact Coding Task commits"}}})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: merge.IntentID, Version: merge.Version, Purpose: merge.Purpose, Action: merge.Action, RequestID: "coding-v1-production-merge-commit", ContentHash: merge.ContentHash, VariantID: "merge"})
	if err != nil || merged.State != "COMPLETED" {
		t.Fatalf("merge=%+v err=%v", merged, err)
	}
	backendContent, err := os.ReadFile(filepath.Join(f.reposPath("backend"), "same.txt"))
	if err != nil || string(backendContent) != "backend source\n" {
		t.Fatalf("non-target backend changed: %q err=%v", backendContent, err)
	}
	assertCodingV1Wire(t, wireLog, paths, f.reposPath("backend"), work, verification, runtime)
}

func (f *upperLifecycleFixture) reposPath(name string) string {
	repository, err := f.plane.Repositories.Get(f.ctx, f.repos[name])
	if err != nil {
		f.t.Fatal(err)
	}
	return repository.Path
}

func assertCodingV1Wire(t *testing.T, logPath string, roots map[string]string, excluded string, work task.WorkSpec, verification task.VerificationSpec, runtime codexruntime.NextRunRuntimeConfig) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	var threads, turns []entry
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var value entry
		if err = json.Unmarshal(line, &value); err != nil {
			t.Fatal(err)
		}
		if value.Method == "thread/start" || value.Method == "thread/resume" {
			threads = append(threads, value)
		}
		if value.Method == "turn/start" {
			turns = append(turns, value)
		}
	}
	if len(threads) != 2 || len(turns) != 2 {
		t.Fatalf("wire thread/turn count=%d/%d log=%s", len(threads), len(turns), data)
	}
	rootValues := rootsSlice(roots)
	expectedRoots := map[string]bool{rootValues[0]: true, rootValues[1]: true}
	for i, raw := range threads {
		var value struct {
			CWD, Model, Permissions, ApprovalPolicy, DeveloperInstructions string
			RuntimeWorkspaceRoots                                          []string `json:"runtimeWorkspaceRoots"`
		}
		if err = json.Unmarshal(raw.Params, &value); err != nil || value.Model != runtime.Model || !strings.HasPrefix(value.Permissions, "devbridge_task_") || value.ApprovalPolicy != "never" || bytes.Contains(raw.Params, []byte(excluded)) {
			t.Fatalf("thread wire=%s err=%v", raw.Params, err)
		}
		assertCodingFixtureEnvironment(t, value.CWD, value.RuntimeWorkspaceRoots, codingFixturePermissionInfo(value.DeveloperInstructions), expectedRoots, i == 1)
		var turn struct {
			Input []struct {
				Text string `json:"text"`
			} `json:"input"`
			Model, Effort, Permissions, ApprovalPolicy string
			RuntimeWorkspaceRoots                      []string        `json:"runtimeWorkspaceRoots"`
			SandboxPolicy                              json.RawMessage `json:"sandboxPolicy"`
		}
		if err = json.Unmarshal(turns[i].Params, &turn); err != nil || len(turn.Input) != 1 || turn.Model != runtime.Model || turn.Effort != runtime.ReasoningEffort || turn.Permissions != value.Permissions || turn.ApprovalPolicy != "never" || len(turn.SandboxPolicy) != 0 || !reflect.DeepEqual(turn.RuntimeWorkspaceRoots, value.RuntimeWorkspaceRoots) {
			t.Fatalf("turn wire=%s err=%v", turns[i].Params, err)
		}
		want := work.CodexInstruction
		if i == 1 {
			want = verification.CodexInstruction
		}
		if turn.Input[0].Text != want {
			t.Fatal("approved engineering prompt changed")
		}
	}

}

func rootsSlice(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func TestMultiRepositoryCodingNoStartApprovalLeavesExplicitStartAvailable(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	targets, work, verification, _ := submitLifecycleApprovalReady(t, f, "coding-admission-proposal")
	approval, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.session, RequestID: "coding-admission-approve", Purpose: c.Execution, Action: "APPROVE",
		Execution: &c.ExecutionBody{Action: "APPROVE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: codingReviewCurrentRevision(t, f, f.task)},
		Variants:  []c.Variant{{ID: "approve", Summary: "Approve the frozen Coding proposal without starting it", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: approval.IntentID, Version: approval.Version, Purpose: approval.Purpose, Action: approval.Action, RequestID: "coding-admission-approve-commit", ContentHash: approval.ContentHash, VariantID: "approve"}); err != nil {
		t.Fatal(err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	start, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.session, RequestID: "coding-admission-start", Purpose: c.Execution, Action: "START_APPROVED",
		Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExpectedTarget: codingReviewCurrentTarget(t, f, f.task)},
		Variants:  []c.Variant{{ID: "start", Summary: "Start the exact approved Coding definition", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}},
	})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, ContentHash: start.ContentHash}})
	if err != nil || len(opts.Variants) != 1 || !opts.Variants[0].Available || len(opts.Variants[0].Failures) != 0 || opts.LockedRuntimeConfig != nil || opts.LockedBackend != nil || len(opts.RuntimeOptions) == 0 || len(opts.BackendCandidates) == 0 {
		t.Fatalf("Coding start options=%+v err=%v", opts, err)
	}
	for table, want := range map[string]int{"execution_proposals": 0, "runtime_handoffs": 0, "feature_area_agent_claims": 0} {
		var got int
		if err := f.plane.DB.SQL().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s=%d want=%d err=%v", table, got, want, err)
		}
	}
}

func TestMultiRepositoryCodingReviewApproveAndStartCreatesAuthorizedExecution(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	installProtocolFixturePlatform(t, f)
	targets, work, verification, _ := submitLifecycleApprovalReady(t, f, "coding-approve-start-proposal")
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.session, RequestID: "coding-approve-start", Purpose: c.Execution, Action: "APPROVE_AND_START",
		Execution: &c.ExecutionBody{Action: "APPROVE_AND_START", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: codingReviewCurrentRevision(t, f, f.task)},
		Variants:  []c.Variant{{ID: "approve-start", Summary: "Confirm the exact Review and start its Coding implementation", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}},
	})
	if err != nil {
		t.Fatal(err)
	}
	options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	if err != nil || len(options.BackendCandidates) == 0 || len(options.RuntimeOptions) == 0 {
		t.Fatalf("approve/start options=%+v err=%v", options, err)
	}
	runtime := codexruntime.NextRunRuntimeConfig{Model: options.RuntimeOptions[0].ModelID, ReasoningEffort: options.RuntimeOptions[0].SupportedReasoningEfforts[0]}
	backend := options.BackendCandidates[0]
	committed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "coding-approve-start-commit", ContentHash: prepared.ContentHash, VariantID: "approve-start", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}})
	if err != nil || committed.State != "COMPLETED" {
		t.Fatalf("approve/start=%+v err=%v", committed, err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || current.CurrentDefinitionRevisionID == "" || current.CurrentExecutionID == "" || current.Phase != task.PhaseImplementationReady {
		t.Fatalf("authorized Task=%+v err=%v", current, err)
	}
	var effects, executions int
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM execution_start_effects WHERE task_id=? AND execution_id=? AND effect='START_AUTHORIZED'),(SELECT count(*) FROM execution_proposals WHERE task_id=? AND id=?)`, f.task, current.CurrentExecutionID, f.task, current.CurrentExecutionID).Scan(&effects, &executions); err != nil {
		t.Fatal(err)
	}
	if effects != 0 || executions != 1 {
		t.Fatalf("authorized effects/executions=%d/%d", effects, executions)
	}
	// APPROVE_AND_START now completes the formal three transactions at READY.
	// T3 grants the exact preparation authority; no Run/start/handoff exists.
	for table, want := range map[string]int{"formal_execution_t1": 1, "formal_execution_t2": 1, "formal_execution_receipts": 1, "runtime_selection_bindings": 1, "runtime_scope_plans": 1, "runtime_preparation_authorities": 1, "runtime_start_authorities": 0, "runtime_handoffs": 0} {
		var count int
		query := "SELECT count(*) FROM " + table
		var args []any
		if table == "runtime_scope_plans" || table == "runtime_preparation_authorities" || table == "runtime_start_authorities" {
			query += " WHERE owner_id=?"
			args = append(args, current.CurrentExecutionID)
		}
		if err = f.plane.DB.SQL().QueryRow(query, args...).Scan(&count); err != nil || count != want {
			t.Fatalf("READY %s=%d want=%d error=%v", table, count, want, err)
		}
	}
	var executionRuns int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, current.CurrentExecutionID).Scan(&executionRuns); err != nil || executionRuns != 0 {
		t.Fatalf("READY created execution Runs: %d error=%v", executionRuns, err)
	}
}

func hasConfirmationFailure(values []c.Failure, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}

// Read the current authored draft/approved revision through the canonical API.
func codingReviewCurrentRevision(t *testing.T, f *upperLifecycleFixture, taskID string) string {
	t.Helper()
	current, err := f.facade.GetTask(f.ctx, taskID)
	if err != nil || current.CurrentDefinitionRevisionID == "" {
		t.Fatalf("current authored revision: task=%s revision=%q error=%v", taskID, current.CurrentDefinitionRevisionID, err)
	}
	return current.CurrentDefinitionRevisionID
}

func codingReviewCurrentTarget(t *testing.T, f *upperLifecycleFixture, taskID string) *wp.TaskExecutionExpectation {
	t.Helper()
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return &expected
}

// This case ends at durable preparation. An asynchronous handoff must not
// accidentally resolve PATH and start an external model during the assertion.
type preparationOnlyCodexFactory struct{}

func (preparationOnlyCodexFactory) Start(context.Context, codexconfig.Config) (execution.CodexSession, error) {
	return nil, errors.New("test stops before AppServer launch")
}

type codingFixturePermission struct {
	Purpose string `json:"purpose"`
	Scratch string `json:"scratch"`
	Copies  []struct {
		SourceRoot string `json:"sourceRoot"`
		Root       string `json:"root"`
	} `json:"copies"`
}

func codingFixturePermissionInfo(instructions string) codingFixturePermission {
	var out codingFixturePermission
	marker := "Core operation sandbox policy: "
	if at := strings.Index(instructions, marker); at >= 0 {
		line := strings.SplitN(instructions[at+len(marker):], "\n", 2)[0]
		_ = json.Unmarshal([]byte(line), &out)
	}
	return out
}
func assertCodingFixtureEnvironment(t *testing.T, cwd string, roots []string, info codingFixturePermission, expected map[string]bool, copied bool) {
	t.Helper()
	if copied && info.Purpose == "VERIFICATION" && os.Getenv("DEVBRIDGE_PARENT_TRUSTED_CODING_FIXTURE") == "1" {
		if len(info.Copies) != 0 {
			t.Fatal("trusted verification unexpectedly copied the source")
		}
		copied = false
	}
	seen := map[string]bool{}
	for _, root := range roots {
		if seen[root] {
			t.Fatal("duplicate root")
		}
		seen[root] = true
	}
	if info.Scratch == "" || !seen[info.Scratch] {
		t.Fatal("missing task scratch")
	}
	delete(seen, info.Scratch)
	if copied {
		if len(info.Copies) != 2 || len(roots) != 5 {
			t.Fatal("copies do not cover both members")
		}
		cwdFound := false
		for _, copy := range info.Copies {
			if !seen[copy.SourceRoot] || !seen[copy.Root] || copy.Root == copy.SourceRoot || (expected != nil && !expected[copy.SourceRoot]) {
				t.Fatal("copy lost source identity")
			}
			delete(seen, copy.SourceRoot)
			delete(seen, copy.Root)
			cwdFound = cwdFound || cwd == copy.Root
		}
		if !cwdFound {
			t.Fatal("read-only run did not use a verification copy")
		}
	} else {
		if len(roots) != 3 || !expected[cwd] {
			t.Fatal("implementation CWD changed")
		}
		for root := range expected {
			if !seen[root] {
				t.Fatal("approved member missing")
			}
			delete(seen, root)
		}
	}
	if len(seen) != 0 {
		t.Fatal("unexpected extra runtime root")
	}
}
