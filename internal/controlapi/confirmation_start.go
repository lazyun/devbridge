package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// CommitConfirmationAndStart is the product composition for a user-confirmed
// start. The original confirmation still seals only its canonical authority;
// the existing start consumer separately prepares and dispatches that exact
// Execution. Reads and definition-only APPROVE never enter this consumer.
func (f *Facade) CommitConfirmationAndStart(ctx context.Context, in confirmation.CommitInput) (confirmation.Intent, error) {
	intent, err := f.CommitConfirmation(ctx, in)
	if err != nil || intent.State != "COMPLETED" || intent.Commit == nil || intent.Content.Execution == nil || (intent.Action != "APPROVE_AND_START" && intent.Action != "START_APPROVED") {
		return intent, err
	}
	for _, variant := range intent.Content.Variants {
		if variant.ID != intent.Commit.VariantID || variant.ImplementationMode != task.ImplementationCodingAgent {
			continue
		}
		for _, step := range intent.Steps {
			if step.State != "COMPLETED" || step.Effect != "PREPARATION_READY" {
				continue
			}
			// These identities are sealed in the original receipt, never taken
			// from a newer Task pointer or a model-authored replacement target.
			proposal, err := f.deps.ExecutionProposals.Get(ctx, intent.AISessionID, step.ExecutionID)
			if err != nil {
				return intent, ConfirmationFailure(err, "DISPATCH")
			}
			selected := intent.Commit.Selection
			if proposal.ID == "" || proposal.ProjectID != intent.ProjectID || proposal.TaskID != step.TaskID || proposal.TaskDefinitionRevisionID != step.RevisionID || proposal.BackendSelection == nil || selected.Backend == nil || selected.RuntimeConfig == nil || proposal.BackendSelection.Input() != *selected.Backend || proposal.RuntimeConfig != *selected.RuntimeConfig {
				return intent, ConfirmationFailure(executionproposal.ErrConflict, "DISPATCH")
			}
			requestID := "confirmation_start_" + confirmationHash([]string{intent.AISessionID, intent.IntentID, intent.Commit.RequestID, step.RequestID})
			result, err := f.StartApprovedTaskExecution(ctx, ApprovedTaskStartInput{
				AISessionID: intent.AISessionID, TaskID: step.TaskID, RequestID: requestID,
				ExpectedTarget: wp.TaskExecutionExpectation{FeatureAreaID: intent.Content.Execution.FeatureAreaID, TaskDefinitionRevisionID: step.RevisionID, ExecutionID: step.ExecutionID, Phase: task.PhaseImplementationReady, PrimaryMemberID: proposal.PrimaryMemberID, ApprovedWorksetHash: proposal.ApprovedWorksetHash},
				RuntimeConfig:  proposal.RuntimeConfig, Backend: proposal.BackendSelection.Input(),
			})
			if err == nil && !result.Handoff.Accepted() {
				err = &executionproposal.HandoffError{Handoff: result.Handoff}
			}
			if err != nil {
				return intent, ConfirmationFailure(err, "DISPATCH")
			}
		}
	}
	return intent, nil
}
