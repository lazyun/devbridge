package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

func TestPromptNeutralityByteIdentityAndSHA(t *testing.T) {
	for name, instruction := range map[string]string{
		"leading": " \tinspect", "trailing": "inspect \t", "newlines": "\r\ninspect\n\n", "unicode": "检查 e\u0301 🚀 中文", "symbols": "<$&`{}\\> # + =", "long": " \n" + strings.Repeat("界", 21000) + "\n ",
	} {
		for _, depth := range []task.ReviewDepth{task.ReviewDepthFast, task.ReviewDepthDeep} {
			for _, purpose := range []run.Purpose{run.PurposeImplementation, run.PurposeScopeReview, run.PurposeVerification} {
				t.Run(name+"/"+string(purpose)+"/"+string(depth), func(t *testing.T) {
					ctx := context.Background()
					c, runs, tasks, trees, session, collector := successfulCoordinator()
					trees.value.Path = t.TempDir()
					trees.value.RepositoryID = "repo-1"
					collector.stable = true
					var prepared PreparedRun
					var err error
					var ceilings []codex.AuthorizationCeiling
					c.Codex = operationAwareRecordingFactory{fakeFactory: fakeFactory{session: session}, ceilings: &ceilings}
					switch purpose {
					case run.PurposeScopeReview:
						reserved := reserveCoordinatorReview(t, c, runs, instruction)
						c.Reviews.(*coordinatorReviewRuntime).value.ReviewDepth = depth
						prepared, err = c.PrepareScopeReview(ctx, reserved.ID)
					case run.PurposeImplementation:
						tasks.value.WorkSpec.CodexInstruction = instruction
						prepared, err = c.PrepareRun(ctx, tasks.value.ID)
					case run.PurposeVerification:
						tasks.value.Status = task.StatusActive
						_, spec := planSpecs()
						spec.CodexInstruction = instruction
						// A reserved execution Run carries immutable runtime evidence.
						// This unit test intentionally constructs the Run directly, so
						// provide the same frozen snapshot the allocator would expose.
						c.Executions = executionSnapshotLookup{value: taskgoal.Execution{ID: "execution-test", RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}}}
						c.RuntimeConfigProvider = codexruntime.Default
						message := reportJSON("PASSED", "PASSED")
						session.message = &message
						session.commands = []ObservedCommand{{Command: "go test ./...", ExitCode: exitCode(0)}}
						reserved, reserveErr := runs.service.CreateForExecution(ctx, tasks.value.ID, purpose, "execution-test")
						if reserveErr != nil {
							t.Fatal(reserveErr)
						}
						c.Runs = neutralRuns{runs}
						prepared, err = c.PrepareReservedExecution(ctx, reserved.ID, Instruction{CodexInstruction: instruction}, &spec)
					}
					if err != nil {
						t.Fatal(err)
					}
					finished, err := c.ExecuteRun(ctx, prepared, nil)
					if err != nil {
						t.Fatal(err)
					}
					sum := sha256.Sum256([]byte(instruction))
					if session.prompt != instruction || finished.FinalCodexPrompt != instruction || finished.PromptSHA256 != hex.EncodeToString(sum[:]) || finished.PromptBytes != int64(len(instruction)) || finished.PromptSentAt == nil {
						t.Fatalf("prompt audit mismatch for %s", purpose)
					}
					if purpose == run.PurposeScopeReview {
						owner := c.Reviews.(*coordinatorReviewRuntime)
						if owner.value.ReviewDepth != depth || owner.value.Instruction != instruction || owner.prompt != instruction {
							t.Fatal("Review durable instruction changed")
						}
					}
					if purpose != run.PurposeImplementation && (len(ceilings) != 1 || ceilings[0] != codex.AuthorizationReadOnly) {
						t.Fatalf("missing mechanical read-only ceiling: %v", ceilings)
					}
				})
			}
		}
	}

}

func TestPromptNeutralityEmptyInstructionRejectsBeforeRun(t *testing.T) {
	for _, empty := range []string{"", " \n\t"} {
		c, runs, tasks, _, session, _ := successfulCoordinator()
		tasks.value.WorkSpec.CodexInstruction = empty
		if _, err := c.PrepareRun(context.Background(), tasks.value.ID); err == nil {
			t.Fatal("empty implementation accepted")
		}
		values, _ := runs.service.List(context.Background())
		if len(values) != 0 || session.startCalls != 0 {
			t.Fatal("empty instruction created a Run")
		}
		f := newRuntimeFixture(t, "task-a")
		work, verification := planSpecs()
		verification.CodexInstruction = empty
		calls := 0
		reserve := func(context.Context) (run.Run, error) { calls++; return run.Run{}, nil }
		_, err := f.supervisor.StartAuthorizedPlan(context.Background(), "task-a", work, verification, reserve, reserve, func(context.Context, executionproposal.Outcome, string, string) error { return nil })
		f.supervisor.Shutdown()
		if err == nil || calls != 0 {
			t.Fatal("empty verification reserved a Run")
		}
	}
	value := task.Task{Goal: "goal", WorkSpec: task.WorkSpec{CodexInstruction: "other instruction"}}
	if BuildPromptWithInstruction(value, "", Instruction{Goal: "fallback forbidden"}) != "" {
		t.Fatal("prompt builder used fallback")
	}
}

// Hide optional schema methods to model a backend with insufficient capability.
type unstructuredSession struct{ CodexSession }

func TestPromptNeutralityDoesNotRequireNativeSchemaCapability(t *testing.T) {
	c, runs, _, _, session, _ := successfulCoordinator()
	c.Codex = sessionFactory{session: unstructuredSession{session}}
	reserved := reserveCoordinatorReview(t, c, runs, "inspect")
	prepared, err := c.PrepareScopeReview(context.Background(), reserved.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.ExecuteRun(context.Background(), prepared, nil)
	if err != nil || result.Failure != nil || result.Status != run.StatusSucceeded || session.turn != 1 {
		t.Fatalf("natural turn was not dispatched: %+v %v", result, err)
	}
}

type neutralRuns struct{ *recordingRuns }

func (r neutralRuns) AttachVerificationReport(ctx context.Context, id string, report run.VerificationReport) (run.Run, error) {
	return r.service.AttachVerificationReport(ctx, id, report)
}
