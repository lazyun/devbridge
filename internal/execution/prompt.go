package execution

import (
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func BuildPrompt(value task.Task, worktreePath string) string {
	return BuildPromptWithInstruction(value, worktreePath, Instruction{CodexInstruction: value.WorkSpec.CodexInstruction})
}

// Instruction carries an already-confirmed exact AI instruction without
// mutating the durable Task definition.
type Instruction struct {
	Goal             string
	Constraints      []string
	CodexInstruction string
	ExecutionScope   task.ExecutionScope
}

func BuildPromptWithInstruction(value task.Task, worktreePath string, instruction Instruction) string {
	return instruction.CodexInstruction
}

func BuildVerificationPrompt(value task.Task, worktreePath, instruction string, spec task.VerificationSpec) string {
	return BuildVerificationRunPrompt(value, worktreePath, "", value.RepositoryID, instruction, spec)
}

func BuildRunPrompt(value task.Task, worktreePath, runID, repositoryID string, scope task.ExecutionScope, instruction Instruction) string {
	return instruction.CodexInstruction
}

func BuildVerificationRunPrompt(value task.Task, worktreePath, runID, repositoryID, instruction string, spec task.VerificationSpec) string {
	return instruction
}

func BuildScopeReviewRunPrompt(value task.Task, worktreePath, runID, repositoryID, instruction string, scope run.ReviewScope, plans ...run.ReviewPlan) string {
	return instruction
}
