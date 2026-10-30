package controlapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/review"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestDirectFileCodexVerification(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			name := "observed"
			if missing {
				name = "missing-member-command"
			}
			if enrolled {
				name = "worker-" + name
			}
			t.Run(name, func(t *testing.T) {
				f, current := approveDirectFixture(t, false, false)
				installProtocolFixturePlatform(t, f)
				root := t.TempDir()
				launcher := filepath.Join(root, "codex-fixture")
				wire := filepath.Join(root, "wire.jsonl")
				t.Setenv("DEVBRIDGE_CODING_V1_HELPER", "1")
				t.Setenv("DEVBRIDGE_CODING_V1_WIRE_LOG", wire)
				t.Setenv("DEVBRIDGE_TEST_FILE_VERIFICATION", "1")
				if missing {
					t.Setenv("DEVBRIDGE_CODING_DUPLICATE_VERIFICATION_ROOT", "1")
				}
				executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
				if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestMultiRepositoryCodingV1AppServerHelper$\n"), 0700); err != nil {
					t.Fatal(err)
				}
				f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: launcher, AgentRuntime: f.plane.Runs.AgentRuntime}, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
					return codex.DynamicToolCallResponse{Success: false}, nil
				}}
				p, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "file-verify-start", Purpose: c.Execution, Action: "START_DIRECT_FILE", Execution: &c.ExecutionBody{Action: "START_DIRECT_FILE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID}, Variants: []c.Variant{{ID: "files", Summary: "GPT implementation with Codex verification", ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
				if err != nil {
					t.Fatal(err)
				}
				opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, ContentHash: p.ContentHash}})
				if err != nil || len(opts.BackendCandidates) == 0 {
					t.Fatalf("options %+v %v", opts, err)
				}
				if output := os.Getenv("DEVBRIDGE_FILE_VERIFICATION_PREVIEW"); output != "" && !missing {
					data, e := json.Marshal(map[string]any{"intent": p, "options": opts})
					if e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(output, data, 0600); e != nil {
						t.Fatal(e)
					}
				}
				backend := opts.BackendCandidates[0]
				runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
				confirmed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "file-verify-start-commit", ContentHash: p.ContentHash, VariantID: "files", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}})
				if err != nil || confirmed.State != "COMPLETED" {
					t.Fatalf("start %+v %v", confirmed, err)
				}
				state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
				if err != nil || state.State != "ACTIVE" {
					t.Fatalf("file grant %+v %v", state, err)
				}
				var workerClaim *directfile.WorkerClaimIdentity
				var workerInput *directfile.WorkerClaimInput
				if enrolled {
					in := directfile.WorkerClaimInput{WorkerTarget: directfile.WorkerTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: state.DecisionID, WorkflowID: state.WorkflowID, ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, WorkerClaimIdentity: directfile.WorkerClaimIdentity{RequestID: "file-verify-delivery", WorkerID: "fixture-external-worker"}}
					claimed, e := f.facade.ClaimFileImplementationWorker(f.ctx, in)
					if e != nil || claimed.ExecutionPayload == nil || claimed.ExecutionPayload.Instruction != current.WorkSpec.CodexInstruction || claimed.ExecutionPayload.Verification.Mode != task.VerificationModeRun {
						for cause := e; cause != nil; cause = errors.Unwrap(cause) {
							t.Logf("worker claim cause %T: %v", cause, cause)
						}
						t.Fatalf("worker must preserve required separate verification: %+v %v", claimed, e)
					}
					workerClaim = &in.WorkerClaimIdentity
					workerInput = &in
				}
				for _, target := range current.RepositoryTargets {
					in := directInput(f, state, target)
					observed, err := f.facade.FileRead(f.ctx, in)
					if err != nil {
						t.Fatal(err)
					}
					in.WorkerClaim = workerClaim
					in.RequestID = "file-verify-write-" + target.RepositoryID
					in.ExpectedDigest = observed.Digest
					name := "sdk"
					if target.RepositoryID == f.repos["app"] {
						name = "app"
					}
					in.Content = "coded " + name + "\n"
					if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
						t.Fatal(err)
					}
				}
				input := directfile.ImplementationResultInput{WorkerClaim: workerClaim, AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "file-verify-submit", Summary: "GPT changed both exact members", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}
				result, err := f.facade.SubmitFileImplementationResult(f.ctx, input)
				if err != nil || result.ExecutionID == "" || result.VerificationStartError != "" || result.VerificationRunID == "" {
					t.Fatalf("submission %+v %v", result, err)
				}
				deadline := time.Now().Add(20 * time.Second)
				var vr run.Run
				for time.Now().Before(deadline) {
					vr, err = f.facade.GetRunForSession(f.ctx, f.session, result.VerificationRunID)
					if err != nil {
						t.Fatal(err)
					}
					if vr.Status == run.StatusSucceeded || vr.Status == run.StatusFailed {
						break
					}
					time.Sleep(30 * time.Millisecond)
				}
				if vr.Status == run.StatusSucceeded && vr.ResultProtocol == run.NaturalResultProtocol && vr.VerificationReport == nil {
					submitNaturalFixtureAnalysis(t, f, vr.ID, nil)
					vr, err = f.facade.GetRunForSession(f.ctx, f.session, vr.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				if vr.Status != run.StatusSucceeded || vr.Purpose != run.PurposeVerification || vr.VerificationReport == nil {
					t.Fatalf("verification status=%s failure=%+v report=%+v", vr.Status, vr.Failure, vr.VerificationReport)
				}
				if workerInput != nil {
					worker, workerErr := f.facade.GetFileImplementationWorker(f.ctx, *workerInput)
					exact, exactErr := f.facade.GetFileImplementationResult(f.ctx, directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID})
					if workerErr != nil || exactErr != nil || worker.ResultReceipt == nil || worker.ResultReceipt.ExecutionID != exact.ExecutionID || worker.ResultReceipt.VerificationRunID != exact.VerificationRunID || worker.ResultReceipt.VerificationStatus != exact.VerificationStatus || worker.ResultReceipt.Accepted {
						t.Fatalf("worker readback hides required verification: worker=%+v exact=%+v errors=%v/%v", worker, exact, workerErr, exactErr)
					}
				}
				var implementations, verifications int
				if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FILTER(WHERE purpose='IMPLEMENTATION'),count(*) FILTER(WHERE purpose='VERIFICATION') FROM runs WHERE execution_proposal_id=?`, result.ExecutionID).Scan(&implementations, &verifications); err != nil || implementations != 0 || verifications != 1 {
					t.Fatalf("Run ownership implementation=%d verification=%d %v", implementations, verifications, err)
				}
				replay, err := f.facade.SubmitFileImplementationResult(f.ctx, input)
				if err != nil || replay.ExecutionID != result.ExecutionID || replay.VerificationRunID != result.VerificationRunID {
					t.Fatalf("submission replay %+v %v", replay, err)
				}
				state, err = f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
				if err != nil || state.State != "CLOSED" || state.Enabled {
					t.Fatalf("closed file authority %+v %v", state, err)
				}
				_, err = f.facade.FileReplace(f.ctx, directInput(f, state, current.RepositoryTargets[0]))
				if err == nil {
					t.Fatal("file writes allowed during/after verification")
				}
				reopenDirectDeliveryFixture(t, f)
				acceptInput := wp.TaskAcceptanceInput{AISessionID: f.session, TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: result.ExecutionID, DecisionID: wp.TaskAcceptanceDecisionID(result.ExecutionID), Option: "ACCEPT", RequestID: "file-verify-accept"}
				if !missing {
					plan, e := f.plane.WorkflowProgression.PrepareTaskCompletionPlan(f.ctx, current.ProjectID, f.task, result.ExecutionID, current.CurrentDefinitionRevisionID)
					if e != nil {
						t.Fatal(e)
					}
					acceptInput.CompletionPlan = &plan
				}
				_, err = f.facade.ResolveTaskAcceptance(f.ctx, acceptInput)
				if missing {
					if err == nil {
						t.Fatal("agent claim replaced missing member command")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				delivered, err := f.facade.GetTask(f.ctx, f.task)
				completion, completionErr := f.plane.WorkflowProgression.ReadTaskCompletion(f.ctx, current.ProjectID, f.task)
				if err != nil || delivered.Status != task.StatusSucceeded || completionErr != nil || completion == nil || completion.State != "COMPLETED" {
					t.Fatalf("delivery %+v %v", delivered, err)
				}
				if workerInput != nil {
					worker, workerErr := f.facade.GetFileImplementationWorker(f.ctx, *workerInput)
					if workerErr != nil || worker.ResultReceipt == nil || !worker.ResultReceipt.Accepted {
						t.Fatalf("worker readback lost separately recorded acceptance: %+v %v", worker, workerErr)
					}
				}
				data, err := os.ReadFile(wire)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "turn-implementation") {
					t.Fatal("GPT file work fabricated a Codex implementation turn")
				}
			})
		}
	}

}

func TestDirectFileScalarCodexVerification(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	installScalarFileCodex(t, f)
	commit := func(in c.PrepareInput) c.Intent {
		t.Helper()
		in.AISessionID = f.session
		p, err := f.facade.PrepareConfirmation(f.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		selection := c.Selection{}
		if in.Action == "APPROVE_AND_START" {
			options, e := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, ContentHash: p.ContentHash}})
			if e != nil || len(options.BackendCandidates) == 0 {
				t.Fatalf("scalar options %+v %v", options, e)
			}
			backend := options.BackendCandidates[0]
			runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
			selection = c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}
		}
		out, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "v1-commit-" + in.RequestID, ContentHash: p.ContentHash, VariantID: in.Variants[0].ID, Selection: selection})
		if err != nil || out.State != "COMPLETED" {
			t.Fatalf("commit %s: %+v %v", in.Action, out, err)
		}
		return out
	}
	area := commit(c.PrepareInput{RequestID: "v1-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Legacy compatibility", ProblemStatement: "Preserve the single-repository Upper Review protocol"}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	areaID := area.Steps[0].FeatureAreaID
	created := commit(c.PrepareInput{RequestID: "v1-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: areaID, RepositoryID: f.repos["app"], Title: "Legacy app review", ProblemStatement: "Review app source", Constraints: []string{"read only"}}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	taskID := created.Steps[0].TaskID
	started := commit(c.PrepareInput{RequestID: "v1-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: taskID, FeatureAreaID: areaID, RepositoryID: f.repos["app"], ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "review", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt"}}})
	reviewID, _ := started.Steps[0].ReviewID, started.Steps[0].RunID
	access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, reviewID)
	if err != nil || access.State != "ACTIVE" {
		t.Fatalf("access=%+v %v", access, err)
	}
	read, err := f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &access.Target, Path: "same.txt"})
	if err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationDirectFile, Goal: "legacy app update", Constraints: []string{"bounded"}, CodexInstruction: "update app", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "app repository", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true}}, AcceptanceCriteria: []string{"same.txt remains present"}, CodexInstruction: "Verify the app repository"}
	submission := review.SubmitInput{UpperTarget: access.Target, RequestID: "v1-report", Report: review.ReviewReport{SchemaVersion: 1, Summary: "legacy report", Disposition: review.DispositionApprovalReady, Coverage: []string{"same.txt"}, Findings: []review.Finding{}, Limitations: []string{"read only"}, RecommendedNextSteps: []string{"approve"}, EvidenceRefs: []review.EvidenceRef{{EvidenceID: read.EvidenceID, Path: "same.txt", Digest: read.Digest}}, WorkSpec: &work, VerificationSpec: &verification}}
	receipt, err := f.facade.SubmitReviewResult(f.ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := f.facade.SubmitReviewResult(f.ctx, submission); err != nil || replay.ReportID != receipt.ReportID {
		t.Fatalf("report replay=%+v %v", replay, err)
	}
	approval := commit(c.PrepareInput{RequestID: "v1-file-approval", Purpose: c.Execution, Action: "APPROVE_AND_START", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, taskID, areaID, reviewID, "APPROVE_AND_START"), Variants: []c.Variant{{ID: "files", Summary: "Explicit single repository file implementation with Codex verification", ImplementationMode: task.ImplementationDirectFile, WorkSpec: &work, VerificationSpec: &verification}}})
	current, err := f.facade.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, taskID)
	if err != nil || state.State != "ACTIVE" {
		t.Fatalf("v1 grant %+v %v", state, err)
	}
	in := directfile.Input{Scope: directfile.Scope{AISessionID: f.session, FeatureAreaID: areaID, WorkflowID: state.WorkflowID}, Path: "same.txt", Content: "single-repo compatibility update\n"}
	if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	resultInput := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: taskID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "v1-file-result", Summary: "v1 file result", VerificationNotes: "explicit user waiver"}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, resultInput)
	if err != nil {
		t.Fatal(err)
	}

	_ = approval
	if result.VerificationStartError != "" || result.ExecutionID == "" || result.VerificationRunID == "" {
		t.Fatalf("scalar result %+v", result)
	}
	deadline := time.Now().Add(15 * time.Second)
	var verified run.Run
	for time.Now().Before(deadline) {
		verified, err = f.facade.GetRunForSession(f.ctx, f.session, result.VerificationRunID)
		if err != nil {
			t.Fatal(err)
		}
		if verified.Status == run.StatusSucceeded || verified.Status == run.StatusFailed || verified.Status == run.StatusInfrastructureFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if verified.Status == run.StatusSucceeded && verified.ResultProtocol == run.NaturalResultProtocol && verified.VerificationReport == nil {
		submitNaturalFixtureAnalysis(t, f, verified.ID, nil)
		verified, err = f.facade.GetRunForSession(f.ctx, f.session, verified.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if verified.Status != run.StatusSucceeded || verified.VerificationReport == nil {
		t.Fatalf("scalar verification status=%s failure=%+v report=%+v", verified.Status, verified.Failure, verified.VerificationReport)
	}
	plan, err := f.plane.WorkflowProgression.PrepareTaskCompletionPlan(f.ctx, current.ProjectID, taskID, result.ExecutionID, current.CurrentDefinitionRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.facade.ResolveTaskAcceptance(f.ctx, wp.TaskAcceptanceInput{AISessionID: f.session, TaskID: taskID, FeatureAreaID: areaID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: result.ExecutionID, DecisionID: wp.TaskAcceptanceDecisionID(result.ExecutionID), Option: "ACCEPT", RequestID: "scalar-accept", CompletionPlan: &plan})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := f.facade.GetTask(f.ctx, taskID)
	completion, completionErr := f.plane.WorkflowProgression.ReadTaskCompletion(f.ctx, current.ProjectID, taskID)
	if err != nil || delivered.Status != task.StatusSucceeded || completionErr != nil || completion == nil || completion.State != "COMPLETED" {
		t.Fatalf("scalar delivery %+v %v", delivered, err)
	}

}

func installScalarFileCodex(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	t.Setenv("DEVBRIDGE_SCALAR_FILE_CODEX_HELPER", "1")
	launcher := filepath.Join(t.TempDir(), "scalar-codex")
	executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
	if e := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestDirectFileScalarCodexHelper$\n"), 0700); e != nil {
		t.Fatal(e)
	}
	f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: launcher, AgentRuntime: f.plane.Runs.AgentRuntime}, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
		return codex.DynamicToolCallResponse{Success: false}, nil
	}}
}
func TestDirectFileScalarCodexHelper(t *testing.T) {
	if os.Getenv("DEVBRIDGE_SCALAR_FILE_CODEX_HELPER") != "1" {
		return
	}
	write := func(v any) { b, _ := json.Marshal(v); fmt.Fprintln(os.Stdout, string(b)) }
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	cwd := ""
	for scanner.Scan() {
		var q struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &q) != nil {
			os.Exit(1)
		}
		switch q.Method {
		case "config/read":
			write(map[string]any{"id": q.ID, "result": map[string]any{"config": map[string]any{}}})
		case "mcpServerStatus/list":
			write(map[string]any{"id": q.ID, "result": map[string]any{"data": []any{}, "nextCursor": nil}})
		case "initialize":
			write(map[string]any{"id": q.ID, "result": map[string]any{"userAgent": "scalar Codex fixture"}})
		case "initialized":
		case "thread/name/set", "thread/inject_items":
			write(map[string]any{"id": q.ID, "result": map[string]any{}})
		case "thread/start", "thread/resume":
			var p struct {
				CWD         string `json:"cwd"`
				Permissions string `json:"permissions"`
			}
			json.Unmarshal(q.Params, &p)
			cwd = p.CWD
			result := map[string]any{"thread": map[string]any{"id": "scalar-thread"}, "cwd": cwd}
			if p.Permissions != "" {
				result["activePermissionProfile"] = map[string]any{"id": p.Permissions}
			}
			write(map[string]any{"id": q.ID, "result": result})
		case "turn/start":
			write(map[string]any{"id": q.ID, "result": map[string]any{"turn": map[string]any{"id": "scalar-verify", "status": "inProgress", "items": []any{}}}})
			command := osexec.Command("/bin/sh", "-c", "test -f same.txt")
			command.Dir = cwd
			output, e := command.CombinedOutput()
			code := 0
			if e != nil {
				code = 1
			}
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "scalar-thread", "turnId": "scalar-verify", "item": map[string]any{"id": "scalar-command", "type": "commandExecution", "command": "test -f same.txt", "cwd": cwd, "status": "completed", "exitCode": code, "aggregatedOutput": string(output)}}})
			message := "检查已完成，请分析实际命令退出码与文件变化。"
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "scalar-thread", "turnId": "scalar-verify", "item": map[string]any{"id": "scalar-message", "type": "agentMessage", "phase": "final_answer", "text": message}}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "scalar-thread", "turn": map[string]any{"id": "scalar-verify", "status": "completed", "items": []any{}}}})
		default:
			os.Exit(2)
		}
	}
	os.Exit(0)
}
