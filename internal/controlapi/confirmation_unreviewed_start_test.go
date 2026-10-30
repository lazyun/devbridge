package controlapi_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestUnreviewedSingleConfirmationStartsAndReplays(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "normal"
		if lost {
			name = "lost-definition-response"
		}
		t.Run(name, func(t *testing.T) {
			f := newCompositionFixture(t)
			repository, err := f.plane.Repositories.Get(f.ctx, f.repositoryID)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(repository.Path, "same.txt"), []byte("fixture\n"), 0600); err != nil {
				t.Fatal(err)
			}
			compositionGit(t, repository.Path, "add", "same.txt")
			compositionGit(t, repository.Path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "scalar fixture")
			p, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("create-none", compositionUpperVariant("upper", "Review", "Read files.")))
			if err != nil {
				t.Fatal(err)
			}
			create := taskReviewCommit(p, "create-none-commit", taskReviewBool(false))
			create.Selection.ReviewMode = "NONE"
			created, err := f.facade.CommitConfirmation(f.ctx, create)
			if err != nil {
				t.Fatal(err)
			}
			owner, err := f.plane.Tasks.Get(f.ctx, compositionStep(t, created, "CREATE_TASK").TaskID)
			if err != nil {
				t.Fatal(err)
			}

			// Use real inventory, SQLite, preparation, and subprocess handoff. Only the
			// Codex protocol peer is the existing deterministic scalar test process.
			launcher := filepath.Join(t.TempDir(), "codex-fixture")
			executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
			if err = os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestDirectFileScalarCodexHelper$\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DEVBRIDGE_SCALAR_FILE_CODEX_HELPER", "1")
			f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: launcher, AgentRuntime: f.plane.Runs.AgentRuntime}}
			backend, err := f.plane.CodexConfigs.Create(f.ctx, codexconfig.CreateInput{Name: "Single confirmation fixture", CodexPath: launcher, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			f.facade, err = controlapi.NewFromControlPlane(f.plane)
			if err != nil {
				t.Fatal(err)
			}
			work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Inspect the selected repository", Constraints: []string{"Preserve source files"}, CodexInstruction: "Inspect same.txt without editing.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
			verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "selected repository", CodexInstruction: "Run test -f same.txt", AcceptanceCriteria: []string{"The file is present"}, Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true}}}
			const action = "CONFIRM_AND_START_IMPLEMENTATION"
			prepare := c.PrepareInput{AISessionID: f.sessionID, RequestID: "single-prepare", Purpose: c.Execution, Action: action, Execution: &c.ExecutionBody{Action: action, TaskID: owner.ID, FeatureAreaID: owner.FeatureAreaID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID}, Variants: []c.Variant{{ID: "coding", Summary: "Confirm plan, runtime and start together", ImplementationMode: task.ImplementationCodingAgent, WorkSpec: &work, VerificationSpec: &verification}}}
			pending, err := f.facade.PrepareConfirmation(f.ctx, prepare)
			if err != nil {
				t.Fatal(err)
			}
			runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-luna", ReasoningEffort: "medium"}
			commit := c.CommitInput{AISessionID: f.sessionID, IntentID: pending.IntentID, Version: pending.Version, Purpose: pending.Purpose, Action: pending.Action, RequestID: "single-commit", ContentHash: pending.ContentHash, VariantID: "coding", Selection: c.Selection{Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ID, ConfigFingerprint: backend.Fingerprint()}, RuntimeConfig: &runtime}}
			missing := commit
			missing.RequestID = "no-runtime"
			missing.Selection.RuntimeConfig = nil
			rejected, err := f.facade.CommitConfirmation(f.ctx, missing)
			if err == nil || rejected.Submission == nil || rejected.Submission.Outcome != c.RejectedNotConsumed {
				t.Fatalf("missing-runtime rejection: %+v %v", rejected, err)
			}
			f.plane.CodexRuntime.SetAgentConfigFingerprintProvider(func() map[string]string { return map[string]string{"pi": strings.Repeat("a", 64)} })
			for index, invalidPolicy := range []codexruntime.NextRunRuntimeConfig{
				{Model: runtime.Model, ReasoningEffort: runtime.ReasoningEffort, ExecutionPolicy: "COOPERATIVE_TRUSTED_V1"},
				{Model: runtime.Model, ReasoningEffort: runtime.ReasoningEffort, AgentID: "pi", ExecutionPolicy: "TASK_SANDBOX_V1"},
				{Model: runtime.Model, ReasoningEffort: runtime.ReasoningEffort, AgentID: "pi", ExecutionPolicy: "COOPERATIVE_TRUSTED_V1", AgentConfigFingerprint: strings.Repeat("a", 64)},
			} {
				invalid := commit
				invalid.RequestID = fmt.Sprintf("invalid-policy-%d", index)
				invalid.Selection.RuntimeConfig = &invalidPolicy
				blocked, policyErr := f.facade.CommitConfirmation(f.ctx, invalid)
				if policyErr == nil || blocked.Submission == nil || blocked.Submission.Outcome != c.RejectedNotConsumed {
					t.Fatalf("invalid policy consumed confirmation: %+v %v", blocked, policyErr)
				}
				if index == 2 && (blocked.Submission.Failure == nil || blocked.Submission.Failure.Code != "AGENT_CAPABILITY_UNAVAILABLE") {
					t.Fatalf("Pi multi-stage plan not refused before effects: %+v %v", blocked, policyErr)
				}
			}
			before, err := f.plane.Tasks.Get(f.ctx, owner.ID)
			if err != nil || before.CurrentDefinitionRevisionID != owner.CurrentDefinitionRevisionID || before.CurrentExecutionID != "" {
				t.Fatalf("preflight mutated definition: %+v %v", before, err)
			}

			if lost {
				consumed := commit
				consumed.SubmittedHash = c.ValueHash(commit)
				consumed.BackendSelection = &codexconfig.BackendSelection{ConfigID: backend.ID, ConfigFingerprint: backend.Fingerprint(), ConfigName: backend.Name, Source: "SESSION_INVENTORY"}
				steps := []c.Step{{Kind: "CONFIRM_IMPLEMENTATION", RequestID: "single-definition-step", State: "PENDING"}, {Kind: "START_IMPLEMENTATION", RequestID: "single-start-step", State: "PENDING"}}
				if _, _, err = f.plane.Confirmations.BeginCommit(f.ctx, consumed, steps); err != nil {
					t.Fatal(err)
				}
				frozen, err := f.plane.Tasks.ConfirmUnreviewedImplementation(f.ctx, task.UnreviewedImplementationInput{AISessionID: f.sessionID, TaskID: owner.ID, IntentID: pending.IntentID, RequestID: commit.RequestID, ExpectedRevisionID: owner.CurrentDefinitionRevisionID, RepositoryID: pending.Content.Execution.RepositoryID, BaseCommit: pending.Content.Execution.BaseCommit, WorkSpec: work, VerificationSpec: verification})
				if err != nil {
					t.Fatal(err)
				}
				expected := wp.TaskExecutionExpectation{FeatureAreaID: owner.FeatureAreaID, TaskDefinitionRevisionID: frozen.CurrentDefinitionRevisionID, Phase: task.PhaseImplementationReady}
				// A consumed envelope without its completed definition step cannot start.
				if _, _, err = sqlite.NewWorkflowProgressionStore(f.plane.DB).ReserveTaskExecutionExpectedCommand(f.ctx, f.sessionID, owner.ID, "START_EXECUTION", steps[1].RequestID, &expected); err == nil {
					t.Fatal("incomplete definition step authorized start")
				}
				steps[0].State, steps[0].Effect = "UNKNOWN", "UNKNOWN"
				if _, err = f.plane.Confirmations.RecordStep(f.ctx, f.sessionID, pending.IntentID, pending.Version, steps[0], "UNKNOWN"); err != nil {
					t.Fatal(err)
				}
			}
			completed, err := f.facade.CommitConfirmation(f.ctx, commit)
			if err != nil || completed.State != "COMPLETED" {
				for cause := err; cause != nil; cause = errors.Unwrap(cause) {
					t.Logf("cause %T: %v", cause, cause)
				}
				t.Fatalf("single confirmation: state=%s steps=%+v err=%v", completed.State, completed.Steps, err)
			}
			if len(completed.Steps) != 2 || completed.Steps[1].RunID == "" || completed.Steps[1].Effect != "RUNTIME_HANDOFF_ACCEPTED" {
				t.Fatalf("no real handoff: %+v", completed.Steps)
			}
			current, err := f.plane.Tasks.Get(f.ctx, owner.ID)
			if err != nil || current.CurrentExecutionID == "" || current.CurrentReviewID != "" || current.CurrentDefinitionRevisionID == owner.CurrentDefinitionRevisionID {
				t.Fatalf("unexpected current Task: %+v %v", current, err)
			}
			proposal, err := f.plane.ExecutionProposals.Get(f.ctx, f.sessionID, current.CurrentExecutionID)
			if err != nil || proposal.RuntimeConfig != runtime || proposal.BackendSelection == nil || proposal.BackendSelection.ConfigID != backend.ID {
				t.Fatalf("lost selected runtime: %+v %v", proposal, err)
			}
			replay, err := f.facade.CommitConfirmation(f.ctx, commit)
			if err != nil || !reflect.DeepEqual(completed.Steps, replay.Steps) {
				t.Fatalf("replay changed steps: %+v %v", replay, err)
			}
			var executions, starts int
			if err = f.plane.DB.SQL().QueryRow("SELECT COUNT(*) FROM execution_proposals WHERE task_id=?", owner.ID).Scan(&executions); err != nil {
				t.Fatal(err)
			}
			if err = f.plane.DB.SQL().QueryRow("SELECT COUNT(*) FROM execution_start_effects WHERE task_id=? AND effect='START_AUTHORIZED'", owner.ID).Scan(&starts); err != nil {
				t.Fatal(err)
			}
			if executions != 1 || starts != 1 {
				t.Fatalf("duplicate start: executions=%d starts=%d", executions, starts)
			}
			changed := commit
			changed.RequestID = "different-start"
			changed.Selection.RuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-luna", ReasoningEffort: "high"}
			if _, err = f.facade.CommitConfirmation(f.ctx, changed); err == nil {
				t.Fatal("changed consumed selection accepted")
			}
			policyChanged := commit
			policyChanged.RequestID = "different-policy"
			policyChanged.Selection.RuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: runtime.Model, ReasoningEffort: runtime.ReasoningEffort, AgentID: "pi", ExecutionPolicy: "COOPERATIVE_TRUSTED_V1"}
			if _, err = f.facade.CommitConfirmation(f.ctx, policyChanged); err == nil {
				t.Fatal("native frozen execution silently converted to cooperative")
			}
		})
	}
}
