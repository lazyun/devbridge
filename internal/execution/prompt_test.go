package execution

import (
	"testing"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestAllRunPromptBuildersReturnOnlyTheAuthoredInstruction(t *testing.T) {
	instruction := "  Line one.\n\nKeep `syntax` and trailing spaces.  "
	value := task.Task{ID: "task", RepositoryID: "repo", BaseCommit: "base", WorkSpec: task.WorkSpec{CodexInstruction: instruction}}
	reviewPlan := run.ReviewPlan{Mode: run.ReviewModeFocused, InitialScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal"}}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, CodexInstruction: instruction}

	for name, got := range map[string]string{
		"implementation": BuildPrompt(value, "/worktree"),
		"run":            BuildRunPrompt(value, "/worktree", "run", "repo", task.ExecutionScope{Kind: task.ExecutionScopeRepository}, Instruction{CodexInstruction: instruction}),
		"review":         BuildScopeReviewRunPrompt(value, "/worktree", "run", "repo", instruction, reviewPlan.InitialScope, reviewPlan),
		"verification":   BuildVerificationRunPrompt(value, "/worktree", "run", "repo", instruction, verification),
	} {
		if got != instruction {
			t.Fatalf("%s prompt changed authored bytes:\n got %q\nwant %q", name, got, instruction)
		}
	}
}

func TestBuildPromptIsDeterministic(t *testing.T) {
	value := task.Task{WorkSpec: task.WorkSpec{CodexInstruction: "exact goal"}}
	if first, second := BuildPrompt(value, "/one"), BuildPrompt(value, "/two"); first != second || first != "exact goal" {
		t.Fatalf("prompt is not exact and deterministic: %q / %q", first, second)
	}
}
